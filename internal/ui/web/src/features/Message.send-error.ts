// Only the producer's explicit code classifies a failure. Human prose never
// grants a cleanup/retry action, and Drive migration pause is not a send error.
const causes = {
  relay_quota: "The workspace attachment quota is full.",
  file_too_large: "This file exceeds the server's file-size limit.",
  relay_disk_full: "The server is out of disk space.",
  local_disk_full: "This device is out of storage.",
  network_unavailable: "The server cannot be reached.",
};
export type SendErrorCode = keyof typeof causes;
export function sendFailure(detail: string | undefined, admin: boolean) {
  const match = /^send_error:([a-z_]+):\s*([\s\S]*)$/.exec(detail || "");
  if (!match || !Object.hasOwn(causes, match[1])) return null;
  const code = match[1] as SendErrorCode;
  const action = code === "relay_quota" ? admin ? "Open Storage to review the quota, then retry this message." : "Ask a workspace admin to review the attachment quota, then retry this message."
    : code === "file_too_large" ? "Choose a smaller file or prepare a smaller photo or video. Your original is kept."
      : code === "relay_disk_full" ? admin ? "Free disk space on the server, then retry this message." : "Ask a workspace admin to free disk space on the server, then retry this message."
        : code === "local_disk_full" ? "Free space on this device, then retry this message."
          : "Your message and files are kept here. Retry when the connection returns.";
  return { code, reason: causes[code], action, storage: code === "local_disk_full" || code === "relay_quota" && admin };
}
