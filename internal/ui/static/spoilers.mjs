// Presentation only. Keep this delimiter scan in step with client/spoilers.go.
// Runs of exactly two unescaped pipes pair from left to right, including
// across lines. Inline backticks and fenced-code blocks keep literal pipes.
export const hiddenText = "[Hidden text]";

export function spoilerRanges(text) {
  if (!text.includes("||")) return [];
  const runs = new Map();
  const next = new Map();
  let paragraph = text.length;
  // Find the next equal backtick run in each paragraph once. Looking ahead
  // separately from every unmatched run makes pasted code quadratic.
  const ticks = [...text.matchAll(/`+|\n[ \t]*\r?\n/g)];
  for (let i = ticks.length - 1; i >= 0; i--) {
    const {0: run, index: at} = ticks[i];
    if (run[0] === "\n") { paragraph = at; continue; }
    const later = runs.get(run.length);
    if (later < paragraph) next.set(at, later + run.length);
    runs.set(run.length, at);
  }
  const ranges = [];
  let open = -1, fence = "", fenceSize = 0;
  for (let i = 0; i < text.length;) {
    if (i === 0 || text[i - 1] === "\n") {
      const lineEnd = text.indexOf("\n", i);
      const end = lineEnd < 0 ? text.length : lineEnd;
      let p = i;
      while (p < end && p - i < 4 && text[p] === " ") p++;
      const ch = text[p];
      let q = p;
      if (p - i < 4 && (ch === "`" || ch === "~")) while (q < end && text[q] === ch) q++;
      if (fence) {
        if (ch === fence && q - p >= fenceSize && !text.slice(q, end).trim()) fence = "";
        i = end + 1;
        continue;
      }
      if (q - p >= 3 && (ch === "~" || !text.slice(q, end).includes("`"))) {
        fence = ch;
        fenceSize = q - p;
        i = end + 1;
        continue;
      }
    }
    const ch = text[i];
    if (ch === "\\") {
      // Skip an escaped first pipe and its adjacent pipe run together.
      if (text[++i] === "|") while (text[i + 1] === "|") i++;
      i++;
    } else if (ch === "`") {
      const end = next.get(i);
      if (end !== undefined) i = end;
      else while (text[++i] === "`") {}
    } else if (ch === "|") {
      let end = i + 1;
      while (text[end] === "|") end++;
      if (end - i === 2) {
        if (open < 0) open = i;
        else { ranges.push({start: open, end}); open = -1; }
      }
      i = end;
    } else i++;
  }
  return ranges;
}

export function replaceSpoilers(text, replacement) {
  const ranges = spoilerRanges(text);
  if (!ranges.length) return text;
  let result = "", at = 0;
  for (const range of ranges) {
    result += text.slice(at, range.start) + replacement(text.slice(range.start + 2, range.end - 2));
    at = range.end;
  }
  return result + text.slice(at);
}

export const maskSpoilers = text => replaceSpoilers(text, () => hiddenText);
