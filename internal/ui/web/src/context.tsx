import { createContext, useContext, useEffect, useState, useSyncExternalStore } from "react";
import type { Store } from "./store";
import { useStore } from "./store";

export const StoreContext = createContext<Store | null>(null);

export function useApp(): Store {
  const s = useContext(StoreContext);
  if (!s) throw new Error("StoreContext missing");
  return s;
}

/** useAgentNames maps agent ids to the names their owners gave them
 *  (agent catalogs), loaded once per change by the store. Missing names
 *  fall back to words built from the address. */
export function useAgentNames(): Record<string, string> {
  return useStore(useApp(), (s) => s.agentNames);
}

/** useWide is true on desktop layouts (1024px and up), one value for every
 *  reader. Phone and desktop are different trees: a swap remounts the open
 *  conversation and drops its state (an open menu, a hovered toolbar). So a
 *  swap applies only once the width still says so at the next frame, and
 *  desktop is left only under 1000px: a classic scrollbar (15-17px) or a
 *  dip at the boundary never swaps it. */
const wideEnter = "(min-width: 1024px)", wideStay = "(min-width: 1000px)";
let wideNow: boolean | undefined, wideFrame = 0, wideQueries: MediaQueryList[] | null = null;
const wideSubs = new Set<() => void>();
const wideRead = () => wideNow === undefined ? matchMedia(wideEnter).matches : matchMedia(wideNow ? wideStay : wideEnter).matches;
const wideSettle = () => {
  cancelAnimationFrame(wideFrame);
  wideFrame = requestAnimationFrame(() => {
    const next = wideRead();
    if (next === wideNow) return;
    wideNow = next;
    for (const fn of wideSubs) fn();
  });
};
const wideSnapshot = () => wideNow ??= wideRead();
const wideSubscribe = (fn: () => void) => {
  wideSubs.add(fn);
  if (!wideQueries) { wideQueries = [matchMedia(wideEnter), matchMedia(wideStay)]; for (const m of wideQueries) m.addEventListener("change", wideSettle); }
  return () => {
    wideSubs.delete(fn);
    if (wideSubs.size || !wideQueries) return;
    for (const m of wideQueries) m.removeEventListener("change", wideSettle);
    wideQueries = null; cancelAnimationFrame(wideFrame); wideNow = undefined;
  };
};
export function useWide(): boolean {
  return useSyncExternalStore(wideSubscribe, wideSnapshot);
}

/** A mouse/trackpad remains usable in a narrow window; width chooses layout. */
export function useHoverPointer(): boolean {
  const q = "(any-hover: hover) and (any-pointer: fine)";
  const [hover, setHover] = useState(() => matchMedia(q).matches);
  useEffect(() => {
    const m = matchMedia(q), on = () => setHover(m.matches);
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return hover;
}
