import assert from 'node:assert/strict';
import { previewText, textPreviewName, TEXT_PREVIEW_LIMIT } from '../web/src/features/Message.files.text.ts';
const bytes = text => new TextEncoder().encode(text);
assert.equal(previewText('report.txt', bytes('Exact text\n<script>window.attacked=true</script>')), 'Exact text\n<script>window.attacked=true</script>');
assert.equal(previewText('notes.md', bytes('Привет 👋')), 'Привет 👋');
assert.equal(previewText('empty.txt', new Uint8Array()), '');
for (const name of ['page.html', 'picture.svg', 'archive.zip', 'report.pdf']) {
  assert.equal(textPreviewName(name), false);
  assert.throws(() => previewText(name, bytes('text')), /does not support/);
}
assert.throws(() => previewText('bad.txt', new Uint8Array([0xc3,0x28])), /not UTF-8/);
assert.throws(() => previewText('binary.txt', bytes('before\0after')), /binary/);
assert.throws(() => previewText('large.txt', new Uint8Array(TEXT_PREVIEW_LIMIT+1)), /too large/);
assert.equal(previewText('limit.txt', bytes('a'.repeat(TEXT_PREVIEW_LIMIT))).length, TEXT_PREVIEW_LIMIT);
console.log('text preview check PASS');
