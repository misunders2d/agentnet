// Package static is the messenger's page bundle, embedded in the binary, and
// the one handler a relay needs to serve its browser page. It depends on
// nothing else in AgentNet, so a relay can serve it without the client.
package static

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed index.html app.js lenses.js app.css device.mjs engine.mjs wire.mjs vendor/age.mjs manifest.webmanifest sw.js
var files embed.FS

// Files is the bundle: the daemon's page (index.html and its assets) and the
// browser device the relay serves (the same page and views, started by
// device.mjs over engine.mjs, wire.mjs and vendor/age.mjs).
var Files fs.FS = files

// relayFiles are the bundle files Relay serves, and the file behind each; ""
// is the device page, made from index.html. With relayIcons, they are the
// only paths it serves.
var relayFiles = map[string]string{
	"/":                      "",
	"/manifest.webmanifest":  "manifest.webmanifest",
	"/sw.js":                 "sw.js", // the service worker: push and click only (its scope is the origin)
	"/assets/app.css":        "app.css",
	"/assets/app.js":         "app.js",
	"/assets/lenses.js":      "lenses.js",
	"/assets/device.mjs":     "device.mjs",
	"/assets/engine.mjs":     "engine.mjs",
	"/assets/wire.mjs":       "wire.mjs",
	"/assets/vendor/age.mjs": "vendor/age.mjs",
}

// relayIcons are the app icon's paths and sizes, named by the manifest.
var relayIcons = map[string]int{"/assets/icon-192.png": 192, "/assets/icon-512.png": 512}

// daemonScripts are the daemon page's views; on the relay, device.mjs loads
// them once the device is ready.
const daemonScripts = `<script src="/assets/lenses.js" defer></script>
<script src="/assets/app.js" defer></script>`

// pageStyle is where the device page adds its manifest and icon.
const pageStyle = `<link rel="stylesheet" href="/assets/app.css">`

// devicePage is index.html with its scripts replaced by device.mjs, and
// the manifest and icon that let a browser install it as an app.
func devicePage() []byte {
	page, err := fs.ReadFile(files, "index.html")
	if err != nil || !bytes.Contains(page, []byte(daemonScripts)) || !bytes.Contains(page, []byte(pageStyle)) {
		panic("static: index.html has no view scripts or style to replace") // a build error
	}
	page = bytes.Replace(page, []byte(daemonScripts), []byte(`<script type="module" src="/assets/device.mjs"></script>`), 1)
	return bytes.Replace(page, []byte(pageStyle), []byte(pageStyle+"\n"+
		`<link rel="manifest" href="/manifest.webmanifest">`+"\n"+`<link rel="icon" type="image/png" href="/assets/icon-192.png">`), 1)
}

// provisionalIcon draws the page's current mark (.brand-mark in app.css:
// two dots on an indigo tile) as a size×size PNG. It stands in for the app
// icon until a logo is chosen; it is not the logo.
func provisionalIcon(size int) []byte {
	const unit, samples = 30.0, 4 // the mark's CSS size; samples per pixel side
	scale := float64(size) / unit
	inTile := func(x, y float64) bool { // a 30-unit square with 9-unit corners
		dx := math.Max(math.Max(9-x, x-21), 0)
		dy := math.Max(math.Max(9-y, y-21), 0)
		return x >= 0 && y >= 0 && x <= unit && y <= unit && dx*dx+dy*dy <= 81
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var tile, white float64
			for s := 0; s < samples*samples; s++ {
				x := (float64(px) + (float64(s%samples)+.5)/samples) / scale
				y := (float64(py) + (float64(s/samples)+.5)/samples) / scale
				if !inTile(x, y) {
					continue
				}
				tile++
				if math.Hypot(x-11, y-11) <= 5 {
					white++
				} else if math.Hypot(x-19, y-19) <= 5 {
					white += .6
				}
			}
			if tile == 0 {
				continue
			}
			w := white / tile
			mix := func(c float64) uint8 { return uint8(c*(1-w) + 255*w + .5) }
			img.SetNRGBA(px, py, color.NRGBA{mix(0x4b), mix(0x45), mix(0xd6), uint8(255*tile/(samples*samples) + .5)})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// relayContent is every answer Relay gives: each path's bytes and type,
// made once.
var relayContent = sync.OnceValue(func() map[string][2]string {
	out := map[string][2]string{}
	for p, name := range relayFiles {
		data := devicePage()
		if name != "" {
			var err error
			if data, err = fs.ReadFile(files, name); err != nil {
				panic(err) // a bundle file the list names is missing: a build error
			}
		}
		out[p] = [2]string{string(data), contentType(name)}
	}
	for p, size := range relayIcons {
		out[p] = [2]string{string(provisionalIcon(size)), "image/png"}
	}
	return out
})

// relayCSP lets the page run only its own origin's files and talk only to
// its own origin.
const relayCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob:; connect-src 'self'; manifest-src 'self'; worker-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Relay serves the browser page and its files on a relay's origin, for GET
// and HEAD, and nothing else: any other path is not found, so it never
// answers for the relay's API or the daemon's page API. Every answer carries
// headers that keep the page to its own origin's code and out of frames and
// other windows. They do not protect against the relay itself: whoever runs
// it can serve other code (MESSENGER_ARCHITECTURE §6).
func Relay() http.Handler {
	content, etags := relayContent(), map[string]string{}
	for p, c := range content {
		sum := sha256.Sum256([]byte(c[0]))
		etags[p] = `"` + hex.EncodeToString(sum[:16]) + `"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", relayCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		c, ok := content[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.Set("Content-Type", c[1])
		h.Set("ETag", etags[r.URL.Path])
		if r.URL.Path == "/" {
			h.Set("Cache-Control", "no-store") // an updated relay serves its new page at once
		} else {
			h.Set("Cache-Control", "no-cache") // checked on every load (ETag)
		}
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(c[0]))
	})
}

func contentType(name string) string {
	switch {
	case name == "" || strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".webmanifest"):
		return "application/manifest+json"
	}
	return "text/javascript; charset=utf-8"
}
