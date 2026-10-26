// Local text survives a responsive parent remount, within this captured Store.
// Callers key the exact editor target and its original revision/configuration.
import { useState, type Dispatch, type SetStateAction } from "react";
import { useApp } from "./context";
import type { Store } from "./store";

type Entry = { value: string };
const drafts = new WeakMap<Store, Map<string, Entry>>();
const scopeOf = (store: Store, key: string) => {
  const me = store.get().overview?.me;
  return JSON.stringify([store.host.workspace.id, me?.address, me?.fingerprint, key]);
};

export function discardRetainedText(store: Store, key: string) {
  drafts.get(store)?.delete(scopeOf(store, key));
}

export function useRetainedText(key: string, initial: string) {
  const store = useApp();
  let entries = drafts.get(store);
  if (!entries) { entries = new Map(); drafts.set(store, entries); }
  const map = entries;
  const scope = scopeOf(store, key);
  const [, render] = useState(0);
  const value = map.get(scope)?.value ?? initial;
  const set: Dispatch<SetStateAction<string>> = next => {
    const previous = map.get(scope)?.value ?? initial;
    map.set(scope, { value: typeof next === "function" ? next(previous) : next });
    render(n => n + 1);
  };
  return [value, set, {
    discard() { map.delete(scope); render(n => n + 1); },
    // A result from an older/unmounted editor cannot erase a newer edit,
    // including a newer write of the same string.
    capture() {
      const submitted = map.get(scope);
      return () => { if (map.get(scope) === submitted) { map.delete(scope); render(n => n + 1); } };
    },
  }] as const;
}
