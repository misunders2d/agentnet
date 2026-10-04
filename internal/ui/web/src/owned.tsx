// The skin's own tree. The host mounts Comic into a root inside a shadow
// tree (docs/UI_SKINS.md); everything Comic draws, styles or focuses stays
// in that root: the theme is an attribute on it, every popup (Base UI
// portals) renders into a container inside it, and focus is read from its
// shadow root, never from the page.
import { createContext, useContext } from "react";

export interface Owned {
  root: HTMLElement;     // the root the host gave (tokens and theme live here)
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
