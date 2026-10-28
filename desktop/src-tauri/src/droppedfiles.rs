//! Only an actual OS drop creates these short-lived file grants. Page code
//! receives opaque IDs, never a filesystem path or a general read command.
use std::collections::HashMap;
use std::path::PathBuf;
use std::time::{Duration, Instant};
use tauri::{Manager, Url, WebviewWindow};

const MAX_FILES: usize = 8; // envelope.MaxAttachments
const MAX_BYTES: u64 = 100 << 20; // client.MaxFileSize
const LIFETIME: Duration = Duration::from_secs(300);

struct Grant { path: PathBuf, page: Url, at: Instant }
#[derive(Default)]
pub struct Drops { next: u64, files: HashMap<String, Grant> }

impl Drops {
    fn register(&mut self, paths: &[PathBuf], page: &Url) -> Result<Vec<serde_json::Value>, String> {
        self.files.retain(|_, file| file.at.elapsed() < LIFETIME);
        if paths.is_empty() || paths.len() > MAX_FILES { return Err("Drop up to 8 files at a time.".into()); }
        if paths.iter().any(|path| !path.is_absolute()) { return Err("Choose local files with absolute OS paths.".into()); }
        if self.files.len() + paths.len() > 32 { return Err("Too many files are being attached. Try this drop again shortly.".into()); }
        let mut files = Vec::new();
        for path in paths {
            self.next = self.next.checked_add(1).ok_or("File drop is unavailable.")?;
            let id = self.next.to_string();
            let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_else(|| "file".into());
            self.files.insert(id.clone(), Grant { path: path.clone(), page: page.clone(), at: Instant::now() });
            files.push(serde_json::json!({"id":id,"name":name}));
        }
        Ok(files)
    }
    fn take(&mut self, id: &str, page: &Url) -> Result<PathBuf, String> {
        self.take_at(id, page, Instant::now())
    }
    fn take_at(&mut self, id: &str, page: &Url, now: Instant) -> Result<PathBuf, String> {
        let grant = self.files.get(id).ok_or("That dropped file is no longer available. Drop it again.")?;
        if now.duration_since(grant.at) >= LIFETIME || grant.page != *page { return Err("That file drop expired or its page changed. Drop it again.".into()); }
        Ok(self.files.remove(id).unwrap().path)
    }
}

fn permitted(label: &str, current: Option<&Url>, actual: &Url) -> bool {
    label == "main" && current.is_some_and(|page| crate::same_origin(page, actual))
}

fn check_page(window: &WebviewWindow) -> Result<Url, String> {
    let actual = window.url().map_err(|_| "File drop is unavailable.")?;
    let shell = window.state::<crate::Shell>();
    if !permitted(window.label(), shell.page.lock().unwrap().as_ref(), &actual) {
        return Err("File drop is unavailable on this page.".into());
    }
    Ok(actual)
}

#[cfg(target_os = "linux")]
fn read_file(path: PathBuf) -> Result<Vec<u8>, String> {
    use std::io::Read;
    use std::os::unix::fs::OpenOptionsExt;
    // Linux O_NONBLOCK | O_NOFOLLOW: a dropped FIFO cannot hang a worker,
    // and a final symlink cannot change the selected file's read authority.
    let file = std::fs::OpenOptions::new().read(true).custom_flags(0o4000 | 0o400000).open(path)
        .map_err(|_| "This dropped file could not be opened. Choose a regular readable file.")?;
    let metadata = file.metadata().map_err(|_| "This dropped file could not be read.")?;
    if !metadata.is_file() { return Err("Folders and special files cannot be attached.".into()); }
    if metadata.len() == 0 { return Err("Empty files cannot be attached.".into()); }
    if metadata.len() > MAX_BYTES { return Err("This file is larger than the 100 MiB attachment limit.".into()); }
    let mut bytes = Vec::new();
    file.take(MAX_BYTES + 1).read_to_end(&mut bytes).map_err(|_| "This dropped file could not be read.")?;
    if bytes.len() as u64 > MAX_BYTES { return Err("This file grew beyond the attachment limit. Drop it again.".into()); }
    Ok(bytes)
}

#[tauri::command]
pub async fn agentnet_dropped_file(window: WebviewWindow, id: String, discard: bool) -> Result<tauri::ipc::Response, String> {
    let page = check_page(&window)?;
    let path = window.state::<crate::Shell>().drops.lock().unwrap().take(&id, &page)?;
    if discard { return Ok(tauri::ipc::Response::new(Vec::<u8>::new())); }
    #[cfg(target_os = "linux")]
    {
        let bytes = tauri::async_runtime::spawn_blocking(move || read_file(path)).await.map_err(|_| "This dropped file could not be read.")??;
        if check_page(&window)? != page { return Err("The page changed while attaching. Drop the file again.".into()); }
        // A binary IPC response avoids expanding file bytes into JSON numbers.
        Ok(tauri::ipc::Response::new(bytes))
    }
    #[cfg(not(target_os = "linux"))]
    { let _ = path; Err("Use this platform's browser file drop.".into()) }
}

#[cfg(target_os = "linux")]
pub fn install(window: &WebviewWindow) {
    let target = window.clone();
    // The locked Tauri runtime routes a WindowContent webview's OS drops
    // to WindowEvent; WebviewEvent is only for child webviews.
    window.on_window_event(move |event| {
        let tauri::WindowEvent::DragDrop(tauri::DragDropEvent::Drop { paths, position }) = event else { return };
        let Ok(page) = check_page(&target) else { return };
        let position = position.to_logical::<f64>(target.scale_factor().unwrap_or(1.0));
        let result = target.state::<crate::Shell>().drops.lock().unwrap().register(paths, &page);
        let payload = match result {
            Ok(files) => serde_json::json!({"x":position.x,"y":position.y,"files":files}),
            Err(error) => serde_json::json!({"x":position.x,"y":position.y,"files":[],"error":error}),
        };
        let _ = target.eval(format!("window.__agentnetNativeFileDrop?.({payload})"));
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn native_reads_require_current_main_page_origin() {
        let page = Url::parse("http://127.0.0.1:1234/?t=fixture").unwrap();
        assert!(permitted("main", Some(&page), &page));
        assert!(!permitted("other", Some(&page), &page));
        assert!(!permitted("main", None, &page));
        for value in ["http://127.0.0.1:1235/", "http://localhost:1234/", "https://127.0.0.1:1234/", "https://example.invalid/", "tauri://localhost/"] {
            assert!(!permitted("main", Some(&page), &Url::parse(value).unwrap()));
        }
    }
    #[test]
    fn only_selected_unexpired_exact_page_grants_can_be_read_once() {
        let page = Url::parse("http://127.0.0.1:1234/?t=fixture").unwrap();
        let mut drops = Drops::default();
        let selected = std::env::temp_dir().join("agentnet-fictional-selected.txt");
        let files = drops.register(&[selected.clone()], &page).unwrap();
        assert!(files[0].get("path").is_none());
        let id = files[0]["id"].as_str().unwrap();
        assert!(drops.take("/etc/passwd", &page).is_err());
        for value in ["http://127.0.0.1:1235/?t=fixture", "http://127.0.0.1:1234/?t=new", "https://example.invalid/"] {
            assert!(drops.take(id, &Url::parse(value).unwrap()).is_err());
        }
        assert_eq!(drops.take(id, &page).unwrap(), selected);
        assert!(drops.take(id, &page).is_err());
        let files = drops.register(&[std::env::temp_dir().join("agentnet-fictional-expired")], &page).unwrap();
        let id = files[0]["id"].as_str().unwrap();
        let expired = drops.files[id].at + LIFETIME;
        assert!(drops.take_at(id, &page, expired).is_err());
        assert!(drops.register(&vec![PathBuf::from("file"); MAX_FILES + 1], &page).is_err());
    }
    #[cfg(target_os = "linux")]
    #[test]
    fn selected_reads_are_bounded_regular_files() {
        let root = std::env::temp_dir().join(format!("agentnet-drop-{}", std::process::id()));
        std::fs::create_dir_all(&root).unwrap();
        std::fs::write(root.join("selected"), b"exact selected bytes").unwrap();
        assert_eq!(read_file(root.join("selected")).unwrap(), b"exact selected bytes");
        assert!(read_file(root.clone()).is_err());
        std::fs::write(root.join("empty"), b"").unwrap();
        assert!(read_file(root.join("empty")).is_err());
        std::os::unix::fs::symlink(root.join("selected"), root.join("link")).unwrap();
        assert!(read_file(root.join("link")).is_err());
        let large = std::fs::File::create(root.join("large")).unwrap();
        large.set_len(MAX_BYTES + 1).unwrap();
        assert!(read_file(root.join("large")).is_err());
        std::fs::remove_dir_all(root).unwrap();
    }
}
