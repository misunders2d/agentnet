// Preview only bounded UTF-8 text. The caller renders the returned string
// as text or through the existing safe Markdown renderer, never raw HTML.
// HTML/SVG and binary formats keep Download only.
export const TEXT_PREVIEW_LIMIT = 256 * 1024;
export const textPreviewName = (name: string): boolean => /\.(txt|md|csv|json|log|yaml|yml|toml|ini|go|rs|py|js|ts|tsx|jsx|css|sh)$/i.test(name);

export function previewText(name: string, bytes: Uint8Array): string {
  if (!textPreviewName(name)) throw new Error("This file does not support text preview. Use Download.");
  if (bytes.byteLength > TEXT_PREVIEW_LIMIT) throw new Error("This text file is too large to preview. Use Download.");
  let text: string;
  try { text = new TextDecoder("utf-8", { fatal: true }).decode(bytes); }
  catch { throw new Error("This file is not UTF-8 text. Use Download."); }
  if (/[\x00-\x08\x0b\x0c\x0e-\x1f]/.test(text)) throw new Error("This file contains binary data. Use Download.");
  return text;
}
