// Responses contain JSON only. Reuse unchanged subtrees, comparing every field:
// ids and delivery state alone do not cover edits, permissions or reactions.
export function share<T>(previous: T | undefined, next: T): T {
  if (Object.is(previous, next)) return next;
  if (!previous || !next || typeof previous !== "object" || typeof next !== "object") return next;
  if (Array.isArray(previous) !== Array.isArray(next)) return next;
  const old = previous as Record<string, unknown>, fresh = next as Record<string, unknown>;
  const keys = Object.keys(fresh);
  let same = Object.keys(old).length === keys.length;
  for (const key of keys) {
    const value = share(old[key], fresh[key]);
    fresh[key] = value;
    if (!Object.hasOwn(old, key) || !Object.is(old[key], value)) same = false;
  }
  return same ? previous : next;
}
