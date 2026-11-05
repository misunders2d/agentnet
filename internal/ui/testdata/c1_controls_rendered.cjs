// Discovery index of 43 historical C1 gaps; draft metadata is not executable coverage.
// The explicit new-group-selection family owns its fixture/case manifest in c1_new_group.cjs.
// Other families fail closed. No subset, table, handler count or synthetic fixture qualifies a release.
//   node c1_controls_rendered.cjs --table
//   C1_OUT=<new private dir> node c1_controls_rendered.cjs --family new-group-selection --part 0
// Part 1 is the other width. Their exact case sets are disjoint; neither alone completes the subset.

const file = name => 'agentnet:internal/ui/web/src/features/' + name;
const H = file('Conversation.header.tsx'), M = file('Message.tsx'), N = file('NewChat.tsx');
const LOCAL = 'no request: local state only';
const sheet = title => `dialog(${JSON.stringify(title)})`, text = s => `hasText(${JSON.stringify(s)})`;

// A chat's own menu, opened as a person does. `at` is the item's handler.
const more = async p => { await p.getByRole('button', { name: 'More', exact: true }).click(); await p.getByRole('menu').waitFor(); };
const control = (p, id) => p.locator(`[data-an-control=${JSON.stringify(id)}]`).first();

const rows = [
  // ---- Conversation.header.tsx: a device thread's grants, the chat menu, helpers
  { id: H + '#copyLink/Menu.Item/onClick#2', scene: 'dm', reach: more, local: text('Link copied'), confirmed: 'clipboard', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#4', scene: 'dm', reach: more, prompt: 'agentnet:fixture', local: 'called("openLink")', confirmed: 'openLink', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#8', scene: 'dm', reach: more, local: sheet('Remind me'), confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/RemindSheet/onOpenChange#1', scene: 'dm', reach: async (p, r) => { await more(p); await control(p, H + '#copyLink/Menu.Item/onClick#8').click(); await r.dialog(p, 'Remind me'); }, act: 'Escape', local: 'noDialog()', confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#10', scene: 'thread', view: { approved: true }, reach: more, local: sheet('Stop answering'), confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#11', scene: 'thread', view: { approved: false }, reach: more, local: sheet('Answer'), confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/Menu.Item/onClick#12', scene: 'thread', view: { task_grant: true }, reach: more, local: sheet('Stop running'), confirmed: 'none', backend: LOCAL },
  { id: H + '#copyLink/DeviceGrantConfirm/onClose#1', scene: 'thread', view: { approved: false }, reach: async (p, r) => { await more(p); await control(p, H + '#copyLink/Menu.Item/onClick#11').click(); await r.dialog(p, 'Answer'); }, act: 'Escape', local: 'noDialog()', confirmed: 'none', backend: LOCAL },
  { id: H + '#current/Button/onClick#1', scene: 'dm', helpers: 2, widths: [390], local: 'panelOpen()', confirmed: 'none', backend: LOCAL },
  { id: H + '#dismiss/Button/onClick#1', scene: 'dm', helpers: 1, review: true, widths: [390], local: 'panelOpen()', confirmed: 'none', backend: LOCAL },
  { id: H + '#panel/button/onClick#1', scene: 'dm', helpers: 1, widths: [1440], closePanel: true, local: 'panelOpen()', confirmed: 'none', backend: LOCAL },

  // ---- Message.tsx: a failed send, a failed edit, a request's answer, the divider
  { id: M + '#result/button/onClick#1', scene: 'dm', message: { state: 'failed', detail: 'relay_disk_full' }, role: 'admin', local: 'tabShown("settings")', confirmed: 'none', backend: LOCAL },
  { id: M + '#result/button/onClick#2', scene: 'dm', message: { state: 'failed', delivery: 'failed', detail: 'relay_disk_full' }, role: 'admin',
    backend: '/api/storage/retry_send', answer: { retried: true },
    local: text('Queuing retry'), confirmed: text('Retry queued'), failing: text('Retry this message'), duplicate: 1, stale: 'open: nothing newer changes this row before its answer' },
  { id: M + '#result/button/onClick#3', scene: 'dm', message: { state: 'failed', detail: 'relay_disk_full' }, role: 'admin', local: sheet('Message details'), confirmed: 'none', backend: LOCAL },
  { id: M + '#Under/SendFailure/onDetails#1', scene: 'dm', message: { state: 'failed', detail: 'relay_disk_full' }, role: 'admin', same: M + '#result/button/onClick#3', local: sheet('Message details'), confirmed: 'none', backend: LOCAL },
  { id: M + '#needsHumanDetail/button/onClick#3', scene: 'dm', message: { _change: { status: 'failed', action: 'edit', error: 'fixture refused', control: 'edited text' } }, local: 'editing()', confirmed: 'none', backend: LOCAL,
    open: 'a failed edit is a Store state made by a refused edit, not view data: reach it by editing with the backend failing' },
  { id: M + '#needsHumanDetail/button/onClick#4', scene: 'dm', open: 'same state as Edit again; its result, the draft gone, needs that state first' },
  { id: M + '#needsHumanDetail/EditBox/onDone#1', scene: 'dm', open: 'reached through the message toolbar Edit, on wide; needs an own stored message row in the view' },
  { id: M + '#needsHumanDetail/DeleteMessage/onOpenChange#1', scene: 'dm', open: 'reached through the message toolbar More actions; needs an own stored message row in the view' },
  { id: M + '#needsHumanDetail/ApprovalCard/onClick#1', scene: 'dm', open: 'needs a stopped request of mine with a live message row' },
  { id: M + '#needsHumanDetail/MessageView/onJump,onSelect#1', scene: 'dm', open: 'needs a message with several agent targets and their outcome rows' },
  { id: M + '#Under/button/onClick#2', scene: 'dm', open: 'needs a request to an agent with its reply in view; result is the timeline moving to the reply' },
  { id: M + '#by/button/onClick#1', scene: 'dm', open: 'needs a joined helper divider with shared history: a real helper invitation with a grant' },
  { id: M + '#by/WhatTheySaw/onOpenChange#1', scene: 'dm', open: 'same state as See what they saw' },

  // ---- NewChat.tsx: the sheet, people, agents, and a chat or group being started
  { id: N + '#close/Sheet/onOpenChange#1', scene: 'newgroup', act: 'Escape', local: 'noDialog()', confirmed: 'none', backend: LOCAL },
  { id: N + '#close/NewGroupForm/onBack,onCreated#1', scene: 'newgroup', same: N + '#back/button/onClick#1', local: sheet('New chat'), confirmed: 'none', backend: LOCAL },
  { id: N + '#back/button/onClick#1', scene: 'newgroup', local: sheet('New chat'), confirmed: 'none', backend: LOCAL },
  { id: N + '#back/input/onChange#2', scene: 'newgroup', people: 41, input: 'nobody', local: text('Clear search'), confirmed: 'none', backend: LOCAL },
  { id: N + '#back/Button/onClick#5', scene: 'newgroup', people: 41, reach: async (p, r) => { await control(p, N + '#back/input/onChange#2').fill('nobody'); }, local: 'inputEmpty()', confirmed: 'none', backend: LOCAL },
  { id: N + '#back/Button/onClick#6', scene: 'newgroup', people: 41, local: 'pagination removed; person 41 available', confirmed: '41 eligible checkboxes', backend: LOCAL },
  { id: N + '#back/input/onChange#3', scene: 'newgroup', people: 41, check: true, local: 'checked()', confirmed: 'none', backend: LOCAL },
  { id: N + '#back/Button/onClick#1', scene: 'creating', open: 'a group being created is Store state after /api/groups/new; reached by a held request' },
  { id: N + '#back/Button/onClick#3', scene: 'creating', open: 'same state: created, invitations done or failed' },
  { id: N + '#back/Button/onClick#4', scene: 'creating', open: 'same state: stopped creation, edited again' },
  { id: N + '#NeedsName/Button/onClick#1', scene: 'newchat', overview: { person: null }, local: 'tabShown("settings")', confirmed: 'none', backend: LOCAL },
  { id: N + '#close/NeedsName/onDone#1', scene: 'newchat', overview: { person: null }, same: N + '#NeedsName/Button/onClick#1', local: 'noDialog()', confirmed: 'none', backend: LOCAL },
  { id: N + '#shownAgents/Button/onClick#2', scene: 'newchat', people: 30, open: 'the page size decides how many must exist; set from the source before a run' },
  { id: N + '#shownAgents/Button/onClick#3', scene: 'newchat', directory: 30, open: 'same, for server members' },
  { id: N + '#shownAgents/Button/onClick#4', scene: 'newchat', agents: 30, open: 'same, for agents you talk to' },
  { id: N + '#shownAgents/AgentRow/onPick#1', scene: 'newchat', agents: 1, open: 'needs a remote agent row in the overview; result is that chat open' },
  { id: N + '#AgentRow/button/onClick#1', scene: 'newchat', agents: 1, open: 'same row' },
  { id: N + '#shownAgents/CandidateRow/onPick#2', scene: 'newchat', directory: 1, backend: '/api/dm/new', local: text('Starting this chat'), confirmed: 'chat open', failing: text('Could not confirm this chat'), duplicate: 1, open: 'written once the people rows run: same flow' },
  { id: N + '#StartingChat/Button/onClick#2', scene: 'newchat', directory: 1, backend: '/api/dm/new', open: 'Try starting chat again: after a failed start, same flow' },
];

function table() {
  const counts = { rows: rows.length, draft_without_open: rows.filter(r => !r.open).length, open: rows.filter(r => r.open).length };
  return { counts, execution:'discovery only; use an exact bound family report',release_qualified:false,
    rows: rows.map(r => ({ id: r.id, scene: r.scene, people:r.people, intended: r.local || '', confirmed: r.confirmed || '', backend: r.backend || '', open: r.open || '' })) };
}

module.exports = { rows, table };

if (require.main === module) {
  const args = process.argv.slice(2);
  if (args.length === 1 && args[0] === '--table') console.log(JSON.stringify(table(), null, 1));
  else if (args[0] === '--family' && args[1] === 'new-group-selection' &&
    (args.length === 2 || (args.length === 4 && ['--negative','--part'].includes(args[2])))) {
    require('./c1_new_group.cjs').run({ out: process.env.C1_OUT,
      negative:args[2]==='--negative'?args[3]:null,part:args[2]==='--part'?Number(args[3]):null }).catch(error => {
      console.error(error.stack || error);process.exitCode = 1;
    });
  } else {
    console.error('C1 incomplete: choose --family new-group-selection explicitly. Other C1 rows remain unimplemented; open/missing cases cannot pass. --table is discovery only.');
    process.exitCode = 1;
  }
}
