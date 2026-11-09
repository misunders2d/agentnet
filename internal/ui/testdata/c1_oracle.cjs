// Shared C1 observer and fail-closed report checks. Synthetic fixtures never qualify a release.
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const path = require('node:path');
const digest = value => crypto.createHash('sha256').update(value).digest('hex');

async function measure(page, target, spec, act) {
  await target.scrollIntoViewIfNeeded();
  const token = 'c1-' + crypto.randomUUID();
  await target.evaluate((el, value) => el.setAttribute('data-c1-input', value), token);
  await page.evaluate(({ token, snapshot, local, confirmed }) => {
    const read = new Function('return (' + snapshot + ')()');
    const wants = new Function('s', 'return (' + local + ')(s)');
    const done = new Function('s', 'return (' + confirmed + ')(s)');
    const before = read();
    if (wants(before)) throw Error('C1 already-true local predicate');
    if (done(before)) throw Error('C1 already-true confirmed predicate');
    const target = document.querySelector('[data-c1-input="' + token + '"]');
    const o = globalThis.__c1Observation = {
      before, driver_at_ms: performance.now(), input: null, feedback: null, confirmed: null,
      fired: [], error: null, baseline_local: false, baseline_confirmed: false,
      target_definition_id: target.getAttribute('data-an-control'),
      target_label: target.getAttribute('aria-label') || target.labels?.[0]?.textContent.trim() || target.textContent.trim(),
    };
    (globalThis.__c1Observations ||= new Map()).set(token,o);
    const types = ['pointerdown', 'keydown', 'beforeinput', 'input', 'click'];
    const input = event => {
      if (o.input || !(event.target === target || target.contains(event.target))) return;
      o.input = { at_ms: performance.now(), trusted: event.isTrusted, type: event.type };
      if (!event.isTrusted) o.error = 'C1 untrusted input';
      const atInput = read();
      o.input.before = atInput;
      o.input.local_before = wants(atInput);
      o.input.confirmed_before = done(atInput);
      if (o.input.local_before || o.input.confirmed_before) o.error = 'C1 predicate already true at input';
    };
    for (const type of types) document.addEventListener(type, input, true);
    const deadline = performance.now() + 1500;
    let localFrames = 0, confirmedFrames = 0;
    let feedbackReady;
    let watchdog;
    let finished=false,frameHandle;
    o.feedbackPromise=new Promise(resolve=>{feedbackReady=resolve});
    o.promise = new Promise(resolve => {
      function finish() {
        if (finished) return;
        finished=true;cancelAnimationFrame(frameHandle);
        clearTimeout(watchdog);
        for (const type of types) document.removeEventListener(type,input,true);
        o.fired=(globalThis.__c1Fired||[]).filter(x=>x.at_ms>=o.driver_at_ms).map(x=>x.id);
        feedbackReady();resolve();
      }
      function frame() {
        if (finished) return;
        try {
        const state = read(), changed = JSON.stringify(state) !== JSON.stringify(before);
        if (o.input) {
          localFrames = changed && wants(state) ? localFrames + 1 : 0;
          confirmedFrames = changed && done(state) ? confirmedFrames + 1 : 0;
          // The second frame follows a paint opportunity. Keep the DOM witness and a screenshot.
          if (!o.feedback && localFrames >= 2) { o.feedback = { at_ms: performance.now(), frames: localFrames, state };feedbackReady(); }
          if (!o.confirmed && confirmedFrames >= 2) o.confirmed = { at_ms: performance.now(), frames: confirmedFrames, state };
        }
        if (o.error || (o.feedback && o.confirmed) || performance.now() >= deadline) {
          finish();
        } else frameHandle=requestAnimationFrame(frame);
        } catch (error) { o.error='C1 observer exception: '+error.message;finish(); }
      }
      watchdog=setTimeout(finish,1500);
      frameHandle=requestAnimationFrame(frame);
    });
  }, { token, snapshot: spec.snapshot.toString(), local: spec.local.toString(), confirmed: spec.confirmed.toString() });
  await act(target);
  const result = await page.evaluate(async token => {
    const o = globalThis.__c1Observations.get(token);
    await o.feedbackPromise;
    o.fired=(globalThis.__c1Fired||[]).filter(x=>x.at_ms>=o.driver_at_ms).map(x=>x.id);
    const { promise, feedbackPromise, ...result } = o;
    return result;
  },token);
  result.observation_token=token;
  checkMeasurement(result, spec.handlers || [], {local:spec.local.toString(),confirmed:spec.confirmed.toString()}, spec.target_definition_id,true);
  return result;
}

async function finishMeasurement(page, measurement) {
  const completed=await page.evaluate(async token=>{
    const o=globalThis.__c1Observations.get(token);await o.promise;
    const {promise,feedbackPromise,...result}=o;globalThis.__c1Observations.delete(token);return result;
  },measurement.observation_token);
  Object.assign(measurement,completed);
}

function checkMeasurement(m, handlers = [], expected, targetDefinition, localOnly=false) {
  assert(m && !m.error, m?.error || 'C1 missing measurement');
  assert.equal(m.baseline_local, false, 'C1 already-true local predicate');
  assert.equal(m.baseline_confirmed, false, 'C1 already-true confirmed predicate');
  assert(m.input?.trusted === true, 'C1 trusted input missing');
  assert(m.input.local_before === false && m.input.confirmed_before === false, 'C1 predicate already true at input');
  assert(targetDefinition && m.target_definition_id === targetDefinition, 'C1 DOM/source identity mismatch');
  assert(Number.isFinite(m.driver_at_ms) && Number.isFinite(m.input.at_ms) && m.driver_at_ms <= m.input.at_ms, 'C1 input clock invalid');
  for (const key of localOnly?['feedback']:['feedback', 'confirmed']) {
    const v = m[key];
    assert(v && Number.isFinite(v.at_ms) && v.at_ms >= m.input.at_ms && v.frames >= 2 && v.state, 'C1 missing ' + key + ' timing/witness');
    assert.notDeepEqual(v.state, m.before, 'C1 unchanged ' + key + ' witness');
  }
  assert(m.feedback.at_ms - m.input.at_ms < 1000, 'C1 local feedback exceeded 1000ms');
  assert(m.feedback.at_ms - m.driver_at_ms < 1000, 'C1 driver-to-feedback exceeded 1000ms');
  for (const id of handlers) assert(m.fired.includes(id), 'C1 handler missing: ' + id);
  assert(expected?.local && expected?.confirmed, 'C1 intended outcome definition missing');
  for (const [kind, witness] of [['local','feedback'],['confirmed','confirmed']]) {
    if (localOnly && kind==='confirmed') continue;
    const predicate = new Function('return (' + expected[kind] + ')')();
    assert.equal(predicate(m.before), false, 'C1 already-true ' + kind + ' predicate');
    assert.equal(predicate(m.input.before), false, 'C1 predicate already true at input');
    assert.equal(predicate(m[witness].state), true, 'C1 intended ' + kind + ' result missing');
  }
}

function checkBackend(witness, mode) {
  assert(witness?.mode === mode && witness.requests > 0, 'C1 backend fixture not exercised');
  if (mode === 'held') assert(witness.pending > 0 && witness.failed === 0, 'C1 backend was not held');
  if (mode === 'failing') assert(witness.failed > 0 && witness.pending === 0, 'C1 backend failure not exercised');
  if (mode === 'normal') assert(witness.failed === 0 && witness.pending === 0, 'C1 normal backend unexpectedly failed/held');
}

async function capturePresented(page, file, snapshot, measurement) {
  const png = await page.screenshot({path:file});
  const witness = await page.evaluate(source => ({at_ms:performance.now(),state:new Function('return ('+source+')()')()}),snapshot.toString());
  measurement.presented = {...witness,path:path.basename(file),sha256:digest(png),clock:'screenshot-completion upper bound'};
}

function checkReport(manifest, report, readArtifact) {
  assert(manifest.required_case_ids.length > 0, 'C1 empty denominator');
  assert.equal(new Set(manifest.required_case_ids).size, manifest.required_case_ids.length, 'C1 duplicate required case');
  assert.deepEqual(report.binding, manifest.binding, 'C1 candidate/binary/runner binding mismatch');
  for (const key of ['revision', 'source_digest', 'bundle_sha256', 'runner_sha256', 'browser_sha256']) {
    assert(typeof manifest.binding[key] === 'string' && /^[a-f0-9]+$/.test(manifest.binding[key]) && manifest.binding[key].length === (key === 'revision' ? 40 : 64), 'C1 invalid binding: ' + key);
  }
  assert.equal(report.results.length, manifest.required_case_ids.length, 'C1 open/missing/extra cases');
  assert.equal(manifest.cases.length, manifest.required_case_ids.length, 'C1 missing case definitions');
  const cases = new Map(manifest.cases.map(c => [c.case_id, c]));
  assert.equal(cases.size, manifest.cases.length, 'C1 duplicate case definition');
  const byID = new Map(report.results.map(r => [r.case_id, r]));
  assert.equal(byID.size, report.results.length, 'C1 duplicate result');
  for (const id of manifest.required_case_ids) {
    const r = byID.get(id);
    const c = cases.get(id);
    assert(c, 'C1 required case undefined: ' + id);
    assert(r && r.status === 'pass', 'C1 open/missing/failed case: ' + id);
    assert.deepEqual(r.dimensions, c.dimensions, 'C1 case dimensions mismatch');
    assert.equal(r.target_id, c.target_id, 'C1 target identity mismatch');
    checkMeasurement(r.local, c.handlers, c.expected, c.target_definition_id);
    checkMeasurement(r.other, c.other_handlers, c.other_expected, c.other_definition_id);
    if (c.target_label) assert.equal(r.local.target_label, c.target_label, 'C1 dynamic target label mismatch');
    checkBackend(r.backend_witness,c.dimensions.backend);
    for (const [key,expected] of [['local',c.expected],['other',c.other_expected]]) {
      const m=r[key],v=m.presented;
      assert(v && Number.isFinite(v.at_ms) && v.at_ms >= m.feedback.at_ms, 'C1 presented feedback witness missing');
      assert(v.at_ms-m.input.at_ms<1000 && v.at_ms-m.driver_at_ms<1000,'C1 presented feedback exceeded 1000ms');
      assert.equal(new Function('return ('+expected.local+')')()(v.state),true,'C1 intended result did not persist through screenshot');
      assert(v.sha256 && /^[a-f0-9]{64}$/.test(v.sha256), 'C1 feedback screenshot missing');
      assert(/^feedback-\d+(?:-other)?\.png$/.test(v.path), 'C1 invalid screenshot path');
      assert.equal(typeof readArtifact, 'function', 'C1 artifact reader missing');
      const png=readArtifact(v.path);
      assert(Buffer.isBuffer(png) && png.subarray(0,8).equals(Buffer.from('89504e470d0a1a0a','hex')) && digest(png)===v.sha256,'C1 screenshot hash/content mismatch');
    }
    assert.equal(r.confirmation_kind, 'local-ui-result', 'C1 local action cannot claim remote confirmation');
  }
  return { required: manifest.required_case_ids.length, passed: byID.size, open: 0, missing: 0,
    subset_only: true, release_qualified: false };
}

module.exports = { measure, finishMeasurement, capturePresented, checkBackend, checkMeasurement, checkReport, digest };
