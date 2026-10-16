// The host's categories and canonical safe summaries are optional activity. Other details
// keep their existing presentation, including room and native-input waits.
const labels = new Set(["Working", "Reading files", "Editing files", "Running a command", "Searching", "Using a tool", "Preparing answer"]);

// Match the host's canonical safe-line grammar, including Unicode normalization
// and rune (not UTF-16 unit) bounds. Only the host selects/clips native content.
function canonicalThinkingSummary(value        )          {
  if (!value || [...value].length > 120 || value !== value.trim()
    || value !== value.split("…").map(part => part.normalize("NFKC")).join("…")
    || /[\p{Cc}\p{Cf}\r\n\u2028\u2029*_~]|^[-#\s]|[.!?:;,\s]$|\s{2,}/u.test(value)
    || /[^\S ]/u.test(value)) return false;
  if ((value.match(/[\p{L}\p{N}]+/gu) || []).some(token => [...token].length >= 16 && /\p{L}/u.test(token) && /\p{N}/u.test(token))) return false;
  return ![
    /[@/\\]/,
    /https?:\/\/|www\.|\b[\w.+-]+@[\w.-]+\.\w{2,}\b|<[@#!]|@(?:channel|everyone|here)\b/i,
    /(?:^|\s)(?:\/(?:[\w.-]+\/)*[\w.-]+|~\/|[A-Za-z]:\\)|\b[0-9a-f]{8}-[0-9a-f-]{27,}\b|\b[A-Za-z0-9_+/=.-]{32,}\b/i,
    /\b[UCDGW][A-Z0-9]{8,}\b/,
    /\b(?:api[ _-]?key|authorization|bearer|client[ _-]?secret|password|refresh[ _-]?token|secret|token|ssn|social security|medical|diagnos\w*|patient|salary|phone number|account number)\b|[`{}\[\]]|=>|\w\s*=\s*\S/i,
  ].some(pattern => pattern.test(value));
}

export function nativeActivityDetail(detail        )                                        {
  let [label, at, extra] = detail.split("\nReported at ");
  if (extra !== undefined) return null;
  if (label.startsWith("Thinking: ")) {
    label = label.slice("Thinking: ".length);
    if (!canonicalThinkingSummary(label)) return null;
  } else if (!labels.has(label)) return null;
  if (at !== undefined) {
    const date = new Date(at);
    if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(at) || !Number.isFinite(date.getTime()) || date.toISOString() !== at.replace("Z", ".000Z")) return null;
  }
  return { label, at };
}
