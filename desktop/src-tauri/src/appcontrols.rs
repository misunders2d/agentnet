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
    if !matches!(action.as_str(), "status" | "check" | "update" | "cli") { return Err("Unknown app action.".into()); }
    let actual = window.url().map_err(|_| "App controls are unavailable.")?;
    if window.label() != "main" || window.state::<crate::Shell>().page.lock().unwrap().is_none() {
        return Err("App controls are unavailable on this page.".into());
    }
    // The authenticated HTTP page trades its URL token for an HttpOnly
    // cookie and redirects to '/'. Read native cookies outside Shell locks:
    // the Linux runtime processes GTK events while resolving this request.
    let cookie = window.cookies_for_url(actual.clone()).map_err(|_| "App controls are unavailable on this page.")?
        .into_iter().find(|c| c.name() == "agentnet_ui" && c.http_only() == Some(true))
        .map(|c| c.value().to_owned());
    let (id, rx) = {
        let shell = window.state::<crate::Shell>();
        if !shell.page.lock().unwrap().as_ref().is_some_and(|p| bound_page(p, &actual, cookie.as_deref())) {
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

fn bound_page(page: &url::Url, actual: &url::Url, cookie: Option<&str>) -> bool {
    let tokens: Vec<_> = page.query_pairs().filter(|(k, _)| k == "t").map(|(_, v)| v).collect();
    if tokens.len() != 1 || tokens[0].is_empty() || cookie != Some(tokens[0].as_ref()) {
        return false;
    }
    let actual_tokens: Vec<_> = actual.query_pairs().filter(|(k, _)| k == "t").map(|(_, v)| v).collect();
    crate::same_origin(page, actual) && page.path() == "/" && actual.path() == "/"
        && page.username().is_empty() && page.password().is_none()
        && actual.username().is_empty() && actual.password().is_none()
        && (actual_tokens.is_empty() || (actual_tokens.len() == 1 && actual_tokens[0] == tokens[0]))
}
#[cfg(test)]
mod tests {
    #[test]
    fn controls_bind_the_advertised_page_token_and_origin() {
        let page = url::Url::parse("http://127.0.0.1:17443/?t=private").unwrap();
        for actual in ["http://127.0.0.1:17443/?t=private#about", "http://127.0.0.1:17443/?skin=comic&t=private"] {
            let actual = url::Url::parse(actual).unwrap();
            assert!(super::bound_page(&page, &actual, Some("private")));
            assert!(!super::bound_page(&page, &actual, None));
            assert!(!super::bound_page(&page, &actual, Some("other")));
        }
        for actual in ["http://127.0.0.1:17443/", "http://127.0.0.1:17443/?t=other", "http://127.0.0.1:1/?t=private"] {
            assert!(!super::bound_page(&page, &url::Url::parse(actual).unwrap(), None));
        }
    }

    #[test]
    fn controls_follow_only_the_authenticated_cookie_redirect() {
        let page = url::Url::parse("http://127.0.0.1:17443/?t=private").unwrap();
        for actual in ["http://127.0.0.1:17443/", "http://127.0.0.1:17443/#about", "http://127.0.0.1:17443/?skin=comic"] {
            let actual = url::Url::parse(actual).unwrap();
            assert!(super::bound_page(&page, &actual, Some("private")), "{actual}");
            for cookie in [None, Some(""), Some("old-session")] {
                assert!(!super::bound_page(&page, &actual, cookie));
            }
        }
        for actual in ["http://127.0.0.1:17443/?t=other", "http://127.0.0.1:17443/?t=", "http://127.0.0.1:17443/?t=private&t=private", "http://127.0.0.1:17443/?t=private&t=other", "http://127.0.0.1:17443/error", "http://127.0.0.1:17444/", "http://localhost:17443/", "https://127.0.0.1:17443/", "http://user@127.0.0.1:17443/", "tauri://localhost/"] {
            assert!(!super::bound_page(&page, &url::Url::parse(actual).unwrap(), Some("private")), "{actual}");
        }
        for advertised in ["http://127.0.0.1:17443/", "http://127.0.0.1:17443/?t=", "http://127.0.0.1:17443/?t=private&t=private"] {
            assert!(!super::bound_page(&url::Url::parse(advertised).unwrap(), &url::Url::parse("http://127.0.0.1:17443/").unwrap(), Some("private")));
        }
    }
}
