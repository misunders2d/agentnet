// The skin's own tree. The host mounts Comic into a root inside a shadow
// tree (docs/UI_SKINS.md); everything Comic draws, styles or focuses stays
// in that root: the theme is an attribute on it, every popup (Base UI
// portals) renders into a container inside it, and focus is read from its
// shadow root, never from the page.
import { createContext, useContext, useEffect, type KeyboardEvent } from "react";

export interface Owned {
  root: HTMLElement;     // the root the host gave (tokens and theme live here)
  app: HTMLElement;      // the app itself, inside root (inert behind a dialog)
  portals: HTMLElement;  // where dialogs, menus and sheets render, inside root
}

export const OwnedContext = createContext<Owned | null>(null);

export function useOwned(): Owned {
  const o = useContext(OwnedContext);
  if (!o) throw new Error("OwnedContext missing");
  return o;
}

/** usePortal is the container every Base UI Portal renders into. */
export const usePortal = (): HTMLElement => useOwned().portals;

/** focusedIn is the element focused in root's tree: its shadow root's
 *  activeElement (the page's document.activeElement is only the host). */
export function focusedIn(root: HTMLElement): Element | null {
  return (root.getRootNode() as Document | ShadowRoot).activeElement;
}

// ---- modal dialogs ---------------------------------------------------------
// Base UI's fully modal dialogs lock scrolling on the page body and hide the
// page's other elements, which a skin must not touch; its focus-trapping
// mode relies on focus guards that refocus a frame later, so a quick Tab
// can leave the dialog. Comic's dialogs therefore make themselves modal
// inside their own root: while one is open the app behind it is inert
// (unreachable by keyboard, pointer and screen readers), and Tab and
// Shift+Tab wrap within the dialog synchronously.

const holds = new WeakMap<HTMLElement, number>();

function holdInert(app: HTMLElement): () => void {
  holds.set(app, (holds.get(app) ?? 0) + 1);
  app.inert = true;
  return () => {
    const n = (holds.get(app) ?? 1) - 1;
    holds.set(app, n);
    if (n <= 0) { holds.delete(app); app.inert = false; }
  };
}

const TABBABLE = 'a[href],area[href],button,input,select,textarea,iframe,summary,[tabindex],[contenteditable]:not([contenteditable="false"])';

/** tabbables lists the elements Tab reaches inside box, in order. */
export function tabbables(box: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = [];
  for (const el of box.querySelectorAll<HTMLElement>(TABBABLE)) {
    if (el.tabIndex < 0 || (el as HTMLButtonElement).disabled || el.closest("[inert]")) continue;
    if (el.hasAttribute("data-base-ui-focus-guard")) continue;
    if (el instanceof HTMLInputElement && el.type === "hidden") continue;
    if (el instanceof HTMLInputElement && el.type === "radio" && !el.checked && el.name
      && box.querySelector(`input[type=radio][name="${CSS.escape(el.name)}"]:checked`)) continue;
    if (!el.getClientRects().length || getComputedStyle(el).visibility === "hidden") continue;
    out.push(el);
  }
  return out;
}

function wrapTab(e: KeyboardEvent<HTMLElement>) {
  if (e.key !== "Tab" || e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey) return;
  const popup = e.currentTarget;
  // React portals bubble too: only keys from the dialog's own elements.
  if (!(e.target instanceof Node) || !popup.contains(e.target)) return;
  const items = tabbables(popup);
  const active = focusedIn(popup);
  if (!items.length) { e.preventDefault(); return; }
  const first = items[0], last = items[items.length - 1];
  if (e.shiftKey && (active === first || active === popup || !items.includes(active as HTMLElement))) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && (active === last || !items.includes(active as HTMLElement) && active !== popup)) { e.preventDefault(); first.focus(); }
}

/** useModal makes an open dialog modal within Comic's root: the app behind
 *  it is inert, and the returned props (spread on Dialog.Popup) mark it
 *  aria-modal and keep Tab inside it. */
export function useModal(open: boolean) {
  const { app } = useOwned();
  useEffect(() => (open ? holdInert(app) : undefined), [open, app]);
  return { "aria-modal": true as const, onKeyDown: wrapTab };
}

// ---- theme: light, dark, or the device's, painted on every mounted root.
export type Theme = "system" | "light" | "dark";

const roots = new Set<HTMLElement>();
let theme: Theme = "system";
let media: MediaQueryList | null = null;
const paint = () => {
  const dark = theme === "dark" || (theme === "system" && !!media?.matches);
  for (const r of roots) r.dataset.theme = dark ? "dark" : "light";
};

/** paintTheme sets the theme of every mounted Comic root. */
export function paintTheme(t: Theme) { theme = t; paint(); }

/** ownTheme paints root now and on every change until the returned stop. */
export function ownTheme(root: HTMLElement): () => void {
  roots.add(root);
  if (!media) { media = matchMedia("(prefers-color-scheme: dark)"); media.addEventListener("change", paint); }
  paint();
  return () => {
    roots.delete(root);
    if (!roots.size && media) { media.removeEventListener("change", paint); media = null; }
  };
}
