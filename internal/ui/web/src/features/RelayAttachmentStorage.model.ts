// The relay owns this account and all ciphertext transfers. This is separate
// from the explicitly plaintext, per-conversation Project Space feature.
export type RelayStorageState = "local" | "not_configured" | "connected" | "migrating" | "needs_reconnect" | "offline";
export type RelayStorageAction = "connect" | "move" | "disconnect";
export interface RelayStorageAmount { files: number; bytes: number }
export interface RelayStorageStatus {
  can_admin: boolean;
  configured: boolean;
  connected: boolean;
  state: RelayStorageState;
  notice?: string;
  account?: { email: string; name: string };
  folder?: { id: string; name: string };
  local: RelayStorageAmount;
  remote: RelayStorageAmount;
  pending: RelayStorageAmount;
  remote_only_files: number;
  migration: { running: boolean; files: number; bytes: number; total_files: number; total_bytes: number };
  consent_url?: string;
}

const states: RelayStorageState[] = ["local", "not_configured", "connected", "migrating", "needs_reconnect", "offline"];
const invalid = () => new Error("The relay’s attachment storage status was not understood. Refresh to try again.");
function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw invalid();
  return value as Record<string, unknown>;
}
function count(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) throw invalid();
  return value;
}
function amount(value: unknown): RelayStorageAmount {
  const v = record(value);
  return { files: count(v.files), bytes: count(v.bytes) };
}

/** Only a server-returned canonical Google consent URL becomes a link. The
 *  user follows it with a fresh gesture in the existing native browser path. */
export function googleConsentURL(value: unknown): string {
  if (typeof value !== "string" || value.length > 8192) return "";
  try {
    const u = new URL(value);
    if (u.protocol !== "https:" || u.hostname !== "accounts.google.com" || u.port || u.username || u.password || u.hash || u.pathname !== "/o/oauth2/v2/auth") return "";
    if ([...u.searchParams.keys()].some(k => /^(?:client_secret|access_token|refresh_token|id_token)$/i.test(k))) return "";
    return value;
  } catch { return ""; }
}

export function readRelayStorage(value: unknown, consent = false): RelayStorageStatus {
  const v = record(value), migration = record(v.migration);
  if (typeof v.can_admin !== "boolean" || typeof v.configured !== "boolean" || typeof v.connected !== "boolean"
    || !states.includes(v.state as RelayStorageState) || typeof migration.running !== "boolean") throw invalid();
  const status: RelayStorageStatus = {
    can_admin: v.can_admin, configured: v.configured, connected: v.connected, state: v.state as RelayStorageState,
    local: amount(v.local), remote: amount(v.remote), pending: amount(v.pending), remote_only_files: count(v.remote_only_files),
    migration: { running: migration.running, files: count(migration.files), bytes: count(migration.bytes), total_files: count(migration.total_files), total_bytes: count(migration.total_bytes) },
  };
  // Drop private operator details even if an older relay includes them for a
  // non-admin. Never retain arbitrary response properties or credentials.
  if (status.can_admin) {
    if (typeof v.notice === "string") status.notice = v.notice;
    if (v.account) {
      const account = record(v.account);
      status.account = { email: typeof account.email === "string" ? account.email : "", name: typeof account.name === "string" ? account.name : "" };
    }
    if (v.folder) {
      const folder = record(v.folder);
      status.folder = { id: typeof folder.id === "string" ? folder.id : "", name: typeof folder.name === "string" ? folder.name : "" };
    }
    if (consent && v.consent_url) {
      status.consent_url = googleConsentURL(v.consent_url);
      if (!status.consent_url) throw new Error("The relay did not return a valid Google consent link. Ask its operator to check Google setup.");
    }
  } else {
    status.local = { files: 0, bytes: 0 };
    status.remote = { files: 0, bytes: 0 };
    status.pending = { files: 0, bytes: 0 };
    status.remote_only_files = 0;
    status.migration = { running: false, files: 0, bytes: 0, total_files: 0, total_bytes: 0 };
  }
  return status;
}

export function canUseStorageAction(status: RelayStorageStatus, action: RelayStorageAction): boolean {
  if (!status.can_admin) return false;
  if (action === "connect") return status.configured && (!status.connected || status.state === "needs_reconnect");
  if (status.migration.running || status.state === "migrating") return false;
  if (action === "move") return status.configured && status.connected && (status.state === "connected" || status.state === "offline") && status.local.files > 0;
  return status.connected && status.remote_only_files === 0 && status.remote.files === 0;
}

export const storageFiles = (n: number) => n.toLocaleString() + (n === 1 ? " file" : " files");
export function storageBytes(n: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let index = 0;
  while (n >= 1024 && index < units.length - 1) { n /= 1024; index++; }
  return n.toLocaleString(undefined, { maximumFractionDigits: index ? 1 : 0 }) + " " + units[index];
}

export function storageTitle(status: RelayStorageStatus, connecting: boolean): string {
  if (connecting) return "Complete the Google connection";
  switch (status.state) {
    case "not_configured": return "Google Drive needs operator setup";
    case "connected": return "Google Drive connected";
    case "migrating": return "Moving existing attachments";
    case "needs_reconnect": return "Google Drive needs reconnection";
    case "offline": return "Google Drive is unavailable";
    default: return "Attachments stored on this relay";
  }
}
