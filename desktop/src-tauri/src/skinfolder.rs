//! Explicit native folder selection for one bounded skin package. There is
//! no path argument and no general filesystem command for page JavaScript.
use serde::Serialize;
use std::collections::HashSet;
use std::fs;
use std::io::Read;
use std::path::{Component, Path};
use tauri::{Manager, WebviewWindow};
use tauri_plugin_dialog::DialogExt;

#[derive(Serialize)]
pub struct SkinFile { name: String, bytes: Vec<u8> }

fn read_file(root: &Path, name: &str, limit: u64) -> Result<Vec<u8>, String> {
    if name.split('/').any(|part| part.is_empty() || part == "." || part == "..") || name.contains(['\\', ':', '%', '?', '#']) || name.chars().any(|c| c.is_control()) {
        return Err("Package paths must be local relative paths.".into());
    }
    let mut path = root.to_path_buf();
    for part in Path::new(name).components() {
        let Component::Normal(part) = part else { return Err("Package paths must be local relative paths.".into()) };
        path.push(part);
        if fs::symlink_metadata(&path).map_err(|_| "A declared package file is missing.")?.file_type().is_symlink() {
            return Err("Skin package files cannot be symbolic links.".into());
        }
    }
    if !fs::metadata(&path).map_err(|_| "A declared package file is missing.")?.is_file() {
        return Err("A declared package file is not a regular file.".into());
    }
    let mut bytes = Vec::new();
    fs::File::open(path).map_err(|_| "A declared package file could not be read.")?.take(limit + 1).read_to_end(&mut bytes)
        .map_err(|_| "A declared package file could not be read.")?;
    if bytes.len() as u64 > limit { return Err("A skin package file exceeds its size limit.".into()); }
    Ok(bytes)
}

fn read_package(root: &Path) -> Result<Vec<SkinFile>, String> {
    let manifest = read_file(root, "skin.json", 16384)?;
    let json: serde_json::Value = serde_json::from_slice(&manifest).map_err(|_| "The manifest must be UTF-8 JSON.")?;
    let files = json.get("files").and_then(|v| v.as_array()).filter(|v| !v.is_empty() && v.len() <= 32)
        .ok_or("The manifest must declare 1 to 32 package files.")?;
    let mut result = vec![SkinFile { name: "skin.json".into(), bytes: manifest }];
    let mut seen = HashSet::from(["skin.json".to_string()]);
    let mut total = 0;
    for value in files {
        let name = value.as_str().ok_or("Invalid declared package path.")?;
        if !seen.insert(name.to_string()) { return Err("Package paths must be distinct.".into()); }
        let bytes = read_file(root, name, 4 * 1024 * 1024)?;
        total += bytes.len();
        if total > 16 * 1024 * 1024 { return Err("The package exceeds 16 MiB.".into()); }
        result.push(SkinFile { name: name.into(), bytes });
    }
    Ok(result)
}

fn check_page(window: &WebviewWindow) -> Result<(), String> {
    let actual = window.url().map_err(|_| "Skin import is unavailable.")?;
    let shell = window.state::<crate::Shell>();
    if window.label() != "main" || !shell.page.lock().unwrap().as_ref().is_some_and(|p| crate::same_origin(p, &actual)) {
        return Err("Skin import is unavailable on this page.".into());
    }
    Ok(())
}

#[tauri::command]
pub async fn agentnet_skin_folder(window: WebviewWindow) -> Result<Option<Vec<SkinFile>>, String> {
    check_page(&window)?;
    let picker = window.dialog().file().set_title("Choose an AgentNet skin folder").set_parent(&window);
    let files = tauri::async_runtime::spawn_blocking(move || {
        let Some(folder) = picker.blocking_pick_folder() else { return Ok(None) };
        let path = folder.into_path().map_err(|_| "Choose a local skin folder.")?;
        read_package(&path).map(Some)
    }).await.map_err(|_| "The skin folder could not be read.")??;
    check_page(&window)?; // navigation while the chooser was open grants no access
    Ok(files)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn only_manifest_declared_bounded_regular_files_are_read() {
        let root = std::env::temp_dir().join(format!("agentnet-skin-folder-{}", std::process::id()));
        let _ = fs::remove_dir_all(&root);
        fs::create_dir_all(root.join("nested")).unwrap();
        fs::write(root.join("skin.json"), br#"{"files":["nested/entry.mjs"]}"#).unwrap();
        fs::write(root.join("nested/entry.mjs"), b"export function mount() {}").unwrap();
        let files = read_package(&root).unwrap();
        assert_eq!(files.len(), 2);
        assert_eq!(files[1].name, "nested/entry.mjs");
        for path in ["../outside", "/outside", "nested/../entry.mjs", "nested//entry.mjs", "nested/./entry.mjs", "nested\\entry.mjs", "nested:entry.mjs"] {
            assert!(read_file(&root, path, 4096).is_err(), "{path}");
        }
        assert!(read_file(&root, "nested/entry.mjs", 2).is_err());
        #[cfg(unix)] {
            std::os::unix::fs::symlink(root.join("nested/entry.mjs"), root.join("link.mjs")).unwrap();
            assert!(read_file(&root, "link.mjs", 4096).is_err());
        }
        fs::write(root.join("skin.json"), br#"{"files":["skin.json"]}"#).unwrap();
        assert!(read_package(&root).is_err());
        fs::remove_dir_all(root).unwrap();
    }
}
