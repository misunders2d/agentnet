// Execute the actual Comic model; preserve device-clock output while bounding
// expensive formatter construction within one synchronous render batch.
import assert from 'node:assert/strict';
import model from './comic_model.mjs';

const NativeFormat = Intl.DateTimeFormat;
const nativeTime = Date.prototype.toLocaleTimeString;
const oldZone = process.env.TZ;
let constructions = 0, localeCalls = 0;
Date.prototype.toLocaleTimeString = function (...args) {
  localeCalls++;
  return nativeTime.apply(this, args);
};
Intl.DateTimeFormat = new Proxy(NativeFormat, {
  construct(target, args) {
    constructions++;
    return Reflect.construct(target, args);
  },
});
const previous = date => nativeTime.call(date, [], { hour: 'numeric', minute: '2-digit' });
const inputs = ['', 'not a date', '2026-01-01T00:00:00Z', '2026-07-01T23:59:00Z',
  '2026-03-08T06:59:00Z', '2026-03-08T07:01:00Z', '2026-11-01T05:59:00Z', '2026-11-01T06:01:00Z'];

try {
  for (const zone of ['UTC', 'America/New_York', 'Europe/Kyiv', 'Asia/Kathmandu', 'Pacific/Auckland']) {
    await Promise.resolve();
    process.env.TZ = zone;
    const before = constructions, beforeLocale = localeCalls;
    for (const input of inputs) {
      const date = new Date(input), expected = previous(date);
      assert.equal(model.timeOf(input), expected, `${zone}: message time ${input}`);
      assert.equal(model.clock(date), expected, `${zone}: reminder clock ${input}`);
    }
    const date = new Date(inputs[2]), expected = previous(date);
    for (let i = 0; i < 1000; i++) assert.equal(model.clock(date), expected);
    assert.equal(localeCalls - beforeLocale, 4, `${zone}: valid rows avoid per-row locale setup; only invalid dates use the old fallback`);
    assert.equal(constructions - before, 1, `${zone}: one formatter for the whole synchronous batch`);
    await Promise.resolve();
    assert.equal(model.clock(date), expected);
    assert.equal(constructions - before, 2, `${zone}: later work rechecks the device clock`);
  }
  await Promise.resolve();
  const before = constructions;
  assert.equal(model.clock(new Date(NaN)), previous(new Date(NaN)));
  assert.equal(constructions, before, 'invalid dates retain their old output without a formatter');
} finally {
  Intl.DateTimeFormat = NativeFormat;
  Date.prototype.toLocaleTimeString = nativeTime;
  if (oldZone === undefined) delete process.env.TZ;
  else process.env.TZ = oldZone;
}
console.log('PASS actual Comic clock: valid/invalid output, five changing device zones, DST, shared batch cost and later-work refresh');
