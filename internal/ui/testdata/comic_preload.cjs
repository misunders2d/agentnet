// Test-only Node preload (TestMain puts it in NODE_OPTIONS for every rendered
// check this package runs). Since MEL-821 a browser that never chose a skin
// opens Office; the rendered checks written for Comic opened Comic that way.
// This keeps them on Comic: each Playwright context starts with
// agentnet.skin = "comic" when nothing is saved yet, as the loader before
// MEL-821 saved on its first open. A check about the default itself sets
// AGENTNET_TEST_FRESH_SKIN=1 and sees the real first open.
if (!process.env.AGENTNET_TEST_FRESH_SKIN) {
  const Module = require('module');
  const load = Module._load, patched = new WeakSet();
  const seed = () => { try { if (localStorage.getItem('agentnet.skin') === null) localStorage.setItem('agentnet.skin', 'comic'); } catch (_) { /* opaque origin */ } };
  const context = async (ctx) => { await ctx.addInitScript(seed); return ctx; };
  const browser = (b) => {
    const newContext = b.newContext.bind(b), newPage = b.newPage.bind(b);
    b.newContext = async (...a) => context(await newContext(...a));
    // newPage makes its own context: seed it, then reload what it opened.
    b.newPage = async (...a) => { const p = await newPage(...a); await p.context().addInitScript(seed); return p; };
    return b;
  };
  const type = (t) => {
    if (!t || patched.has(t) || typeof t.launch !== 'function') return;
    patched.add(t);
    const launch = t.launch.bind(t), persistent = t.launchPersistentContext?.bind(t), connect = t.connectOverCDP?.bind(t);
    t.launch = async (...a) => browser(await launch(...a));
    if (persistent) t.launchPersistentContext = async (...a) => context(await persistent(...a));
    if (connect) t.connectOverCDP = async (...a) => browser(await connect(...a));
  };
  Module._load = function (...a) {
    const out = load.apply(this, a);
    if (out && typeof out === 'object' && out.chromium) for (const k of ['chromium', 'firefox', 'webkit']) type(out[k]);
    return out;
  };
}
