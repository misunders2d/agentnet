//! The agentnet program the app runs (`agentnet app`, cmd/agentnet/app.go):
//! started next to this shell's own executable, stopped by "quit" on its
//! stdin (or closing it), restarted with growing delays when it exits on
//! its own. Its stdout carries JSON events with the page's address (and
//! token), so it is never logged; its stderr is its log.

use serde::Deserialize;
use std::ffi::OsString;
use std::fs::{self, OpenOptions};
use std::io::{BufRead, BufReader, Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::time::{Duration, Instant};

/// Shell tunables (code constants, not settings).
pub const STOP_WAIT: Duration = Duration::from_secs(10); // after "quit", before the program is killed
pub const RESTART_DELAYS: [Duration; 5] = [
    Duration::from_secs(1),
    Duration::from_secs(2),
    Duration::from_secs(5),
    Duration::from_secs(10),
    Duration::from_secs(30),
];
pub const CRASH_WINDOW: Duration = Duration::from_secs(5 * 60); // crashes counted within it
pub const MAX_CRASHES: usize = 5; // in CRASH_WINDOW: then the window shows the error and Try again
pub const MAX_LOG: u64 = 5 << 20; // agentnet.log is rotated to agentnet.log.1 beyond this
pub const STOP_LINE: &str = "quit";

/// One line of the program's stdout.
#[derive(Debug, Clone, PartialEq, Deserialize)]
#[serde(tag = "event", rename_all = "lowercase")]
pub enum Event {
    Page { mode: String, url: String },
    Error { text: String },
    Update { helper: String, plan: String, home: String },
}

/// parse reads one stdout line; anything else is ignored.
pub fn parse(line: &str) -> Option<Event> {
    let e: Event = serde_json::from_str(line.trim()).ok()?;
    if let Event::Page { url, .. } = &e {
        // Only the program's own loopback page is ever shown.
        let u = url::Url::parse(url).ok()?;
        let host = u.host_str()?;
        if u.scheme() != "http" || !(host == "127.0.0.1" || host == "localhost" || host == "[::1]") {
            return None;
        }
    }
    Some(e)
}

/// The program next to this executable (the bundle puts it there).
pub fn program() -> std::io::Result<PathBuf> {
    let exe = std::env::current_exe()?;
    let dir = exe.parent().ok_or_else(|| std::io::Error::other("no directory"))?;
    Ok(dir.join(if cfg!(windows) { "agentnet.exe" } else { "agentnet" }))
}

/// The app as the person starts it: the AppImage file itself when run from
/// one (never its temporary mount), otherwise this executable.
pub fn app_exe(appimage: Option<OsString>, current: &Path) -> OsString {
    match appimage {
        Some(p) if !p.is_empty() => p,
        _ => current.as_os_str().to_owned(),
    }
}

/// The environment the program gets: an AppImage's runtime points library
/// and data variables into its mount, which the person's coding agents,
/// started by the program, must not inherit. Variables naming appdir are
/// dropped; list variables keep their other entries.
pub fn clean_env(vars: impl Iterator<Item = (OsString, OsString)>, appdir: Option<&str>) -> Vec<(OsString, Option<OsString>)> {
    let Some(appdir) = appdir.filter(|d| !d.is_empty()) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for (k, v) in vars {
        let value = v.to_string_lossy().to_string();
        if !value.contains(appdir) {
            continue;
        }
        let key = k.to_string_lossy().to_string();
        let kept: Vec<&str> = value.split(':').filter(|e| !e.is_empty() && !e.contains(appdir)).collect();
        let listish = key == "PATH" || key.ends_with("PATH") || key.ends_with("_DIRS");
        if listish && !kept.is_empty() {
            out.push((k, Some(OsString::from(kept.join(":")))));
        } else {
            out.push((k, None));
        }
    }
    out
}

/// Backoff counts unexpected exits: the delay before the next start, and
/// whether there were too many lately to keep trying on its own.
#[derive(Default)]
pub struct Backoff {
    crashes: Vec<Instant>,
}

impl Backoff {
    /// crashed records an exit at now and returns the delay before the
    /// next start, or None when the window should show the error instead.
    pub fn crashed(&mut self, now: Instant) -> Option<Duration> {
        self.crashes.retain(|t| now.duration_since(*t) < CRASH_WINDOW);
        self.crashes.push(now);
        if self.crashes.len() >= MAX_CRASHES {
            return None;
        }
        Some(RESTART_DELAYS[(self.crashes.len() - 1).min(RESTART_DELAYS.len() - 1)])
    }

    /// reset forgets earlier exits (the person pressed Try again).
    pub fn reset(&mut self) {
        self.crashes.clear();
    }
}

/// A running program: its stdin (the stop request) and the process.
pub struct Running {
    pub child: Child,
    stdin: Option<ChildStdin>,
}

impl Running {
    pub fn send_line(&mut self, line: &str) -> std::io::Result<()> {
        let stdin = self.stdin.as_mut().ok_or_else(|| std::io::Error::other("program input closed"))?;
        writeln!(stdin, "{line}")?;
        stdin.flush()
    }

    /// stop asks the program to quit, waits up to STOP_WAIT, then kills it.
    pub fn stop(&mut self) {
        if let Some(mut s) = self.stdin.take() {
            let _ = s.write_all(format!("{STOP_LINE}\n").as_bytes());
            drop(s); // EOF too
        }
        let deadline = Instant::now() + STOP_WAIT;
        while Instant::now() < deadline {
            if let Ok(Some(_)) = self.child.try_wait() {
                return;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// start runs `agentnet app` with the app's variables; on_line gets each
/// stdout line, on_exit runs when stdout ends (the program exited), and
/// stderr is appended to log.
pub fn start(
    program: &Path,
    app_exe: &OsString,
    log: PathBuf,
    on_line: impl Fn(String) + Send + 'static,
    on_exit: impl FnOnce() + Send + 'static,
) -> std::io::Result<Running> {
    let mut cmd = Command::new(program);
    cmd.arg("app")
        .env("AGENTNET_APP", "1")
        .env("AGENTNET_APP_EXE", app_exe)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let appdir = std::env::var("APPDIR").ok();
    for (k, v) in clean_env(std::env::vars_os(), appdir.as_deref()) {
        match v {
            Some(v) => cmd.env(k, v),
            None => cmd.env_remove(k),
        };
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        cmd.creation_flags(CREATE_NO_WINDOW);
    }
    let mut child = cmd.spawn()?;
    let stdin = child.stdin.take();
    let out = child.stdout.take();
    std::thread::spawn(move || {
        if let Some(out) = out {
            for line in BufReader::new(out).lines() {
                match line {
                    Ok(l) => on_line(l),
                    Err(_) => break,
                }
            }
        }
        on_exit();
    });
    if let Some(mut err) = child.stderr.take() {
        std::thread::spawn(move || {
            let mut buf = [0u8; 8192];
            loop {
                match err.read(&mut buf) {
                    Ok(0) | Err(_) => break,
                    Ok(n) => append_log(&log, &buf[..n]),
                }
            }
        });
    }
    Ok(Running { child, stdin })
}

/// append_log writes the program's own log lines, rotating at MAX_LOG.
pub fn append_log(path: &Path, data: &[u8]) {
    if let Ok(meta) = fs::metadata(path) {
        if meta.len() > MAX_LOG {
            let _ = fs::rename(path, path.with_extension("log.1"));
        }
    }
    if let Some(dir) = path.parent() {
        let _ = fs::create_dir_all(dir);
    }
    if let Ok(mut f) = OpenOptions::new().create(true).append(true).open(path) {
        let _ = f.write_all(data);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn events_parse() {
        assert_eq!(
            parse(r#"{"event":"page","mode":"setup","url":"http://127.0.0.1:17443/?t=abc"}"#),
            Some(Event::Page { mode: "setup".into(), url: "http://127.0.0.1:17443/?t=abc".into() })
        );
        assert_eq!(parse(r#"{"event":"error","text":"newer"}"#), Some(Event::Error { text: "newer".into() }));
        for junk in [
            "",
            "profile noise",
            r#"{"event":"page","mode":"daemon","url":"https://evil.example/"}"#,
            r#"{"event":"page","mode":"daemon","url":"http://evil.example:17443/"}"#,
            r#"{"event":"page","mode":"daemon","url":"file:///etc/passwd"}"#,
            r#"{"event":"other"}"#,
        ] {
            assert_eq!(parse(junk), None, "{junk}");
        }
    }

    #[test]
    fn backoff_grows_then_gives_up() {
        let mut b = Backoff::default();
        let t = Instant::now();
        assert_eq!(b.crashed(t), Some(RESTART_DELAYS[0]));
        assert_eq!(b.crashed(t), Some(RESTART_DELAYS[1]));
        assert_eq!(b.crashed(t), Some(RESTART_DELAYS[2]));
        assert_eq!(b.crashed(t), Some(RESTART_DELAYS[3]));
        assert_eq!(b.crashed(t), None, "too many in the window");
        b.reset();
        assert_eq!(b.crashed(t), Some(RESTART_DELAYS[0]));
        // Exits long ago no longer count.
        let mut c = Backoff::default();
        for _ in 0..4 {
            c.crashed(t);
        }
        assert_eq!(c.crashed(t + CRASH_WINDOW + Duration::from_secs(1)), Some(RESTART_DELAYS[0]));
    }

    #[test]
    fn app_exe_prefers_the_appimage() {
        let cur = Path::new("/tmp/.mount_AgentXYZ/usr/bin/agentnet-app");
        assert_eq!(app_exe(Some("/home/a/Apps/AgentNet.AppImage".into()), cur), OsString::from("/home/a/Apps/AgentNet.AppImage"));
        assert_eq!(app_exe(Some("".into()), cur), cur.as_os_str().to_owned());
        assert_eq!(app_exe(None, cur), cur.as_os_str().to_owned());
    }

    #[test]
    fn appimage_variables_are_not_inherited() {
        let dir = "/tmp/.mount_AgentXYZ";
        let vars = vec![
            ("PATH", "/tmp/.mount_AgentXYZ/usr/bin:/usr/bin:/bin"),
            ("XDG_DATA_DIRS", "/tmp/.mount_AgentXYZ/usr/share:/usr/share"),
            ("GTK_PATH", "/tmp/.mount_AgentXYZ/usr/lib/gtk-3.0"),
            ("LD_LIBRARY_PATH", "/tmp/.mount_AgentXYZ/usr/lib"),
            ("APPDIR", "/tmp/.mount_AgentXYZ"),
            ("HOME", "/home/a"),
            ("APPIMAGE", "/home/a/AgentNet.AppImage"),
        ]
        .into_iter()
        .map(|(k, v)| (OsString::from(k), OsString::from(v)));
        let mut got: Vec<(String, Option<String>)> = clean_env(vars, Some(dir))
            .into_iter()
            .map(|(k, v)| (k.to_string_lossy().into(), v.map(|v| v.to_string_lossy().into())))
            .collect();
        got.sort();
        assert_eq!(
            got,
            vec![
                ("APPDIR".into(), None),
                ("GTK_PATH".into(), None),
                ("LD_LIBRARY_PATH".into(), None),
                ("PATH".into(), Some("/usr/bin:/bin".into())),
                ("XDG_DATA_DIRS".into(), Some("/usr/share".into())),
            ]
        );
        assert!(clean_env(std::iter::empty(), None).is_empty());
    }

    #[test]
    fn log_rotates() {
        let dir = std::env::temp_dir().join(format!("agentnet-log-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let log = dir.join("agentnet.log");
        append_log(&log, &vec![b'x'; (MAX_LOG + 1) as usize]);
        append_log(&log, b"next\n");
        assert_eq!(fs::read(&log).unwrap(), b"next\n");
        assert_eq!(fs::metadata(dir.join("agentnet.log.1")).unwrap().len(), MAX_LOG + 1);
        let _ = fs::remove_dir_all(&dir);
    }
}
