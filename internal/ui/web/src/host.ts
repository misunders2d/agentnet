// Host API v1, as loader.js builds it (docs/UI_SKINS.md "Host API").
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

export interface Host {
  version: number;
  platform: "daemon" | "browser";
  api<T = unknown>(path: string, body?: unknown): Promise<T>;
  listen(fn: (e: HostEvent) => void): () => void;
  file(id: string, index: number, dir?: string): Promise<{ bytes: Uint8Array }>;
  stage(file: File): Promise<unknown>;
  onOpen(fn: (conv: string, kind?: string) => void): void;
  skins: { id: string; name: string }[];
  selectSkin(id: string): void;
  workspace: Workspace;
  workspaces: null | {
    list(): Workspace[];
    active(): string;
    has(id: string): boolean;
    select(id: string): void;
    onChange(fn: (e: { id: string }) => void): () => void;
    // Present on hosts with the workspace shell (loader.js); older hosts lack them.
    join?(body: { name: string; invite: string; agent: string; id?: string }): Promise<Host>;
    disconnect?(id: string): Promise<void>;
    state?(id: string): Record<string, unknown>;
  };
  /** Mounts the browser-local interface package importer (local-skins.mjs) into root. */
  manageLocalSkins?(root: HTMLElement): void;
}
