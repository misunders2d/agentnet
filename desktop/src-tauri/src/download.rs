//! Linux WebKit downloads blob links through its native download facility.
//! Never turn blob HTML into navigation with the app's IPC authority.
use tauri::{Manager, Url, WebviewWindow};

fn filename(name: &str) -> bool {
    !name.is_empty() && name.len() <= 255 && name != "." && name != ".."
        && !name.contains(['/', '\\', ':']) && !name.chars().any(|c| c.is_control())
}

#[tauri::command]
pub async fn agentnet_download_blob(window: WebviewWindow, url: String, name: String) -> Result<(), String> {
    let actual = window.url().map_err(|_| "Download is unavailable.")?;
    let blob = Url::parse(&url).map_err(|_| "Invalid download.")?;
    if !filename(&name) { return Err("Invalid download filename.".into()); }
    {
        let shell = window.state::<crate::Shell>();
        let page = shell.page.lock().unwrap();
        if window.label() != "main" || !page.as_ref().is_some_and(|p| crate::same_origin(p, &actual) && crate::navigation::page_blob(p, &blob)) {
            return Err("Download is unavailable on this page.".into());
        }
        let mut pending = shell.downloads.lock().unwrap();
        if pending.len() >= 32 { return Err("Too many downloads are starting. Try again shortly.".into()); }
        pending.insert(url.clone(), name.into());
    }
    #[cfg(target_os = "linux")]
    {
        let uri = url.clone();
        if window.with_webview(move |view| { use webkit2gtk::WebViewExt; view.inner().download_uri(&uri); }).is_err() {
            window.state::<crate::Shell>().downloads.lock().unwrap().remove(&url);
            return Err("The download could not start.".into());
        }
        Ok(())
    }
    #[cfg(not(target_os = "linux"))]
    { window.state::<crate::Shell>().downloads.lock().unwrap().remove(&url); Err("Use this platform's normal download link.".into()) }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn download_names_cannot_choose_a_path() {
        for n in ["agent-result.txt", "unsafe.html", "picture.svg"] { assert!(filename(n)); }
        for n in ["", ".", "..", "../secret", "/tmp/secret", "C:\\secret", "x\0y", "x\ny"] { assert!(!filename(n)); }
    }
}
