//! Linux, run from an AppImage: one visible entry in the app launcher
//! (~/.local/share/applications/agentnet.desktop) that starts the AppImage
//! file, written on start and again when the file moved. Starting at login
//! and the agentnet:// handler come from their plugins (both use the
//! AppImage's path). Packages (.deb, .rpm) bring their own entry.

use std::fs;
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
/// XDG data directory) when they are missing or name another file.
pub fn ensure_launcher(data: &Path, appimage: &str) -> std::io::Result<PathBuf> {
    let icon = data.join("icons/hicolor/128x128/apps/agentnet.png");
    if fs::read(&icon).ok().as_deref() != Some(ICON) {
        fs::create_dir_all(icon.parent().unwrap())?;
        fs::write(&icon, ICON)?;
    }
    let entry = data.join("applications/agentnet.desktop");
    let want = desktop_entry(appimage, &icon);
    if fs::read_to_string(&entry).ok().as_deref() != Some(want.as_str()) {
        fs::create_dir_all(entry.parent().unwrap())?;
        let tmp = entry.with_extension("desktop.tmp");
        fs::write(&tmp, &want)?;
        fs::rename(&tmp, &entry)?;
    }
    Ok(entry)
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
        let entry = ensure_launcher(&data, "/home/a/Apps/AgentNet.AppImage").unwrap();
        let text = fs::read_to_string(&entry).unwrap();
        assert!(text.contains("Exec=\"/home/a/Apps/AgentNet.AppImage\" %U"), "{text}");
        assert!(!text.contains("MimeType"), "the plugin's own entry handles agentnet://");
        assert!(data.join("icons/hicolor/128x128/apps/agentnet.png").exists());
        ensure_launcher(&data, "/home/a/Downloads/AgentNet.AppImage").unwrap();
        assert!(fs::read_to_string(&entry).unwrap().contains("/home/a/Downloads/AgentNet.AppImage"));
        let _ = fs::remove_dir_all(&data);
    }
}
