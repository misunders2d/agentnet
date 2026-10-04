// Development proxy: serves a dev build of the Comic skin package (dev.sh
// OUT) at /assets/skins/comic/ and the host's own files as checked out, in
// front of a running daemon's page, forwarding everything else (the page,
// the catalog, the API, the event stream) to that daemon as if the browser
// were on its own origin. Loopback only; for development and journeys,
// never shipped.
//
//   node dev-proxy.mjs DAEMON_URL_WITH_TOKEN OUT [PORT]
// prints: proxy http://127.0.0.1:PORT/
import http from "node:http";
import fs from "node:fs";
import path from "node:path";

const [target, out, port = "0"] = process.argv.slice(2);
if (!target || !out) { console.error("usage: dev-proxy.mjs DAEMON_URL OUT [PORT]"); process.exit(2); }
const base = new URL(target);
const token = base.searchParams.get("t");
let cookie = "";
await new Promise((resolve, reject) => {
  http.get({ host: base.hostname, port: base.port, path: "/?t=" + token }, (r) => {
    cookie = (r.headers["set-cookie"] || []).map((c) => c.split(";")[0]).join("; ");
    r.resume(); r.on("end", resolve);
  }).on("error", reject);
});
const types = { ".mjs": "text/javascript; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".woff2": "font/woff2", ".json": "application/json" };
const comic = "/assets/skins/comic/";
const staticDir = path.join(path.dirname(new URL(import.meta.url).pathname), "..", "static");
// The host, as checked out (no binary rebuild).
const hostFiles = ["loader.js", "skinbar.mjs", "skinbar.css", "skin-choice.mjs", "core.css", "skin-base.css", "local-skins.mjs"];
const fileFor = (p) => {
  if (p.startsWith(comic) && p.length > comic.length) return path.join(path.resolve(out), p.slice(comic.length));
  if (p.startsWith("/assets/") && hostFiles.includes(p.slice("/assets/".length))) return path.join(staticDir, p.slice("/assets/".length));
  return null;
};
const server = http.createServer((req, res) => {
  const p = new URL(req.url, "http://x").pathname;
  const file = req.method === "GET" ? fileFor(p) : null;
  if (file) {
    if (!file.startsWith(path.resolve(out) + path.sep) && !file.startsWith(staticDir + path.sep)) { res.writeHead(404).end(); return; }
    fs.readFile(file, (err, data) => {
      if (err) { res.writeHead(404).end(); return; }
      res.writeHead(200, { "Content-Type": types[path.extname(file)] || "application/octet-stream", "Cache-Control": "no-store" }).end(data);
    });
    return;
  }
  const headers = { ...req.headers, host: base.host, cookie };
  if (headers.origin) headers.origin = base.origin;
  if (headers["sec-fetch-site"]) headers["sec-fetch-site"] = "same-origin";
  delete headers.referer;
  const up = http.request({ host: base.hostname, port: base.port, path: req.url, method: req.method, headers }, (r) => {
    const h = { ...r.headers };
    delete h["set-cookie"];
    res.writeHead(r.statusCode || 502, h);
    r.pipe(res);
  });
  up.on("error", () => { if (!res.headersSent) res.writeHead(502); res.end(); });
  req.pipe(up);
});
server.listen(Number(port), "127.0.0.1", () => console.log("proxy http://127.0.0.1:" + server.address().port + "/"));
