// Host API v1, as loader.js builds it (docs/UI_SKINS.md "Host API v1"),
// with its documented additive extensions. Comic uses only this.
export type HostEvent = { type: "change"; seq: number } | { type: "restart" } | { type: "disconnect" };

export interface Workspace {
  id: string;
  name: string;
  endpoint: string;
  address: string;
  realm: string;
  handle?: string;
  state: string;
}

/** A catalog entry (host.skins): a built-in, installed or browser-local package. */
export interface SkinEntry {
  api: number;
  id: string;
  name: string;
  digest?: string;
  local?: boolean;     // stored in this browser (id "local:…")
  builtin?: boolean;   // embedded in this program; trusted by the host's own list
}

/** Where a notification or link asks the skin to go (host.onOpen). */
export type OpenKind = "channel" | "conversation" | "message" | "review";
export interface OpenContext { conv?: string; dir?: "in" | "out" }

export interface Host {
  version: number;
  platform: "daemon" | "browser";
  api<T = unknown>(path: string, body?: unknown): Promise<T>;
  listen(fn: (e: HostEvent) => void): () => void;
  file(id: string, index: number, dir?: string): Promise<{ bytes: Uint8Array }>;
  stage(file: File): Promise<unknown>;
  /** Registers notification routing; kinds lists the destinations handled
   *  beyond channel and conversation (the host offers the rest itself). */
  onOpen(fn: (target: string, kind?: OpenKind, context?: OpenContext) => void, kinds?: OpenKind[]): void;
  skins: SkinEntry[];
  /** Calls fn whenever host.skins changes (a package imported or removed). */
  onSkinsChange(fn: () => void): () => void;
  selectSkin(id: string): void;
  /** Mounts the browser-local package manager into root; returns its teardown. */
  manageLocalSkins?(root: HTMLElement): () => void;
  /** Present only where this computer's program can bind this membership
   *  again after it restarted: identity-checked, then the host mounts the
   *  skin again over the new binding. Never replays a send. */
  reconnect?(): Promise<void>;
  workspace: Workspace;
  workspaces: null | {
    list(): Workspace[];
    active(): string;
    has(id: string): boolean;
    select(id: string): void;
    onChange(fn: (e: { id: string }) => void): () => void;
    join?(body: { name: string; invite: string; agent: string; id?: string }): Promise<Host>;
    disconnect?(id: string): Promise<void>;
    state?(id: string): Record<string, unknown>;
    // Present only where this computer's program can route a membership it
    // disconnected again (never a browser enrollment): the memberships
    // disconnected here, and reconnecting one (same keys and history, a new
    // handle; it is not selected).
    disconnected?(): Promise<Workspace[]>;
    reconnect?(id: string): Promise<Host>;
  };
}
