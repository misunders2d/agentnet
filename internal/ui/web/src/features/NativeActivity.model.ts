// Only the host's fixed public categories are activity. Other request details
// keep their existing presentation, including room and native-input waits.
const labels = new Set(["Working", "Reading files", "Editing files", "Running a command", "Searching", "Using a tool", "Preparing answer"]);

export function nativeActivityDetail(detail: string): { label: string; at?: string } | null {
  const [label, at, extra] = detail.split("\nReported at ");
  if (!labels.has(label) || extra !== undefined) return null;
  if (at !== undefined) {
    const date = new Date(at);
    if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(at) || !Number.isFinite(date.getTime()) || date.toISOString() !== at.replace("Z", ".000Z")) return null;
  }
  return { label, at };
}

export function currentNativeActivity(local: string | undefined, exec: { state: string; stale?: boolean; detail?: string } | undefined, running: boolean): string {
  if (!running) return "";
  const detail = local || (exec?.state === "running" && !exec.stale ? exec.detail : "") || "";
  return nativeActivityDetail(detail) ? detail : "";
}
