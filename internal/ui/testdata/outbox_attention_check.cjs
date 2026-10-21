// Execute the actual Outbox and shared attention model without a browser.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const esbuild = require('../web/node_modules/esbuild');
const sourceRoot = path.join(__dirname, '../web/src');
const jsx = (tag, props, ...children) => ({ tag, props: { ...props, children: children.flat(Infinity).filter(x => x !== false && x != null) } });
const nodes = tree => tree && typeof tree === 'object' ? [tree, ...(tree.props?.children || []).flatMap(nodes)] : [];
const words = tree => tree && typeof tree === 'object' ? (tree.props?.children || []).map(words).join('') : String(tree ?? '');
function load(file, globals = {}) {
  const env = { module: { exports: {} }, jsx, Fragment: 'Fragment', ...globals };
  env.exports = env.module.exports;
  const code = fs.readFileSync(path.join(sourceRoot, file), 'utf8').replace(/^import[\s\S]*?from[^;]+;/gm, '');
  vm.runInNewContext(esbuild.transformSync(code, { loader: file.endsWith('tsx') ? 'tsx' : 'ts', jsxFactory: 'jsx', jsxFragment: 'Fragment', format: 'cjs' }).code, env);
  return env.module.exports;
}
let state, navigation;
const store = { showTab: (...args) => { navigation = args; } };
// The baseline component can be loaded before the new model exists, proving
// the user-visible regression first rather than merely a missing export.
const modelFile = path.join(sourceRoot, 'outbox-attention.ts');
const model = fs.existsSync(modelFile) ? load('outbox-attention.ts') : {};
const { Outbox } = load('features/Outbox.tsx', {
  ...model, useApp: () => store, useStore: (_store, select) => select(state),
  useState: value => [value, () => {}], Button: 'Button', ScheduleDialogs: 'ScheduleDialogs', DraftsView: 'DraftsView', Reminders: 'Reminders',
});
const reminder = overdue => ({ message: 'reminder', due: '2030-01-01T00:00:00Z', overdue });
function render(count, { section = 'scheduled', remind = true, future = 0 } = {}) {
  state = { section, scheduleSaves: {}, overview: { schedules: true, remind, reminders: [...Array.from({ length: count }, () => reminder(true)), ...Array.from({ length: future }, () => reminder(false))] } };
  return Outbox();
}
const sectionButtons = tree => nodes(tree).find(node => node.tag === 'nav').props.children;
const button = (tree, title) => sectionButtons(tree).find(node => words(node).startsWith(title));
let tree = render(3);
assert.match(words(button(tree, 'Reminders')), /3 due/, 'Reminders identifies the source of the rail badge even when Scheduled is empty');
assert.equal(nodes(tree).filter(node => node.tag === 'ScheduleDialogs').length, 1, 'the selected empty Scheduled pane remains selected');
assert.equal(button(tree, 'Scheduled').props['aria-current'], 'page');
assert.equal(button(tree, 'Reminders').props['aria-label'], 'Reminders, 3 reminders due');
button(tree, 'Reminders').props.onClick();
assert.deepEqual(navigation, ['outbox', 'reminders'], 'the native button callback opens the exact section');
for (const section of ['scheduled', 'drafts', 'reminders', 'unknown']) {
  for (const count of [0, 1, 25]) {
    tree = render(count, { section, future: 2 });
    const buttons = sectionButtons(tree), due = buttons.flatMap(node => nodes(node).filter(n => n.props['data-outbox-attention']));
    assert.equal(buttons.length, 3, 'all sections stay discoverable');
    assert.equal(due.length, count ? 1 : 0, 'zero has no attention marker');
    assert.equal(button(tree, 'Reminders').props['aria-label'], count ? `Reminders, ${count} ${count === 1 ? 'reminder' : 'reminders'} due` : undefined);
    assert.equal(button(tree, 'Scheduled').props['aria-label'], undefined);
    assert.equal(button(tree, 'Drafts').props['aria-label'], undefined, 'stored drafts never become unread attention');
    if (count) {
      assert.equal(words(due[0]), `${count} due`);
      assert.equal(due[0].props['aria-hidden'], 'true', 'the accessible button name announces the count once');
    }
    assert.equal(buttons.filter(node => node.props['aria-current'] === 'page').length, 1);
  }
}
tree = render(3, { remind: false });
assert.equal(nodes(tree).filter(n => n.props['data-outbox-attention']).length, 0, 'unsupported reminders never produce attention');
assert.equal(typeof model.outboxAttention, 'function');
for (const overview of [null, undefined, {}, { remind: false, reminders: [reminder(true)] }, { remind: true }]) {
  assert.deepEqual(JSON.parse(JSON.stringify(model.outboxAttention(overview))), { scheduled: 0, drafts: 0, reminders: 0, total: 0 });
}
assert.deepEqual(JSON.parse(JSON.stringify(model.outboxAttention({ remind: true, reminders: [reminder(true), reminder(false), reminder(true)] }))), { scheduled: 0, drafts: 0, reminders: 2, total: 2 });
const app = fs.readFileSync(path.join(sourceRoot, 'App.tsx'), 'utf8');
assert.equal((app.match(/outboxAttention\(overview\)\.total/g) || []).length, 2, 'phone and desktop navigation both use the shared source');
console.log('PASS actual Outbox attention: shared rail/mobile source; zero/one/many and future/unsupported cases; empty selected pane; exact section callback; accessible labels/current section; no draft unread');
