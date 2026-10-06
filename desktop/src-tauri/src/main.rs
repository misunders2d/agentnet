//! The AgentNet app: a window and a tray icon around the agentnet program
//! (`agentnet app`), which does all of AgentNet's work and serves the page
//! the window shows on this computer's loopback address. The shell starts
//! with the computer (hidden in the tray), keeps one instance, hands
//! agentnet:// links to the window and stops the program on Quit. The main
//! loopback page has one guarded native clipboard-image read command.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod deeplink;
mod clipboard;
#[cfg(target_os = "linux")]
mod linux;
mod navigation;
mod sidecar;
#[cfg(any(windows, test))]
mod win_autostart;

use std::ffi::OsString;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, RecvTimeoutError, Sender};
use std::sync::Mutex;
use std::time::Instant;

use tauri::menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::webview::{DownloadEvent, NewWindowResponse};
use tauri::{AppHandle, Manager, RunEvent, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder, WindowEvent};
use tauri_plugin_autostart::MacosLauncher;
#[cfg(not(windows))]
use tauri_plugin_autostart::ManagerExt;
use tauri_plugin_deep_link::DeepLinkExt;
use tauri_plugin_opener::OpenerExt;

const AUTOSTART_ARG: &str = "--autostart";
const AUTOSTART_CHOSEN: &str = "autostart-chosen"; // in the app's config dir: the person's choice stands

/// What the supervisor is told.
enum Signal {
    Exited,
    Retry,
    Quit,
}

/// The shell's state, shared by the window, the tray and the supervisor.
#[derive(Default)]
struct Shell {
    page: Mutex<Option<Url>>,       // the page the program announced last (with its token)
    splash: Mutex<Option<Url>>,     // the window's own starting page
    pending: Mutex<Option<String>>, // a link's fragment that came before the page
    running: Mutex<Option<sidecar::Running>>,
    signals: Mutex<Option<Sender<Signal>>>,
    last_error: Mutex<Option<String>>,
    quitting: AtomicBool,
}

fn main() {
    let hidden = std::env::args().any(|a| a == AUTOSTART_ARG);
    tauri::Builder::default()
        // First: a second start hands its link to this one and shows the window.
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| show(app)))
        .plugin(tauri_plugin_deep_link::init())
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, Some(vec![AUTOSTART_ARG])))
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_clipboard_manager::init())
        .invoke_handler(tauri::generate_handler![clipboard::agentnet_clipboard_image])
        .manage(Shell::default())
        .setup(move |app| {
            let handle = app.handle().clone();
            let window = WebviewWindowBuilder::new(app, "main", WebviewUrl::App("index.html".into()))
                .initialization_script(include_str!("clipboard.js"))
                .title("AgentNet")
                .inner_size(1200.0, 800.0)
                .min_inner_size(380.0, 600.0)
                .visible(!hidden)
                // Comic, Classic and Zoom take dropped files themselves.
                .disable_drag_drop_handler()
                // Copy link / Copy invitation on Linux and Windows.
                .enable_clipboard_access()
                .on_navigation({
                    let h = handle.clone();
                    move |url| allow_navigation(&h, url)
                })
                .on_new_window({
                    let h = handle.clone();
                    move |url, _| {
                        if allow_navigation(&h, &url) {
                            if let Some(w) = window(&h) {
                                let _ = w.navigate(url);
                            }
                        }
                        NewWindowResponse::Deny
                    }
                })
                .on_download({
                    let h = handle.clone();
                    move |_, event| {
                        if let DownloadEvent::Requested { destination, .. } = event {
                            if let Ok(dir) = h.path().download_dir() {
                                let name = destination.file_name().map(|n| n.to_owned()).unwrap_or_else(|| OsString::from("download"));
                                *destination = unique(dir.join(name));
                            }
                        }
                        true
                    }
                })
                .build()?;
            *app.state::<Shell>().splash.lock().unwrap() = window.url().ok();
            first_autostart(app.handle());
            tray(app.handle())?;
            #[cfg(target_os = "linux")]
            if let Ok(appimage) = std::env::var("APPIMAGE") {
                if let Some(data) = linux::data_home() {
                    let _ = linux::ensure_launcher(&data, &appimage);
                }
                let _ = app.deep_link().register_all();
            }
            // Links: the one this start came with, and every later one.
            if let Ok(Some(urls)) = app.deep_link().get_current() {
                for u in urls {
                    take_link(app.handle(), u.as_str(), false);
                }
            }
            let h = handle.clone();
            app.deep_link().on_open_url(move |event| {
                for u in event.urls() {
                    take_link(&h, u.as_str(), true);
                }
            });
            let h = handle.clone();
            std::thread::spawn(move || supervise(h));
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                // Closing hides the window: AgentNet keeps running in the tray.
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .build(tauri::generate_context!())
        .expect("AgentNet could not start")
        .run(|app, event| match event {
            RunEvent::ExitRequested { .. } | RunEvent::Exit => stop_program(app),
            #[cfg(target_os = "macos")]
            RunEvent::Reopen { .. } => show(app),
            _ => {}
        });
}

fn window(app: &AppHandle) -> Option<WebviewWindow> {
    app.get_webview_window("main")
}

fn show(app: &AppHandle) {
    if let Some(w) = window(app) {
        let _ = w.unminimize();
        let _ = w.show();
        let _ = w.set_focus();
    }
}

fn same_origin(a: &Url, b: &Url) -> bool {
    a.scheme() == b.scheme() && a.host_str() == b.host_str() && a.port_or_known_default() == b.port_or_known_default()
}

/// allow_navigation keeps the window on AgentNet's own pages: the starting
/// page and the program's loopback page. Other web pages open in the
/// system browser; nothing else loads.
fn allow_navigation(app: &AppHandle, url: &Url) -> bool {
    // The first callback can precede build() returning and splash being set.
    // In particular, Windows serves the starting page at http://tauri.localhost.
    if navigation::app_origin(url) {
        if url.query_pairs().any(|(k, _)| k == "retry") {
            signal(app, Signal::Retry);
        }
        return true;
    }
    let shell = app.state::<Shell>();
    if let Some(splash) = shell.splash.lock().unwrap().as_ref() {
        if same_origin(url, splash) {
            if url.query_pairs().any(|(k, _)| k == "retry") {
                signal(app, Signal::Retry); // Try again on the starting page
            }
            return true;
        }
    }
    if let Some(page) = shell.page.lock().unwrap().as_ref() {
        if same_origin(url, page) {
            return true;
        }
    }
    if url.scheme() == "about" {
        return true;
    }
    open_outside(app, url);
    false
}

fn open_outside(app: &AppHandle, url: &Url) {
    if navigation::app_origin(url) {
        return;
    }
    if url.scheme() == "http" || url.scheme() == "https" || url.scheme() == "mailto" {
        let _ = app.opener().open_url(url.as_str(), None::<&str>);
    }
}

/// unique is path, or path with " (2)", " (3)" … when that file exists.
fn unique(path: PathBuf) -> PathBuf {
    if !path.exists() {
        return path;
    }
    let stem = path.file_stem().map(|s| s.to_string_lossy().to_string()).unwrap_or_default();
    let ext = path.extension().map(|e| format!(".{}", e.to_string_lossy())).unwrap_or_default();
    for n in 2..1000 {
        let p = path.with_file_name(format!("{stem} ({n}){ext}"));
        if !p.exists() {
            return p;
        }
    }
    path
}

/// take_link shows a link's destination: in the page when it is known,
/// otherwise once the program announces it. The window comes to the front.
fn take_link(app: &AppHandle, link: &str, raise: bool) {
    let Some(fragment) = deeplink::fragment(link) else {
        return; // not one of AgentNet's links: dropped, never logged
    };
    let shell = app.state::<Shell>();
    let page = shell.page.lock().unwrap().clone();
    match page {
        Some(mut u) if !fragment.is_empty() => {
            u.set_query(None); // the session cookie is set: same page, new destination
            u.set_fragment(Some(&fragment));
            if let Some(w) = window(app) {
                let _ = w.navigate(u);
            }
        }
        None if !fragment.is_empty() => *shell.pending.lock().unwrap() = Some(fragment),
        _ => {}
    }
    if raise {
        show(app);
    }
}

fn signal(app: &AppHandle, s: Signal) {
    if let Some(tx) = app.state::<Shell>().signals.lock().unwrap().as_ref() {
        let _ = tx.send(s);
    }
}

/// on_line acts on one line of the program's stdout.
fn on_line(app: &AppHandle, line: String) {
    match sidecar::parse(&line) {
        Some(sidecar::Event::Page { url, .. }) => {
            let Ok(mut u) = Url::parse(&url) else { return };
            let shell = app.state::<Shell>();
            *shell.last_error.lock().unwrap() = None;
            {
                let mut page = shell.page.lock().unwrap();
                let same = page.as_ref().is_some_and(|p| p.as_str() == u.as_str());
                *page = Some(u.clone());
                if same {
                    return; // the same page and session (setup became the messenger in place)
                }
            }
            if let Some(f) = shell.pending.lock().unwrap().take() {
                u.set_fragment(Some(&f)); // kept across the token's redirect
            }
            if let Some(w) = window(app) {
                let _ = w.navigate(u);
            }
        }
        Some(sidecar::Event::Error { text }) => {
            *app.state::<Shell>().last_error.lock().unwrap() = Some(text.clone());
            show_error(app, &text);
        }
        Some(sidecar::Event::Update { helper, plan, home }) => {
            let h = app.clone();
            std::thread::spawn(move || apply_update(&h, &helper, &plan, &home));
        }
        None => {}
    }
}

/// show_error puts the starting page, with text and Try again, in the window.
fn show_error(app: &AppHandle, text: &str) {
    let shell = app.state::<Shell>();
    let Some(mut u) = shell.splash.lock().unwrap().clone() else { return };
    *shell.page.lock().unwrap() = None;
    let encoded: String = url::form_urlencoded::byte_serialize(text.as_bytes()).collect();
    u.set_query(None);
    u.set_fragment(Some(&format!("error={encoded}")));
    if let Some(w) = window(app) {
        let _ = w.navigate(u);
    }
}

fn log_path(app: &AppHandle) -> PathBuf {
    app.path().app_log_dir().unwrap_or_else(|_| std::env::temp_dir()).join("agentnet.log")
}

/// supervise runs the program for the app's lifetime: restarted after an
/// exit of its own with growing delays, and after too many the window says
/// so and waits for Try again.
fn supervise(app: AppHandle) {
    let (tx, rx) = mpsc::channel();
    *app.state::<Shell>().signals.lock().unwrap() = Some(tx.clone());
    let current = std::env::current_exe().unwrap_or_default();
    let app_exe = sidecar::app_exe(std::env::var_os("APPIMAGE"), &current);
    let mut backoff = sidecar::Backoff::default();
    loop {
        let shell = app.state::<Shell>();
        if shell.quitting.load(Ordering::SeqCst) {
            return;
        }
        let started = sidecar::program().and_then(|program| {
            let h = app.clone();
            let t = tx.clone();
            sidecar::start(&program, &app_exe, log_path(&app), move |line| on_line(&h, line), move || {
                let _ = t.send(Signal::Exited);
            })
        });
        let mut stopped_by_itself = true;
        match started {
            Ok(r) => {
                *shell.running.lock().unwrap() = Some(r);
                loop {
                    match rx.recv() {
                        Ok(Signal::Exited) => break,
                        Ok(Signal::Retry) => {
                            // Try again while it runs (it showed an error): start it afresh.
                            let running = shell.running.lock().unwrap().take();
                            if let Some(mut r) = running {
                                r.stop();
                            }
                            stopped_by_itself = false;
                            backoff.reset();
                        }
                        Ok(Signal::Quit) | Err(_) => return,
                    }
                }
                let running = shell.running.lock().unwrap().take();
                if let Some(mut r) = running {
                    let _ = r.child.wait();
                }
            }
            Err(e) => {
                *shell.last_error.lock().unwrap() = Some(format!("AgentNet could not start its program: {e}"));
            }
        }
        if shell.quitting.load(Ordering::SeqCst) {
            return;
        }
        if !stopped_by_itself {
            continue;
        }
        match backoff.crashed(Instant::now()) {
            Some(delay) => match rx.recv_timeout(delay) {
                Ok(Signal::Quit) | Err(RecvTimeoutError::Disconnected) => return,
                _ => {}
            },
            None => {
                let text = shell.last_error.lock().unwrap().clone().unwrap_or_else(|| {
                    "AgentNet stopped unexpectedly several times. Its log is agentnet.log in the app's log folder.".to_string()
                });
                show_error(&app, &text);
                loop {
                    match rx.recv() {
                        Ok(Signal::Retry) => break,
                        Ok(Signal::Quit) | Err(_) => return,
                        Ok(Signal::Exited) => {}
                    }
                }
                backoff.reset();
            }
        }
    }
}

/// stop_program stops the program once, before the app exits.
fn stop_program(app: &AppHandle) {
    let shell = app.state::<Shell>();
    shell.quitting.store(true, Ordering::SeqCst);
    signal(app, Signal::Quit);
    let running = shell.running.lock().unwrap().take();
    if let Some(mut r) = running {
        r.stop();
    }
}

fn tray(app: &AppHandle) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, "open", "Open AgentNet", true, None::<&str>)?;
    let at_login = autostart_enabled(app);
    let login = CheckMenuItem::with_id(app, "login", "Start when I log in", true, at_login, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "Quit AgentNet", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&open, &login, &PredefinedMenuItem::separator(app)?, &quit])?;
    let login_item = login.clone();
    let mut builder = TrayIconBuilder::with_id("agentnet")
        .tooltip("AgentNet")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(move |app, event| match event.id.as_ref() {
            "open" => show(app),
            "login" => {
                let on = !autostart_enabled(app);
                if set_autostart(app, on).is_ok() {
                    chose_autostart(app);
                }
                let _ = login_item.set_checked(autostart_enabled(app));
            }
            "quit" => {
                let h = app.clone();
                if let Some(w) = window(app) {
                    let _ = w.hide();
                }
                std::thread::spawn(move || {
                    stop_program(&h);
                    h.exit(0);
                });
            }
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click { button: MouseButton::Left, button_state: MouseButtonState::Up, .. } = event {
                show(tray.app_handle());
            }
        });
    if let Some(icon) = app.default_window_icon() {
        builder = builder.icon(icon.clone());
    }
    builder.build(app)?;
    Ok(())
}

fn chose_autostart(app: &AppHandle) {
    if let Ok(dir) = app.path().app_config_dir() {
        let _ = std::fs::create_dir_all(&dir);
        let _ = std::fs::write(dir.join(AUTOSTART_CHOSEN), b"1\n");
    }
}

fn autostart_enabled(_app: &AppHandle) -> bool {
    #[cfg(windows)]
    { win_autostart::is_enabled() }
    #[cfg(not(windows))]
    { _app.autolaunch().is_enabled().unwrap_or(false) }
}

fn set_autostart(_app: &AppHandle, on: bool) -> Result<(), String> {
    #[cfg(windows)]
    { win_autostart::set_enabled(on, AUTOSTART_ARG).map_err(|e| e.to_string()) }
    #[cfg(not(windows))]
    {
        if on { _app.autolaunch().enable() } else { _app.autolaunch().disable() }
            .map_err(|e| e.to_string())
    }
}

/// first_autostart starts AgentNet with the computer from its first run on
/// (the person's later choice in the tray stands).
fn first_autostart(app: &AppHandle) {
    // Repair an enabled entry from the unquoted dependency. A disabled
    // entry and a saved choice stay disabled.
    #[cfg(windows)]
    if autostart_enabled(app) {
        let _ = set_autostart(app, true);
    }
    let Ok(dir) = app.path().app_config_dir() else { return };
    if dir.join(AUTOSTART_CHOSEN).exists() {
        return;
    }
    if set_autostart(app, true).is_ok() {
        chose_autostart(app);
    }
}

// Only the local program's stdout can request an update; web pages have no
// app update command. The helper waits for EOF until the old daemon stops.
fn apply_update(app: &AppHandle, helper: &str, plan: &str, home: &str) {
    use std::process::{Command, Stdio};
    let mut cmd = Command::new(helper);
    cmd.args(["--home", home, "app-update-helper", plan]).stdin(Stdio::piped()).stdout(Stdio::null()).stderr(Stdio::null());
    #[cfg(windows)] { use std::os::windows::process::CommandExt; cmd.creation_flags(0x08000000); }
    match cmd.spawn() {
        Ok(mut child) => {
            let held = child.stdin.take();
            let acknowledged = app.state::<Shell>().running.lock().unwrap().as_mut()
                .is_some_and(|running| running.send_line("app-update-ready").is_ok());
            if !acknowledged { let _ = child.kill(); let _ = child.wait(); return; }
            // Let the HTTP response reach the page before stopping its program.
            std::thread::sleep(std::time::Duration::from_millis(500));
            stop_program(app);
            // Keep the pipe alive until process exit: otherwise the new app
            // could start while single-instance still sees this old shell.
            std::mem::forget(held);
            app.exit(0);
        }
        Err(_) => {
            if let Some(running) = app.state::<Shell>().running.lock().unwrap().as_mut() {
                let _ = running.send_line("app-update-failed");
            }
        },
    }
}
