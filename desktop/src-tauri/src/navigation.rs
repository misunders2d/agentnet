//! The bundled starting page has a platform-specific origin. Recognize it
//! before the first navigation callback, which can run while the window builds.

use url::Url;

/// Blob downloads are created by the current AgentNet page after its host
/// checked and decrypted an attachment. Keep the embedded origin exact.
pub fn page_blob(page: &Url, url: &Url) -> bool {
    if url.scheme() != "blob" { return false; }
    let Ok(inner) = Url::parse(url.path()) else { return false };
    inner.username().is_empty() && inner.password().is_none()
        && crate::same_origin(page, &inner)
}

pub fn app_origin(url: &Url) -> bool {
    url.username().is_empty()
        && url.password().is_none()
        && url.port().is_none()
        && matches!(
            (url.scheme(), url.host_str()),
            ("tauri", Some("localhost")) | ("http" | "https", Some("tauri.localhost"))
        )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn blobs_belong_only_to_the_current_page_origin() {
        let page = Url::parse("http://127.0.0.1:17443/?t=private").unwrap();
        for value in ["blob:http://127.0.0.1:17443/attachment"] {
            assert!(page_blob(&page, &Url::parse(value).unwrap()), "{value}");
        }
        for value in ["blob:null/attachment", "blob:http://127.0.0.1:17444/attachment", "blob:http://localhost:17443/attachment", "blob:https://127.0.0.1:17443/attachment", "blob:http://user@127.0.0.1:17443/attachment", "blob:https://evil.example/attachment"] {
            assert!(!page_blob(&page, &Url::parse(value).unwrap()), "{value}");
        }
    }

    #[test]
    fn bundled_pages_stay_inside_on_every_platform() {
        for value in [
            "tauri://localhost/",
            "tauri://localhost/index.html?retry=1",
            "http://tauri.localhost/",
            "https://tauri.localhost/index.html#error=stopped",
        ] {
            assert!(app_origin(&Url::parse(value).unwrap()), "{value}");
        }
    }

    #[test]
    fn similar_web_addresses_are_not_bundled_pages() {
        for value in [
            "https://tauri.localhost.example/",
            "http://tauri.localhost:8888/",
            "http://user@tauri.localhost/",
            "https://tauri.localhost@evil.example/",
            "tauri://other/",
            "http://localhost/",
            "https://example.com/",
            "file:///index.html",
        ] {
            assert!(!app_origin(&Url::parse(value).unwrap()), "{value}");
        }
    }
}
