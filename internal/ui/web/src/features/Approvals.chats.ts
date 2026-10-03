// The chats where something may wait for this person: questions held for
// them, and chats their agent is in or invited to (requests to it and its
// invitations live only in the conversation itself, /api/dm). One load per
// change, shared by the OKs badge, the home banner and the OKs list, so all
// three count the same things.
import { useEffect, useSyncExternalStore } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { useStore, type Store } from "../store";

export type Chats = Record<string, T.DMThread>;

/** The chats to read: held questions, and where this device's agent is in or invited. */
export function chatIds(o: T.Overview): string[] {
  return [...new Set([
    ...(o.dms || []).filter((d) => d.held > 0).map((d) => d.id),
    ...(o.person?.agents || []).filter((a) => a.address === o.me.address)
      .flatMap((a) => (a.dms || []).filter((d) => d.state === "invited" || d.state === "active").map((d) => d.conv)),
  ])];
}

class Loader {
  private chats: Chats = {};
  private asked = "";            // the ids and overview seq last read
  private gen = 0;
  private subs = new Set<() => void>();
  constructor(private store: Store) {}

  subscribe = (fn: () => void) => { this.subs.add(fn); return () => { this.subs.delete(fn); }; };
  get = () => this.chats;

  sync(o: T.Overview | null) {
    if (!o) return;
    const ids = chatIds(o);
    const asked = ids.join(",") + "@" + o.seq;
    if (asked === this.asked) return;
    this.asked = asked;
    const mine = ++this.gen;
    Promise.all(ids.map((id) => this.store.api.dm(id).catch(() => null))).then((rows) => {
      if (mine !== this.gen) return; // a newer read started meanwhile
      const next: Chats = {};
      // A chat that could not be read this time keeps what was read before.
      ids.forEach((id, i) => { const t = rows[i] || this.chats[id]; if (t) next[id] = t; });
      this.chats = next;
      for (const f of this.subs) f();
    });
  }
}

const loaders = new WeakMap<Store, Loader>();

/** useNeedsYouChats: the chats read for OKs, shared by every screen that counts them. */
export function useNeedsYouChats(): Chats {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  let l = loaders.get(store);
  if (!l) { l = new Loader(store); loaders.set(store, l); }
  const loader = l;
  useEffect(() => loader.sync(o), [loader, o]);
  return useSyncExternalStore(loader.subscribe, loader.get);
}
