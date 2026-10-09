//! Navigation for page URLs already validated by the sidecar parser.

pub fn should_navigate(mode: &str, current: Option<&str>, next: &str) -> bool {
    // A fresh attached event follows a ready daemon restart. Its address and
    // session can be unchanged while the webview still shows a failed load.
    mode == "attached" || current != Some(next)
}

#[cfg(test)]
mod tests {
    use super::should_navigate;

    const PAGE: &str = "http://127.0.0.1:17443/?t=unchanged";

    #[test]
    fn attached_page_reopens_the_same_url_after_daemon_handoff() {
        assert!(should_navigate("attached", Some(PAGE), PAGE),
            "a ready daemon restart must replace a connection-refused page even with the same address and token");
        assert!(should_navigate("attached", None, PAGE));
    }

    #[test]
    fn ordinary_page_events_preserve_the_same_page() {
        for mode in ["setup", "daemon"] {
            assert!(should_navigate(mode, None, PAGE), "first {mode} page");
            assert!(!should_navigate(mode, Some(PAGE), PAGE),
                "same {mode} page must preserve drafts, including setup becoming the messenger in place");
            for next in ["http://127.0.0.1:17443/?t=new", "http://127.0.0.1:17444/?t=unchanged"] {
                assert!(should_navigate(mode, Some(PAGE), next), "changed {mode} page must navigate");
            }
        }
    }
}
