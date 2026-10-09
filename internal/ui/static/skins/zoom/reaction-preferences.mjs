// A local convenience shared by the skins, never message state or authority.
// Counts mean successful human additions on this browser, not received marks.
const key = "agentnet.messenger.reaction-usage.v1";
const limit = 64, maxCount = 1_000_000;
const local = () => localStorage;
const emojiValue = e => typeof e === "string" && e.length > 0 && e.length <= 128;

function usage(storage) {
  try {
    const raw = storage()?.getItem(key);
    if (!raw || raw.length > 16384) return [];
    const rows = JSON.parse(raw), seen = new Set();
    if (!Array.isArray(rows) || rows.length > limit) return [];
    return rows.filter(r => {
      if (!Array.isArray(r) || r.length !== 2 || !emojiValue(r[0]) || !Number.isSafeInteger(r[1]) || r[1] < 1 || r[1] > maxCount || seen.has(r[0])) return false;
      seen.add(r[0]);
      return true;
    });
  } catch { return []; }
}

export function recordReaction(emoji, storage = local) {
  if (!emojiValue(emoji)) return;
  const rows = usage(storage), row = rows.find(r => r[0] === emoji);
  if (row) row[1] = Math.min(maxCount, row[1] + 1);
  else {
    // Make room for a new choice by forgetting the least-used saved one.
    // Stable sorting keeps first-use order for equal counts.
    if (rows.length === limit) {
      const least = Math.min(...rows.map(r => r[1]));
      rows.splice(rows.findLastIndex(r => r[1] === least), 1);
    }
    rows.push([emoji, 1]);
  }
  try { storage()?.setItem(key, JSON.stringify(rows)); } catch { /* preference only */ }
}

export function reactionChoices(fallback = [], count = fallback.length, storage = local) {
  const frequent = usage(storage).sort((a, b) => b[1] - a[1]).map(r => r[0]);
  return [...new Set([...frequent, ...fallback])].slice(0, count);
}

export function humanReactionOrder(reactions) {
  const count = r => (r.by || []).filter(b => !b.assistant).length;
  return [...reactions].sort((a, b) => count(b) - count(a));
}
