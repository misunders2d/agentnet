//! agentnet:// links. A link only shows something in the window or fills in
//! a form that still needs the person's click; it never acts by itself.
//! Only fragments that name an invitation, a device link or a page
//! destination pass; anything else is dropped. Links are never logged: an
//! invitation or device link is a secret.

/// The longest fragment taken (an invitation with its hints is well under).
pub const MAX_FRAGMENT: usize = 8192;

/// What a fragment may start with: an invitation, a device link from
/// another device of the person, or a destination of a notification click.
pub const ALLOWED: [&str; 8] = [
    "realm=",
    "agentnet-invite-v1:",
    "agentnet-link-v2:",
    "google-signin=",
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
    if frag.starts_with("realm=") || frag.contains("&realm=") || frag.contains("topic=") || frag.contains("thread=") {
        if link.strip_prefix("agentnet://open#") != Some(frag) || !portable_fragment(frag) { return None; }
        return Some(frag.to_string());
    }
    if ALLOWED.iter().any(|p| frag.starts_with(p)) {
        Some(frag.to_string())
    } else {
        None
    }
}

fn portable_fragment(fragment: &str) -> bool {
    if fragment.len() > 240 { return false; }
    let mut realm = None; let mut conv = None; let mut topic = None; let mut thread = None;
    for part in fragment.split('&') {
        let Some((key, value)) = part.split_once('=') else { return false; };
        let field = match key { "realm" => &mut realm, "conv" => &mut conv, "topic" => &mut topic, "thread" => &mut thread, _ => return false };
        if field.is_some() { return false; } *field = Some(value);
    }
    let hex = |value: &str, length: usize| value.len() == length && value.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b));
    if !realm.is_some_and(|v| hex(v, 32)) { return false; }
    if let Some(root) = thread { conv.is_none() && topic.is_none() && hex(root, 32) }
    else { conv.is_some_and(|v| hex(v, 64)) && topic.is_some_and(|v| v == "main" || hex(v, 32)) }
}

fn notification_fragment(fragment: &str) -> bool {
    let mut message = None; let mut conv = None; let mut direction = None; let mut workspace = None;
    let mut review = false;
    for part in fragment.split('&') {
        if part == "review" {
            if review { return false; }
            review = true;
            continue;
        }
        let Some((key, value)) = part.split_once('=') else { return false; };
        let field = match key { "msg" => &mut message, "conv" => &mut conv, "dir" => &mut direction, "workspace" => &mut workspace, _ => return false };
        if field.is_some() { return false; } *field = Some(value);
    }
    let hex = |value: &str, length: usize| value.len() == length && value.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b));
    if workspace.is_some_and(|v| v != "default" && !hex(v, 32)) { return false; }
    if let Some(id) = message {
        fragment.starts_with("msg=") && !review && hex(id, 32) && conv.map_or(true, |v| hex(v, 64)) && direction.map_or(true, |v| v == "in" || v == "out")
    } else if let Some(id) = conv {
        fragment.starts_with("conv=") && !review && hex(id, 64) && direction.is_none()
    } else if review {
        (fragment == "review" || fragment.starts_with("review&")) && direction.is_none()
    } else {
        fragment.starts_with("workspace=") && workspace.is_some() && direction.is_none()
    }
}

/// Warm navigation stays on the existing local page so a refused link
/// cannot reload it and discard draft files. Validated fields contain no JS
/// string delimiters; the page's shared router performs destination checks.
pub fn navigation_script(fragment: &str) -> Option<String> {
    (portable_fragment(fragment) || notification_fragment(fragment)).then(|| format!("location.hash=\"{fragment}\";"))
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
            "google-signin=https%3A%2F%2Frelay.example.com",
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

    #[test]
    fn portable_destinations_are_exact() {
        let r = "a".repeat(32); let c = "b".repeat(64); let t = "c".repeat(32);
        for fragment in [format!("realm={r}&conv={c}&topic=main"), format!("realm={r}&conv={c}&topic={t}"), format!("realm={r}&thread={t}")] {
            assert_eq!(navigation_script(&fragment), Some(format!("location.hash=\"{fragment}\";")));
            assert_eq!(super::fragment(&format!("agentnet://open#{fragment}")), Some(fragment.clone()));
            assert_eq!(super::fragment(&format!("agentnet://open/#{fragment}")), None);
            assert_eq!(super::fragment(&format!(" agentnet://open#{fragment}")), None);
        }
        for fragment in [format!("realm={r}&conv={c}"), format!("realm={r}&conv={c}&topic=main&realm={r}"), format!("realm={r}&thread={t}&topic=main"), format!("realm={r}&conv={c}&topic=%6dain"), format!("realm={r}&conv={c}&topic=main&extra=1")] {
            assert_eq!(navigation_script(&fragment), None);
            assert_eq!(super::fragment(&format!("agentnet://open#{fragment}")), None);
        }
    }

    #[test]
    fn notification_navigation_preserves_exact_fields_without_reloading() {
        let id = "a".repeat(32); let conv = "b".repeat(64); let workspace = "c".repeat(32);
        for fragment in [format!("msg={id}"), format!("msg={id}&dir=in&workspace=default"), format!("msg={id}&conv={conv}&dir=out&workspace={workspace}"), format!("conv={conv}&workspace={workspace}"), format!("review&workspace={workspace}"), format!("workspace={workspace}")] {
            assert_eq!(super::fragment(&format!("agentnet://open#{fragment}")), Some(fragment.clone()));
            assert_eq!(navigation_script(&fragment), Some(format!("location.hash=\"{fragment}\";")));
        }
        for fragment in [format!("msg={id}&conv=invalid"), format!("msg={id}&dir=unknown"), format!("msg={id}&workspace=unknown"), format!("msg={id}&msg={id}"), format!("conv={conv}&msg={id}"), format!("msg={id}&review"), format!("msg={id}&extra=1"), format!("msg={id}&workspace=default%22"), "review&review".into(), "msg=0123".into(), "conv=0123abcd".into()] {
            assert_eq!(super::fragment(&format!("agentnet://open#{fragment}")), Some(fragment.clone()), "legacy fallback: {fragment}");
            assert_eq!(navigation_script(&fragment), None, "{fragment}");
        }
        assert_eq!(navigation_script("review&workspace=bbbb"), None);
        assert_eq!(navigation_script("agentnet-invite-v1:secret"), None);
    }
}
