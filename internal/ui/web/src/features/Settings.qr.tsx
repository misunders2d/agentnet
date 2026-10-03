// A QR code drawn as SVG elements by React (no markup strings): dark
// modules on white with the standard 4-module quiet zone, in both themes,
// because scanners need dark on light.
import { useMemo } from "react";
import encodeQR from "qr";

export function QrCode({ text, label, size = 200 }: { text: string; label: string; size?: number }) {
  const shape = useMemo(() => {
    let cells: boolean[][];
    try { cells = encodeQR(text, "raw", { ecc: "medium", border: 4 }); } catch { return null; }
    // One path of horizontal runs keeps the drawing small.
    let d = "";
    cells.forEach((row, y) => {
      for (let x = 0; x < row.length; x++) {
        if (!row[x]) continue;
        let w = 1;
        while (row[x + w]) w++;
        d += "M" + x + " " + y + "h" + w + "v1h-" + w + "z";
        x += w - 1;
      }
    });
    return { n: cells.length, d };
  }, [text]);
  if (!shape) return null;
  return (
    <span className="inline-block overflow-hidden rounded-xl bg-white stroke">
      <svg role="img" aria-label={label} viewBox={"0 0 " + shape.n + " " + shape.n} width={size} height={size} shapeRendering="crispEdges" className="block">
        <rect width={shape.n} height={shape.n} fill="#FFFFFF" />
        <path d={shape.d} fill="#1B1530" />
      </svg>
    </span>
  );
}
