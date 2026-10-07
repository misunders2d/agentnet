//! Native bridge to the bundled backend's existing appAPI, even when an
//! independent daemon owns the page. No arbitrary path or service command.
use serde_json::Value;
use serde::Serialize;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tauri::{Manager, WebviewWindow};

#[derive(Serialize)]
pub struct AppResponse { status: u16, body: String }

#[tauri::command]
pub async fn agentnet_app_controls(window: WebviewWindow, action: String, body: Option<Value>) -> Result<AppResponse, String> {
    if !matches!(action.as_str(), "status" | "update" | "cli") { return Err("Unknown app action.".into()); }
    let actual = window.url().map_err(|_| "App controls are unavailable.")?;
    let (id, rx) = {
        let shell = window.state::<crate::Shell>();
        if window.label() != "main" || !shell.page.lock().unwrap().as_ref().is_some_and(|p| bound_page(p, &actual)) {
            return Err("App controls are unavailable on this page.".into());
        }
        let id = shell.app_call_id.fetch_add(1, Ordering::Relaxed) + 1;
        let request = serde_json::to_string(&serde_json::json!({"command":"app-api","id":id,"action":action,"body":body.unwrap_or_else(|| serde_json::json!({}))}))
            .map_err(|_| "Invalid app request.")?;
        if request.len()>2048 { return Err("App request is too large.".into()); }
        let (tx,rx) = std::sync::mpsc::channel();
        let mut pending = shell.app_calls.lock().unwrap();
        if pending.len()>=8 { return Err("An app action is already waiting. Try again shortly.".into()); }
        pending.insert(id,tx);
        if !shell.running.lock().unwrap().as_mut().is_some_and(|r| r.send_line(&request).is_ok()) {
            pending.remove(&id); return Err("The app's program is unavailable. Reopen AgentNet.".into());
        }
        (id,rx)
    };
    // The existing HTTP updater has this same bounded request lifetime.
    let reply = tauri::async_runtime::spawn_blocking(move || rx.recv_timeout(Duration::from_secs(10*60)))
        .await.map_err(|_| "The app action could not complete.")?;
    window.state::<crate::Shell>().app_calls.lock().unwrap().remove(&id);
    let (status,body) = reply.map_err(|_| "The app's program did not answer. Reopen AgentNet.")?;
    Ok(AppResponse { status, body })
}

fn bound_page(page: &url::Url, actual: &url::Url) -> bool {
    let token = page.query_pairs().find(|(k, _)| k == "t").map(|(_, v)| v.into_owned());
    crate::same_origin(page, actual) && token.as_ref().is_some_and(|v| !v.is_empty())
        && actual.query_pairs().find(|(k, _)| k == "t").map(|(_, v)| v.into_owned()) == token
}
#[cfg(test)]
mod tests {
    #[test]
    fn controls_bind_the_advertised_page_token_and_origin() {
        let page = url::Url::parse("http://127.0.0.1:17443/?t=private").unwrap();
        for actual in ["http://127.0.0.1:17443/?t=private#about", "http://127.0.0.1:17443/?skin=comic&t=private"] {
            assert!(super::bound_page(&page, &url::Url::parse(actual).unwrap()));
        }
        for actual in ["http://127.0.0.1:17443/", "http://127.0.0.1:17443/?t=other", "http://127.0.0.1:1/?t=private"] {
            assert!(!super::bound_page(&page, &url::Url::parse(actual).unwrap()));
        }
    }
}
