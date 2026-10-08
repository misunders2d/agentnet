//! Linux, run from an AppImage: one visible entry in the app launcher
//! (~/.local/share/applications/agentnet.desktop) that starts the AppImage
//! file, installed in the person's data directory before startup. Starting at login
//! and the agentnet:// handler come from their plugins (both use the
//! AppImage's path). Packages (.deb, .rpm) bring their own entry.

use std::fs;
use std::io::Write;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};

const ICON: &[u8] = include_bytes!("../icons/128x128.png");

/// The launcher entry for the AppImage at appimage, with the icon at icon.
pub fn desktop_entry(appimage: &str, icon: &Path) -> String {
    format!(
        "[Desktop Entry]\nType=Application\nName=AgentNet\nComment=Chat with people and their agents\nExec=\"{}\" %U\nIcon={}\nTerminal=false\nCategories=Network;Chat;\nStartupWMClass=agentnet-app\n",
        appimage.replace('\\', "\\\\").replace('"', "\\\""),
        icon.display()
    )
}

/// ensure_launcher writes the entry and icon under data (the person's
/// XDG data directory) when missing, or still exactly ours before installation.
pub fn ensure_launcher(data: &Path, appimage: &str, previous: Option<&str>) -> std::io::Result<PathBuf> {
    let icon = data.join("icons/hicolor/128x128/apps/agentnet.png");
    let entry = data.join("applications/agentnet.desktop");
    let want = desktop_entry(appimage, &icon);
    // Only our exact previous entry may move with an installation. A changed
    // command, custom entry or symlink still belongs to the person who made it.
    match fs::symlink_metadata(&entry) {
        Ok(st) => {
            if !st.is_file() { return Ok(entry); }
            let text = fs::read_to_string(&entry)?;
            if text != want && !previous.is_some_and(|p| text == desktop_entry(p, &icon)) {
                return Ok(entry);
            }
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {},
        Err(e) => return Err(e),
    }
    if fs::symlink_metadata(&icon).is_ok_and(|s| !s.is_file()) { return Ok(entry); }
    if fs::read(&icon).ok().as_deref() != Some(ICON) {
        fs::create_dir_all(icon.parent().unwrap())?;
        fs::write(&icon, ICON)?;
    }
    if fs::read_to_string(&entry).ok().as_deref() != Some(want.as_str()) {
        fs::create_dir_all(entry.parent().unwrap())?;
        let tmp = entry.with_extension(format!("desktop.{}.tmp", std::process::id()));
        let mut file = fs::OpenOptions::new().write(true).create_new(true).mode(0o600).open(&tmp)?;
        let result = (|| {
            file.write_all(want.as_bytes())?;
            file.sync_all()?;
            fs::rename(&tmp, &entry)
        })();
        let _ = fs::remove_file(&tmp);
        result?;
    }
    Ok(entry)
}

/// Refresh an enabled login entry only when it is exactly the one the
/// existing autostart plugin wrote for the prior package. Disabled/custom
/// entries are preserved. The plugin still writes the new entry itself.
pub fn owns_autostart(previous: &str) -> bool {
    // auto-launch 0.6 uses ~/.config even when XDG_CONFIG_HOME is set.
    let config = std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".config"));
    config.is_some_and(|config| owns_autostart_at(&config.join("autostart/AgentNet.desktop"), previous))
}

fn owns_autostart_at(entry: &Path, previous: &str) -> bool {
    let want = format!("[Desktop Entry]\nType=Application\nVersion=1.0\nName=AgentNet\nComment=AgentNet startup script\nExec={previous} --autostart\nStartupNotify=false\nTerminal=false");
    fs::symlink_metadata(entry).is_ok_and(|s| s.is_file()) && fs::read_to_string(entry).ok().as_deref() == Some(&want)
}

/// The person's data directory: $XDG_DATA_HOME, else ~/.local/share.
pub fn data_home() -> Option<PathBuf> {
    match std::env::var_os("XDG_DATA_HOME") {
        Some(d) if !d.is_empty() => Some(PathBuf::from(d)),
        _ => std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".local/share")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn launcher_follows_the_appimage() {
        let data = std::env::temp_dir().join(format!("agentnet-launcher-{}", std::process::id()));
        let _ = fs::remove_dir_all(&data);
        let entry = ensure_launcher(&data, "/home/a/Apps/AgentNet.AppImage", None).unwrap();
        let text = fs::read_to_string(&entry).unwrap();
        assert!(text.contains("Exec=\"/home/a/Apps/AgentNet.AppImage\" %U"), "{text}");
        assert!(!text.contains("MimeType"), "the plugin's own entry handles agentnet://");
        assert!(data.join("icons/hicolor/128x128/apps/agentnet.png").exists());
        ensure_launcher(&data, "/home/a/Downloads/AgentNet.AppImage", Some("/home/a/Apps/AgentNet.AppImage")).unwrap();
        assert!(fs::read_to_string(&entry).unwrap().contains("/home/a/Downloads/AgentNet.AppImage"));
        let _ = fs::remove_dir_all(&data);
    }

    #[test]
    fn custom_launcher_and_disabled_login_are_preserved() {
        let data = std::env::temp_dir().join(format!("agentnet-custom-launcher-{}", std::process::id()));
        let _ = fs::remove_dir_all(&data);
        let source = "/home/a/Downloads/AgentNet.AppImage";
        let entry = ensure_launcher(&data, source, None).unwrap();
        let custom = fs::read_to_string(&entry).unwrap().replace(" %U", " --custom %U");
        fs::write(&entry, &custom).unwrap();
        ensure_launcher(&data, "/stable/AgentNet.AppImage", Some(source)).unwrap();
        assert_eq!(fs::read_to_string(&entry).unwrap(), custom);
        let target = data.join("custom.desktop");
        fs::write(&target, &custom).unwrap();
        fs::remove_file(&entry).unwrap();
        std::os::unix::fs::symlink(&target, &entry).unwrap();
        ensure_launcher(&data, "/stable/AgentNet.AppImage", Some(source)).unwrap();
        assert!(fs::symlink_metadata(&entry).unwrap().file_type().is_symlink());
        assert_eq!(fs::read_to_string(&target).unwrap(), custom);
        assert!(!owns_autostart_at(&data.join("absent.desktop"), source));
        let login = data.join("login.desktop");
        let owned = format!("[Desktop Entry]\nType=Application\nVersion=1.0\nName=AgentNet\nComment=AgentNet startup script\nExec={source} --autostart\nStartupNotify=false\nTerminal=false");
        fs::write(&login, &owned).unwrap();
        assert!(owns_autostart_at(&login, source));
        fs::write(&login, format!("{owned}\nHidden=true")).unwrap();
        assert!(!owns_autostart_at(&login, source));
        let _ = fs::remove_dir_all(&data);
    }
}
