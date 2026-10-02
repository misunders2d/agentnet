// Production Comic renderer with a small DOM stand-in; no browser dependency.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const el = (tag, attrs = {}, ...children) => ({
  tag, attrs, children: children.flat(Infinity).filter(c => c !== false && c != null),
  setAttribute(k, v) { this.attrs[k] = v; },
});
const ctx = vm.createContext({
  el, fill: (n, ...children) => { n.children = children; return n; },
  avatar: (name, cls) => el('span', { class: 'avatar ' + cls }, name[0]),
  state: { overview: { me: { address: 'mira/laptop' } } },
  firstLine: (s, n) => s.split('\n')[0].slice(0, n),
  shownText: m => m.deleted ? '' : m.body,
  kindTag: {}, stampWord: {}, statusWord: {}, isReport: () => false,
  when: () => 'now', reactorNames: () => '', actionButton: () => null,
});
vm.runInContext(fs.readFileSync(path.join(__dirname, '../static/lenses.js'), 'utf8') + '\nglobalThis.comic = Comic;', ctx);
const Comic = ctx.comic;
const message = extra => ({ id: 'child', dir: 'in', from: 'studio/desk', to: 'mira/laptop', kind: 'message', body: 'A turn', at: '2026-09-30T12:00:00Z', author: { label: 'Builder (agent)' }, actions: [], ...extra });
const all = n => typeof n === 'object' ? [n, ...(n.children || []).flatMap(all)] : [];
const text = n => typeof n === 'object' ? (n.children || []).map(text).join('') : String(n);
const face = panel => all(panel).find(n => n.attrs?.['data-emotion']);
const reference = panel => all(panel).find(n => n.attrs?.class === 'replyref comic-reply');
Comic.t = { messages: [] };
for (const emotion of ['neutral', 'happy', 'sad', 'focused', 'curious', 'concerned', 'celebrating']) {
  const panel = Comic.panel(message({ emotion, state: 'failed', state_text: 'Execution failed' }), false);
  assert.equal(face(panel).attrs['data-emotion'], emotion);
  assert.equal(face(panel).attrs.role, 'img');
  assert.match(face(panel).attrs['aria-label'], new RegExp('^Expression: ' + emotion));
  assert.match(text(panel), /Builder \(agent\) → you/);
  assert.match(text(panel), /Execution failed/);
}
for (const emotion of [undefined, null, '', 'unknown', 'done', 'needs_human', '__proto__', 'toString', {}, ['happy']]) {
  assert.equal(face(Comic.panel(message({ emotion, state: 'done' }), false)).attrs['data-emotion'], 'neutral');
}
const parent = message({ id: 'root', body: 'Exact parent message' });
Comic.t.messages = [parent];
let focused;
Comic.focus = id => { focused = id; };
const reply = reference(Comic.panel(message({ reply_to: 'root' }), false));
assert.equal(reply.tag, 'button');
assert.equal(text(reply), 'Reply to: Exact parent message');
reply.attrs.onclick();
assert.equal(focused, 'root');
parent.deleted = true;
assert.equal(text(reference(Comic.panel(message({ reply_to: 'root' }), false))), 'Reply to: (deleted message)');
assert.equal(reference(Comic.panel(message({ reply_to: 'absent' }), false)).tag, 'span');
assert.equal(text(reference(Comic.panel(message({ reply_to: 'absent' }), false))), 'Reply to a message not shown here');
assert.equal(face(Comic.panel(message({ event: 'participant_joined' }), false)), undefined);
console.log('comic expressions, author attribution, status separation and local replies ok');
