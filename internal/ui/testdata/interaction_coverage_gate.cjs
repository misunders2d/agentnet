// Offline gate for reviewed, expanded interaction cases. Existing presentation
// reports are deliberately not performance evidence. Run:
// node interaction_coverage_gate.cjs manifest.json report.json evidence-root
// The reviewed manifest and runner are trust inputs. Hashes prove matching
// artifact bytes, not that a runner, timestamp or recording is authentic.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');

const hash = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const object = v => v !== null && typeof v === 'object' && !Array.isArray(v);
const text = v => typeof v === 'string' && v.trim().length > 0;
const sha = v => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v);
const time = v => typeof v === 'number' && Number.isFinite(v) && v >= 0;
const sorted = v => Array.isArray(v) ? v.map(sorted) : object(v) ? Object.fromEntries(Object.keys(v).sort().map(k => [k, sorted(v[k])])) : v;
const canonical = v => JSON.stringify(sorted(v));
const equal = (a, b) => canonical(a) === canonical(b);
const percentile = values => [...values].sort((a, b) => a - b)[Math.ceil(values.length * .95) - 1];
const summary = values => ({ n: values.length, p95Ms: percentile(values), maxMs: Math.max(...values) });

function evidenceReader(root) {
  const base = fs.realpathSync(root);
  return ref => {
    if (!object(ref) || !text(ref.path) || !sha(ref.sha256) || path.isAbsolute(ref.path)) throw Error('invalid evidence reference');
    const file = fs.realpathSync(path.resolve(base, ref.path));
    const rel = path.relative(base, file);
    if (!rel || rel === '..' || rel.startsWith('..' + path.sep) || path.isAbsolute(rel)) throw Error('evidence escapes root');
    if (!fs.statSync(file).isFile()) throw Error('evidence is not a file');
    const bytes = fs.readFileSync(file);
    if (hash(bytes) !== ref.sha256) throw Error('evidence digest mismatch');
    return bytes;
  };
}

function validateCoverage(manifest, report, readEvidence) {
  const errors = [], cases = [];
  const fail = (code, id = '') => errors.push({ code, ...(id ? { id } : {}) });
  const require = (test, code, id) => { if (!test) fail(code, id); return !!test; };
  const read = (ref, id, json = true) => {
    try {
      if (typeof readEvidence !== 'function') throw Error('no evidence reader');
      const bytes = readEvidence(ref);
      if (!object(ref) || !sha(ref.sha256) || hash(bytes) !== ref.sha256) throw Error('bad reference digest');
      return json ? JSON.parse(bytes.toString('utf8')) : bytes;
    } catch { fail('unreadable_or_mismatched_evidence', id); return null; }
  };
  const answer = () => ({ schema_version: 1, functional_status: report?.functional_status ?? 'unreported',
    performance_status: errors.length ? 'blocked' : 'qualified',
    evidence_scope: 'Reviewed manifest and runner; matching artifact bytes do not independently authenticate recordings.',
    percentile_method: 'Per exact case, nearest-rank empirical p95; all declared repetitions and worst sample retained.',
    timing_origin: 'Driver input request to presented frame, including dispatch/actionability; event-to-frame intervals also retained.', cases, errors });
  if (!require(object(manifest) && object(report) && manifest.schema_version === 1 && report.schema_version === 1, 'invalid_schema')) return answer();
  require(report.type === 'interaction-coverage-evidence' && !['ok', 'smooth', 'actions', 'families'].some(k => Object.hasOwn(report, k)), 'legacy_presentation_report');
  // Owner's October 26–27 corrections replace live messenger comparisons with
  // a fixed local-feedback bound. Completion remains measured, not budgeted.
  const fixedTarget = manifest.acceptance?.kind === 'local-feedback';
  if (manifest.acceptance !== undefined) require(fixedTarget && manifest.acceptance.max_ms === 1000 && text(manifest.acceptance.owner_reference), 'invalid_owner_target');
  require(manifest.review?.complete === true && text(manifest.review?.reference), 'manifest_not_reviewed');
  require(manifest.reconciliation?.complete === true && Array.isArray(manifest.reconciliation?.unresolved_bindings) && manifest.reconciliation.unresolved_bindings.length === 0 && Array.isArray(manifest.reconciliation?.unresolved_controls) && manifest.reconciliation.unresolved_controls.length === 0, 'inventory_not_reconciled');
  require(/^[a-f0-9]{40}$/.test(manifest.source_revision || '') && sha(manifest.artifact_sha256) && sha(manifest.runner_sha256), 'missing_candidate_identity');
  require(report.manifest_sha256 === hash(canonical(manifest)) && report.source_revision === manifest.source_revision && report.artifact_sha256 === manifest.artifact_sha256 && report.runner_sha256 === manifest.runner_sha256, 'report_identity_mismatch');
  if (!require(Array.isArray(manifest.controls) && manifest.controls.length > 0 && Array.isArray(manifest.cases) && manifest.cases.length > 0 && Array.isArray(report.observations) && Array.isArray(report.comparators), 'missing_inventory_or_observations')) return answer();
  const controls = new Map(), requirements = new Map();
  for (const c of manifest.controls) {
    if (!require(object(c) && text(c.control_id) && text(c.definition_id) && text(c.source_ref?.repo) && text(c.source_ref?.path) && Number.isInteger(c.source_ref?.line) && c.source_ref.line > 0 && Array.isArray(c.required_case_ids) && c.required_case_ids.length > 0 && c.required_case_ids.every(text) && new Set(c.required_case_ids).size === c.required_case_ids.length, 'invalid_control')) continue;
    if (controls.has(c.control_id)) fail('duplicate_control', c.control_id);
    controls.set(c.control_id, c);
  }
  for (const c of manifest.cases) {
    if (!require(object(c) && text(c.case_id) && controls.has(c.control_id), 'unknown_case_control', c?.case_id)) continue;
    if (requirements.has(c.case_id)) fail('duplicate_case', c.case_id);
    let valid = true;
    const check = (test, code) => { valid = require(test, code, c.case_id) && valid; };
    const d = c.dimensions;
    check(object(d) && ['input', 'platform', 'skin', 'state', 'history', 'view', 'sync'].every(k => text(d[k])) && ['normal', '10x'].includes(d.history) && ['cold', 'warm'].includes(d.view), 'case_matrix_not_expanded');
    check(object(c.conditions) && Object.keys(c.conditions).length > 0 && text(c.operation) && text(c.target_id), 'missing_case_conditions_or_target');
    check(Number.isInteger(c.repetitions) && c.repetitions >= 2, 'missing_repetition_plan');
    check(object(c.expected) && text(c.expected.outcome) && text(c.expected.state) && Array.isArray(c.expected.feedback_states) && c.expected.feedback_states.length > 0 && c.expected.feedback_states.every(text) && ['request', 'local_commit', 'remote_completion'].every(k => typeof c.expected[k] === 'boolean') && (!(c.expected.local_commit || c.expected.remote_completion) || c.expected.request) && (!c.expected.remote_completion || text(c.expected.remote_receipt)), 'missing_semantic_contract');
    const products = c.comparators?.products;
    const na = c.comparators?.not_applicable;
    if (fixedTarget) check(c.comparators == null && report.comparators.length === 0, 'unexpected_comparator_for_owner_target');
    else check(Array.isArray(products) && new Set(products).size === products.length && products.every(p => ['Slack', 'Telegram'].includes(p)) && (products.length > 0 && na == null || products.length === 0 && text(na?.reason) && text(na?.review_reference) && ['feedback', 'confirmed'].every(m => time(na?.budgets?.[m]?.p95Ms) && time(na?.budgets?.[m]?.maxMs) && na.budgets[m].p95Ms <= na.budgets[m].maxMs)), 'missing_comparator_plan');
    if (valid) requirements.set(c.case_id, c);
  }
  for (const c of controls.values()) {
    for (const id of c.required_case_ids) require(requirements.get(id)?.control_id === c.control_id, 'control_without_required_case', id);
    for (const r of requirements.values()) if (r.control_id === c.control_id) require(c.required_case_ids.includes(r.case_id), 'case_not_in_control_inventory', r.case_id);
  }

  const actual = new Map(), references = new Map(), usedEvidence = new Set();
  function observation(ref, product, comparator) {
    const r = read(ref, product);
    if (!require(object(r), 'invalid_observation', product)) return null;
    const c = requirements.get(r.case_id), key = product + ':' + r.case_id + ':' + r.repetition;
    if (!require(r.schema_version === 1 && r.type === 'interaction-observation' && c && r.control_id === c.control_id, 'unknown_observation_control_or_case', key)) return null;
    require(r.subject === product && text(r.subject_version) && r.execution === 'executed', 'observation_not_executed', key);
    require(Array.isArray(r.failures) && r.failures.length === 0 && Array.isArray(r.runtime_errors) && r.runtime_errors.length === 0, 'observation_failed_or_errors_missing', key);
    require(!usedEvidence.has(ref.sha256), 'reused_observation_evidence', key); usedEvidence.add(ref.sha256);
    require(Number.isInteger(r.repetition) && r.repetition >= 0 && r.repetition < c.repetitions, 'invalid_repetition', key);
    require(equal(r.dimensions, c.dimensions) && equal(r.conditions, c.conditions), 'incomparable_conditions', key);
    require(r.provenance?.runner_sha256 === manifest.runner_sha256 && r.provenance?.backend === 'real' && text(r.provenance?.clock), 'synthetic_or_unbound_backend', key);
    if (!comparator) {
      require(r.provenance?.source_revision === manifest.source_revision && r.provenance?.artifact_sha256 === manifest.artifact_sha256, 'candidate_identity_mismatch', key);
      const definition = controls.get(c.control_id);
      require(equal(r.source_ref, definition.source_ref) && r.dom?.definition_id === definition.definition_id, 'source_identity_mismatch', key);
      require(Array.isArray(r.visible_controls) && r.visible_controls.length > 0 && r.visible_controls.some(v => v.control_id === c.control_id && v.node_id === r.dom?.node_id && v.locator === r.dom?.locator), 'missing_rendered_control_mapping', key);
      if (Array.isArray(r.visible_controls)) for (const v of r.visible_controls) {
        const known = controls.get(v?.control_id);
        require(known && known.definition_id === v.definition_id && equal(known.source_ref, v.source_ref) && text(v.locator) && text(v.node_id), 'unknown_or_unmapped_rendered_control', key);
      }
    }
    require(text(r.dom?.locator) && text(r.dom?.node_id) && text(r.dom?.target_id) && (comparator || r.dom.target_id === c.target_id), 'missing_dom_identity', key);
    const input = r.input;
    require(input?.trusted === true && ['browser-trusted-event', 'native-os-event'].includes(input?.source) && input.kind === c.dimensions.input && time(input.driver_at_ms) && time(input.at_ms) && input.driver_at_ms <= input.at_ms, 'synthetic_or_invalid_input', key);
    function frame(f, expectedState, name) {
      require(object(f) && time(f.at_ms) && f.clock === r.provenance?.clock && text(f.frame_id) && f.kind === 'presented-frame' && (expectedState === null ? text(f.state) : f.state === expectedState), 'missing_' + name + '_frame', key);
      if (f?.witness) {
        const bytes = read(f.witness, key + ':' + name, false);
        require(bytes && (bytes.subarray(0, 8).equals(Buffer.from('89504e470d0a1a0a', 'hex')) || bytes.subarray(0, 3).equals(Buffer.from('ffd8ff', 'hex')) || bytes.subarray(0, 4).toString() === 'RIFF' && bytes.subarray(8, 12).toString() === 'WEBP'), 'invalid_frame_capture', key + ':' + name);
      } else fail('missing_' + name + '_witness', key);
    }
    frame(r.feedback, null, 'feedback'); frame(r.confirmed, c.expected?.state, 'confirmed');
    require(c.expected.feedback_states.includes(r.feedback?.state), 'untruthful_feedback_state', key);
    require(time(input?.at_ms) && time(r.feedback?.at_ms) && time(r.confirmed?.at_ms) && input.at_ms <= r.feedback.at_ms && r.feedback.at_ms <= r.confirmed.at_ms, 'invalid_milestone_order', key);
    require(r.result?.outcome === c.expected?.outcome && r.result?.operation === c.operation && r.result?.target_id === r.dom?.target_id, 'semantic_result_mismatch', key);
    const localRequired = !comparator && c.expected?.local_commit;
    const remoteRequired = !!c.expected?.remote_completion;
    if (comparator ? remoteRequired : c.expected.request) {
      require(text(r.request?.id) && r.request.operation === c.operation && r.request.target_id === r.dom?.target_id && time(r.request.at_ms) && input?.at_ms <= r.request.at_ms && r.request.at_ms <= r.confirmed?.at_ms, 'missing_request_identity', key);
    } else require(r.request === null && r.commit === null, 'inapplicable_commit_not_null', key);
    if (localRequired) {
      require(time(r.commit?.at_ms) && r.commit.request_id === r.request?.id && r.commit.operation === c.operation && r.commit.target_id === r.dom?.target_id && r.request?.at_ms <= r.commit.at_ms && r.commit.at_ms <= r.confirmed?.at_ms, 'missing_or_invalid_commit', key);
      if (r.commit?.witness) {
        const proof = read(r.commit.witness, key + ':commit');
        require(proof?.type === 'local-commit' && proof.request_id === r.request?.id && proof.operation === c.operation && proof.target_id === r.dom?.target_id && proof.at_ms === r.commit.at_ms && proof.clock === r.provenance?.clock, 'commit_witness_mismatch', key);
      } else fail('missing_commit_witness', key);
    } else require(r.commit === null, 'inapplicable_commit_not_null', key);
    if (remoteRequired) {
      require(time(r.remote?.at_ms) && r.remote.request_id === r.request?.id && r.remote.receipt === c.expected.remote_receipt && r.request?.at_ms <= r.remote.at_ms && r.remote.at_ms <= r.confirmed?.at_ms, 'missing_or_invalid_remote_completion', key);
      if (r.remote?.witness) {
        const proof = read(r.remote.witness, key + ':remote');
        require(proof?.type === 'remote-completion' && proof.request_id === r.request?.id && proof.receipt === r.remote.receipt && proof.at_ms === r.remote.at_ms && proof.clock === r.provenance?.clock, 'remote_witness_mismatch', key);
      } else fail('missing_remote_witness', key);
    } else require(r.remote === null, 'inapplicable_remote_not_null', key);
    return { c, key, r, feedback: r.feedback?.at_ms - input?.driver_at_ms, confirmed: r.confirmed?.at_ms - input?.driver_at_ms,
      eventFeedback: r.feedback?.at_ms - input?.at_ms, eventConfirmed: r.confirmed?.at_ms - input?.at_ms, dispatch: input?.at_ms - input?.driver_at_ms };
  }
  for (const ref of report.observations) {
    const o = observation(ref, 'AgentNet', false);
    if (!o) continue;
    if (actual.has(o.key)) fail('duplicate_observation', o.key);
    actual.set(o.key, o);
  }
  for (const group of report.comparators) {
    const c = requirements.get(group?.case_id), product = group?.product, key = product + ':' + group?.case_id;
    if (!require(c && c.comparators?.products?.includes(product) && Array.isArray(group.observations), 'unknown_comparator', key)) continue;
    if (references.has(key)) fail('duplicate_comparator', key);
    const observations = new Map();
    for (const ref of group.observations) {
      const o = observation(ref, product, true);
      if (!o) continue;
      require(o.c.case_id === group.case_id, 'wrong_comparator_case', key);
      if (observations.has(o.r.repetition)) fail('duplicate_observation', o.key);
      observations.set(o.r.repetition, o);
    }
    references.set(key, observations);
  }
  for (const c of requirements.values()) {
    if (!Number.isInteger(c.repetitions) || c.repetitions < 2) continue;
    const rows = [];
    for (let i = 0; i < c.repetitions; i++) {
      const r = actual.get('AgentNet:' + c.case_id + ':' + i);
      if (r) rows.push(r); else fail('missing_required_observation', c.case_id + ':' + i);
    }
    if (rows.length !== c.repetitions) continue;
    const measured = { feedback: summary(rows.map(r => r.feedback)), confirmed: summary(rows.map(r => r.confirmed)),
      input_event_feedback: summary(rows.map(r => r.eventFeedback)), input_event_confirmed: summary(rows.map(r => r.eventConfirmed)), dispatch: summary(rows.map(r => r.dispatch)) };
    const compared = [];
    if (fixedTarget) require(time(measured.feedback.maxMs) && measured.feedback.maxMs < 1000, 'over_owner_feedback_budget', c.case_id);
    for (const product of c.comparators?.products || []) {
      const group = references.get(product + ':' + c.case_id);
      if (!require(group?.size === c.repetitions && [...group.keys()].every(i => Number.isInteger(i) && i >= 0 && i < c.repetitions), 'missing_required_comparator', product + ':' + c.case_id)) continue;
      const values = [...group.values()];
      compared.push({ product, feedback: summary(values.map(r => r.feedback)), confirmed: summary(values.map(r => r.confirmed)) });
    }
    if (c.comparators?.not_applicable) compared.push({ product: 'reviewed_not_applicable', ...c.comparators.not_applicable.budgets });
    for (const reference of compared) for (const metric of ['feedback', 'confirmed']) for (const tail of ['p95Ms', 'maxMs']) require(time(measured[metric][tail]) && time(reference[metric]?.[tail]) && measured[metric][tail] <= reference[metric][tail], 'over_budget_' + metric + '_' + tail, c.case_id + ':' + reference.product);
    cases.push({ case_id: c.case_id, measured, comparators: compared });
  }
  return answer();
}

module.exports = { validateCoverage, evidenceReader, hash, canonical };
if (require.main === module) {
  try {
    if (process.argv.length !== 5) throw Error('usage: node interaction_coverage_gate.cjs manifest.json report.json evidence-root');
    const manifest = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
    const report = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
    const result = validateCoverage(manifest, report, evidenceReader(process.argv[4]));
    process.stdout.write(JSON.stringify(result) + '\n');
    process.exitCode = result.performance_status === 'qualified' ? 0 : 1;
  } catch { process.stderr.write('Invalid gate arguments, JSON, or evidence root; performance qualification blocked.\n'); process.exitCode = 2; }
}
