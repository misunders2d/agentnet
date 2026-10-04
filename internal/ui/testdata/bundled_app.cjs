// The bundled app (static/default.html, app.css, lenses.js, app.js) as a
// page of its own, for the fixtures that exercise it. No interface loads
// it any more: the UI host mounts skin packages only (docs/UI_SKINS.md),
// and the bundled app's sources stay in the tree for these checks and for
// the Classic and Zoom packages built from them later.
'use strict';
const fs = require('node:fs'), path = require('node:path');
const markup = () => fs.readFileSync(path.join(__dirname, '..', 'static', 'default.html'), 'utf8');
/** bundledAppPage: the page at '/', with scripts (e.g. a fixture bootstrap) before the app's. */
exports.bundledAppPage = (title, scripts = []) =>
  '<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>' + title + '</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/assets/app.css">'
  + markup() + scripts.map((s) => '<script src="' + s + '"></script>').join('') + '<script src="/assets/lenses.js"></script><script src="/assets/app.js"></script>';
