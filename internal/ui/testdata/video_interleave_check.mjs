// interleaveMp4 turns a file whose picture runs ahead of its sound (the
// layout mediabunny writes in encoder order; the Linux app's player froze
// two seconds into it) into an index-first file with every sample its own
// chunk in time order, and exactly the same samples. Synthetic input only.
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {createRequire} from 'node:module';
import {pathToFileURL} from 'node:url';

const web = path.resolve(import.meta.dirname, '../web'), work = fs.mkdtempSync(path.join(os.tmpdir(), 'agentnet-interleave-'));
try {
  const require = createRequire(path.join(web, 'package.json'));
  // One bundle for the helper and the fixture's mediabunny: one copy loaded.
  require('esbuild').buildSync({stdin: {contents: "export {interleaveMp4} from './src/features/video-interleave.ts'; export * as mb from 'mediabunny';", resolveDir: web, loader: 'ts'}, bundle: true, format: 'esm', platform: 'node', outfile: path.join(work, 'interleave.mjs'), logLevel: 'error'});
  const {interleaveMp4, mb} = await import(pathToFileURL(path.join(work, 'interleave.mjs')));
  const ffmpeg = (...args) => execFileSync('ffmpeg', ['-nostdin', '-v', 'error', ...args]);
  const file = name => path.join(work, name);

  const skew = name => {
    const last = {}; let most = 0;
    const packets = JSON.parse(execFileSync('ffprobe', ['-v', 'error', '-show_entries', 'packet=stream_index,dts_time,pos', '-of', 'json', file(name)], {encoding: 'utf8'})).packets;
    for (const p of packets.sort((a, b) => a.pos - b.pos)) { last[p.stream_index] = +p.dts_time; most = Math.max(most, Math.max(...Object.values(last)) - Math.min(...Object.values(last))); }
    return most;
  };
  const boxes = name => { const b = fs.readFileSync(file(name)), out = []; for (let i = 0; i + 8 <= b.length;) { const n = b.readUInt32BE(i); out.push(b.toString('latin1', i + 4, i + 8)); if (n < 8) break; i += n; } return out; };
  const samples = async name => {
    const input = new mb.Input({source: new mb.BufferSource(fs.readFileSync(file(name))), formats: mb.ALL_FORMATS}), out = [];
    for (const track of await input.getTracks()) {
      const data = createHash('sha256'), times = createHash('sha256'); let count = 0;
      for await (const p of new mb.EncodedPacketSink(track).packets()) { data.update(p.data); times.update(p.timestamp.toFixed(6) + p.duration.toFixed(6) + p.type + ','); count++; }
      out.push({type: track.type, codec: track.codec, rotation: track.isVideoTrack() ? track.rotation : 0, count, data: data.digest('hex'), times: times.digest('hex')});
    }
    input.dispose();
    return out;
  };
  const decodes = name => execFileSync('ffmpeg', ['-nostdin', '-v', 'error', '-i', file(name), '-f', 'null', '-'], {encoding: 'utf8'}) === '';
  const rewrite = (from, to) => { const started = performance.now(); fs.writeFileSync(file(to), interleaveMp4(new Uint8Array(fs.readFileSync(file(from))))); return Math.round(performance.now() - started); };
  const reports = [];

  // 1. The prepared-video layout: mediabunny's own copy writes each track's
  // half-second chunks as its reader delivers them.
  ffmpeg('-f', 'lavfi', '-i', 'testsrc2=size=320x240:rate=24', '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=48000', '-t', '8', '-c:v', 'libx264', '-preset', 'ultrafast', '-g', '48', '-c:a', 'aac', '-shortest', file('source.mp4'));
  const target = new mb.BufferTarget();
  await (await mb.Conversion.init({input: new mb.Input({source: new mb.BufferSource(fs.readFileSync(file('source.mp4'))), formats: mb.ALL_FORMATS}), output: new mb.Output({format: new mb.Mp4OutputFormat({fastStart: 'in-memory'}), target})})).execute();
  fs.writeFileSync(file('bad.mp4'), new Uint8Array(target.buffer));
  // 2. A camera-style QuickTime file: B-frames, an edit list, rotation and
  // its index at the end.
  ffmpeg('-f', 'lavfi', '-i', 'testsrc2=size=320x240:rate=30', '-f', 'lavfi', '-i', 'sine=frequency=330:sample_rate=44100', '-t', '5', '-c:v', 'libx264', '-preset', 'veryfast', '-bf', '2', '-c:a', 'aac', '-metadata:s:v', 'rotate=90', '-shortest', '-f', 'mov', file('camera.mov'));

  for (const [name, wantBefore, wantBoxes] of [['bad.mp4', 0.9, ['ftyp', 'moov', 'mdat']], ['camera.mov', 0, ['ftyp', 'wide', 'mdat', 'moov']]]) {
    const out = name.replace(/\.\w+$/, '-interleaved.mp4'), ms = rewrite(name, out), before = skew(name), after = skew(out), order = boxes(out);
    assert(before > wantBefore, name + ': fixture layout as intended: ' + before);
    assert.deepEqual(boxes(name), wantBoxes, name + ': fixture boxes as intended');
    assert(after < 0.1, name + ': every sample its own chunk in time order: ' + after);
    assert.deepEqual(order, ['ftyp', 'moov', 'mdat'], name + ': index before media');
    assert.deepEqual(await samples(out), await samples(name), name + ': same tracks, samples, key frames, timing and rotation');
    assert(decodes(out), name + ': decodes cleanly');
    reports.push({name, skewBefore: +before.toFixed(2), skewAfter: +after.toFixed(2), boxes: order, ms});
  }
  // Not a complete MP4: refused, so the caller keeps the bytes as sent.
  ffmpeg('-i', file('source.mp4'), '-c', 'copy', '-movflags', 'frag_keyframe+empty_moov', file('fragmented.mp4'));
  assert.throws(() => interleaveMp4(new Uint8Array(fs.readFileSync(file('fragmented.mp4')))), /unfragmented/);
  assert.throws(() => interleaveMp4(new Uint8Array(fs.readFileSync(file('bad.mp4'))).subarray(0, 4000)));
  console.log(JSON.stringify({ok: true, reports}));
} finally {
  fs.rmSync(work, {recursive: true, force: true});
}
