// Which skin a page opens, and whether it needs the person's consent
// (docs/UI_SKINS.md). The UI host (loader.js) and the browser-local package
// manager (local-skins.mjs) share these rules; static/skins.go keeps the
// same ids (BuiltinSkins, ReservedSkinIDs) and its tests pin both copies.

// BUILT_IN are the built-in skins: trusted by this list, never by a
// manifest. HOME, Comic, is AgentNet's own skin: the way back from any
// other and what an unknown choice opens. FRESH, Office, is what a profile
// that never chose a skin opens. Classic, Zoom, Pulp… are packages from the
// agentnet-skins collection.
export const BUILT_IN = Object.freeze(["comic", "office"]);
export const HOME = "comic";
export const FRESH = "office";
// RESERVED: no installed or browser-local package may use these ids: the
// built-in skins and "default", the default interface's old name.
export const RESERVED = Object.freeze([...BUILT_IN, "default"]);

/** takenName: a built-in skin's name, ignoring case and spacing; no
 *  installed or browser-local package may show as one
 *  (static/skins.go ReservedSkinName). */
export const takenName = (name) => BUILT_IN.includes(String(name).trim().split(/\s+/).join(" ").toLowerCase());

/** mark: a catalog entry with the host's word on whether it is built in. */
export const mark = (skin) => Object.freeze({ ...skin, builtin: !skin.local && BUILT_IN.includes(skin.id) });

/** choose picks the skin to open from the catalog (marked entries), the
 *  page's ?skin= and the saved choice. A choice saved before Comic was a
 *  package ("default", or "classic", the bundled app) opens Comic and is
 *  rewritten once (save is the value to store, or null); a saved gallery
 *  Office ("local:office") opens the built-in Office and is rewritten once;
 *  ?skin=default stays a name for Comic. No choice at all opens Office
 *  (Comic if Office is missing); an unknown id opens Comic. remember says
 *  whether the skin opened is the one asked for, so the page may record it:
 *  nothing is saved for a profile that never chose, and a saved choice whose
 *  package is missing for now is kept. */
export function choose(skins, { query = null, saved = null, packageChoice = false } = {}) {
  const home = skins.find((s) => s.id === HOME) || null;
  let save = null;
  if (saved === "default" || saved === "classic" && !packageChoice) saved = save = HOME;
  if (saved === "local:" + FRESH && skins.some((s) => s.id === FRESH && s.builtin)) saved = save = FRESH;
  let requested = query || saved || null;
  if (requested === "default") requested = HOME;
  if (requested === "local:" + FRESH) requested = FRESH;
  const asked = requested ? skins.find((s) => s.id === requested) || null : null;
  const fresh = requested ? null : skins.find((s) => s.id === FRESH && s.builtin) || null;
  return { selected: asked || fresh || home, home, save, remember: !!asked };
}

/** trusted: a built-in skin always; any other only when the digest the
 *  person accepted (stored) is exactly the package's current one. */
export const trusted = (skin, stored) => !!skin.builtin || (typeof stored === "string" && /^[0-9a-f]{64}$/.test(stored) && stored === skin.digest);
