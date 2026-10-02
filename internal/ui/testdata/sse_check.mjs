// Checks the signed stream's framing (static/vendor/sse.mjs, eventsource-
// parser, driven as engine.mjs drives it) against the engine's previous
// line framing: the relay's real frames (hub/stream.go) must dispatch
// identically under every chunking, UTF-8 split mid-character and CRLF;
// an incomplete frame is held (dropped at EOF); the grammar the old code
// lacked (bare CR, multi-line data, no-space fields, comments, id, retry,
// unknown fields) is asserted explicitly; an unterminated line beyond the
// buffer limit ends the stream. Run by page_test.go when node is installed.
import { createParser } from "../static/vendor/sse.mjs";

// oldFrames is the engine's previous reader loop, verbatim in logic.
function oldFrames(chunks) {
  const out = [];
  const decoder = new TextDecoder();
  let buf = "", event = "", data = "";
  for (const value of chunks) {
    buf += decoder.decode(value, { stream: true });
    let i;
    while ((i = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, i).replace(/\r$/, "");
      buf = buf.slice(i + 1);
      if (line === "") {
        if (event) out.push([event, data]);
        event = "";
        data = "";
      } else if (line.startsWith("event: ")) event = line.slice(7);
      else if (line.startsWith("data: ")) data += line.slice(6);
    }
  }
  return out;
}
// newFrames is the probe's reader loop: feed each chunk, then drain in order.
function newFrames(chunks, max = 8 << 20) {
  const out = [], queue = [], errors = [];
  const decoder = new TextDecoder();
  const parser = createParser({ onEvent: (m) => queue.push(m), onError: (e) => { errors.push(e.type); if (e.type === "max-buffer-size-exceeded") queue.push({ overflow: true }); }, maxBufferSize: max });
  for (const value of chunks) {
    parser.feed(decoder.decode(value, { stream: true }));
    while (queue.length) { const m = queue.shift(); if (m.overflow) { out.push(["<overflow>", ""]); return { out, errors }; } if (m.event) out.push([m.event, m.data]); }
  }
  return { out, errors };
}
const enc = new TextEncoder();
const bytes = (s) => enc.encode(s);
// split cuts one byte stream at the given offsets (mid-character allowed).
const split = (b, cuts) => { const out = []; let p = 0; for (const c of [...cuts, b.length]) { out.push(b.slice(p, c)); p = c; } return out; };
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
let failed = 0;
const check = (ok, what) => { if (!ok) { failed++; console.log("FAIL: " + what); } };

// 1. The relay's real frames (hub/stream.go), in one chunk and cut everywhere.
const env = JSON.stringify({ v: 2, id: "a".repeat(32), ct: "x".repeat(300), from: "bob/desk" });
const real = "event: members\ndata: {\"members\":[],\"truncated\":false}\n\n" +
  "event: teams\ndata: {\"realm_id\":\"r\",\"teams\":[]}\n\n" +
  "event: message\ndata: " + env + "\n\n" +
  "event: ping\ndata: {\"conn\":\"c1\"}\n\n" +
  "event: link\ndata: {\"state\":\"pending\"}\n\n" +
  "event: linked\ndata: {}\n\n" +
  "event: release\ndata: {\"version\":\"1.0\"}\n\n";
const rb = bytes(real);
{
  const whole = split(rb, []);
  const a = oldFrames(whole), b = newFrames(whole).out;
  check(a.length === 7 && same(a, b), "real hub frames, one chunk: " + a.length + " events");
  let ok = true;
  for (let cut = 1; cut < rb.length; cut++) { const ch = split(rb, [cut]); if (!same(oldFrames(ch), newFrames(ch).out)) { ok = false; console.log("  differs at cut " + cut); break; } }
  check(ok, "real hub frames, every single cut point (" + (rb.length - 1) + " splits)");
  const many = split(rb, [3, 7, 8, 40, 41, 42, 100, 333, 334, 400, 401]);
  check(same(oldFrames(many), newFrames(many).out), "real hub frames, many cuts incl. around newlines");
  const one = []; for (let i = 0; i < rb.length; i++) one.push(rb.slice(i, i + 1));
  check(same(oldFrames(one), newFrames(one).out), "real hub frames, byte by byte");
}
// 2. UTF-8 multi-byte split inside data (a relay body may carry any UTF-8 in JSON strings).
{
  const s = "event: message\ndata: {\"t\":\"héllo — 日本 🙂\"}\n\n";
  const b = bytes(s);
  const cutsInside = [s.indexOf("é") + 8, s.indexOf("🙂") + 9]; // byte offsets past the multi-byte lead bytes
  const ch = split(b, cutsInside.map((c) => Math.min(c, b.length - 1)));
  const a = oldFrames(ch), n = newFrames(ch).out;
  check(same(a, n) && n[0][1].includes("日本 🙂"), "UTF-8 split mid-character: " + JSON.stringify(n[0][1]));
}
// 3. CRLF frames.
{
  const crlf = real.replace(/\n/g, "\r\n");
  const ch = split(bytes(crlf), [10, 11, 12, 200]);
  check(same(oldFrames(ch), newFrames(ch).out), "CRLF line ends, cut around \\r\\n");
  const cr = bytes("event: ping\rdata: {}\r\r");
  const a = oldFrames([cr]), n = newFrames([cr]).out;
  check(a.length === 0 && n.length === 1 && n[0][0] === "ping", "bare CR terminators: old saw no frame; the parser follows the spec (documented difference)");
}
// 4. Multi-line data: the spec joins with LF; the old code concatenated without.
{
  const b = bytes("event: message\ndata: line1\ndata: line2\n\n");
  const a = oldFrames([b]), n = newFrames([b]).out;
  check(a[0][1] === "line1line2" && n[0][1] === "line1\nline2", "multi-line data: old 'line1line2', parser 'line1\\nline2' (documented difference; the relay never sends multi-line data)");
}
// 5. Comments, unknown fields, id/retry, field without the optional space.
{
  const b = bytes(": keep-alive\nretry: 5000\nid: 7\nx-unknown: v\nevent:ping\ndata:{\"conn\":\"c2\"}\n\n");
  const a = oldFrames([b]), r = newFrames([b]);
  check(a.length === 0 && r.out.length === 1 && r.out[0][0] === "ping" && r.out[0][1] === '{"conn":"c2"}' && r.errors.includes("unknown-field"),
    "no-space fields, comment, id, retry, unknown field: old drops the frame, parser dispatches ping and reports the unknown field (" + r.errors.join(",") + ")");
}
// 6. A bounded buffer: a frame larger than the limit ends the stream instead of growing memory.
{
  const unterminated = bytes("event: message\ndata: " + "y".repeat(5000)); // a line that never ends
  const r = newFrames([unterminated], 1024);
  check(r.out.length === 1 && r.out[0][0] === "<overflow>" && r.errors.includes("max-buffer-size-exceeded"), "an unterminated line beyond the buffer limit: the parser reports it and the engine ends the stream (old: unbounded)");
  const whole = bytes("event: message\ndata: " + "y".repeat(5000) + "\n\n"); // a complete frame is not what the limit bounds
  const w = newFrames([whole], 1024);
  check(w.out.length === 1 && w.out[0][0] === "message" && w.out[0][1].length === 5000, "a complete frame larger than the limit still arrives: its size is checked where it is admitted, not here");
}
// 7. An event with data but no name is not dispatched by either.
{
  const b = bytes("data: orphan\n\n");
  check(oldFrames([b]).length === 0 && newFrames([b]).out.length === 0, "a frame without an event name is ignored by both");
}
// 8. A frame cut before its blank line is held until it completes (nothing dispatched early).
{
  const b = bytes("event: ping\ndata: {\"conn\":\"c3\"}\n");
  check(oldFrames([b]).length === 0 && newFrames([b]).out.length === 0, "an incomplete frame is not dispatched");
  const done = [b, bytes("\n")];
  check(same(oldFrames(done), newFrames(done).out) && oldFrames(done).length === 1, "…and is dispatched once the blank line arrives");
}
if (failed) process.exit(1);
console.log("stream framing ok");
