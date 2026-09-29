// The vendored QR encoder with a new device's link as long as a real one
// gets (static/vendor/qr.mjs, as the page loads it): it fits a code a
// phone reads off a screen, with the four-module quiet zone around it.
import { encodeQR } from "../static/vendor/qr.mjs";

const b64url = (s) => Buffer.from(s).toString("base64url");
const hex = (n) => "a1b2c3d4e5f60718".repeat(n / 16);
// The contract's offer (identity v2 §C'), each field at its full size.
const offer = {
  v: 2, invite: "agentnet-invite-v1:" + b64url(JSON.stringify({ hub: "https://agentnet.bezosapp.uk", label: "sergey", secret: hex(64), ttl: 600, person: hex(64) })),
  id: hex(32), expires: 1790700000, person: hex(64), seq: 7, hash: hex(64),
  approver: { address: "sergey/laptop-work", fingerprint: "SHA256:" + b64url(hex(32)).slice(0, 43) },
  secret: b64url(Buffer.alloc(32, 7)),
};
const link = "https://agentnet.bezosapp.uk/#agentnet-link-v2:" + b64url(JSON.stringify(offer));
let failed = 0;
const check = (ok, what) => { if (!ok) { failed++; console.error("FAIL: " + what); } };

const m = encodeQR(link, "raw", { ecc: "low", border: 4 });
const n = m.length, modules = n - 8, version = (modules - 17) / 4;
check(m.every((row) => row.length === n), "a square code");
check(Number.isInteger(version) && version <= 25, "a " + link.length + "-character link fits version 25 or less at low correction: version " + version);
const quiet = (i) => i < 4 || i >= n - 4;
check(m.every((row, y) => row.every((on, x) => !(on && (quiet(x) || quiet(y))))), "four clear modules around the code");
check(m[4][4] && m[4][n - 5] && m[n - 5][4], "finder patterns at three corners, inside the quiet zone");
// At the page's 320 CSS pixels, each module keeps at least 2.5 pixels.
check(320 / n >= 2.5, "modules large enough at the page's size: " + (320 / n).toFixed(2) + " px");
if (failed) process.exit(1);
console.log("qr ok: " + link.length + " characters, version " + version + ", " + n + " modules with the quiet zone");
