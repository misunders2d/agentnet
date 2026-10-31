// The skin gallery (docs/UI_SKINS.md, Skin gallery): the published packages
// of the agentnet-skins collection, offered to this browser. Only the latest
// published release counts, read from one fixed repository; every file and
// preview must match the SHA-256 its catalog names, and a package must match
// its catalog digest before it is stored. Installing goes through the
// browser-local package manager, so choosing the skin still asks for trust,
// except for an update of a version the person trusted.
// Nothing is uploaded and no AgentNet data leaves this page.
import * as localSkins from './local-skins.mjs';

export const REPOSITORY = 'misunders2d/agentnet-skins';
export const LATEST = 'https://api.github.com/repos/' + REPOSITORY + '/releases/latest';
export const RAW = 'https://raw.githubusercontent.com/' + REPOSITORY + '/';
const TAG = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/, ID = /^[a-z][a-z0-9-]{0,47}$/, HEX = /^[0-9a-f]{64}$/;
const RELEASE = /^v(\d{1,6})\.(\d{1,6})\.(\d{1,6})$/;
// A development build stamped from a release counts as that release (protocol.Newer).
const RUNNING = /^v(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:-\d{1,9}-g[0-9a-f]{4,40}|\+[0-9A-Za-z.]{1,64})?(?:-dirty)?$/;
const LIMITS = { latest: 256 << 10, catalog: 512 << 10, file: 4 << 20, preview: 1 << 20, package: 16 << 20 };
const enc = new TextEncoder();
const fail = (text) => { throw new Error(text); };
const hex = (bytes) => [...new Uint8Array(bytes)].map((x) => x.toString(16).padStart(2, '0')).join('');
const segment = /^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$/;
const pathOK = (p) => typeof p === 'string' && p.length <= 200 && p.split('/').every((s) => segment.test(s) && s !== '..');
const types = { png: 'image/png', webp: 'image/webp' };

/** newer reports whether release candidate is newer than the running build;
 *  an unknown running version (a development build) never refuses. */
export function newer(candidate, running) {
  const a = RELEASE.exec(candidate || ''), b = RUNNING.exec(running || '');
  if (!a || !b) return false;
  for (let i = 1; i <= 3; i++) if (+a[i] !== +b[i]) return +a[i] > +b[i];
  return false;
}

async function get(url, limit, fetcher) {
  let response;
  try {
    response = await fetcher(url, { credentials: 'omit', referrerPolicy: 'no-referrer', cache: 'no-cache', redirect: 'error' });
  } catch (_) { fail('The skin gallery could not be reached. Check the connection and try again.'); }
  if (!response.ok) fail(response.status === 403 || response.status === 429
    ? 'The skin gallery is busy. Try again in a few minutes.'
    : 'The skin gallery could not be read (' + response.status + '). Try again later.');
  const declared = Number(response.headers.get('Content-Length'));
  if (declared > limit) fail('A gallery file is larger than allowed.');
  const bytes = new Uint8Array(await response.arrayBuffer());
  if (bytes.length > limit) fail('A gallery file is larger than allowed.');
  return bytes;
}
const json = (bytes) => { try { return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)); } catch (_) { fail('The skin gallery sent an unreadable catalog.'); } };
const sha = async (bytes) => hex(await crypto.subtle.digest('SHA-256', bytes));

// validate keeps only well-formed entries; one malformed entry never hides
// the others, and nothing it says is trusted beyond its own fields.
function validate(raw) {
  if (!raw || raw.catalog !== 1 || !Array.isArray(raw.skins) || raw.skins.length > 64) fail('The skin gallery sent an unreadable catalog.');
  const out = [], seen = new Set();
  for (const s of raw.skins) {
    if (!s || typeof s !== 'object' || !ID.test(s.id) || seen.has(s.id)) continue;
    if (typeof s.name !== 'string' || !s.name.trim() || enc.encode(s.name).length > 80) continue;
    if (s.api !== 1 && !(Number.isInteger(s.api) && s.api > 1)) continue;
    if (s.agentnet !== undefined && !RELEASE.test(s.agentnet)) continue;
    if (!HEX.test(s.digest) || typeof s.version !== 'string' || !/^[0-9A-Za-z.+-]{1,32}$/.test(s.version)) continue;
    const file = (f, previews) => f && pathOK(f.path) && HEX.test(f.sha256) && Number.isInteger(f.size) && f.size >= 0 &&
      f.size <= (previews ? LIMITS.preview : LIMITS.file) && (!previews || (f.path.startsWith('previews/') && types[f.path.split('.').pop()]));
    if (!Array.isArray(s.files) || !s.files.length || s.files.length > 33 || !s.files.every((f) => file(f, false))) continue;
    if (!s.files.some((f) => f.path === 'skin.json') || new Set(s.files.map((f) => f.path)).size !== s.files.length) continue;
    if (s.files.reduce((n, f) => n + f.size, 0) > LIMITS.package) continue;
    const previews = Array.isArray(s.previews) ? s.previews.slice(0, 6) : [];
    if (!previews.every((p) => file(p, true))) continue;
    seen.add(s.id);
    out.push(Object.freeze({
      id: s.id, name: s.name.trim(), version: s.version, api: s.api, digest: s.digest,
      description: typeof s.description === 'string' ? s.description.trim().slice(0, 400) : '',
      ...(s.agentnet ? { agentnet: s.agentnet } : {}),
      files: Object.freeze(s.files.map((f) => Object.freeze({ path: f.path, sha256: f.sha256, size: f.size }))),
      previews: Object.freeze(previews.map((p) => Object.freeze({ path: p.path, sha256: p.sha256, size: p.size, alt: typeof p.alt === 'string' ? p.alt.slice(0, 200) : '' }))),
    }));
  }
  return out;
}

/** gallery binds the fixed source to this page. running() names the
 *  AgentNet version serving the page ("" while unknown); changed() refreshes
 *  host.skins after an install. */
// trust reads and writes the digest a person trusted for a stored skin id
// (the host keeps it); an update of a version they trusted keeps that trust.
export function gallery({ running = () => '', changed = async () => {}, fetcher = (...a) => globalThis.fetch(...a), local = localSkins, trust = null } = {}) {
  const { supported, prepare, install: store, catalog: stored } = local;
  let cached = null; // { at, release, skins } for this page
  const load = async (fresh) => {
    if (!fresh && cached && Date.now() - cached.at < 5 * 60e3) return cached;
    const latest = json(await get(LATEST, LIMITS.latest, fetcher));
    const release = latest && typeof latest.tag_name === 'string' ? latest.tag_name : '';
    if (!TAG.test(release) || latest.draft || latest.prerelease) fail('The skin gallery has no published release yet.');
    const skins = validate(json(await get(RAW + release + '/published/catalog.json', LIMITS.catalog, fetcher)));
    cached = { at: Date.now(), release, skins };
    return cached;
  };
  const find = async (id) => {
    const { release, skins } = await load(false);
    const skin = skins.find((s) => s.id === id);
    if (!skin) fail('That skin is not in the gallery any more.');
    return { release, skin };
  };
  const compatible = (s) => s.api === 1 && !newer(s.agentnet, running());
  return Object.freeze({
    /** The latest published release's skins, with what this browser holds. */
    async list({ fresh = false } = {}) {
      if (!supported()) fail('This browser cannot store skins.');
      const { release, skins } = await load(fresh);let local;
      try { local = await stored(); } catch (e) {
        // Discovery never repairs storage. An unreadable local catalog cannot
        // prove installation; the explicit verified store still checks safety.
        if (e.cause !== 'skin-catalog-unavailable') throw e;
        local = [];
      }
      return Object.freeze({ release, skins: Object.freeze(skins.map((s) => {
        const held = local.find((x) => x.package_id === s.id);
        const state = !compatible(s) ? 'incompatible' : !held ? 'install' : held.digest === s.digest ? 'installed' : 'update';
        return Object.freeze({ id: s.id, name: s.name, description: s.description, version: s.version, api: s.api,
          ...(s.agentnet ? { agentnet: s.agentnet } : {}), state, installed: held ? held.id : '', previews: s.previews.length,
          alts: Object.freeze(s.previews.map((p) => p.alt)) });
      })) });
    },
    /** One preview image as a Blob, after its SHA-256 matched. */
    async preview(id, index) {
      const { release, skin } = await find(id), p = skin.previews[index];
      if (!p) fail('That preview does not exist.');
      const bytes = await get(RAW + release + '/published/' + skin.id + '/' + p.path, LIMITS.preview, fetcher);
      if (bytes.length !== p.size || await sha(bytes) !== p.sha256) fail('A preview did not match the gallery. It was not shown.');
      return new Blob([bytes], { type: types[p.path.split('.').pop()] });
    },
    /** Stores the package in this browser; resolves its catalog id for
     *  host.selectSkin, which asks for trust. Never selects it itself. */
    async install(id) {
      if (!supported()) fail('This browser cannot store skins.');
      const { release, skin } = await find(id);
      if (!compatible(skin)) fail(skin.agentnet ? 'This skin needs AgentNet ' + skin.agentnet + ' or newer. Update AgentNet first.' : 'This skin needs a newer AgentNet.');
      const files = [];
      for (const f of skin.files) {
        const bytes = await get(RAW + release + '/published/' + skin.id + '/' + f.path, LIMITS.file, fetcher);
        if (bytes.length !== f.size || await sha(bytes) !== f.sha256) fail('A file of ' + skin.name + ' did not match the gallery. Nothing was installed.');
        // The package manager reads these like chosen files (local-skins.mjs prepare).
        files.push({ name: f.path, webkitRelativePath: f.path, size: bytes.length, arrayBuffer: async () => bytes.slice().buffer });
      }
      const { item } = await prepare(files);
      if (item.package_id !== skin.id || item.digest !== skin.digest) fail(skin.name + ' did not match the gallery. Nothing was installed.');
      let held = null;
      try { held = (await stored()).find((x) => x.package_id === skin.id) || null; } catch (_) { /* no usable earlier version */ }
      const saved = await store(files);
      // Owner decision (October 31): a gallery update keeps trust given to the
      // exact version it replaces. Anything else asks as for any stored skin.
      try { if (trust && held && held.digest !== saved.digest && trust.get(saved.id) === held.digest) trust.set(saved.id, saved.digest); } catch (_) { /* asks again */ }
      await changed();
      return saved.id;
    },
  });
}

/** Installs the published version of every gallery skin stored here that
 *  has a newer compatible one; resolves the ids updated. One failing skin
 *  never stops the others. A browser storing no skin never asks the gallery. */
export async function updateInstalled(g, local = localSkins) {
  const updated = [];
  if (!local.supported() || !(await local.catalog()).length) return updated;
  for (const s of (await g.list()).skins) {
    if (s.state !== 'update') continue;
    try { await g.install(s.id); updated.push(s.id); } catch (_) { /* the gallery offers it again */ }
  }
  return updated;
}
