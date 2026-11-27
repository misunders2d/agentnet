// Telegram's grouped media, ported rule for rule (MEL-856).
//
// groupRanges is GroupRanges from Telegram Desktop
// Telegram/SourceFiles/ui/chat/attach/attach_prepare.cpp: consecutive files of
// one kind form a group, a new group starts when the kind changes or after
// kMaxAlbumCount items, and a run of one stays a single file.
//
// layoutMediaGroup is LayoutMediaGroup from Telegram/SourceFiles/ui/
// grouped_layout.cpp, with the album sizes of history_view_media_grouped.cpp
// (historyGroupWidthMax = maxMediaSize 430, historyGroupWidthMin =
// minPhotoSize 100, historyGroupSkip 4). Revision
// fca0d08b6852df2195e11db91f7a4e3be2cbc4a4. Integer arithmetic stays
// integer where the C++ uses int, and Round is base::SafeRound.
// testdata/grouped_layout_vectors.json holds the geometry the unmodified C++
// produced; testdata/grouped_layout_check.mjs compares this port with it.

export type GroupKind = "media" | "music" | "file";
export type GroupRange = { from: number; till: number; kind: GroupKind; album: boolean };

export const MAX_ALBUM = 10; // kMaxAlbumCount
export const ALBUM_WIDTH = 430; // st::historyGroupWidthMax (maxMediaSize)
export const ALBUM_MIN_WIDTH = 100; // st::historyGroupWidthMin (minPhotoSize)
export const ALBUM_SPACING = 4; // st::historyGroupSkip

export function groupRanges(kinds: readonly GroupKind[]): GroupRange[] {
  const result: GroupRange[] = [];
  if (!kinds.length) return result;
  let from = 0;
  let kind = kinds[0];
  for (let i = 0; i < kinds.length; i++) {
    const count = i - from;
    if ((i > from && kind !== kinds[i]) || count === MAX_ALBUM) {
      result.push({ from, till: i, kind, album: count > 1 });
      from = i;
    }
    kind = kinds[i];
  }
  result.push({ from, till: kinds.length, kind, album: kinds.length - from > 1 });
  return result;
}

// RectPart bits as in ui/rect_part.h.
export const Side = { Top: 2, Left: 8, Right: 16, Bottom: 64 } as const;
export type Tile = { x: number; y: number; width: number; height: number; sides: number };
type Size = { width: number; height: number };

const round = (v: number) => Math.round(v); // base::SafeRound on the values used here
const idiv = (a: number, b: number) => Math.trunc(a / b); // C++ int / int
const clamp = (v: number, lo: number, hi: number) => Math.min(Math.max(v, lo), hi);

export function layoutMediaGroup(sizes: readonly Size[], maxWidth = ALBUM_WIDTH, minWidth = ALBUM_MIN_WIDTH, spacing = ALBUM_SPACING): Tile[] {
  const count = sizes.length;
  if (!count) return [];
  const ratios = sizes.map(s => s.width / s.height);
  const proportions = ratios.map(r => r > 1.2 ? "w" : r < 0.8 ? "n" : "q").join("");
  const maxHeight = maxWidth; // "All apps currently use square max size first."
  const averageRatio = ratios.reduce((a, r) => a + r, 1) / count; // accumulate starts at 1.
  const maxSizeRatio = maxWidth / maxHeight;
  const { Top, Left, Right, Bottom } = Side;
  const tile = (x: number, y: number, width: number, height: number, sides: number): Tile => ({ x, y, width, height, sides });

  if (count === 1) {
    const width = maxWidth;
    return [tile(0, 0, width, idiv(sizes[0].height * width, sizes[0].width), Left | Top | Right | Bottom)];
  }
  if (count >= 5 || ratios.some(r => r > 2)) return complexLayout(ratios, averageRatio, maxWidth, minWidth, spacing);

  if (count === 2) {
    if (proportions === "ww" && averageRatio > 1.4 * maxSizeRatio && ratios[1] - ratios[0] < 0.2) {
      const width = maxWidth;
      const height = round(Math.min(width / ratios[0], Math.min(width / ratios[1], (maxHeight - spacing) / 2)));
      return [tile(0, 0, width, height, Left | Top | Right), tile(0, height + spacing, width, height, Left | Bottom | Right)];
    }
    if (proportions === "ww" || proportions === "qq") {
      const width = idiv(maxWidth - spacing, 2);
      const height = round(Math.min(width / ratios[0], Math.min(width / ratios[1], maxHeight)));
      return [tile(0, 0, width, height, Top | Left | Bottom), tile(width + spacing, 0, width, height, Top | Right | Bottom)];
    }
    const minimalWidth = round(minWidth * 1.5);
    const secondWidth = Math.min(
      round(Math.max(0.4 * (maxWidth - spacing), (maxWidth - spacing) / ratios[0] / (1 / ratios[0] + 1 / ratios[1]))),
      maxWidth - spacing - minimalWidth);
    const firstWidth = maxWidth - secondWidth - spacing;
    const height = Math.min(maxHeight, round(Math.min(firstWidth / ratios[0], secondWidth / ratios[1])));
    return [tile(0, 0, firstWidth, height, Top | Left | Bottom), tile(firstWidth + spacing, 0, secondWidth, height, Top | Right | Bottom)];
  }

  if (count === 3) {
    if (proportions[0] === "n") {
      const firstHeight = maxHeight;
      const thirdHeight = round(Math.min((maxHeight - spacing) / 2, ratios[1] * (maxWidth - spacing) / (ratios[2] + ratios[1])));
      const secondHeight = firstHeight - thirdHeight - spacing;
      const rightWidth = Math.max(minWidth, round(Math.min((maxWidth - spacing) / 2, Math.min(thirdHeight * ratios[2], secondHeight * ratios[1]))));
      const leftWidth = Math.min(round(firstHeight * ratios[0]), maxWidth - spacing - rightWidth);
      return [
        tile(0, 0, leftWidth, firstHeight, Top | Left | Bottom),
        tile(leftWidth + spacing, 0, rightWidth, secondHeight, Top | Right),
        tile(leftWidth + spacing, secondHeight + spacing, rightWidth, thirdHeight, Bottom | Right),
      ];
    }
    const firstWidth = maxWidth;
    const firstHeight = round(Math.min(firstWidth / ratios[0], (maxHeight - spacing) * 0.66));
    const secondWidth = idiv(maxWidth - spacing, 2);
    const secondHeight = Math.min(maxHeight - firstHeight - spacing, round(Math.min(secondWidth / ratios[1], secondWidth / ratios[2])));
    const thirdWidth = firstWidth - secondWidth - spacing;
    return [
      tile(0, 0, firstWidth, firstHeight, Left | Top | Right),
      tile(0, firstHeight + spacing, secondWidth, secondHeight, Bottom | Left),
      tile(secondWidth + spacing, firstHeight + spacing, thirdWidth, secondHeight, Bottom | Right),
    ];
  }

  // Four.
  if (proportions[0] === "w") {
    const w = maxWidth;
    const h0 = round(Math.min(w / ratios[0], (maxHeight - spacing) * 0.66));
    const h = round((maxWidth - 2 * spacing) / (ratios[1] + ratios[2] + ratios[3]));
    const w0 = Math.max(minWidth, round(Math.min((maxWidth - 2 * spacing) * 0.4, h * ratios[1])));
    const w2 = round(Math.max(Math.max(minWidth, (maxWidth - 2 * spacing) * 0.33), h * ratios[3]));
    const w1 = w - w0 - w2 - 2 * spacing;
    const h1 = Math.min(maxHeight - h0 - spacing, h);
    return [
      tile(0, 0, w, h0, Left | Top | Right),
      tile(0, h0 + spacing, w0, h1, Bottom | Left),
      tile(w0 + spacing, h0 + spacing, w1, h1, Bottom),
      tile(w0 + spacing + w1 + spacing, h0 + spacing, w2, h1, Right | Bottom),
    ];
  }
  const h = maxHeight;
  const w0 = round(Math.min(h * ratios[0], (maxWidth - spacing) * 0.6));
  const w = round((maxHeight - 2 * spacing) / (1 / ratios[1] + 1 / ratios[2] + 1 / ratios[3]));
  const h0 = round(w / ratios[1]);
  const h1 = round(w / ratios[2]);
  const h2 = h - h0 - h1 - 2 * spacing;
  const w1 = Math.max(minWidth, Math.min(maxWidth - w0 - spacing, w));
  return [
    tile(0, 0, w0, h, Top | Left | Bottom),
    tile(w0 + spacing, 0, w1, h0, Top | Right),
    tile(w0 + spacing, h0 + spacing, w1, h1, Right),
    tile(w0 + spacing, h0 + h1 + 2 * spacing, w1, h2, Bottom | Right),
  ];
}

// ComplexLayouter: rows of at most three (four in the middle row of a
// portrait-heavy album), the attempt closest to a 3:4 block wins.
function complexLayout(rawRatios: readonly number[], averageRatio: number, maxWidth: number, minWidth: number, spacing: number): Tile[] {
  const ratios = rawRatios.map(r => averageRatio > 1.1 ? clamp(r, 1, 2.75) : clamp(r, 0.6667, 1));
  const count = ratios.length;
  const maxHeight = idiv(maxWidth * 4, 3);
  const multiHeight = (offset: number, n: number) => (maxWidth - (n - 1) * spacing) / ratios.slice(offset, offset + n).reduce((a, r) => a + r, 0);
  const attempts: { counts: number[]; heights: number[] }[] = [];
  const push = (counts: number[]) => {
    let offset = 0;
    const heights = counts.map(n => { const h = multiHeight(offset, n); offset += n; return h; });
    attempts.push({ counts, heights });
  };
  for (let first = 1; first !== count; first++) {
    const second = count - first;
    if (first > 3 || second > 3) continue;
    push([first, second]);
  }
  for (let first = 1; first !== count - 1; first++) {
    for (let second = 1; second !== count - first; second++) {
      const third = count - first - second;
      if (first > 3 || second > (averageRatio < 0.85 ? 4 : 3) || third > 3) continue;
      push([first, second, third]);
    }
  }
  for (let first = 1; first !== count - 1; first++) {
    for (let second = 1; second !== count - first; second++) {
      for (let third = 1; third !== count - first - second; third++) {
        const fourth = count - first - second - third;
        if (first > 3 || second > 3 || third > 3 || fourth > 3) continue;
        push([first, second, third, fourth]);
      }
    }
  }
  let optimal = attempts[0], optimalDiff = 0;
  attempts.forEach((attempt, n) => {
    const lines = attempt.counts.length;
    const total = attempt.heights.reduce((a, h) => a + h, 0) + spacing * (lines - 1);
    const bad1 = Math.min(...attempt.heights) < minWidth ? 1.5 : 1;
    let bad2 = 1;
    for (let line = 1; line !== lines; line++) if (attempt.counts[line - 1] > attempt.counts[line]) { bad2 = 1.5; break; }
    const diff = Math.abs(total - maxHeight) * bad1 * bad2;
    if (n === 0 || diff < optimalDiff) { optimal = attempt; optimalDiff = diff; }
  });
  const result: Tile[] = [];
  const { Top, Left, Right, Bottom } = Side;
  const rows = optimal.counts.length;
  let index = 0, y = 0;
  for (let row = 0; row !== rows; row++) {
    const cols = optimal.counts[row];
    const lineHeight = optimal.heights[row];
    const height = round(lineHeight);
    let x = 0;
    for (let col = 0; col !== cols; col++) {
      const sides = (row === 0 ? Top : 0) | (row === rows - 1 ? Bottom : 0) | (col === 0 ? Left : 0) | (col === cols - 1 ? Right : 0);
      const width = col === cols - 1 ? maxWidth - x : round(ratios[index] * lineHeight);
      result.push({ x, y: Math.trunc(y), width, height, sides }); // QRect takes the double y as int
      x += width + spacing;
      index++;
    }
    y += height + spacing;
  }
  return result;
}
