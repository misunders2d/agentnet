//! agentnet:// links. A link only shows something in the window or fills in
//! a form that still needs the person's click; it never acts by itself.
//! Only fragments that name an invitation, a device link or a page
//! destination pass; anything else is dropped. Links are never logged: an
//! invitation or device link is a secret.

/// The longest fragment taken (an invitation with its hints is well under).
pub const MAX_FRAGMENT: usize = 8192;

/// What a fragment may start with: an invitation, a device link from
/// another device of the person, or a destination of a notification click.
pub const ALLOWED: [&str; 6] = [
    "agentnet-invite-v1:",
    "agentnet-link-v2:",
    "conv=",
    "msg=",
    "review",
    "workspace=",
];

/// The fragment of an agentnet://open link: Some("") opens the window
/// only, Some(fragment) also shows that in it, None drops the link.
pub fn fragment(link: &str) -> Option<String> {
    let rest = link.trim().strip_prefix("agentnet://open")?;
    let rest = rest.strip_prefix('/').unwrap_or(rest);
    if rest.is_empty() {
        return Some(String::new());
    }
    let frag = rest.strip_prefix('#')?;
    if frag.is_empty() {
        return Some(String::new());
    }
    if frag.len() > MAX_FRAGMENT || !frag.bytes().all(fragment_byte) {
        return None;
    }
    if ALLOWED.iter().any(|p| frag.starts_with(p)) {
        Some(frag.to_string())
    } else {
        None
    }
}

/// Bytes a fragment keeps as they are in a URL: visible ASCII without
/// quotes, angle brackets, backslash or backtick.
fn fragment_byte(b: u8) -> bool {
    b.is_ascii_graphic() && !matches!(b, b'"' | b'<' | b'>' | b'\\' | b'`' | b'{' | b'}' | b'|' | b'^')
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn allowed_fragments_pass() {
        assert_eq!(fragment("agentnet://open"), Some(String::new()));
        assert_eq!(fragment("agentnet://open/"), Some(String::new()));
        assert_eq!(fragment("agentnet://open#"), Some(String::new()));
        for f in [
            "agentnet-invite-v1:eyJodWIiOiJodHRwczovL3gifQ",
            "agentnet-link-v2:abc_-123",
            "conv=0123abcd",
            "msg=0123&dir=in&workspace=default",
            "review",
            "review&workspace=bbbb",
            "workspace=default",
        ] {
            assert_eq!(fragment(&format!("agentnet://open#{f}")), Some(f.to_string()), "{f}");
            assert_eq!(fragment(&format!("agentnet://open/#{f}")), Some(f.to_string()), "{f}");
        }
    }

    #[test]
    fn junk_is_dropped() {
        for link in [
            "agentnet://other#review",
            "https://evil.example/#review",
            "agentnet://open#javascript:alert(1)",
            "agentnet://open#settings",
            "agentnet://open#review\"><script>",
            "agentnet://open#conv=a b",
            "agentnet://open?x=1#review",
            "agentnet://openx#review",
            "",
        ] {
            assert_eq!(fragment(link), None, "{link}");
        }
        let long = format!("agentnet://open#review{}", "a".repeat(MAX_FRAGMENT));
        assert_eq!(fragment(&long), None);
    }
}
