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
  /** The workspace's own name its admin set, as last listed ("Mellanni"); name is this device's own label ("" for none). */
  hub_name?: string;
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

/** Project-space operations use the host's captured membership. Google files
 *  remain outside AgentNet encryption; the core/provider enforces consent. */
export interface DriveRequest { conv: string; action: string; [key: string]: unknown }
export interface DriveProvider {
  drive<T = unknown>(request: DriveRequest): Promise<T>;
  driveUpload<T = unknown>(conv: string, file: File, confirm: boolean): Promise<T>;
  prepareGoogle?(): Promise<void>;
  /** Call directly from a click after prepareGoogle, without an intervening await. */
  beginGoogleConsent?<T = unknown>(request: { conv: string; full?: boolean; confirm_account: boolean }): Promise<T>;
}

export interface AppCommandStatus {
  cli_path: string;
  cli_state: "installed" | "custom" | "error";
  cli_problem?: string;
}

export interface AppStatus extends AppCommandStatus {
  version: string;
  app_update_supported: boolean;
  problem?: string;
  update_result?: string;
}

export interface Host {
  /** Native shell fallback, read only in response to paste; absent in browsers. */
  clipboardImage?(): Promise<File | null>;
  version: number;
  platform: "daemon" | "browser";
  api<T = unknown>(path: string, body?: unknown): Promise<T>;
  listen(fn: (e: HostEvent) => void): () => void;
  file(id: string, index: number, dir?: string): Promise<{ bytes: Uint8Array }>;
  stage(file: File): Promise<unknown>;
  /** Desktop shell only; actions on this computer, never membership APIs. */
  appStatus?(): Promise<AppStatus>;
  appUpdate?(): Promise<{ state: string; message: string }>;
  appReplaceCommand?(): Promise<AppCommandStatus>;
  drive?: DriveProvider;
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
    // Present where the host can keep this device's own label of a
    // membership: name "" clears it (the workspace's own name shows).
    rename?(id: string, name: string): Promise<Workspace>;
  };
}
