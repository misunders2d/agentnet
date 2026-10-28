//! The AgentNet app: a window and a tray icon around the agentnet program
//! (`agentnet app`), which does all of AgentNet's work and serves the page
//! the window shows on this computer's loopback address. The shell starts
//! with the computer (hidden in the tray), keeps one instance, hands
//! agentnet:// links to the window and stops the program on Quit. The main
//! loopback page has one guarded native clipboard-image read command.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod deeplink;
mod clipboard;
mod droppedfiles;
mod skinfolder;
mod download;
mod appcontrols;
#[cfg(target_os = "linux")]
mod linux;
mod navigation;
mod page_event;
mod sidecar;
mod unread;
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
use tauri_plugin_dialog::DialogExt;

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
    app_calls: Mutex<std::collections::HashMap<u64, Sender<(u16, String)>>>,
    app_call_id: std::sync::atomic::AtomicU64,
    downloads: Mutex<std::collections::HashMap<String, OsString>>,
    download_destinations: Mutex<std::collections::HashMap<String, PathBuf>>,
    page: Mutex<Option<Url>>,       // the page the program announced last (with its token)
    splash: Mutex<Option<Url>>,     // the window's own starting page
    pending: Mutex<Option<String>>, // a link's fragment that came before the page
    running: Mutex<Option<sidecar::Running>>,
    signals: Mutex<Option<Sender<Signal>>>,
    last_error: Mutex<Option<String>>,
    unread: Mutex<unread::State>,
    drops: Mutex<droppedfiles::Drops>,
    quitting: AtomicBool,
}

fn main() {
    #[cfg(target_os = "linux")]
    let installation = install_appimage();
    let hidden = std::env::args().any(|a| a == AUTOSTART_ARG);
    tauri::Builder::default()
        // First: a second start hands its link to this one and shows the window.
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| show(app)))
        .plugin(tauri_plugin_deep_link::init())
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, Some(vec![AUTOSTART_ARG])))
        .plugin(tauri_plugin_opener::Builder::new().open_js_links_on_click(false).build())
        .plugin(tauri_plugin_clipboard_manager::init())
        .plugin(tauri_plugin_dialog::init())
        .invoke_handler(tauri::generate_handler![clipboard::agentnet_clipboard_image, droppedfiles::agentnet_dropped_file, skinfolder::agentnet_skin_folder, download::agentnet_download_blob, download::agentnet_cancel_download, appcontrols::agentnet_app_controls])
        .manage(Shell::default())
        .setup(move |app| {
            #[cfg(target_os = "linux")]
            if let Err(problem) = &installation {
                let handle = app.handle().clone();
                app.dialog().message(format!("AgentNet could not install its app. {problem}\nYour download and app data have been kept."))
                    .title("AgentNet installation needs attention").show(move |_| handle.exit(1));
                return Ok(());
            }
            let handle = app.handle().clone();
            let builder = WebviewWindowBuilder::new(app, "main", WebviewUrl::App("index.html".into()))
                .initialization_script(include_str!("clipboard.js"))
                .initialization_script(if cfg!(target_os = "linux") { include_str!("droppedfiles.js") } else { "" })
                .initialization_script(if cfg!(target_os = "linux") { concat!("window.__agentnetLinuxDownloads=true;", include_str!("download.js")) } else { include_str!("download.js") });
            // Windows needs HTML5 handling. Linux WebKit supplies URI strings
            // instead of File objects, so use its trusted native OS drop.
            #[cfg(not(target_os = "linux"))]
            let builder = builder.disable_drag_drop_handler();
            let window = builder
                .title("AgentNet")
                .inner_size(1200.0, 800.0)
                .min_inner_size(380.0, 600.0)
                .visible(!hidden)
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
                        if let DownloadEvent::Requested { url, destination } = event {
                            let requested_name = h.state::<Shell>().downloads.lock().unwrap().remove(url.as_str());
                            if let Ok(dir) = h.path().download_dir() {
                                let name = requested_name
                                    .or_else(|| destination.file_name().map(|n| n.to_owned())).unwrap_or_else(|| OsString::from("download"));
                                // Reserve before the native downloader creates
                                // the file: simultaneous clicks must not select
                                // the same as-yet nonexistent destination.
                                let shell = h.state::<Shell>();
                                let mut reserved = shell.download_destinations.lock().unwrap();
                                let Some(available) = unique(dir.join(name), &reserved) else {
                                    if let Some(window) = h.get_webview_window("main") {
                                        download::notice(&window, url.as_str(), "", "failed", None);
                                    }
                                    return false;
                                };
                                *destination = available;
                                reserved.insert(url.to_string(), destination.clone());
                            }
                            if let Some(window) = h.get_webview_window("main") {
                                download::notice(&window, url.as_str(), &destination.file_name().unwrap_or_default().to_string_lossy(), "starting", None);
                            }
                        } else if let DownloadEvent::Finished { url, success, .. } = event {
                            h.state::<Shell>().downloads.lock().unwrap().remove(url.as_str());
                            h.state::<Shell>().download_destinations.lock().unwrap().remove(url.as_str());
                            if let Some(window) = h.get_webview_window("main") {
                                download::notice(&window, url.as_str(), "", if success { "saved" } else { "failed" }, None);
                            }
                        }
                        true
                    }
                })
                .build()?;
            #[cfg(target_os = "linux")]
            droppedfiles::install(&window);
            *app.state::<Shell>().splash.lock().unwrap() = window.url().ok();
            #[cfg(target_os = "linux")]
            if let Ok(Some(previous)) = &installation {
                if linux::owns_autostart(previous) {
                    let _ = set_autostart(app.handle(), true);
                }
            }
            first_autostart(app.handle());
            tray(app.handle())?;
            #[cfg(target_os = "linux")]
            if let Ok(appimage) = std::env::var("APPIMAGE") {
                if let Some(data) = linux::data_home() {
                    let previous = installation.as_ref().ok().and_then(|p| p.as_deref());
                    let _ = linux::ensure_launcher(&data, &appimage, previous);
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

// Finish the portable package's installation before any plugin registers a
// launcher, login entry or link handler. Reopening the verified stable copy
// also makes the backend's normal app-exe registration and updater agree.
#[cfg(target_os = "linux")]
fn install_appimage() -> Result<Option<String>, String> {
    use std::os::unix::process::CommandExt;
    use std::process::Command;
    const PREVIOUS: &str = "AGENTNET_APPIMAGE_PREVIOUS";
    let previous = std::env::var(PREVIOUS).ok();
    std::env::remove_var(PREVIOUS);
    let Some(source) = std::env::var_os("APPIMAGE").filter(|s| !s.is_empty()) else { return Ok(None) };
    let data = linux::data_home().ok_or("Cannot locate your application data directory.")?;
    let program = sidecar::program().map_err(|e| e.to_string())?;
    let output = Command::new(program).arg("app-install").arg(&source).arg(data)
        .output().map_err(|e| e.to_string())?;
    if !output.status.success() {
        return Err(String::from_utf8_lossy(&output.stderr).trim().to_owned());
    }
    let installed: String = serde_json::from_slice(&output.stdout).map_err(|e| e.to_string())?;
    if std::path::Path::new(&installed) == std::path::Path::new(&source) {
        return Ok(previous);
    }
    let mut cmd = Command::new(installed);
    cmd.args(std::env::args_os().skip(1)).env(PREVIOUS, &source);
    let mount = std::env::var("APPDIR").ok();
    for (k, v) in sidecar::clean_env(std::env::vars_os(), mount.as_deref()) {
        match v { Some(v) => { cmd.env(k, v); }, None => { cmd.env_remove(k); } }
    }
    cmd.env_remove("APPIMAGE").env_remove("APPDIR");
    // Extract-and-run can launch from a temporary mount that disappears.
    cmd.current_dir("/");
    Err(format!("Could not open the installed app: {}", cmd.exec()))
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
fn unique(path: PathBuf, reserved: &std::collections::HashMap<String, PathBuf>) -> Option<PathBuf> {
    let taken = |candidate: &PathBuf| candidate.exists() || reserved.values().any(|p| p == candidate);
    if !taken(&path) {
        return Some(path);
    }
    let stem = path.file_stem().map(|s| s.to_string_lossy().to_string()).unwrap_or_default();
    let ext = path.extension().map(|e| format!(".{}", e.to_string_lossy())).unwrap_or_default();
    for n in 2..1000 {
        let p = path.with_file_name(format!("{stem} ({n}){ext}"));
        if !taken(&p) {
            return Some(p);
        }
    }
    None
}

#[cfg(test)]
mod download_destination_tests {
    use super::*;
    #[test]
    fn independent_click_leases_reserve_every_active_destination() {
        let path = std::env::temp_dir().join(format!("agentnet-leases-{}.txt", std::process::id()));
        let mut reserved = std::collections::HashMap::new();
        for click in 1..=3 {
            let destination = unique(path.clone(), &reserved).unwrap();
            assert!(!reserved.values().any(|p| p == &destination));
            reserved.insert(format!("blob:host/click-{click}"), destination);
        }
        assert_eq!(reserved.len(), 3);
        reserved.remove("blob:host/click-2");
        let next = unique(path.clone(), &reserved).unwrap();
        assert_ne!(next, reserved["blob:host/click-1"]);
        assert_ne!(next, reserved["blob:host/click-3"]);
        assert_eq!(next, path.with_file_name(format!("agentnet-leases-{} (2).txt", std::process::id())));
    }
    #[test]
    fn exhausted_names_never_overwrite_an_existing_file() {
        let root = std::env::temp_dir().join(format!("agentnet-download-names-{}", std::process::id()));
        std::fs::create_dir_all(&root).unwrap();
        let path = root.join("result.txt");
        std::fs::write(&path, b"original").unwrap();
        let mut reserved = std::collections::HashMap::new();
        for n in 2..1000 {
            let name = root.join(format!("result ({n}).txt"));
            std::fs::write(&name, b"earlier").unwrap();
        }
        assert_eq!(unique(path.clone(), &reserved), None);
        assert_eq!(std::fs::read(&path).unwrap(), b"original");
        let spare = root.join("result (999).txt");
        std::fs::remove_file(&spare).unwrap();
        assert_eq!(unique(path.clone(), &reserved), Some(spare.clone()));
        reserved.insert("active".into(), spare);
        assert_eq!(unique(path, &reserved), None);
        std::fs::remove_dir_all(root).unwrap();
    }
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
            if let Some(w) = window(app) {
                if let Some(script) = deeplink::navigation_script(&fragment) {
                    let _ = w.eval(&script); // preserve the warm page until host validation
                } else {
                    u.set_query(None); // the session cookie is set: same page, new destination
                    u.set_fragment(Some(&fragment));
                    let _ = w.navigate(u);
                }
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

/// Reset the sequence even for a repeated Page URL: Go starts a new epoch.
fn reset_unread(app: &AppHandle, page: bool) {
    let shell = app.state::<Shell>();
    {
        let mut state = shell.unread.lock().unwrap_or_else(|e| e.into_inner());
        if page && !shell.quitting.load(Ordering::SeqCst) {
            state.page();
        } else {
            state.reset();
        }
    }
    refresh_unread(app);
}

/// Native calls run on the main thread, with no Shell lock held. Read the
/// latest value there so an already queued update cannot restore an old badge
/// after a reset. Unsupported platform calls or OS failures remain nonfatal.
fn refresh_unread(app: &AppHandle) {
    let h = app.clone();
    let _ = app.run_on_main_thread(move || {
        let value = {
            let shell = h.state::<Shell>();
            let state = shell.unread.lock().unwrap_or_else(|e| e.into_inner());
            state.value()
        };
        let original = h.default_window_icon();
        let indicator = original.and_then(|icon| {
            unread::icon_rgba(icon.rgba(), icon.width(), icon.height(), value.total > 0)
                .map(|rgba| tauri::image::Image::new_owned(rgba, icon.width(), icon.height()))
        });
        if let Some(tray) = h.tray_by_id("agentnet") {
            if let Some(icon) = indicator.clone() {
                let _ = tray.set_icon(Some(icon));
            } else if let Some(icon) = original {
                let _ = tray.set_icon(Some(icon.clone()));
            }
            // Linux does not support this tooltip; its supported surface is
            // the tray dot, not a promised universal launcher count.
            let _ = tray.set_tooltip(Some(value.tooltip()));
            #[cfg(target_os = "macos")]
            { let _ = tray.set_title(value.label().as_deref()); }
        }
        if let Some(w) = window(&h) {
            #[cfg(target_os = "windows")]
            {
                let overlay = unread::overlay_rgba(value.total > 0).map(|rgba|
                    tauri::image::Image::new_owned(rgba, unread::OVERLAY_SIDE, unread::OVERLAY_SIDE));
                let _ = w.set_overlay_icon(overlay);
            }
            #[cfg(target_os = "macos")]
            {
                let _ = w.set_badge_count(value.count());
                // A partial aggregate retains estimates; never present it as
                // an exact count. Labels also retain totals beyond i64::MAX.
                let _ = w.set_badge_label(value.label());
            }
            #[cfg(not(any(target_os = "windows", target_os = "macos")))]
            let _ = w;
        }
    });
}

/// on_line acts on one line of the program's stdout.
fn on_line(app: &AppHandle, line: String) {
    match sidecar::parse(&line) {
        Some(sidecar::Event::Page { mode, url }) => {
            let Ok(mut u) = Url::parse(&url) else { return };
            reset_unread(app, true); // BEFORE the same-URL navigation return
            let shell = app.state::<Shell>();
            *shell.last_error.lock().unwrap() = None;
            {
                let mut page = shell.page.lock().unwrap();
                let navigate = page_event::should_navigate(&mode, page.as_ref().map(Url::as_str), u.as_str());
                *page = Some(u.clone());
                if !navigate {
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
            reset_unread(app, false);
            *app.state::<Shell>().last_error.lock().unwrap() = Some(text.clone());
            show_error(app, &text);
        }
        Some(sidecar::Event::Update { helper, plan, home }) => {
            let h = app.clone();
            std::thread::spawn(move || apply_update(&h, &helper, &plan, &home));
        }
        Some(sidecar::Event::Appreply { call_id, status, body }) => {
            if let Some(reply) = app.state::<Shell>().app_calls.lock().unwrap().remove(&call_id) { let _ = reply.send((status, body)); }
        }
        Some(sidecar::Event::Unread { seq, total, partial, .. }) => {
            let accepted = {
                let shell = app.state::<Shell>();
                let mut state = shell.unread.lock().unwrap_or_else(|e| e.into_inner());
                !shell.quitting.load(Ordering::SeqCst) && state.accept(seq, total, partial)
            };
            if accepted { refresh_unread(app); }
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
        reset_unread(&app, false); // initial start and every daemon restart
        let started = sidecar::program().and_then(|program| {
            let h = app.clone();
            let exited = app.clone();
            let t = tx.clone();
            sidecar::start(&program, &app_exe, log_path(&app), move |line| on_line(&h, line), move || {
                reset_unread(&exited, false);
                let _ = t.send(Signal::Exited);
            })
        });
        let mut stopped_by_itself = true;
        match started {
            Ok(r) => {
                *shell.running.lock().unwrap() = Some(r);
                loop {
                    match rx.recv() {
                        Ok(Signal::Exited) => { shell.app_calls.lock().unwrap().clear(); break; },
                        Ok(Signal::Retry) => {
                            reset_unread(&app, false);
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
    reset_unread(app, false);
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
    // Linux entries can be customized outside the app. Migration already
    // refreshed an exact owned entry; do not replace any other existing one.
    #[cfg(target_os = "linux")]
    if std::env::var_os("APPIMAGE").is_some() && linux::has_autostart_entry() {
        chose_autostart(app);
        return;
    }
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
