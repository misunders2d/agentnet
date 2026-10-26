// Fabricated unit inputs test the validator, never qualify app responsiveness.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { validateCoverage, evidenceReader, hash, canonical } = require('./interaction_coverage_gate.cjs');
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jZ4kAAAAASUVORK5CYII=', 'base64');
let checks = 0;

function fixture() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'interaction-gate-unit-'));
  let sequence = 0;
  const write = (value, ext = 'json') => {
    const bytes = Buffer.isBuffer(value) ? value : Buffer.from(JSON.stringify(value));
    const file = 'evidence-' + sequence++ + '.' + ext;
    fs.writeFileSync(path.join(root, file), bytes);
    return { path: file, sha256: hash(bytes) };
  };
  const capture = write(png, 'png'), clock = 'unit-monotonic-ms';
  const control = { control_id: 'comic:src-unit', definition_id: 'src-unit', source_ref: { repo: 'core', path: 'internal/ui/web/src/fixture.tsx', line: 1 } };
  const c = { case_id: control.control_id + '::pointer::linux-normal-cold-idle-enabled', control_id: control.control_id,
    dimensions: { input: 'pointer', platform: 'Linux-desktop', skin: 'comic', state: 'enabled', history: 'normal', view: 'cold', sync: 'idle' },
    conditions: { machine: 'unit-fixture', os: 'invented', viewport: [1440, 960], network: 'invented', load: 'idle', messages: 400 },
    operation: 'reaction.add', target_id: 'fixture-message', repetitions: 3,
    expected: { outcome: 'success', feedback_states: ['pending'], state: 'reaction-stored', request: true, local_commit: true, remote_completion: false },
    comparators: { products: ['Slack', 'Telegram'] } };
  control.required_case_ids = [c.case_id];
  const manifest = { schema_version: 1, review: { complete: true, reference: 'unit-fixture-only' },
    reconciliation: { complete: true, unresolved_controls: [], unresolved_bindings: [] },
    source_revision: 'a'.repeat(40), artifact_sha256: 'b'.repeat(64), runner_sha256: 'c'.repeat(64), controls: [control], cases: [c] };
  const report = { schema_version: 1, type: 'interaction-coverage-evidence', functional_status: 'unit_fixture_pass', source_revision: manifest.source_revision,
    artifact_sha256: manifest.artifact_sha256, runner_sha256: manifest.runner_sha256, observations: [], comparators: [] };
  for (const product of ['AgentNet', 'Slack', 'Telegram']) {
    const records = [];
    for (let i = 0; i < c.repetitions; i++) {
      const local = product === 'AgentNet', target = local ? c.target_id : 'reference-message';
      const request = local ? { id: 'request-' + i, operation: c.operation, target_id: target, at_ms: 12 } : null;
      const commit = local ? { at_ms: 70, request_id: request.id, operation: c.operation, target_id: target } : null;
      if (commit) commit.witness = write({ type: 'local-commit', ...commit, clock });
      const record = { schema_version: 1, type: 'interaction-observation', subject: product, subject_version: 'unit-only', execution: 'executed',
        case_id: c.case_id, control_id: c.control_id, repetition: i, source_ref: control.source_ref, dimensions: c.dimensions, conditions: c.conditions,
        provenance: { source_revision: manifest.source_revision, artifact_sha256: manifest.artifact_sha256, runner_sha256: manifest.runner_sha256, backend: 'real', clock },
        dom: { definition_id: control.definition_id, locator: '[data-unit-control]', node_id: 'node-1', target_id: target },
        visible_controls: [{ control_id: control.control_id, definition_id: control.definition_id, source_ref: control.source_ref, locator: '[data-unit-control]', node_id: 'node-1' }],
        input: { driver_at_ms: 0, at_ms: 10, kind: c.dimensions.input, trusted: true, source: 'browser-trusted-event' },
        feedback: { at_ms: (local ? 50 : 120) + i, clock, frame_id: 'feedback-' + i, kind: 'presented-frame', state: 'pending', witness: capture },
        confirmed: { at_ms: (local ? 100 : 220) + i, clock, frame_id: 'confirmed-' + i, kind: 'presented-frame', state: c.expected.state, witness: capture },
        result: { operation: c.operation, outcome: c.expected.outcome, target_id: target }, request, commit, remote: null, failures: [], runtime_errors: [] };
      records.push(write(record));
    }
    if (product === 'AgentNet') report.observations = records;
    else report.comparators.push({ case_id: c.case_id, product, observations: records });
  }
  const refs = product => product === 'AgentNet' ? report.observations : report.comparators.find(g => g.product === product).observations;
  return { root, manifest, report, write, c,
    update(product, i, edit) {
      const ref = refs(product)[i], record = JSON.parse(fs.readFileSync(path.join(root, ref.path)));
      edit(record);
      Object.assign(ref, write(record));
    },
    run() { report.manifest_sha256 = hash(canonical(manifest)); return validateCoverage(manifest, report, evidenceReader(root)); },
    close() { fs.rmSync(root, { recursive: true, force: true }); } };
}

function blocked(name, mutate, code) {
  const f = fixture();
  try {
    mutate(f);
    const result = f.run();
    assert.equal(result.performance_status, 'blocked', name);
    if (code) assert(result.errors.some(e => e.code === code), name + ': ' + JSON.stringify(result.errors));
    checks++;
  } finally { f.close(); }
}

{
  const f = fixture();
  try {
    const result = f.run();
    assert.equal(result.performance_status, 'qualified', JSON.stringify(result.errors));
    assert.equal(result.functional_status, 'unit_fixture_pass');
    assert.equal(result.cases[0].measured.feedback.n, 3);
    assert.equal(result.cases[0].measured.feedback.p95Ms, 52);
    assert.equal(result.cases[0].measured.input_event_feedback.maxMs, 42);
    assert.equal(result.cases[0].comparators.length, 2);
    const before = JSON.stringify([f.manifest, f.report]); f.run();
    assert.equal(JSON.stringify([f.manifest, f.report]), before, 'validation does not mutate inputs');
    checks++;
  } finally { f.close(); }
}

blocked('raw unreconciled inventory', f => { f.manifest.reconciliation.complete = false; }, 'inventory_not_reconciled');
const ownerTarget = f => {
  f.manifest.acceptance = { kind: 'local-feedback', max_ms: 500, owner_reference: 'October 26 owner correction; unit fixture only' };
  delete f.c.comparators; f.report.comparators = [];
};
{
  const f = fixture();
  try {
    ownerTarget(f);
    f.update('AgentNet', 0, r => { r.feedback.at_ms = 499; r.confirmed.at_ms = 5000; });
    const result = f.run();
    assert.equal(result.performance_status, 'qualified', JSON.stringify(result.errors));
    assert.equal(result.cases[0].measured.confirmed.maxMs, 5000, 'background duration stays visible');
    assert.deepEqual(result.cases[0].comparators, []);
    checks++;
  } finally { f.close(); }
}
blocked('owner target excludes 500 ms', f => { ownerTarget(f); f.update('AgentNet', 0, r => { r.feedback.at_ms = 500; r.confirmed.at_ms = 700; }); }, 'over_owner_feedback_budget');
blocked('owner target cannot be silently raised', f => { ownerTarget(f); f.manifest.acceptance.max_ms = 2000; }, 'invalid_owner_target');
blocked('owner target still needs complete control coverage', f => { ownerTarget(f); f.report.observations.pop(); }, 'missing_required_observation');
blocked('owner target still requires actual backend evidence', f => { ownerTarget(f); f.update('AgentNet', 0, r => { r.provenance.backend = 'synthetic'; }); });
blocked('unresolved user input listener', f => { f.manifest.reconciliation.unresolved_bindings.push('unmapped-click'); }, 'inventory_not_reconciled');
blocked('unreviewed source candidates', f => { f.manifest.review.complete = false; }, 'manifest_not_reviewed');
blocked('empty inventory', f => { f.manifest.controls = []; f.manifest.cases = []; }, 'missing_inventory_or_observations');
blocked('control without required tests', f => { f.manifest.controls.push({ ...f.manifest.controls[0], control_id: 'comic:unvisited' }); }, 'control_without_required_case');
blocked('silently missing matrix cell', f => { f.manifest.controls[0].required_case_ids.push(f.c.case_id + '-warm'); }, 'control_without_required_case');
blocked('case absent from control denominator', f => { f.manifest.controls[0].required_case_ids = ['other-case']; }, 'case_not_in_control_inventory');
blocked('unexpanded raw case row', f => { delete f.c.dimensions; }, 'case_matrix_not_expanded');
blocked('single sample not repeated plan', f => { f.c.repetitions = 1; }, 'missing_repetition_plan');
blocked('duplicate definition', f => { f.manifest.controls.push(f.manifest.controls[0]); }, 'duplicate_control');
blocked('duplicate required case', f => { f.manifest.cases.push(f.c); }, 'duplicate_case');
blocked('legacy functional PASS and family counts', f => { Object.assign(f.report, { ok: true, smooth: false, actions: [{ ok: true, feedbackMs: 1999 }], families: { all: { p95: 1 } } }); f.report.observations = []; }, 'missing_required_observation');
blocked('legacy summary cannot coexist with qualified performance envelope', f => { f.report.smooth = false; }, 'legacy_presentation_report');
blocked('extra malformed observation is not ignored', f => { f.report.observations.push(f.write(null)); }, 'invalid_observation');
blocked('missing exact repetition', f => { f.report.observations.pop(); }, 'missing_required_observation');
blocked('duplicate observation', f => { f.report.observations.push(f.report.observations[0]); }, 'duplicate_observation');
blocked('unknown control', f => f.update('AgentNet', 0, r => { r.control_id = 'other-control'; }), 'unknown_observation_control_or_case');
blocked('unknown case', f => f.update('AgentNet', 0, r => { r.case_id = 'other-case'; }), 'unknown_observation_control_or_case');
blocked('out of plan repeat', f => f.update('AgentNet', 0, r => { r.repetition = 99; }), 'invalid_repetition');
for (const status of ['skipped', 'not_executed', 'failed', 'pending']) blocked(status, f => f.update('AgentNet', 0, r => { r.execution = status; }), 'observation_not_executed');
blocked('unexpected runtime failure despite semantic success', f => f.update('AgentNet', 0, r => { r.runtime_errors.push('render exception'); }), 'observation_failed_or_errors_missing');
blocked('missing runtime error evidence', f => f.update('AgentNet', 0, r => { delete r.failures; }), 'observation_failed_or_errors_missing');
blocked('wrong source reference', f => f.update('AgentNet', 0, r => { r.source_ref.line++; }), 'source_identity_mismatch');
blocked('wrong source definition', f => f.update('AgentNet', 0, r => { r.dom.definition_id = 'shared-handler'; }), 'source_identity_mismatch');
blocked('missing DOM target', f => f.update('AgentNet', 0, r => { delete r.dom.locator; }), 'missing_dom_identity');
blocked('missing rendered discovery mapping', f => f.update('AgentNet', 0, r => { delete r.visible_controls; }), 'missing_rendered_control_mapping');
blocked('unknown rendered control', f => f.update('AgentNet', 0, r => { r.visible_controls.push({ ...r.visible_controls[0], control_id: 'unmapped-menu-item' }); }), 'unknown_or_unmapped_rendered_control');
blocked('rendered discovery does not map actual target node', f => f.update('AgentNet', 0, r => { r.visible_controls[0].node_id = 'other-node'; }), 'missing_rendered_control_mapping');
blocked('different exact target', f => f.update('AgentNet', 0, r => { r.dom.target_id = 'other-message'; }), 'missing_dom_identity');
blocked('wrong candidate binary', f => f.update('AgentNet', 0, r => { r.provenance.artifact_sha256 = 'd'.repeat(64); }), 'candidate_identity_mismatch');
blocked('unreviewed runner', f => f.update('AgentNet', 0, r => { r.provenance.runner_sha256 = 'd'.repeat(64); }), 'synthetic_or_unbound_backend');
blocked('invented backend', f => f.update('AgentNet', 0, r => { r.provenance.backend = 'synthetic'; }), 'synthetic_or_unbound_backend');
blocked('synthetic DOM input', f => f.update('AgentNet', 0, r => { r.input.trusted = false; r.input.source = 'DOM.click'; }), 'synthetic_or_invalid_input');
blocked('wrong input modality', f => f.update('AgentNet', 0, r => { r.input.kind = 'keyboard'; }), 'synthetic_or_invalid_input');
blocked('driver queue omitted/reversed', f => f.update('AgentNet', 0, r => { r.input.driver_at_ms = 20; }), 'synthetic_or_invalid_input');
blocked('missing feedback timestamp', f => f.update('AgentNet', 0, r => { delete r.feedback.at_ms; }), 'missing_feedback_frame');
blocked('null is not zero', f => f.update('AgentNet', 0, r => { r.feedback.at_ms = null; }), 'missing_feedback_frame');
blocked('negative timestamp', f => f.update('AgentNet', 0, r => { r.feedback.at_ms = -1; }), 'missing_feedback_frame');
blocked('infinite timestamp from valid JSON exponent', f => {
  const ref = f.report.observations[0], record = fs.readFileSync(path.join(f.root, ref.path), 'utf8');
  Object.assign(ref, f.write(Buffer.from(record.replace('"at_ms":50', '"at_ms":1e999'))));
}, 'missing_feedback_frame');
blocked('unrelated clock domain', f => f.update('AgentNet', 0, r => { r.feedback.clock = 'other-clock'; }), 'missing_feedback_frame');
blocked('feedback before actual input', f => f.update('AgentNet', 0, r => { r.feedback.at_ms = 1; }), 'invalid_milestone_order');
blocked('RAF is not frame presentation', f => f.update('AgentNet', 0, r => { r.feedback.kind = 'two-RAF'; }), 'missing_feedback_frame');
blocked('frame has no capture', f => f.update('AgentNet', 0, r => { delete r.confirmed.witness; }), 'missing_confirmed_witness');
blocked('arbitrary text is not a capture', f => f.update('AgentNet', 0, r => { r.confirmed.witness = f.write(Buffer.from('PASS'), 'txt'); }), 'invalid_frame_capture');
blocked('optimistic success hides pending', f => f.update('AgentNet', 0, r => { r.feedback.state = 'reaction-stored'; }), 'untruthful_feedback_state');
blocked('visible result is wrong', f => f.update('AgentNet', 0, r => { r.confirmed.state = 'queued'; }), 'missing_confirmed_frame');
blocked('semantic error not success', f => f.update('AgentNet', 0, r => { r.result.outcome = 'failure'; }), 'semantic_result_mismatch');
blocked('wrong mutation operation', f => f.update('AgentNet', 0, r => { r.result.operation = 'delete'; }), 'semantic_result_mismatch');
blocked('no exact request identity', f => f.update('AgentNet', 0, r => { r.request = null; }), 'missing_request_identity');
blocked('commit belongs to other request', f => f.update('AgentNet', 0, r => { r.commit.request_id = 'other-request'; }), 'missing_or_invalid_commit');
blocked('success precedes local commit', f => f.update('AgentNet', 0, r => { r.commit.at_ms = 500; }), 'missing_or_invalid_commit');
blocked('commit has no byte proof', f => f.update('AgentNet', 0, r => { delete r.commit.witness; }), 'missing_commit_witness');
blocked('commit proof mismatch', f => f.update('AgentNet', 0, r => { r.commit.witness = f.write({ type: 'local-commit', request_id: 'wrong' }); }), 'commit_witness_mismatch');
blocked('remote completion not measured', f => { f.c.expected.remote_completion = true; f.c.expected.remote_receipt = 'peer-stored'; }, 'missing_or_invalid_remote_completion');
blocked('fabricated remote time for inapplicable action', f => f.update('AgentNet', 0, r => { r.remote = { at_ms: 90 }; }), 'inapplicable_remote_not_null');
blocked('missing comparator plan', f => { delete f.c.comparators; }, 'missing_comparator_plan');
blocked('cannot choose slower comparator only', f => { f.report.comparators = f.report.comparators.filter(g => g.product === 'Slack'); }, 'missing_required_comparator');
blocked('pooled comparator percentile cannot replace raw data', f => { f.report.comparators[0].observations = []; f.report.comparators[0].p95Ms = 999; }, 'missing_required_comparator');
blocked('comparator skipped', f => f.update('Slack', 0, r => { r.execution = 'skipped'; }), 'observation_not_executed');
blocked('different history scale', f => f.update('Slack', 0, r => { r.dimensions.history = '10x'; }), 'incomparable_conditions');
blocked('different machine or network', f => f.update('Telegram', 0, r => { r.conditions.machine = 'faster-machine'; }), 'incomparable_conditions');
blocked('cannot select slower Slack while required Telegram is faster', f => {
  for (let i = 0; i < f.c.repetitions; i++) f.update('Telegram', i, r => { r.feedback.at_ms = 20; r.confirmed.at_ms = 30; });
}, 'over_budget_feedback_maxMs');
blocked('one slow feedback sample cannot hide in family p95', f => f.update('AgentNet', 0, r => { r.feedback.at_ms = 180; r.confirmed.at_ms = 200; }), 'over_budget_feedback_maxMs');
blocked('confirmed result slow despite instant pending feedback', f => f.update('AgentNet', 0, r => { r.confirmed.at_ms = 2000; }), 'over_budget_confirmed_maxMs');
blocked('reference evidence modified after hashing', f => { fs.appendFileSync(path.join(f.root, f.report.observations[0].path), ' '); }, 'unreadable_or_mismatched_evidence');
blocked('forged report source identity', f => { f.report.source_revision = 'e'.repeat(40); }, 'report_identity_mismatch');

// Read-only/navigation actions explicitly have no commit or request timestamps.
{
  const f = fixture();
  try {
    f.c.expected.request = false; f.c.expected.local_commit = false;
    for (let i = 0; i < f.c.repetitions; i++) f.update('AgentNet', i, r => { r.request = null; r.commit = null; });
    assert.equal(f.run().performance_status, 'qualified'); checks++;
    f.update('AgentNet', 0, r => { r.commit = { at_ms: 1 }; });
    assert.equal(f.run().performance_status, 'blocked'); checks++;
  } finally { f.close(); }
}
// A request-only read has no commit, but must actually start before its result.
{
  const f = fixture();
  try {
    f.c.expected.local_commit = false;
    for (let i = 0; i < f.c.repetitions; i++) f.update('AgentNet', i, r => { r.commit = null; });
    assert.equal(f.run().performance_status, 'qualified'); checks++;
    f.update('AgentNet', 0, r => { r.request.at_ms = 10000; });
    const result = f.run(); assert.equal(result.performance_status, 'blocked');
    assert(result.errors.some(e => e.code === 'missing_request_identity')); checks++;
  } finally { f.close(); }
}
// Remote success requires the exact declared receipt and matching raw byte proof.
{
  const f = fixture();
  try {
    f.c.expected.remote_completion = true; f.c.expected.remote_receipt = 'peer-stored';
    for (const product of ['AgentNet', 'Slack', 'Telegram']) for (let i = 0; i < f.c.repetitions; i++) f.update(product, i, r => {
      r.request ??= { id: product + '-request-' + i, operation: f.c.operation, target_id: r.dom.target_id, at_ms: 12 };
      r.remote = { at_ms: 90, request_id: r.request.id, receipt: 'peer-stored' };
      r.remote.witness = f.write({ type: 'remote-completion', ...r.remote, clock: r.provenance.clock });
    });
    assert.equal(f.run().performance_status, 'qualified'); checks++;
    f.update('AgentNet', 0, r => { r.remote.receipt = 'custody'; });
    const result = f.run(); assert.equal(result.performance_status, 'blocked');
    assert(result.errors.some(e => e.code === 'missing_or_invalid_remote_completion')); checks++;
  } finally { f.close(); }
}
// An explicit reviewed N/A needs its own acceptance budgets, never an implicit waiver.
{
  const f = fixture();
  try {
    f.c.comparators = { products: [], not_applicable: { reason: 'invented unit-only operation', review_reference: 'unit-review', budgets: { feedback: { p95Ms: 80, maxMs: 90 }, confirmed: { p95Ms: 120, maxMs: 130 } } } };
    f.report.comparators = [];
    assert.equal(f.run().performance_status, 'qualified'); checks++;
    delete f.c.comparators.not_applicable.review_reference;
    assert.equal(f.run().performance_status, 'blocked'); checks++;
  } finally { f.close(); }
}
// Filesystem authority: absolute paths, traversal and symlink escapes cannot read outside evidence root.
{
  const f = fixture(), outside = fs.mkdtempSync(path.join(os.tmpdir(), 'interaction-gate-outside-'));
  try {
    const file = path.join(outside, 'outside.json'); fs.writeFileSync(file, '{}');
    const reader = evidenceReader(f.root), digest = hash('{}');
    assert.throws(() => reader({ path: file, sha256: digest })); checks++;
    assert.throws(() => reader({ path: path.relative(f.root, file), sha256: digest })); checks++;
    try {
      fs.symlinkSync(file, path.join(f.root, 'escape.json'));
      assert.throws(() => reader({ path: 'escape.json', sha256: digest })); checks++;
    } catch (e) { if (e.code !== 'EPERM') throw e; }
  } finally { f.close(); fs.rmSync(outside, { recursive: true, force: true }); }
}
// CLI validates bytes and fails closed without a manifest, evidence or records.
{
  const f = fixture();
  try {
    f.run(); const m = path.join(f.root, 'manifest.json'), r = path.join(f.root, 'report.json');
    fs.writeFileSync(m, JSON.stringify(f.manifest)); fs.writeFileSync(r, JSON.stringify(f.report));
    const command = path.join(__dirname, 'interaction_coverage_gate.cjs');
    let p = spawnSync(process.execPath, [command, m, r, f.root], { encoding: 'utf8' });
    assert.ifError(p.error); assert.equal(p.status, 0, p.stderr); assert.equal(JSON.parse(p.stdout).performance_status, 'qualified'); checks++;
    f.report.observations = []; fs.writeFileSync(r, JSON.stringify(f.report));
    p = spawnSync(process.execPath, [command, m, r, f.root], { encoding: 'utf8' });
    assert.ifError(p.error); assert.equal(p.status, 1); assert.equal(JSON.parse(p.stdout).performance_status, 'blocked'); checks++;
    fs.writeFileSync(m, '{'); p = spawnSync(process.execPath, [command, m, r, f.root], { encoding: 'utf8' });
    assert.ifError(p.error); assert.equal(p.status, 2); checks++;
  } finally { f.close(); }
}
console.log(JSON.stringify({ ok: true, checks, scope: 'validator unit guards only; no app or comparator timings qualified' }));
