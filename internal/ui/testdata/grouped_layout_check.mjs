// MEL-856: Comic's port of Telegram Desktop's album rules against the
// geometry the unmodified C++ produced (testdata/grouped_layout_oracle).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
const source = stripTypeScriptTypes(readFileSync(new URL('../web/src/features/Message.groups.ts', import.meta.url), 'utf8'));
const { layoutMediaGroup, groupRanges, validRatio, MAX_ALBUM } = await import('data:text/javascript,' + encodeURIComponent(source));
const vectors = JSON.parse(readFileSync(new URL('grouped_layout_vectors.json', import.meta.url), 'utf8'));
assert(vectors.length >= 30, 'every layout branch has vectors');
for (const { sizes, layout } of vectors) {
  const got = layoutMediaGroup(sizes.map(([width, height]) => ({ width, height })));
  assert.deepEqual(got.map(t => [t.x, t.y, t.width, t.height, t.sides]), layout, 'Telegram geometry for ' + JSON.stringify(sizes));
}
// GroupRanges: consecutive kinds, at most ten, a single file stays single.
const r = k => groupRanges(k).map(g => [g.from, g.till, g.kind, g.album]);
assert.deepEqual(r([]), []);
assert.deepEqual(r(['media']), [[0, 1, 'media', false]]);
assert.deepEqual(r(['media', 'media', 'file', 'file', 'music', 'media']), [[0, 2, 'media', true], [2, 4, 'file', true], [4, 5, 'music', false], [5, 6, 'media', false]]);
assert.deepEqual(r(Array(12).fill('media')), [[0, MAX_ALBUM, 'media', true], [10, 12, 'media', true]]);
assert.deepEqual(r(Array(11).fill('file')), [[0, 10, 'file', true], [10, 11, 'file', false]]);
assert.deepEqual(r(['file', 'media', 'file']), [[0, 1, 'file', false], [1, 2, 'media', false], [2, 3, 'file', false]]);
// A GIF is AlbumType::None (attach_prepare.cpp GroupRanges): never an album,
// never counted toward ten, and it splits the albums around it.
assert.deepEqual(r(['media', 'single', 'media', 'media']), [[0, 1, 'media', false], [1, 2, 'single', false], [2, 4, 'media', true]]);
assert.deepEqual(r(['single', 'single']), [[0, 2, 'single', false]]);
assert.deepEqual(r(Array(12).fill('single')), [[0, 12, 'single', false]]);
assert.deepEqual(r(['media', 'media', 'single', 'file', 'file']), [[0, 2, 'media', true], [2, 3, 'single', false], [3, 5, 'file', true]]);
// Ratios: ValidateThumbDimensions' bounds (under 20:1 either way); no ratio is square.
for (const [ratio, want] of [[0, 1], [NaN, 1], [Infinity, 1], [-Infinity, 1], [-2, 1], [1e-9, 1 / 20], [1 / 50, 1 / 20], [50, 20], [20, 20], [1 / 20, 1 / 20], [1.5, 1.5]])
  assert.equal(validRatio(ratio), want, 'validRatio(' + ratio + ')');
for (const sizes of [[[0, 0], [100, 100]], [[100, 0], [0, 100], [100, 100]], [[1e6, 1], [1, 1e6], [3, 4], [4, 3], [5, 5]]]) {
  const got = layoutMediaGroup(sizes.map(([width, height]) => ({ width, height })));
  assert.equal(got.length, sizes.length);
  for (const t of got) assert(Number.isFinite(t.x + t.y + t.width + t.height) && t.width > 0 && t.height > 0, 'a finite tile for ' + JSON.stringify(sizes));
  const clean = sizes.map(([w, h]) => { const q = validRatio(w / h); return { width: Math.round(q * 1000), height: 1000 }; });
  assert.deepEqual(got, layoutMediaGroup(clean), 'bad sizes lay out as their valid ratios');
}
console.log('GROUPED_LAYOUT ' + JSON.stringify({ vectors: vectors.length, result: 'PASS' }));
