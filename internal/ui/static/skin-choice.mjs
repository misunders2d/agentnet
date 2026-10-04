// Which skin a page opens, and whether it needs the person's consent
// (docs/UI_SKINS.md). The UI host (loader.js) and the browser-local package
// manager (local-skins.mjs) share these rules; static/skins.go keeps the
// same ids (BuiltinSkins, ReservedSkinIDs) and its tests pin both copies.

// BUILT_IN are the built-in skins, present and to come: trusted by this
// list, never by a manifest. HOME is the default skin.
export const BUILT_IN = Object.freeze(["comic", "classic", "zoom"]);
export const HOME = "comic";
// RESERVED: no installed or browser-local package may use these ids: the
// built-in skins and "default", the default interface's old name.
export const RESERVED = Object.freeze([...BUILT_IN, "default"]);

/** takenName: a built-in skin's name (present or to come), ignoring case
 *  and spacing; no installed or browser-local package may show as one
 *  (static/skins.go ReservedSkinName). */
export const takenName = (name) => BUILT_IN.includes(String(name).trim().split(/\s+/).join(" ").toLowerCase());

/** mark: a catalog entry with the host's word on whether it is built in. */
export const mark = (skin) => Object.freeze({ ...skin, builtin: !skin.local && BUILT_IN.includes(skin.id) });

/** choose picks the skin to open from the catalog (marked entries), the
 *  page's ?skin= and the saved choice. A choice saved before Comic was a
 *  package ("default", or "classic", the bundled app) opens Comic and is
 *  rewritten once (save is the value to store, or null); ?skin=default
 *  stays a name for Comic. An unknown id opens Comic. */
export function choose(skins, { query = null, saved = null } = {}) {
  const home = skins.find((s) => s.id === HOME) || null;
  let save = null;
  if (saved === "default" || saved === "classic") saved = save = HOME;
  let requested = query || saved || HOME;
  if (requested === "default") requested = HOME;
  return { selected: skins.find((s) => s.id === requested) || home, home, save };
}

/** trusted: a built-in skin always; any other only when the digest the
 *  person accepted (stored) is exactly the package's current one. */
export const trusted = (skin, stored) => !!skin.builtin || (typeof stored === "string" && /^[0-9a-f]{64}$/.test(stored) && stored === skin.digest);
