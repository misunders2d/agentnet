//! One renderer-independent aggregate, scoped to the current sidecar page.
//! This module never reads a store, polls, or counts messages itself.

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Value {
    pub total: u64,
    pub partial: bool,
}

impl Default for Value {
    fn default() -> Self { Self { total: 0, partial: true } }
}

impl Value {
    pub fn tooltip(self) -> String {
        match (self.total, self.partial) {
            (0, true) => "AgentNet — unread unavailable".into(),
            (n, true) => format!("AgentNet — estimated {n} unread"),
            (n, false) => format!("AgentNet — {n} unread"),
        }
    }

    #[cfg(any(target_os = "macos", test))]
    pub fn label(self) -> Option<String> {
        if self.total == 0 { return None; }
        Some(if self.partial { format!("~{}", self.total) } else { self.total.to_string() })
    }

    // Never overflow or clamp an unsigned aggregate into a false native count.
    #[cfg(any(target_os = "macos", test))]
    pub fn count(self) -> Option<i64> {
        if self.partial || self.total == 0 { return None; }
        i64::try_from(self.total).ok()
    }
}

#[derive(Default)]
pub struct State {
    page: bool,
    seq: Option<u64>,
    value: Value,
}

impl State {
    /// Every valid Page is a fresh epoch, even when its URL is unchanged.
    pub fn page(&mut self) {
        self.reset();
        self.page = true;
    }

    pub fn reset(&mut self) {
        self.page = false;
        self.seq = None;
        self.value = Value::default();
    }

    pub fn accept(&mut self, seq: u64, total: u64, partial: bool) -> bool {
        if !self.page || seq == 0 || self.seq.is_some_and(|seen| seq <= seen) {
            return false;
        }
        self.seq = Some(seq);
        self.value = Value { total, partial };
        true
    }

    pub fn value(&self) -> Value { self.value }
}

/// Add a small opaque red dot with a light border to the original icon.
/// All work is bounded by the embedded icon dimensions; malformed or oversized
/// inputs are ignored. Clearing returns the exact original RGBA, never a redraw.
pub fn icon_rgba(original: &[u8], width: u32, height: u32, marked: bool) -> Option<Vec<u8>> {
    if width < 4 || height < 4 || width > 1024 || height > 1024 {
        return None;
    }
    let size = (width as usize).checked_mul(height as usize)?.checked_mul(4)?;
    if original.len() != size { return None; }
    let mut rgba = original.to_vec();
    if !marked { return Some(rgba); }
    let radius = i64::from((width.min(height) / 6).max(1));
    let inner = (radius - (radius / 4).max(1)).max(1);
    let cx = i64::from(width) - radius - 1;
    let cy = i64::from(height) - radius - 1;
    for y in (cy - radius).max(0)..=(cy + radius).min(i64::from(height) - 1) {
        for x in (cx - radius).max(0)..=(cx + radius).min(i64::from(width) - 1) {
            let distance = (x - cx).pow(2) + (y - cy).pow(2);
            if distance > radius * radius { continue; }
            let pixel = ((y as usize) * (width as usize) + x as usize) * 4;
            let color = if distance <= inner * inner { [220, 38, 38, 255] } else { [255, 255, 255, 255] };
            rgba[pixel..pixel + 4].copy_from_slice(&color);
        }
    }
    Some(rgba)
}

#[cfg(any(target_os = "windows", test))]
pub const OVERLAY_SIDE: u32 = 32;

/// Windows draws this image in its taskbar corner: a dot, not a second
/// miniature app icon. Transparent pixels outside the circle stay untouched.
#[cfg(any(target_os = "windows", test))]
pub fn overlay_rgba(marked: bool) -> Option<Vec<u8>> {
    if !marked { return None; }
    let mut rgba = vec![0; (OVERLAY_SIDE * OVERLAY_SIDE * 4) as usize];
    for y in 0..OVERLAY_SIDE {
        for x in 0..OVERLAY_SIDE {
            let dx = i64::from(2 * x + 1) - i64::from(OVERLAY_SIDE);
            let dy = i64::from(2 * y + 1) - i64::from(OVERLAY_SIDE);
            let distance = dx * dx + dy * dy;
            if distance > 28 * 28 { continue; }
            let color = if distance <= 22 * 22 { [220, 38, 38, 255] } else { [255, 255, 255, 255] };
            let pixel = ((y * OVERLAY_SIDE + x) * 4) as usize;
            rgba[pixel..pixel + 4].copy_from_slice(&color);
        }
    }
    Some(rgba)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn windows_overlay_is_only_a_bounded_dot_and_clears() {
        assert_eq!(overlay_rgba(false), None);
        let rgba = overlay_rgba(true).unwrap();
        assert_eq!(rgba.len(), 32 * 32 * 4);
        for y in 0..32 {
            for x in 0..32 {
                let pixel = (y * 32 + x) * 4;
                let distance = (2 * x as i64 + 1 - 32).pow(2) + (2 * y as i64 + 1 - 32).pow(2);
                if distance > 28 * 28 {
                    assert_eq!(&rgba[pixel..pixel + 4], &[0, 0, 0, 0]);
                } else {
                    assert_eq!(rgba[pixel + 3], 255);
                }
            }
        }
        assert_eq!(&rgba[(16 * 32 + 16) * 4..(16 * 32 + 16) * 4 + 4], &[220, 38, 38, 255]);
        assert_eq!(overlay_rgba(true).unwrap(), rgba);
    }

    #[test]
    fn initial_and_reset_values_need_a_page() {
        let mut state = State::default();
        assert_eq!(state.value(), Value::default());
        assert!(!state.accept(1, 8, false));
        state.page();
        assert!(state.accept(1, 8, false));
        state.reset();
        assert_eq!(state.value(), Value::default());
        assert!(!state.accept(2, 9, false));
        state.page();
        assert!(state.accept(1, 0, false));
        assert_eq!(state.value(), Value { total: 0, partial: false });
    }

    #[test]
    fn sequence_is_monotonic_only_inside_the_current_epoch() {
        let mut state = State::default();
        state.page();
        assert!(!state.accept(0, 99, false));
        assert!(state.accept(9, 3, true));
        assert!(!state.accept(9, 0, false));
        assert!(!state.accept(8, 0, false));
        assert_eq!(state.value(), Value { total: 3, partial: true });
        assert!(state.accept(u64::MAX, u64::MAX, false));
        assert!(!state.accept(1, 0, false));
        state.page(); // also used for a same-URL Page announcement
        assert_eq!(state.value(), Value::default());
        assert!(state.accept(1, 2, false));
    }

    #[test]
    fn partial_zero_is_unknown_and_positive_partial_is_an_estimate() {
        let unknown = Value::default();
        assert_eq!(unknown.tooltip(), "AgentNet — unread unavailable");
        assert_eq!(unknown.label(), None);
        assert_eq!(unknown.count(), None);
        let estimate = Value { total: 7, partial: true };
        assert_eq!(estimate.tooltip(), "AgentNet — estimated 7 unread");
        assert_eq!(estimate.label().as_deref(), Some("~7"));
        assert_eq!(estimate.count(), None);
        let clear = Value { total: 0, partial: false };
        assert_eq!(clear.tooltip(), "AgentNet — 0 unread");
        assert_eq!(clear.label(), None);
        assert_eq!(clear.count(), None);
        let known = Value { total: 7, partial: false };
        assert_eq!(known.label().as_deref(), Some("7"));
        assert_eq!(known.count(), Some(7));
        let large = Value { total: u64::MAX, partial: false };
        assert_eq!(large.count(), None);
        assert_eq!(large.label().as_deref(), Some("18446744073709551615"));
    }

    #[test]
    fn dot_is_bounded_and_clear_restores_the_exact_original() {
        let original: Vec<u8> = (0..32 * 24 * 4).map(|i| (i % 251) as u8).collect();
        let marked = icon_rgba(&original, 32, 24, true).unwrap();
        assert_eq!(marked.len(), original.len());
        assert_ne!(marked, original);
        assert_eq!(&marked[..4], &original[..4]);
        for y in 0..24 {
            for x in 0..32 {
                let i = (y * 32 + x) * 4;
                if marked[i..i + 4] != original[i..i + 4] {
                    // Radius4, center(27,19), wholly inside the source icon.
                    let distance = (x as i64 - 27).pow(2) + (y as i64 - 19).pow(2);
                    assert!(distance <= 16);
                    assert_eq!(marked[i + 3], 255);
                }
            }
        }
        assert_eq!(&marked[(19 * 32 + 27) * 4..(19 * 32 + 27) * 4 + 4], &[220, 38, 38, 255]);
        assert_eq!(icon_rgba(&original, 32, 24, false).unwrap(), original);
        // Repeated notifications always start from the original, not an old dot.
        assert_eq!(icon_rgba(&original, 32, 24, true).unwrap(), marked);
    }

    #[test]
    fn invalid_icon_geometry_is_ignored_without_allocating_a_dot() {
        for (w, h) in [(0, 0), (1, 1), (3, 32), (1025, 32), (u32::MAX, u32::MAX)] {
            assert_eq!(icon_rgba(&[], w, h, true), None);
        }
        assert_eq!(icon_rgba(&[0; 16], 4, 4, true), None);
        assert!(icon_rgba(&[0; 64], 4, 4, true).is_some());
    }
}
