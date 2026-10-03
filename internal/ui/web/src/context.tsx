import { createContext, useContext, useEffect, useState } from "react";
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

/** useWide is true on desktop layouts (1024px and up). */
export function useWide(): boolean {
  const q = "(min-width: 1024px)";
  const [wide, setWide] = useState(() => matchMedia(q).matches);
  useEffect(() => {
    const m = matchMedia(q);
    const on = () => setWide(m.matches);
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return wide;
}
