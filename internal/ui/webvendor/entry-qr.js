// The only part of qr (paulmillr/qr) the pages use: its encoder (the reader
// and DOM helpers are not bundled). encodeQR(text, "raw") gives the
// modules as rows of booleans.
export { default as encodeQR } from "qr";
