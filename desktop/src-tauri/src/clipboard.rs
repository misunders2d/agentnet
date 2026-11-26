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

/// Longest clipboard text a paste takes, as the composer's own limit allows.
const MAX_CLIPBOARD_TEXT: usize = 1 << 20;

/// A secret of this app run, placed only inside the clipboard init script's
/// closure: page code that calls the command itself does not know it, so
/// clipboard text is read only through the script's trusted-paste gate.
pub fn paste_key() -> &'static str {
    static KEY: std::sync::OnceLock<String> = std::sync::OnceLock::new();
    KEY.get_or_init(|| {
        use std::hash::{BuildHasher, Hasher};
        // RandomState is keyed from the operating system's randomness.
        (0..4u8).map(|i| { let mut h = std::collections::hash_map::RandomState::new().build_hasher(); h.write_u8(i); format!("{:016x}", h.finish()) }).collect()
    })
}

/// The clipboard init script, with this run's paste key in its closure.
pub fn init_script() -> String { include_str!("clipboard.js").replace("__AGENTNET_PASTE_KEY__", paste_key()) }

/// The message for a clipboard with no text at all (arboard's
/// ContentNotAvailable); any other read failure is an error, never "no text".
const NO_TEXT: &str = "The clipboard contents were not available in the requested format or the clipboard is empty.";

fn text_reply(text: Result<String, String>) -> Result<Option<String>, String> {
    match text {
        Ok(text) if text.len() > MAX_CLIPBOARD_TEXT => Err("This text is too long to paste.".into()),
        Ok(text) if !text.is_empty() => Ok(Some(text)),
        Ok(_) => Ok(None),
        Err(e) if e == NO_TEXT => Ok(None),
        Err(_) => Err("Could not read the clipboard's text.".into()),
    }
}

/// The clipboard's text, for a paste whose web event carried none: WebKitGTK
/// can offer no text for a spreadsheet copy (WPS Office) that does hold it,
/// whose picture flavour (BMP) is unreadable, so nothing was pasted.
#[tauri::command]
pub async fn agentnet_clipboard_text(window: WebviewWindow, key: String) -> Result<Option<String>, String> {
    if key != paste_key() {
        return Err("Clipboard is unavailable on this page.".into());
    }
    let actual = window.url().map_err(|_| "Clipboard is unavailable.")?;
    {
        let shell = window.state::<crate::Shell>();
        if !permitted(window.label(), shell.page.lock().unwrap().as_ref(), &actual) {
            return Err("Clipboard is unavailable on this page.".into());
        }
    }
    let text = tauri::async_runtime::spawn_blocking(move || window.clipboard().read_text().map_err(|e| e.to_string()))
        .await.map_err(|_| "Clipboard is unavailable.")?;
    text_reply(text)
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
    #[test]
    fn clipboard_text_replies() {
        assert_eq!(text_reply(Ok("SKU-1".into())), Ok(Some("SKU-1".into())));
        assert_eq!(text_reply(Ok(String::new())), Ok(None));
        assert_eq!(text_reply(Err(NO_TEXT.into())), Ok(None));
        assert!(text_reply(Err("conversion failed".into())).is_err(), "a failed read is not \"no text\"");
        assert!(text_reply(Ok("x".repeat(MAX_CLIPBOARD_TEXT + 1))).is_err());
        let key = paste_key();
        assert_eq!(key.len(), 64);
        assert_eq!(key, paste_key());
        assert!(init_script().contains(key) && !init_script().contains("__AGENTNET_PASTE_KEY__"));
    }
}
