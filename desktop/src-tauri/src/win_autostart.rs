//! Windows login startup: the existing per-user Run entry, with an
//! explicitly quoted executable path (including user names with spaces).

#[cfg(windows)]
const RUN_KEY: &str = r"HKCU\Software\Microsoft\Windows\CurrentVersion\Run";
#[cfg(windows)]
const NAME: &str = "AgentNet"; // productName; NSIS removes this entry on uninstall

fn command_value(exe: &std::path::Path, argument: &str) -> String {
    format!("\"{}\" {argument}", exe.display())
}

#[cfg(windows)]
fn reg(args: &[&str]) -> std::io::Result<std::process::Output> {
    use std::os::windows::process::CommandExt;
    // Resolve the system tool explicitly, independently of the person's PATH.
    let system = std::env::var_os("SystemRoot").ok_or_else(|| std::io::Error::other("no SystemRoot"))?;
    std::process::Command::new(std::path::Path::new(&system).join("System32/reg.exe"))
        .args(args)
        .creation_flags(0x0800_0000) // CREATE_NO_WINDOW
        .output()
}

#[cfg(windows)]
pub fn is_enabled() -> bool {
    reg(&["query", RUN_KEY, "/v", NAME]).is_ok_and(|o| o.status.success())
}

#[cfg(windows)]
pub fn set_enabled(on: bool, argument: &str) -> std::io::Result<()> {
    let result = if on {
        let value = command_value(&std::env::current_exe()?, argument);
        reg(&["add", RUN_KEY, "/v", NAME, "/t", "REG_SZ", "/d", &value, "/f"])?
    } else {
        if !is_enabled() {
            return Ok(());
        }
        reg(&["delete", RUN_KEY, "/v", NAME, "/f"])?
    };
    if result.status.success() {
        Ok(())
    } else {
        Err(std::io::Error::other("could not change AgentNet's login startup entry"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn login_command_quotes_the_whole_executable() {
        for exe in [r"C:\Users\Bohdan Name\AppData\Local\AgentNet\agentnet-app.exe", r"C:\apps\AgentNet\agentnet-app.exe"] {
            assert_eq!(command_value(std::path::Path::new(exe), "--autostart"), format!("\"{exe}\" --autostart"));
        }
    }

    #[cfg(windows)]
    #[test]
    fn user_path_updates_preserve_other_entries() {
        // Exercise the exact installer helper without touching the registry.
        let script = format!("& {{ {} }} -SelfTest", include_str!("../windows/path.ps1"));
        let out = std::process::Command::new("powershell.exe")
            .args(["-NoProfile", "-NonInteractive", "-Command", &script])
            .output().unwrap();
        assert!(out.status.success(), "{}{}", String::from_utf8_lossy(&out.stdout), String::from_utf8_lossy(&out.stderr));
    }
}
