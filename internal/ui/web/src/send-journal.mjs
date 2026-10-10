// Android's explicit send gestures survive process death. This records exact
// API requests, not delivery claims; only a successful native admission clears
// an entry. Reading never retries or grants authority.
const endpoints = new Set(["/api/send", "/api/dm/send", "/api/dm/agent/ask", "/api/act"]);
const key = workspace => "agentnet.android.send-journal.v1." + workspace;
const clone = value => JSON.parse(JSON.stringify(value));
function valid(entry) {
  return entry && /^[0-9a-f]{32}$/.test(entry.id) && typeof entry.conversation === "string" &&
    endpoints.has(entry.endpoint) && entry.request && typeof entry.request === "object" && !Array.isArray(entry.request) &&
    (entry.endpoint === "/api/act" ? entry.request.do === "reply" && entry.request.send_id === entry.id : entry.request.id === entry.id);
}
export function readPending(storage, workspace) {
  const value = JSON.parse(storage.getItem(key(workspace)) || "[]");
  if (!Array.isArray(value) || value.some(e => !valid(e)) || new Set(value.map(e => e.id)).size !== value.length)
    throw new Error("The saved send journal is damaged. Nothing was retried.");
  return clone(value);
}
function write(storage, workspace, entries) {
  storage.setItem(key(workspace), JSON.stringify(entries));
  if (typeof window !== "undefined") window.dispatchEvent(new Event("agentnet-send-journal"));
}
export function saveBatch(storage, workspace, entries) {
  const pending = readPending(storage, workspace);
  for (const original of entries) {
    const entry = clone(original);
    if (!valid(entry)) throw new Error("Cannot save an incomplete send request.");
    const existing = pending.find(e => e.id === entry.id);
    if (existing) {
      if (existing.endpoint !== entry.endpoint || existing.conversation !== entry.conversation || JSON.stringify(existing.request) !== JSON.stringify(entry.request))
        throw new Error("This send ID already belongs to a different request.");
    } else pending.push(entry);
  }
  // One synchronous write freezes every target before any target is submitted.
  write(storage, workspace, pending);
}
export function acknowledge(storage, workspace, id) {
  write(storage, workspace, readPending(storage, workspace).filter(e => e.id !== id));
}
export function recordFailure(storage, workspace, id, error) {
  write(storage, workspace, readPending(storage, workspace).map(e => e.id === id ? { ...e, error: String(error) } : e));
}
