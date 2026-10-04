// Native setup is explicit and uses the same installers as the CLI. Browser
// devices never inspect/install host software. No session identity is rendered.
export async function mountAssistantSetup({ root, api, isCurrent = () => true, isBrowser = false, onChanged = () => {} }) {
 if (!root || !isCurrent()) return;
 const doc = root.ownerDocument;
 const node = (tag, text, cls) => { const el = doc.createElement(tag); if (text) el.textContent = text; if (cls) el.className = cls; return el; };
 const button = (text, action, primary = false) => { const b = node('button', text, 'setup-button' + (primary ? ' primary' : '')); b.type = 'button'; b.onclick = action; return b; };
 let view, catalog, chosen = new Set(), review, busy = false, stage = 'home', outcomes = [];
 const configs = new Map();
 const records = harness => (catalog?.agents || []).filter(a => a.enabled && a.responder?.harness === harness);
 const managed = harness => (catalog?.harnesses || []).some(h => h.name === harness && h.found);
 function configFor(h) {
  if (!configs.has(h.id)) { const matches = records(h.id), existing = matches.length === 1 ? matches[0] : null; configs.set(h.id, { id: existing?.record.id || '', label: existing?.record.label || h.label, dir: existing?.responder.dir || '', requireChoice: matches.length > 1 }); }
  return configs.get(h.id);
 }
 const agentAPI = async body => { if (!current()) throw Error('Workspace changed. Reopen setup there.'); const value = await api('/api/agents', body); if (!current()) throw Error('Workspace changed. Assistant changes remain bound to the original workspace.'); return value; };
 const error = node('p', '', 'setup-error'); error.setAttribute('role', 'alert');
 const current = () => isCurrent() && root.isConnected;
 const fail = e => { if (current()) { busy = false; error.textContent = e.message || 'Setup could not be completed. Read the current tool list before retrying.'; draw(); } };
 const call = async body => { if (!current()) throw Error('Workspace changed. Reopen setup in that workspace.'); const result = await api('/api/assistant-setup', body); if (!current()) throw Error('Workspace changed. Setup still belongs to the original workspace.'); return result; };
 const selected = () => (view?.harnesses || []).filter(h => chosen.has(h.id));
 const choose = async () => {
  if (busy) return; busy = true; error.textContent = ''; draw();
  try { [view, catalog] = await Promise.all([call(), agentAPI()]); configs.clear(); chosen = new Set((view.harnesses || []).filter(h => h.detected && h.supported && h.configured).map(h => h.id)); busy = false; stage = 'choose'; draw(true); } catch (e) { fail(e); }
 };
 const checkAll = () => {
  const eligible = (view.harnesses || []).filter(h => h.detected && h.supported);
  return { eligible, all: eligible.length > 0 && eligible.every(h => chosen.has(h.id)) };
 };
 const reviewSelection = async () => {
  if (busy || !selected().length) return; busy = true; error.textContent = ''; draw();
  try { for (const h of selected().filter(h => managed(h.id))) { const c = configFor(h); if (c.requireChoice || !c.label.trim() || !c.dir.trim()) throw Error('Choose an assistant name and working folder for ' + h.label + ' before reviewing.'); } review = await call({ action: 'review', harnesses: selected().map(h => h.id) }); stage = 'review'; busy = false; draw(true); } catch (e) { fail(e); }
 };
 const apply = async () => {
  if (busy || !review?.review_id) return; busy = true; error.textContent = ''; draw();
  try {
   const requested = selected().map(h => ({ harness: h.id, config: { ...configFor(h) }, managed: managed(h.id) }));
   view = await call({ action: 'apply', harnesses: selected().map(h => h.id), review_id: review.review_id }); outcomes = [];
   // Refresh before creation after any uncertain attempt; reuse the saved exact
   // identity rather than duplicating a record on a rerun/network-error retry.
   catalog = await agentAPI();
   for (const item of requested.filter(x => x.managed)) {
    const c = item.config, exact = c.id && (catalog.agents || []).find(a => a.record.id === c.id);
    if (c.id && (!exact || !exact.enabled || exact.responder?.harness !== item.harness)) throw Error('The selected assistant changed or was disabled. Reload setup before changing it.');
    const existing = exact || records(item.harness).find(a => a.record.label === c.label.trim() && a.responder.dir === c.dir.trim());
    let result;
    if (existing && existing.responder?.harness === item.harness && existing.responder.dir === c.dir.trim()) result = { saved: true, published: false, agent: existing };
    else result = await agentAPI(existing ? { action: 'update', id: existing.record.id, harness: item.harness, dir: c.dir.trim() } : { action: 'create', label: c.label.trim(), harness: item.harness, dir: c.dir.trim() });
    if (!result.saved || !result.agent) throw Error('Assistant save not confirmed. Reload setup before retrying.');
    configs.set(item.harness, { id: result.agent.record.id, label: result.agent.record.label, dir: result.agent.responder.dir, requireChoice: false });
    outcomes.push(result.agent.record.label);
    catalog = await agentAPI();
   }
   let publication;
   if (outcomes.length) publication = await agentAPI({ action: 'publish' });
   if (outcomes.length && !publication?.published) view.note = 'Assistant settings saved locally. Publishing their catalog was not confirmed. Run setup again to retry publication; existing assistants will be reused.';
   else if (outcomes.length) view.note = 'Conversation assistants saved and public catalog publication confirmed. Add them to a conversation; invitation and task permissions still apply. Native hook trust/activation is separate.';
   stage = 'saved'; busy = false; review = null; draw(true); onChanged();
  } catch (e) { review = null; stage = 'choose'; fail(new Error('Setup did not finish: ' + e.message + ' Earlier reviewed changes may be saved. Reload the tool list before retrying.')); }
 };
 const close = () => { stage = 'home'; review = null; error.textContent = ''; draw(true); };
 function draw(focus = false) {
  if (!current()) return;
  const card = node('section', '', 'assistant-setup-card'); card.setAttribute('aria-label', 'Set up harnesses');
  const heading = node('h3', stage === 'choose' ? 'Choose your tools' : stage === 'review' ? 'Review setup changes' : stage === 'saved' ? 'Setup result' : 'Connect your tools'); heading.tabIndex = -1; card.append(heading);
  if (isBrowser || view?.local === false) {
   card.append(node('p', 'This browser cannot inspect or install software. Open Settings → Agent → Set up harnesses on your native AgentNet computer.', 'setup-description')); root.replaceChildren(card); return;
  }
  if (stage === 'home') {
   card.append(node('p', 'Set up AgentNet integration for Codex, Claude, Pi or OMP. Run this again when tools are installed or removed. Your default responder stays separate.', 'setup-description'));
   const start = button(busy ? 'Checking tools…' : 'Set up harnesses', choose, true); start.disabled = busy; card.append(start);
  } else if (stage === 'choose') {
   card.append(node('p', 'Choose installed tools to configure. Existing skills, plugins, permissions and model sign-in stay untouched.', 'setup-description'));
   const { eligible, all } = checkAll();
   const allLabel = node('label', '', 'setup-all'); const allBox = doc.createElement('input'); allBox.type = 'checkbox'; allBox.checked = all; allBox.indeterminate = !all && eligible.some(h => chosen.has(h.id)); allBox.disabled = busy || !eligible.length;
   allBox.setAttribute('aria-label', 'Select all detected tools'); allBox.onchange = () => { chosen = new Set(allBox.checked ? eligible.map(h => h.id) : []); draw(); root.querySelector('.setup-all input')?.focus(); };
   allLabel.append(allBox, doc.createTextNode(' Select all detected tools')); card.append(allLabel);
   const list = node('div', '', 'setup-tools');
   for (const h of view.harnesses || []) {
    const row = node('label', '', 'setup-tool-row'); const input = doc.createElement('input'); input.type = 'checkbox'; input.checked = chosen.has(h.id); input.disabled = busy || !h.detected || !h.supported; input.setAttribute('aria-label', 'Connect ' + h.label);
    input.id = 'setup-tool-' + h.id;
    input.onchange = () => { if (input.checked) chosen.add(h.id); else chosen.delete(h.id); draw(); root.querySelector('#setup-tool-' + h.id)?.focus(); };
    const body = node('div', '', 'setup-tool-body'); const title = node('div', '', 'setup-row-title'); title.append(node('strong', h.label));
    const statuses = { connected: 'Registered context', detected: 'Detected', needs_setup: 'Needs setup', needs_activation: 'Needs activation', not_detected: 'Not detected', unsupported: 'Unsupported', error: 'Needs attention' };
    title.append(node('span', statuses[h.state] || 'Unknown', 'setup-status ' + (h.state === 'connected' ? 'connected' : h.state === 'detected' ? '' : 'attention')));
    body.append(title, node('p', h.note)); row.append(input, body); list.append(row);
    if (chosen.has(h.id)) {
     const config = node('div', '', 'setup-assistant-config');
     if (!managed(h.id)) config.append(node('p', h.id === 'omp' ? 'OMP supports native hooks and CLI replies only here. Managed conversation assistants are unavailable; setup will not create one.' : 'No managed responder is available for this tool. Existing assistant settings are unchanged.', 'setup-note'));
     else {
      const c = configFor(h), matches = records(h.id);
      if (matches.length) { const label = node('label', 'Conversation assistant'); const select = doc.createElement('select'); select.setAttribute('aria-label', h.label + ' conversation assistant'); const placeholder = node('option', 'Choose an existing assistant'); placeholder.value = ''; placeholder.disabled = true; select.append(placeholder);
       for (const a of matches) { const option = node('option', a.record.label); option.value = a.record.id; option.selected = a.record.id === c.id; select.append(option); }
       if (!c.id) select.value = ''; select.disabled = busy; select.onchange = () => { const a = matches.find(x => x.record.id === select.value); if (a) { Object.assign(c, { id: a.record.id, label: a.record.label, dir: a.responder.dir, requireChoice: false }); draw(); } }; label.append(select); config.append(label);
      } else { const label = node('label', 'Assistant name'); const field = doc.createElement('input'); field.type = 'text'; field.value = c.label; field.maxLength = 64; field.setAttribute('aria-label', h.label + ' assistant name'); field.disabled = busy; field.oninput = () => c.label = field.value; label.append(field); config.append(label); }
      const label = node('label', 'Working folder'); const field = doc.createElement('input'); field.type = 'text'; field.value = c.dir; field.placeholder = 'Choose an existing absolute folder'; field.setAttribute('aria-label', h.label + ' working folder'); field.disabled = busy; field.oninput = () => c.dir = field.value; label.append(field); config.append(label, node('p', c.id ? 'Reuse this exact assistant. Its other configuration and your default stay unchanged.' : 'A new named assistant runs in this folder with your existing harness configuration, subject to normal permissions.', 'setup-count'));
     }
     list.append(config);
    }
   }
   card.append(list, node('p', selected().length + ' selected', 'setup-count'));
   card.append(node('p', 'Review both native integration and named conversation-assistant configuration. Setup grants no conversation history or task permissions; native sessions are not conversation executors.', 'setup-note'));
   const actions = node('div', '', 'setup-actions'); const cancel = button('Back to settings', close); cancel.disabled = busy; const next = button(busy ? 'Reading changes…' : 'Review changes', reviewSelection, true); next.disabled = busy || !selected().length; actions.append(cancel, next); card.append(actions);
  } else if (stage === 'review' || stage === 'saved') {
   const rows = stage === 'review' ? review.harnesses.filter(h => chosen.has(h.id)) : view.harnesses.filter(h => chosen.has(h.id));
   const list = node('ul', '', 'setup-review');
   for (const h of rows) {
    const row = node('li'); row.append(node('strong', h.label), node('p', stage === 'review' ? h.change : h.note));
    if (h.target && stage === 'review') { const detail = node('details'); detail.append(node('summary', 'Configuration file'), node('p', h.target, 'setup-path')); row.append(detail); }
    if (managed(h.id)) { const c = configFor(h); row.append(node('p', (c.id ? 'Reuse assistant: ' : 'Create assistant: ') + c.label, 'setup-next'), node('p', 'Working folder: ' + c.dir, 'setup-path')); }
    else if (h.id === 'omp') row.append(node('p', 'Native hooks/CLI only. Managed conversations unavailable.', 'setup-next'));
    if (h.next) row.append(node('p', h.next, 'setup-next')); list.append(row);
   }
   card.append(list, node('p', (stage === 'review' ? review : view).note, 'setup-note'));
   const actions = node('div', '', 'setup-actions');
   if (stage === 'review') { const back = button('Your selection', () => { stage = 'choose'; review = null; draw(true); }); back.disabled = busy; const confirm = button(busy ? 'Applying selected changes…' : 'Confirm setup', apply, true); confirm.disabled = busy; actions.append(back, confirm); }
   else actions.append(button('Back to settings', close, true)); card.append(actions);
  }
  if (error.textContent) card.append(error);
  root.replaceChildren(card); if (focus) heading.focus({ preventScroll: true });
 }
 draw();
 return { refresh: choose };
}
