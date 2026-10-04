// node_modules/qr/index.js
/*!
 * Copyright (c) 2023 Paul Miller (paulmillr.com)
 * SPDX-License-Identifier: MIT OR Apache-2.0
 */
var MAX_OUTPUT_SIZE = 1024;
var MAX_COMPACT_OUTPUT_SIZE = MAX_OUTPUT_SIZE * 4;
var BYTES = /* @__PURE__ */ (() => {
  const res = [];
  for (let ver = 1; ver <= 40; ver++) {
    let bits = (16 * ver + 128) * ver + 64;
    if (ver >= 2) {
      const align = Math.floor(ver / 7) + 2;
      bits -= (25 * align - 10) * align - 55;
      if (ver >= 7)
        bits -= 36;
    }
    res.push(bits >>> 3);
  }
  return res;
})();
var ECC_LEVELS = ["low", "medium", "quartile", "high"];
var WORDS_PER_BLOCK = {
  low: [
    7,
    10,
    15,
    20,
    26,
    18,
    20,
    24,
    30,
    18,
    20,
    24,
    26,
    30,
    22,
    24,
    28,
    30,
    28,
    28,
    28,
    28,
    30,
    30,
    26,
    28,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30
  ],
  medium: [
    10,
    16,
    26,
    18,
    24,
    16,
    18,
    22,
    22,
    26,
    30,
    22,
    22,
    24,
    24,
    28,
    28,
    26,
    26,
    26,
    26,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28,
    28
  ],
  quartile: [
    13,
    22,
    18,
    26,
    18,
    24,
    18,
    22,
    20,
    24,
    28,
    26,
    24,
    20,
    30,
    24,
    28,
    28,
    26,
    30,
    28,
    30,
    30,
    30,
    30,
    28,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30
  ],
  high: [
    17,
    28,
    22,
    16,
    22,
    28,
    26,
    26,
    24,
    28,
    24,
    28,
    22,
    24,
    24,
    30,
    28,
    28,
    26,
    28,
    30,
    24,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30,
    30
  ]
};
var ECC_BLOCKS = {
  low: [1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12, 12, 12, 13, 14, 15, 16, 17, 18, 19, 19, 20, 21, 22, 24, 25],
  medium: [1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49],
  quartile: [1, 1, 2, 2, 4, 4, 6, 6, 8, 8, 8, 10, 12, 16, 12, 17, 16, 18, 21, 20, 23, 23, 25, 27, 29, 34, 34, 35, 38, 40, 43, 45, 48, 51, 53, 56, 59, 62, 65, 68],
  high: [1, 1, 2, 4, 4, 4, 5, 6, 8, 8, 11, 11, 16, 16, 18, 16, 19, 21, 25, 25, 25, 34, 30, 32, 35, 37, 40, 42, 45, 48, 51, 54, 57, 60, 63, 66, 70, 74, 77, 81]
};
var EC_CODE = { low: 1, medium: 0, quartile: 3, high: 2 };
var ALPHANUMERIC = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ $%*+-./:";
function alignmentPatterns(ver) {
  ver = asVersion(ver);
  if (ver === 1)
    return [];
  const last = 21 + 4 * (ver - 1) - 7;
  const count = Math.ceil((last - 6) / 28);
  let interval = Math.floor((last - 6) / count);
  if (interval % 2)
    interval += 1;
  else if ((last - 6) % count * 2 >= count)
    interval += 2;
  const res = [6];
  for (let m = 1; m < count; m++)
    res.push(last - (count - m) * interval);
  res.push(last);
  return res;
}
function formatBits(ecc, mask) {
  const data = EC_CODE[ecc] << 3 | mask;
  let d = data;
  for (let i = 0; i < 10; i++)
    d = d << 1 ^ (d >> 9) * 1335;
  return (data << 10 | d) ^ 21522;
}
function versionBits(ver) {
  let d = ver;
  for (let i = 0; i < 12; i++)
    d = d << 1 ^ (d >> 11) * 7973;
  return ver << 12 | d;
}
var MODE_BITS = { numeric: 1, alphanumeric: 2, byte: 4 };
var LENGTH_BITS = {
  numeric: [10, 12, 14],
  alphanumeric: [9, 11, 13],
  byte: [8, 16, 16]
};
var GF256 = /* @__PURE__ */ (() => {
  const exp = new Uint8Array(510);
  const log = new Uint8Array(256);
  for (let i = 0, x = 1; i < 255; i++) {
    exp[i] = exp[i + 255] = x;
    log[x] = i;
    x <<= 1;
    if (x & 256)
      x ^= 285;
  }
  return { exp, log };
})();
function rsGenerator(eccWords) {
  const { exp: EXP, log: LOG } = GF256;
  const gen = new Uint8Array(eccWords);
  gen[eccWords - 1] = 1;
  for (let i = 0, root = 1; i < eccWords; i++) {
    for (let j = 0; j < eccWords; j++) {
      const c = gen[j];
      gen[j] = (c ? EXP[LOG[c] + LOG[root]] : 0) ^ (j + 1 < eccWords ? gen[j + 1] : 0);
    }
    root = EXP[LOG[root] + 1];
  }
  return gen;
}
var RS_CACHE = [];
function rsCached(eccWords) {
  let cached = RS_CACHE[eccWords];
  if (cached !== void 0)
    return cached;
  const gen = rsGenerator(eccWords);
  const { exp: EXP, log: LOG } = GF256;
  const stride = eccWords + 3 >>> 2;
  const mul32 = new Int32Array(256 * stride);
  for (let f = 1; f < 256; f++) {
    const lf = LOG[f];
    for (let j = 0; j < eccWords; j++) {
      const c = gen[j];
      if (c)
        mul32[f * stride + (j >>> 2)] |= EXP[LOG[c] + lf] << 8 * (j & 3);
    }
  }
  return RS_CACHE[eccWords] = { gen, mul32 };
}
var RS_TMP = /* @__PURE__ */ new Int32Array(8);
function rsEcc(data, gen, mul32) {
  const eccWords = gen.length;
  const stride = mul32.length >>> 8;
  const last = stride - 1;
  const w = RS_TMP.fill(0, 0, stride);
  for (let i = 0; i < data.length; i++) {
    let cur = w[0];
    const off = (data[i] ^ cur & 255) * stride;
    for (let k = 0; k < last; k++) {
      const next = w[k + 1];
      w[k] = (cur >>> 8 | next << 24) ^ mul32[off + k];
      cur = next;
    }
    w[last] = cur >>> 8 ^ mul32[off + last];
  }
  const res = new Uint8Array(eccWords);
  for (let j = 0; j < eccWords; j++)
    res[j] = w[j >>> 2] >>> 8 * (j & 3);
  return res;
}
function capacity(ver, ecc) {
  const bytes = BYTES[ver - 1];
  const words = WORDS_PER_BLOCK[ecc][ver - 1];
  const numBlocks = ECC_BLOCKS[ecc][ver - 1];
  const blockLen = Math.floor(bytes / numBlocks) - words;
  const shortBlocks = numBlocks - bytes % numBlocks;
  return { words, numBlocks, shortBlocks, blockLen, capacity: (bytes - words * numBlocks) * 8 };
}
var err = (msg) => {
  throw new Error(msg);
};
function asVersion(ver) {
  if (typeof ver !== "number")
    throw new TypeError(`"ver" expected number, got type=${typeof ver}`);
  if (!Number.isSafeInteger(ver))
    throw new RangeError(`"ver" expected safe integer, got ${ver}`);
  if (ver < 1 || ver > 40)
    throw new RangeError(`Invalid version=${ver}. Expected number [1..40]`);
  return ver;
}
function detectType(str) {
  let type = "numeric";
  for (let i = 0; i < str.length; i++) {
    const v = ALNUM_VAL[str.charCodeAt(i)];
    if (!(v >= 0))
      return "byte";
    if (v > 9)
      type = "alphanumeric";
  }
  return type;
}
var ALNUM_VAL = /* @__PURE__ */ (() => {
  const t = new Int8Array(128).fill(-1);
  for (let i = 0; i < ALPHANUMERIC.length; i++)
    t[ALPHANUMERIC.charCodeAt(i)] = i;
  return t;
})();
function encodeData(ver, ecc, text, type, utf8) {
  const cap = capacity(ver, ecc);
  const lengthBits = LENGTH_BITS[type][Math.floor((ver + 7) / 17)];
  const dataLen = type === "byte" ? utf8.length : text.length;
  if (dataLen >= 1 << lengthBits)
    err("Capacity overflow");
  const bytes = new Uint8Array(cap.capacity >>> 3);
  let acc = 0;
  let accBits = 0;
  let bytePos = 0;
  const push = (value, len) => {
    acc = acc << len | value;
    for (accBits += len; accBits >= 8; )
      bytes[bytePos++] = acc >>> (accBits -= 8) & 255;
  };
  push(MODE_BITS[type], 4);
  push(dataLen, lengthBits);
  if (type === "numeric") {
    for (let i = 0; i < dataLen; i += 3) {
      const n = Math.min(3, dataLen - i);
      push(Number(text.slice(i, i + n)), [0, 4, 7, 10][n]);
    }
  } else if (type === "alphanumeric") {
    for (let i = 0; i + 1 < dataLen; i += 2)
      push(ALNUM_VAL[text.charCodeAt(i)] * 45 + ALNUM_VAL[text.charCodeAt(i + 1)], 11);
    if (dataLen & 1)
      push(ALNUM_VAL[text.charCodeAt(dataLen - 1)], 6);
  } else {
    for (let i = 0; i < utf8.length; i++)
      push(utf8[i], 8);
  }
  let bitPos = bytePos * 8 + accBits;
  if (bitPos > cap.capacity)
    err("Capacity overflow");
  if (accBits)
    bytes[bytePos] = acc << 8 - accBits & 255;
  bitPos += Math.min(4, cap.capacity - bitPos);
  if (bitPos & 7)
    bitPos += 8 - (bitPos & 7);
  for (let i = bitPos >>> 3, pad = 0; i < bytes.length; i++, pad ^= 1)
    bytes[i] = pad ? 17 : 236;
  const { words, numBlocks, shortBlocks, blockLen } = cap;
  const rs = rsCached(words);
  const blocks = [];
  const eccs = [];
  for (let i = 0, pos = 0; i < numBlocks; i++) {
    const len = blockLen + (i < shortBlocks ? 0 : 1);
    blocks.push(bytes.subarray(pos, pos + len));
    eccs.push(rsEcc(blocks[i], rs.gen, rs.mul32));
    pos += len;
  }
  const res = new Uint8Array(bytes.length + words * numBlocks);
  let p = 0;
  for (let i = 0; i <= blockLen; i++) {
    for (const b of blocks)
      if (i < b.length)
        res[p++] = b[i];
  }
  for (let i = 0; i < words; i++)
    for (const e of eccs)
      res[p++] = e[i];
  return res;
}
function maskCalc(x, y) {
  const x2 = x % 2;
  const y2 = y % 2;
  const x3 = x % 3;
  const xy3 = x3 * (y % 3) % 3;
  const xy2 = x2 & y2;
  let bits = 0;
  if (x2 === y2)
    bits |= 1;
  if (y2 === 0)
    bits |= 2;
  if (x3 === 0)
    bits |= 4;
  if ((x + y) % 3 === 0)
    bits |= 8;
  if ((Math.floor(y / 2) + Math.floor(x / 3)) % 2 === 0)
    bits |= 16;
  if (xy2 + xy3 === 0)
    bits |= 32;
  if ((xy2 + xy3) % 2 === 0)
    bits |= 64;
  if (((x2 ^ y2) + xy3) % 2 === 0)
    bits |= 128;
  return bits;
}
var MASK_TABLE = /* @__PURE__ */ (() => {
  const t = new Uint8Array(72);
  for (let y = 0; y < 12; y++)
    for (let x = 0; x < 6; x++)
      t[y * 6 + x] = maskCalc(x, y);
  return t;
})();
function maskBits(x, y) {
  return MASK_TABLE[y % 12 * 6 + x % 6];
}
var POP16 = /* @__PURE__ */ (() => {
  const t = new Uint8Array(1 << 16);
  for (let i = 1; i < t.length; i++)
    t[i] = t[i >>> 1] + (i & 1);
  return t;
})();
var popcnt = (n) => POP16[n & 65535] + POP16[n >>> 16];
var TRANSPOSE_TMP = /* @__PURE__ */ new Int32Array(32);
function transpose32(a) {
  const masks = [1431655765, 858993459, 252645135, 16711935, 65535];
  for (let stage = 0; stage < 5; stage++) {
    const m = masks[stage];
    const s = 1 << stage;
    for (let i = 0; i < 32; i += s << 1) {
      for (let k = 0; k < s; k++) {
        const x = a[i + k];
        const y = a[i + k + s];
        const t = (x >>> s ^ y) & m;
        a[i + k] = x ^ t << s;
        a[i + k + s] = y ^ t;
      }
    }
  }
}
var mat = (size) => {
  const words = size + 31 >>> 5;
  return { size, words, v: new Int32Array(words * size) };
};
var matGet = (m, x, y) => m.v[y * m.words + (x >>> 5)] >>> (x & 31) & 1;
var matSet = (m, x, y, bit) => {
  const i = y * m.words + (x >>> 5);
  const b = 1 << (x & 31);
  m.v[i] = bit ? m.v[i] | b : m.v[i] & ~b;
};
function transposeMat(src, dst) {
  const { size, words, v } = src;
  const tmp = TRANSPOSE_TMP;
  for (let by = 0; by < size; by += 32) {
    for (let bx = 0; bx < words; bx++) {
      const rows = Math.min(32, size - by);
      for (let r = 0; r < rows; r++)
        tmp[r] = v[(by + r) * words + bx];
      tmp.fill(0, rows);
      transpose32(tmp);
      for (let i = 0, dstY = bx * 32; i < 32 && dstY < size; i++, dstY++) {
        dst.v[dstY * dst.words + (by >>> 5)] = tmp[i];
      }
    }
  }
}
function runsPenaltyVertical(m) {
  const { size, words, v } = m;
  const tail = size & 31 ? ~(-1 << (size & 31)) : -1;
  let score = 0;
  for (let wi = 0; wi < words; wi++) {
    const valid = wi === words - 1 ? tail : -1;
    let r3 = v[3 * words + wi];
    let dPrev = -1;
    let d0 = v[wi] ^ v[words + wi];
    let d1 = v[words + wi] ^ v[2 * words + wi];
    let d2 = v[2 * words + wi] ^ r3;
    for (let y = 0, idx = 4 * words + wi; y <= size - 5; y++, idx += words) {
      const r4 = v[idx];
      const d3 = r3 ^ r4;
      const w = ~(d0 | d1 | d2 | d3) & valid;
      if (w)
        score += popcnt(w) + 2 * popcnt(w & dPrev);
      dPrev = d0;
      d0 = d1;
      d1 = d2;
      d2 = d3;
      r3 = r4;
    }
  }
  return score;
}
function finderPenaltyVertical(m) {
  const { size, words, v } = m;
  const tail = size & 31 ? ~(-1 << (size & 31)) : -1;
  let count = 0;
  for (let wi = 0; wi < words; wi++) {
    const valid = wi === words - 1 ? tail : -1;
    let i = wi;
    let r0 = v[i];
    let r1 = v[i += words];
    let r2 = v[i += words];
    let r3 = v[i += words];
    let r4 = v[i += words];
    let r5 = v[i += words];
    let r6 = v[i += words];
    let r7 = v[i += words];
    let r8 = v[i += words];
    let r9 = v[i += words];
    for (let y = 0; y <= size - 11; y++) {
      const r10 = v[i += words];
      const m0 = valid & r0 & ~r1 & r2 & r3 & r4 & ~r5 & r6 & ~(r7 | r8 | r9 | r10);
      const m1 = valid & ~(r0 | r1 | r2 | r3) & r4 & ~r5 & r6 & r7 & r8 & ~r9 & r10;
      count += popcnt(m0) + popcnt(m1);
      r0 = r1;
      r1 = r2;
      r2 = r3;
      r3 = r4;
      r4 = r5;
      r5 = r6;
      r6 = r7;
      r7 = r8;
      r8 = r9;
      r9 = r10;
    }
  }
  return count;
}
function penaltyScore(m, t, limit = Infinity) {
  const { size, words, v } = m;
  const adjacent = runsPenaltyVertical(m) + runsPenaltyVertical(t);
  if (adjacent >= limit)
    return adjacent;
  const tail2 = ~(-1 << size - 32 * (words - 1) - 1);
  let boxes = 0;
  let dark2 = 0;
  for (let y = 0; y < size; y++) {
    for (let wi = 0; wi < words; wi++) {
      const a0 = v[y * words + wi];
      dark2 += popcnt(a0);
      if (y === size - 1)
        continue;
      const a1 = v[(y + 1) * words + wi];
      const n0 = wi + 1 < words ? v[y * words + wi + 1] : 0;
      const n1 = wi + 1 < words ? v[(y + 1) * words + wi + 1] : 0;
      const eqV = ~(a0 ^ a1);
      const eqH0 = ~(a0 ^ (a0 >>> 1 | n0 << 31));
      const eqH1 = ~(a1 ^ (a1 >>> 1 | n1 << 31));
      let w = eqV & eqH0 & eqH1;
      if (wi === words - 1)
        w &= tail2;
      boxes += popcnt(w);
    }
  }
  const total = size * size;
  const darkSteps = Math.ceil(Math.max(0, Math.abs(dark2 * 100 - total * 50) - total * 5) / (total * 5));
  const partial = adjacent + 3 * boxes + 10 * darkSteps;
  if (partial >= limit)
    return partial;
  return partial + 40 * (finderPenaltyVertical(m) + finderPenaltyVertical(t));
}
function drawInfo(m, ver, ecc, mask) {
  const size = m.size;
  const bits = formatBits(ecc, mask);
  for (let i = 0; i < 15; i++) {
    const bit = bits >> i & 1;
    if (i < 6)
      matSet(m, 8, i, bit);
    else if (i < 8)
      matSet(m, 8, i + 1, bit);
    else if (i === 8)
      matSet(m, 7, 8, bit);
    else
      matSet(m, 14 - i, 8, bit);
    if (i < 8)
      matSet(m, size - 1 - i, 8, bit);
    else
      matSet(m, 8, size - 15 + i, bit);
  }
  matSet(m, 8, size - 8, 1);
  if (ver >= 7) {
    const vbits = versionBits(ver);
    for (let i = 0; i < 18; i++) {
      const bit = vbits >> i & 1;
      const x = size - 11 + i % 3;
      const y = i / 3 | 0;
      matSet(m, x, y, bit);
      matSet(m, y, x, bit);
    }
  }
}
var symCache;
function buildSymCache(ver) {
  const size = 21 + 4 * (ver - 1);
  const m = mat(size);
  const fun = new Uint8Array(size * size);
  const setF = (x, y, bit) => {
    matSet(m, x, y, bit);
    fun[y * size + x] = 1;
  };
  for (const [fx, fy] of [
    [0, 0],
    [size - 7, 0],
    [0, size - 7]
  ]) {
    for (let dy = -1; dy < 8; dy++) {
      for (let dx = -1; dx < 8; dx++) {
        const x = fx + dx;
        const y = fy + dy;
        if (x < 0 || y < 0 || x >= size || y >= size)
          continue;
        const on = dx >= 0 && dx < 7 && dy >= 0 && dy < 7 && (dx === 0 || dx === 6 || dy === 0 || dy === 6 || dx > 1 && dx < 5 && dy > 1 && dy < 5);
        setF(x, y, on ? 1 : 0);
      }
    }
  }
  const align = alignmentPatterns(ver);
  for (const ay of align) {
    for (const ax of align) {
      if (fun[ay * size + ax])
        continue;
      for (let dy = -2; dy <= 2; dy++) {
        for (let dx = -2; dx <= 2; dx++) {
          const on = Math.max(Math.abs(dx), Math.abs(dy)) !== 1;
          setF(ax + dx, ay + dy, on ? 1 : 0);
        }
      }
    }
  }
  for (let i = 0; i < size; i++) {
    if (!fun[6 * size + i])
      setF(i, 6, i % 2 === 0 ? 1 : 0);
    if (!fun[i * size + 6])
      setF(6, i, i % 2 === 0 ? 1 : 0);
  }
  for (let i = 0; i < 9; i++) {
    if (i !== 6) {
      setF(8, i, 0);
      setF(i, 8, 0);
    }
    if (i < 8) {
      setF(size - 1 - i, 8, 0);
      setF(8, size - 1 - i, 0);
    }
  }
  if (ver >= 7) {
    for (let i = 0; i < 18; i++) {
      const x = size - 11 + i % 3;
      const y = i / 3 | 0;
      setF(x, y, 0);
      setF(y, x, 0);
    }
  }
  const planes = [];
  for (let i = 0; i < 8; i++)
    planes.push(mat(size));
  const posBuf = new Uint16Array(size * size);
  let n = 0;
  for (let xOffset = size - 1, dir = -1, y = size - 1; xOffset > 0; xOffset -= 2, dir = -dir) {
    if (xOffset === 6)
      xOffset = 5;
    for (; ; y += dir) {
      for (let j = 0; j < 2; j++) {
        const x = xOffset - j;
        if (fun[y * size + x])
          continue;
        const wi = y * m.words + (x >>> 5);
        posBuf[n++] = wi << 5 | x & 31;
        for (let p = 0, mb = maskBits(x, y); mb; p++, mb >>= 1) {
          if (mb & 1)
            planes[p].v[wi] |= 1 << (x & 31);
        }
      }
      if (y + dir < 0 || y + dir >= size)
        break;
    }
  }
  const pair = new Int32Array(n >>> 1);
  for (let i = 0; i + 1 < n; i += 2) {
    const a = posBuf[i];
    const b = posBuf[i + 1];
    if (a >>> 5 === b >>> 5 && (a & 31) === (b & 31) + 1) {
      pair[i >>> 1] = a >>> 5 << 6 | (b & 31) << 1 | 1;
    }
  }
  const planesT = planes.map((p) => {
    const t = mat(size);
    transposeMat(p, t);
    return t.v;
  });
  return {
    ver,
    tpl: m.v,
    pos: posBuf.slice(0, n),
    pair,
    planes: planes.map((p) => p.v),
    planesT,
    work: [mat(size), mat(size), mat(size), mat(size)]
  };
}
function drawSymbol(ver, ecc, data, maskIdx, test = false) {
  if (symCache === void 0 || symCache.ver !== ver)
    symCache = buildSymCache(ver);
  const { tpl, pos, pair, planes, planesT, work } = symCache;
  const [m, t, cand, candT] = work;
  m.v.set(tpl);
  const need = Math.min(8 * data.length, pos.length);
  for (let i = 0; i < need; i += 2) {
    const two = data[i >>> 3] >>> 6 - (i & 7) & 3;
    if (two === 0)
      continue;
    const pr = pair[i >>> 1];
    if (pr & 1)
      m.v[pr >>> 6] |= two << (pr >>> 1 & 31);
    else {
      if (two & 2) {
        const p = pos[i];
        m.v[p >>> 5] |= 1 << (p & 31);
      }
      if (two & 1) {
        const p = pos[i + 1];
        m.v[p >>> 5] |= 1 << (p & 31);
      }
    }
  }
  let mask = maskIdx;
  if (mask === void 0) {
    transposeMat(m, t);
    let bestScore = Infinity;
    for (let p = 0; p < 8; p++) {
      const pv2 = planes[p];
      const ptv = planesT[p];
      for (let i = 0; i < cand.v.length; i++) {
        cand.v[i] = m.v[i] ^ pv2[i];
        candT.v[i] = t.v[i] ^ ptv[i];
      }
      const score = penaltyScore(cand, candT, bestScore);
      if (score < bestScore) {
        bestScore = score;
        mask = p;
      }
    }
  }
  const pv = planes[mask];
  for (let i = 0; i < m.v.length; i++)
    m.v[i] ^= pv[i];
  if (!test)
    drawInfo(m, ver, ecc, mask);
  return m;
}
var asNum = (n, title) => {
  if (typeof n !== "number")
    throw new TypeError(`"${title}" expected number, got type=${typeof n}`);
  if (!Number.isSafeInteger(n))
    throw new RangeError(`"${title}" expected safe integer, got ${n}`);
  return n;
};
var asString = (s, title) => {
  if (typeof s !== "string")
    throw new TypeError(`"${title}" expected string, got type=${typeof s}`);
  return s;
};
function utf8Length(str) {
  let length = 0;
  for (let i = 0; i < str.length; i++) {
    const c = str.charCodeAt(i);
    if (c < 128)
      length++;
    else if (c < 2048)
      length += 2;
    else if (c < 55296 || c > 57343)
      length += 3;
    else if (c <= 56319 && i + 1 < str.length) {
      const next = str.charCodeAt(i + 1);
      if (next >= 56320 && next <= 57343) {
        length += 4;
        i++;
      } else
        length += 3;
    } else
      length += 3;
  }
  return length;
}
function byteCapacity(ver, ecc) {
  const lengthBits = LENGTH_BITS.byte[Math.floor((ver + 7) / 17)];
  return Math.min((1 << lengthBits) - 1, Math.floor((capacity(ver, ecc).capacity - 4 - lengthBits) / 8));
}
function _isBytes(a) {
  return a instanceof Uint8Array || ArrayBuffer.isView(a) && a.constructor.name === "Uint8Array" && "BYTES_PER_ELEMENT" in a && a.BYTES_PER_ELEMENT === 1;
}
var dark = (r, x, y) => r.map[x] >= 0 && r.map[y] >= 0 && matGet(r.m, r.map[x], r.map[y]) === 1;
var CTRL = [10, 27];
var NL = /* @__PURE__ */ String.fromCharCode(CTRL[0]);
function renderRaw(r) {
  const { m, W, map } = r;
  const res = new Array(W);
  const { words, v } = m;
  for (let y = 0; y < W; y++) {
    const my = map[y];
    const row = new Array(W);
    if (my < 0) {
      for (let x = 0; x < W; x++)
        row[x] = false;
    } else {
      const base = my * words;
      for (let x = 0; x < W; x++) {
        const mx = map[x];
        row[x] = mx >= 0 && (v[base + (mx >>> 5)] >>> (mx & 31) & 1) === 1;
      }
    }
    res[y] = row;
  }
  return res;
}
var GLYPH = ["\u2588", "\u2580", "\u2584", " "];
var QUAD = /* @__PURE__ */ (() => {
  const t = [];
  for (let i = 0; i < 256; i++)
    t.push(GLYPH[i >> 6] + GLYPH[i >> 4 & 3] + GLYPH[i >> 2 & 3] + GLYPH[i & 3]);
  return t;
})();
function renderAscii(r) {
  const { m, W, map } = r;
  const { words, v } = m;
  let out = "";
  for (let y = 0; y < W; y += 2) {
    const my0 = map[y];
    const my1 = y + 1 < W ? map[y + 1] : -2;
    const b0 = my0 * words;
    const b1 = my1 * words;
    let acc = 0;
    let n = 0;
    for (let x = 0; x < W; x++) {
      const mx = map[x];
      let g = my1 === -2 ? 1 : 0;
      if (mx >= 0) {
        if (my0 >= 0 && v[b0 + (mx >>> 5)] >>> (mx & 31) & 1)
          g |= 2;
        if (my1 >= 0 && v[b1 + (mx >>> 5)] >>> (mx & 31) & 1)
          g |= 1;
      }
      acc = acc << 2 | g;
      if (++n === 4) {
        out += QUAD[acc];
        acc = 0;
        n = 0;
      }
    }
    for (let i = 0; i < n; i++)
      out += GLYPH[acc >> 2 * (n - 1 - i) & 3];
    out += NL;
  }
  return out;
}
function renderTerm(r) {
  const W = r.W;
  const esc = String.fromCharCode(CTRL[1]);
  const reset = esc + "[0m";
  let out = "";
  for (let y = 0; y < W; y++) {
    for (let x = 0; x < W; x++) {
      out += dark(r, x, y) ? `${esc}[40m  ${reset}` : `${esc}[1;47m  ${reset}`;
    }
    out += NL;
  }
  return out;
}
var digits = (n) => n < 10 ? 1 : n < 100 ? 2 : n < 1e3 ? 3 : n < 1e4 ? 4 : String(n).length;
var chars = (d) => d < 0 ? 1 + digits(-d) : digits(d);
var svgCache;
function renderSvg(r, optimize) {
  const { m, W, map } = r;
  const { words, v } = m;
  if (svgCache === void 0 || svgCache.W !== W)
    svgCache = { W, cmds: new Array(4 * W) };
  const cmds = svgCache.cmds;
  let out = `<svg viewBox="0 0 ${W} ${W}" xmlns="http://www.w3.org/2000/svg">`;
  let pathData = "";
  let prevX = 0;
  let prevY = 0;
  let hasPrev = false;
  for (let y = 0; y < W; y++) {
    const my = map[y];
    if (my < 0)
      continue;
    const base = my * words;
    for (let x = 0; x < W; x++) {
      const mx = map[x];
      if (mx < 0 || !(v[base + (mx >>> 5)] >>> (mx & 31) & 1))
        continue;
      if (!optimize) {
        out += `<rect x="${x}" y="${y}" width="1" height="1" />`;
        continue;
      }
      const dx = x - prevX;
      const dy = y - prevY;
      let cmd;
      if (hasPrev && x >= 10 && dy <= 1 && chars(dx) + 1 <= digits(x) + digits(y)) {
        const k = (2 * dy + 1) * W + dx;
        cmd = cmds[k];
        if (cmd === void 0)
          cmd = cmds[k] = `m${dx} ${dy}h1v1h-1Z`;
      } else {
        let mv;
        if (hasPrev) {
          mv = chars(dx) + chars(dy) <= digits(x) + digits(y) ? `m${dx} ${dy}` : `M${x} ${y}`;
        } else
          mv = `M${x} ${y}`;
        cmd = `${mv}h1v1${x < 10 ? `H${x}` : "h-1"}Z`;
      }
      pathData += cmd;
      prevX = x;
      prevY = y;
      hasPrev = true;
    }
  }
  if (optimize)
    out += `<path d="${pathData}"/>`;
  return out + "</svg>";
}
function renderGif(r) {
  const W = r.W;
  const pixels = W * W;
  const N = 126;
  const fullChunks = Math.floor(pixels / N);
  const tail = pixels % N;
  const out = new Uint8Array(408 + fullChunks * (N + 2) + 2 + tail + 4);
  let p = 0;
  const u16 = (v2) => {
    out[p++] = v2 & 255;
    out[p++] = v2 >>> 8;
  };
  for (const b of [71, 73, 70, 56, 55, 97])
    out[p++] = b;
  u16(W);
  u16(W);
  out[p++] = 246;
  p += 2;
  out[p++] = 255;
  out[p++] = 255;
  out[p++] = 255;
  p += 3 * 127;
  out[p++] = 44;
  p += 4;
  u16(W);
  u16(W);
  out[p++] = 0;
  out[p++] = 7;
  const { m, map } = r;
  const { words, v } = m;
  const row = new Uint8Array(W);
  const src = out.length - 4 - pixels;
  let prevMy = -2;
  for (let y = 0, q = src; y < W; y++, q += W) {
    const my = map[y];
    if (my !== prevMy) {
      prevMy = my;
      row.fill(0);
      if (my >= 0) {
        const base = my * words;
        for (let x = 0; x < W; x++) {
          const mx = map[x];
          if (mx >= 0)
            row[x] = v[base + (mx >>> 5)] >>> (mx & 31) & 1;
        }
      }
    }
    out.set(row, q);
  }
  for (let i = 0, s = src; i < pixels; i += N, s += N) {
    const n = pixels - i < N ? pixels - i : N;
    out[p++] = n + 1;
    out[p++] = 128;
    out.copyWithin(p, s, s + n);
    p += n;
  }
  if (tail === 0) {
    out[p++] = 1;
    out[p++] = 128;
  }
  out[p++] = 1;
  out[p++] = 129;
  out[p++] = 0;
  out[p++] = 59;
  return out;
}
function gifDataUrl(gif) {
  const g = gif;
  let b64;
  if (typeof g.toBase64 === "function")
    b64 = g.toBase64();
  else {
    let bin = "";
    for (let i = 0; i < g.length; i += 8192)
      bin += String.fromCharCode(...g.subarray(i, i + 8192));
    b64 = btoa(bin);
  }
  return "data:image/gif;base64," + b64;
}
function encodeQR(text, output = "raw", opts = {}) {
  asString(text, "text");
  asString(output, "output");
  if (typeof opts !== "object" || opts === null || Array.isArray(opts))
    throw new TypeError(`"opts" expected object, got type=${typeof opts}`);
  let ver = opts.version;
  if (ver !== void 0)
    ver = asVersion(ver);
  const ecc = opts.ecc !== void 0 ? opts.ecc : "medium";
  if (!ECC_LEVELS.includes(ecc))
    err(`invalid ecc=${ecc}`);
  const encoding = opts.encoding !== void 0 ? opts.encoding : detectType(text);
  if (!LENGTH_BITS[encoding])
    err(`invalid encoding=${encoding}`);
  if (encoding !== "byte") {
    const limit = encoding === "numeric" ? 10 : ALPHANUMERIC.length;
    for (let i = 0; i < text.length; i++) {
      const v = ALNUM_VAL[text.charCodeAt(i)];
      if (!(v >= 0 && v < limit)) {
        const ch = String.fromCodePoint(text.codePointAt(i));
        err(`Unknown letter: "${ch}". Allowed: ${ALPHANUMERIC.slice(0, limit)}`);
      }
    }
  }
  if (opts.mask !== void 0 && (asNum(opts.mask, "opts.mask") < 0 || opts.mask > 7))
    err(`invalid mask=${opts.mask}`);
  const textEncoder = opts.textEncoder;
  if (encoding === "byte" && textEncoder === void 0) {
    const maxBytes = byteCapacity(ver === void 0 ? 40 : ver, ecc);
    if (text.length > maxBytes || utf8Length(text) > maxBytes)
      err("Capacity overflow");
  }
  const utf8 = encoding === "byte" ? (textEncoder !== void 0 ? textEncoder : (s) => new TextEncoder().encode(s))(text) : void 0;
  if (utf8 !== void 0 && !_isBytes(utf8))
    throw new TypeError(`"opts.textEncoder" expected Uint8Array, got type=${typeof utf8}`);
  const dataLen = encoding === "byte" ? utf8.length : text.length;
  const encodedBits = encoding === "numeric" ? Math.floor(dataLen / 3) * 10 + [0, 4, 7][dataLen % 3] : encoding === "alphanumeric" ? Math.floor(dataLen / 2) * 11 + dataLen % 2 * 6 : dataLen * 8;
  if (ver === void 0) {
    for (ver = 1; ver <= 40; ver++) {
      const lengthBits = LENGTH_BITS[encoding][Math.floor((ver + 7) / 17)];
      if (dataLen < 1 << lengthBits && 4 + lengthBits + encodedBits <= capacity(ver, ecc).capacity)
        break;
    }
    if (ver > 40)
      err("Capacity overflow");
  } else {
    const lengthBits = LENGTH_BITS[encoding][Math.floor((ver + 7) / 17)];
    if (dataLen >= 1 << lengthBits || 4 + lengthBits + encodedBits > capacity(ver, ecc).capacity)
      err("Capacity overflow");
  }
  const data = encodeData(ver, ecc, text, encoding, utf8);
  const m = drawSymbol(ver, ecc, data, opts.mask);
  const border = opts.border === void 0 ? 2 : asNum(opts.border, "opts.border");
  if (border <= 0)
    throw new RangeError(`invalid border=${border}`);
  const scale = opts.scale === void 0 ? 1 : asNum(opts.scale, "opts.scale");
  if (scale <= 0 || scale > 1024)
    throw new RangeError(`invalid scale factor: ${scale}`);
  const W = (m.size + 2 * border) * scale;
  const maxOutputSize = output === "ascii" || output === "gif" || output === "data-url" ? MAX_COMPACT_OUTPUT_SIZE : MAX_OUTPUT_SIZE;
  if (W > maxOutputSize)
    throw new RangeError(`invalid opts: output is ${W}x${W} (max ${maxOutputSize}), reduce border/scale`);
  const map = new Int32Array(W);
  for (let i = 0; i < W; i++) {
    const f = Math.floor(i / scale) - border;
    map[i] = f >= 0 && f < m.size ? f : -1;
  }
  const r = { m, W, map };
  if (output === "raw")
    return renderRaw(r);
  if (output === "ascii")
    return renderAscii(r);
  if (output === "term")
    return renderTerm(r);
  if (output === "svg")
    return renderSvg(r, opts.optimize === void 0 ? true : opts.optimize);
  if (output === "gif")
    return renderGif(r);
  if (output === "data-url")
    return gifDataUrl(renderGif(r));
  return err(`Unknown output: ${output}`);
}
var qr_default = encodeQR;
export {
  qr_default as encodeQR
};
