//! The bundled starting page has a platform-specific origin. Recognize it
//! before the first navigation callback, which can run while the window builds.

use url::Url;

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
