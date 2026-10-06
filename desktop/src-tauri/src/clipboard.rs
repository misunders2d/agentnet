//! Clipboard read only for the app's current loopback page. No plugin IPC
//! permission is granted to pages; the custom command checks actual webview.
use serde::Serialize;
use tauri::{Manager, Url, WebviewWindow};
use tauri_plugin_clipboard_manager::ClipboardExt;

#[derive(Serialize)]
pub struct ClipboardImage { width: u32, height: u32, rgba: Vec<u8> }

fn permitted(label: &str, page: Option<&Url>, actual: &Url) -> bool {
    label == "main" && page.is_some_and(|page| crate::same_origin(page, actual))
}

#[tauri::command]
pub async fn agentnet_clipboard_image(window: WebviewWindow) -> Result<Option<ClipboardImage>, String> {
    let actual = window.url().map_err(|_| "Clipboard is unavailable.")?;
    {
        let shell = window.state::<crate::Shell>();
        if !permitted(window.label(), shell.page.lock().unwrap().as_ref(), &actual) {
            return Err("Clipboard is unavailable on this page.".into());
        }
    }
    // The official plugin warns of Linux deadlocks on the main thread.
    let image = tauri::async_runtime::spawn_blocking(move || window.clipboard().read_image().map(|image| (image.width(), image.height(), image.rgba().to_vec())))
        .await.map_err(|_| "Clipboard is unavailable.")?;
    let (width, height, rgba) = match image { Ok(image) => image, Err(_) => return Ok(None) };
    if width == 0 || height == 0 || width.checked_mul(height).is_none_or(|n| n > 16_000_000) { return Err("This picture is too large to paste.".into()); }
    Ok(Some(ClipboardImage { width, height, rgba }))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn clipboard_only_from_current_main_page() {
        let page = Url::parse("http://127.0.0.1:17443/?t=private").unwrap();
        let actual = Url::parse("http://127.0.0.1:17443/").unwrap();
        assert!(permitted("main", Some(&page), &actual));
        assert!(!permitted("other", Some(&page), &actual));
        assert!(!permitted("main", None, &actual));
        for value in ["http://127.0.0.1:17444/", "http://localhost:17443/", "https://127.0.0.1:17443/", "http://evil.example/", "tauri://localhost/"] {
            assert!(!permitted("main", Some(&page), &Url::parse(value).unwrap()), "{value}");
        }
    }
}
