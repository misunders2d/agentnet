// Rewrite an MP4 or QuickTime file with its samples in time order, unchanged.
//
// Mediabunny's MP4 muxer writes each track's half-second chunks in the order
// its encoders deliver them, so a prepared video can hold a second or more of
// picture ahead of its sound in the file. The Linux app's player (WebKitGTK
// 2.50 with GStreamer 1.20) then seeks back and forth in its source and, from
// about two seconds on, reads the wrong bytes: the picture freezes while the
// sound plays on (owner report, November 2, 2026). Half-second chunks in time
// order still stutter there; one sample per chunk, as ffmpeg writes, plays
// smoothly. So: index first, then every sample as its own chunk, in decode
// time order across tracks. Every other box is kept byte for byte: codecs,
// timing, edit lists, rotation and metadata stay as they are.

type Box = { type: string; body: Uint8Array; kids?: Box[] };
type Sample = { offset: number; size: number; time: number; track: number; index: number };

const containers = new Set(["moov", "trak", "mdia", "minf", "stbl"]);

function parse(b: Uint8Array, from = 0, to = b.length): Box[] {
  const v = new DataView(b.buffer, b.byteOffset, b.byteLength), out: Box[] = [];
  for (let at = from; at < to;) {
    if (to - at < 8) throw Error("Truncated box.");
    let size = v.getUint32(at), head = 8;
    const type = String.fromCharCode(...b.subarray(at + 4, at + 8));
    if (size === 1) { size = Number(v.getBigUint64(at + 8)); head = 16; } else if (size === 0) size = to - at;
    if (size < head || at + size > to) throw Error("Malformed " + type + " box.");
    out.push({ type, body: b.subarray(at + head, at + size), kids: containers.has(type) ? parse(b, at + head, at + size) : undefined });
    at += size;
  }
  return out;
}
const size = (x: Box): number => 8 + (x.kids ? x.kids.reduce((n, k) => n + size(k), 0) : x.body.length);
function write(boxes: Box[], out: Uint8Array, at: number): number {
  for (const x of boxes) {
    new DataView(out.buffer).setUint32(at, size(x));
    for (let i = 0; i < 4; i++) out[at + 4 + i] = x.type.charCodeAt(i);
    if (x.kids) write(x.kids, out, at + 8); else out.set(x.body, at + 8);
    at += size(x);
  }
  return at;
}
const find = (boxes: Box[] | undefined, type: string) => boxes?.find(x => x.type === type);
const view = (x: Box) => new DataView(x.body.buffer, x.body.byteOffset, x.body.byteLength);
// A full box (version and flags 0) holding an entry count and its entries.
function table(type: string, count: number, entry: number) {
  const body = new Uint8Array(8 + count * entry);
  new DataView(body.buffer).setUint32(4, count);
  return { type, body };
}

export function interleaveMp4(data: Uint8Array): Uint8Array<ArrayBuffer> {
  const top = parse(data), moov = find(top, "moov");
  if (!moov || top.some(x => x.type === "moof") || find(moov.kids, "mvex")) throw Error("Not a complete, unfragmented MP4 file.");
  const samples: Sample[] = [], tracks: { stbl: Box; desc: number[] }[] = [];
  for (const trak of moov.kids!.filter(x => x.type === "trak")) {
    const mdia = find(trak.kids, "mdia"), mdhd = find(mdia?.kids, "mdhd"), stbl = find(find(mdia?.kids, "minf")?.kids, "stbl");
    const stsz = find(stbl?.kids, "stsz"), stsc = find(stbl?.kids, "stsc"), stts = find(stbl?.kids, "stts"), stco = find(stbl?.kids, "stco") || find(stbl?.kids, "co64");
    if (!mdhd || !stbl || !stsz || !stsc || !stts || !stco) throw Error("A track lacks its sample tables.");
    const scale = mdhd.body[0] === 1 ? view(mdhd).getUint32(20) : view(mdhd).getUint32(12);
    const sz = view(stsz), fixed = sz.getUint32(4), count = sz.getUint32(8), co = view(stco), sc = view(stsc), tt = view(stts);
    const chunk = (c: number) => stco.type === "co64" ? Number(co.getBigUint64(8 + c * 8)) : co.getUint32(8 + c * 4);
    const chunks = co.getUint32(4), runs = sc.getUint32(4), track = tracks.length, desc: number[] = [];
    let index = 0, time = 0, timing = 0, left = 0, delta = 0;
    for (let r = 0; r < runs; r++) {
      const first = sc.getUint32(8 + r * 12), per = sc.getUint32(12 + r * 12), d = sc.getUint32(16 + r * 12);
      const end = r + 1 < runs ? sc.getUint32(8 + (r + 1) * 12) : chunks + 1;
      if (first < 1 || end > chunks + 1) throw Error("Sample table names a missing chunk.");
      for (let c = first; c < end; c++) {
        let offset = chunk(c - 1);
        for (let s = 0; s < per && index < count; s++, index++) {
          while (!left) {
            if (timing >= tt.getUint32(4)) throw Error("Sample timing ends early.");
            left = tt.getUint32(8 + timing * 8); delta = tt.getUint32(12 + timing * 8); timing++;
          }
          const bytes = fixed || sz.getUint32(12 + index * 4);
          if (offset + bytes > data.length) throw Error("A sample lies outside the file.");
          samples.push({ offset, size: bytes, time: time / scale, track, index }); desc.push(d);
          offset += bytes; time += delta; left--;
        }
      }
    }
    if (index !== count) throw Error("Sample tables disagree.");
    tracks.push({ stbl, desc });
  }
  if (!samples.length) throw Error("This file has no samples.");
  // Decode order within each track; the earliest sample of any track first.
  samples.sort((a, b) => a.time - b.time || a.track - b.track || a.index - b.index);
  const media = samples.reduce((n, s) => n + s.size, 0), large = media + 8 > 0xffffffff;
  const head = [find(top, "ftyp"), moov, ...top.filter(x => !["ftyp", "moov", "mdat", "free", "skip", "wide"].includes(x.type))].filter((x): x is Box => !!x);
  // The index's size depends only on whether offsets need 64 bits.
  let wide = false, base = 0;
  for (;;) {
    for (const { stbl, desc } of tracks) {
      const runs: number[] = [];
      desc.forEach((d, i) => { if (!i || d !== desc[i - 1]) runs.push(i + 1, 1, d); });
      stbl.kids = stbl.kids!.map(x => {
        if (x.type === "stco" || x.type === "co64") return table(wide ? "co64" : "stco", desc.length, wide ? 8 : 4);
        if (x.type !== "stsc") return x;
        const t = table("stsc", runs.length / 3, 12), v = view(t);
        runs.forEach((n, i) => v.setUint32(8 + i * 4, n));
        return t;
      });
    }
    base = head.reduce((n, x) => n + size(x), 0) + (large ? 16 : 8);
    if (wide || base + media <= 0xffffffff) break;
    wide = true;
  }
  let next = base;
  const offsets = tracks.map(t => new Array<number>(t.desc.length));
  for (const s of samples) { offsets[s.track][s.index] = next; next += s.size; }
  tracks.forEach(({ stbl }, t) => {
    const v = view(find(stbl.kids, wide ? "co64" : "stco")!);
    offsets[t].forEach((o, i) => wide ? v.setBigUint64(8 + i * 8, BigInt(o)) : v.setUint32(8 + i * 4, o));
  });
  const out = new Uint8Array(base + media), v = new DataView(out.buffer);
  let at = write(head, out, 0);
  if (large) { v.setUint32(at, 1); v.setBigUint64(at + 8, BigInt(media + 16)); } else v.setUint32(at, media + 8);
  out.set([109, 100, 97, 116], at + 4); // "mdat"
  at += large ? 16 : 8;
  for (const s of samples) { out.set(data.subarray(s.offset, s.offset + s.size), at); at += s.size; }
  return out;
}
