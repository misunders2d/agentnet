// Package static is the messenger's page bundle, embedded in the binary, and
// the one handler a relay needs to serve its browser page. It depends on
// nothing else in AgentNet, so a relay can serve it without the client.
package static

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"maps"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed android.js pictures.mjs index.html loader.js core.css skin-base.css skinbar.mjs skinbar.css skin-choice.mjs skins device.mjs engine.mjs devicehistory.mjs historyarchive.mjs historywindow.mjs historycontribution.mjs wire.mjs optimistic.mjs vendor/age.mjs vendor/qr.mjs vendor/idb.mjs vendor/sse.mjs manifest.webmanifest sw.js workspaces-sw.js ant.png drivespace.mjs drivespace.css drivespace-setup.mjs assistant-setup.mjs assistant-setup.css typing.mjs local-skins.mjs teams.mjs workspaces.mjs workspaces.css setup.mjs getapp.json getapp.mjs landing.mjs landing.css google-signin.mjs
var files embed.FS

// Files is the bundle: the daemon's page (index.html and its assets) and the
// browser device the relay serves (the same page and views, started by
// device.mjs over engine.mjs, wire.mjs and vendor/age.mjs, idb.mjs, sse.mjs).
var Files fs.FS = files

// relayFiles are the bundle files Relay serves, and the file behind each; ""
// is the device page, made from index.html. With relayIcons, they are the
// only paths it serves.
var relayFiles = map[string]string{
	"/assets/pictures.mjs":            "pictures.mjs",
	"/assets/google-signin.mjs":       "google-signin.mjs",
	"/":                               "",
	"/manifest.webmanifest":           "manifest.webmanifest",
	"/sw.js":                          "sw.js",            // push/click and explicitly selected local-package assets (origin scope)
	"/workspaces-sw.js":               "workspaces-sw.js", // the workspace switcher's worker: registered per local workspace id (workspaces.mjs)
	"/assets/core.css":                "core.css",
	"/assets/loader.js":               "loader.js",
	"/assets/skin-base.css":           "skin-base.css", // the host's base sheet in every skin's shadow tree
	"/assets/skinbar.mjs":             "skinbar.mjs",   // the switcher over every skin but the default
	"/assets/skinbar.css":             "skinbar.css",
	"/assets/skin-choice.mjs":         "skin-choice.mjs", // which skin opens and its trust (loader.js, local-skins.mjs)
	"/assets/device.mjs":              "device.mjs",
	"/assets/optimistic.mjs":          "optimistic.mjs",
	"/assets/engine.mjs":              "engine.mjs",
	"/assets/devicehistory.mjs":       "devicehistory.mjs",
	"/assets/historyarchive.mjs":      "historyarchive.mjs",
	"/assets/historywindow.mjs":       "historywindow.mjs",
	"/assets/historycontribution.mjs": "historycontribution.mjs",
	"/assets/wire.mjs":                "wire.mjs",
	"/assets/vendor/age.mjs":          "vendor/age.mjs",
	"/assets/vendor/qr.mjs":           "vendor/qr.mjs",  // loaded only to show a new device's link
	"/assets/vendor/idb.mjs":          "vendor/idb.mjs", // idb: the device store's IndexedDB plumbing
	"/assets/vendor/sse.mjs":          "vendor/sse.mjs", // eventsource-parser: the signed stream's framing
	"/assets/drivespace.mjs":          "drivespace.mjs",
	"/assets/drivespace.css":          "drivespace.css",
	"/assets/drivespace-setup.mjs":    "drivespace-setup.mjs",
	"/assets/assistant-setup.mjs":     "assistant-setup.mjs",
	"/assets/assistant-setup.css":     "assistant-setup.css",
	"/assets/local-skins.mjs":         "local-skins.mjs",
	"/assets/typing.mjs":              "typing.mjs",
	"/assets/teams.mjs":               "teams.mjs",
	"/assets/workspaces.mjs":          "workspaces.mjs",
	"/assets/workspaces.css":          "workspaces.css",
	"/assets/landing.mjs":             "landing.mjs", // "Get AgentNet" for a device that has not joined
	"/assets/landing.css":             "landing.css",
	"/assets/getapp.mjs":              "getapp.mjs", // the app's downloads, from getapp.json (shared with Go)
	"/assets/getapp.json":             "getapp.json",
}

// relayIcons are the app icon's paths and sizes, named by the manifest.
var relayIcons = map[string]int{"/assets/icon-192.png": 192, "/assets/icon-512.png": 512}

// daemonScripts are the daemon page's views; on the relay, device.mjs loads
// them once the device is ready.
const daemonScripts = `<script src="/assets/loader.js" defer></script>`

// pageStyle is the shared page's core stylesheet marker.
const pageStyle = `<link rel="stylesheet" href="/assets/core.css">`

// devicePage keeps index.html's shared install metadata and replaces only
// its daemon scripts with the browser enrollment engine.
func devicePage() []byte {
	page, err := fs.ReadFile(files, "index.html")
	if err != nil || !bytes.Contains(page, []byte(daemonScripts)) || !bytes.Contains(page, []byte(pageStyle)) {
		panic("static: index.html has no view scripts or style to replace") // a build error
	}
	return bytes.Replace(page, []byte(daemonScripts), []byte(`<script type="module" src="/assets/device.mjs"></script>`), 1)
}

// SetupPage is the AgentNet app's first-run page (setup.mjs): index.html's
// metadata and core style, with its views replaced by the setup script.
func SetupPage() []byte {
	page, err := fs.ReadFile(files, "index.html")
	if err != nil || !bytes.Contains(page, []byte(daemonScripts)) {
		panic("static: index.html has no view scripts to replace") // a build error
	}
	return bytes.Replace(page, []byte(daemonScripts), []byte(`<script type="module" src="/assets/setup.mjs"></script>`), 1)
}

// The app icon is the logo (ant.png: a dark teal ant, the owner's choice)
// on a light rounded tile, so it reads on dark and light tabs alike. The
// ant is fitted into the middle 70% of the tile, clear of the edges.
const (
	iconTileR, iconTileG, iconTileB = 0xf2, 0xf7, 0xf6 // light, a trace of the ant's teal
	iconCorner                      = .22              // the tile's corner radius, of its size
	iconAnt                         = .70              // the ant's longer side, of the tile's size
)

// AppIcon is the app icon as a size×size PNG (the favicon, the header's
// mark and the installed app's icon), made once per size.
func AppIcon(size int) []byte {
	iconsMu.Lock()
	defer iconsMu.Unlock()
	if b, ok := icons[size]; ok {
		return b
	}
	b := drawIcon(size)
	icons[size] = b
	return b
}

var (
	iconsMu sync.Mutex
	icons   = map[int][]byte{}
)

func drawIcon(size int) []byte {
	src := ant()
	box := opaqueBounds(src)
	n := float64(size)
	// The ant's box, scaled to fit iconAnt of the tile and centred.
	scale := iconAnt * n / float64(max(box.Dx(), box.Dy()))
	ox := (n - float64(box.Dx())*scale) / 2
	oy := (n - float64(box.Dy())*scale) / 2
	r := iconCorner * n
	inTile := func(x, y float64) bool {
		dx := math.Max(math.Max(r-x, x-(n-r)), 0)
		dy := math.Max(math.Max(r-y, y-(n-r)), 0)
		return dx*dx+dy*dy <= r*r
	}
	const samples = 4 // per pixel side, for the tile's rounded edge
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var cover float64
			for s := 0; s < samples*samples; s++ {
				if inTile(float64(px)+(float64(s%samples)+.5)/samples, float64(py)+(float64(s/samples)+.5)/samples) {
					cover++
				}
			}
			if cover == 0 {
				continue
			}
			// The ant over this pixel: the source area it covers, averaged
			// (premultiplied), then laid over the tile.
			x0, y0 := float64(box.Min.X)+(float64(px)-ox)/scale, float64(box.Min.Y)+(float64(py)-oy)/scale
			ar, ag, ab, aa := area(src, x0, y0, x0+1/scale, y0+1/scale)
			mix := func(tile float64, c float64) uint8 { return uint8(tile*(1-aa) + c + .5) }
			img.SetNRGBA(px, py, color.NRGBA{mix(iconTileR, ar), mix(iconTileG, ag), mix(iconTileB, ab), uint8(255*cover/(samples*samples) + .5)})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// ant is the logo, decoded once.
var ant = sync.OnceValue(func() *image.NRGBA {
	data, err := fs.ReadFile(files, "ant.png")
	if err != nil {
		panic(err) // a build error
	}
	m, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		panic(err)
	}
	out := image.NewNRGBA(m.Bounds())
	for y := m.Bounds().Min.Y; y < m.Bounds().Max.Y; y++ {
		for x := m.Bounds().Min.X; x < m.Bounds().Max.X; x++ {
			out.Set(x, y, m.At(x, y))
		}
	}
	return out
})

// opaqueBounds is the smallest rectangle holding every visible pixel.
func opaqueBounds(m *image.NRGBA) image.Rectangle {
	b := image.Rectangle{Min: m.Bounds().Max, Max: m.Bounds().Min}
	for y := m.Bounds().Min.Y; y < m.Bounds().Max.Y; y++ {
		for x := m.Bounds().Min.X; x < m.Bounds().Max.X; x++ {
			if m.NRGBAAt(x, y).A > 8 {
				b = b.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return b
}

// area averages m over [x0,x1)×[y0,y1) in its pixels, each weighted by how
// much of it the area covers: premultiplied colour (0-255) and alpha (0-1).
// Outside the image counts as clear.
func area(m *image.NRGBA, x0, y0, x1, y1 float64) (r, g, b, a float64) {
	var w float64
	for y := int(math.Floor(y0)); float64(y) < y1; y++ {
		wy := math.Min(y1, float64(y+1)) - math.Max(y0, float64(y))
		for x := int(math.Floor(x0)); float64(x) < x1; x++ {
			wx := math.Min(x1, float64(x+1)) - math.Max(x0, float64(x))
			w += wx * wy
			if !(image.Point{x, y}.In(m.Bounds())) {
				continue
			}
			c := m.NRGBAAt(x, y)
			al := float64(c.A) / 255 * wx * wy
			r += float64(c.R) * al
			g += float64(c.G) * al
			b += float64(c.B) * al
			a += al
		}
	}
	if w == 0 {
		return 0, 0, 0, 0
	}
	return r / w, g / w, b / w, a / w
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
		out[p] = [2]string{string(AppIcon(size)), "image/png"}
	}
	return out
})

// relayCSP lets the page run only its own origin's files and talk only to
// its own origin.
const relayCSP = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' blob:; connect-src 'self'; manifest-src 'self'; worker-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Relay serves the browser page and its files on a relay's origin, for GET
// and HEAD, and nothing else: any other path is not found, so it never
// answers for the relay's API or the daemon's page API. Every answer carries
// headers that keep the page to its own origin's code and out of frames and
// other windows. They do not protect against the relay itself: whoever runs
// it can serve other code (MESSENGER_ARCHITECTURE §6).
func Relay(skinsDirectory ...string) http.Handler { return RelayBuild("", skinsDirectory...) }

// buildStamp is the line of engine.mjs a relay writes its version into.
const buildStamp = `export const BUILD = "";`

// RelayBuild is Relay for a relay running AgentNet version build: the
// engine it serves says build on its requests (engine.mjs BUILD), so the
// relay knows which code a browser device runs. "" leaves it unsaid.
func RelayBuild(build string, skinsDirectory ...string) http.Handler {
	dir := ""
	if len(skinsDirectory) > 0 {
		dir = skinsDirectory[0]
	}
	skins := Skins(dir)
	content, etags := maps.Clone(relayContent()), map[string]string{}
	if build != "" {
		engine := content["/assets/engine.mjs"]
		quoted, _ := json.Marshal(build)
		stamped := strings.Replace(engine[0], buildStamp, `export const BUILD = `+string(quoted)+`;`, 1)
		if stamped == engine[0] {
			panic("static: engine.mjs has no build stamp to write") // a build error
		}
		content["/assets/engine.mjs"] = [2]string{stamped, engine[1]}
	}
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
		if strings.HasPrefix(r.URL.Path, "/assets/skins/") {
			skins.ServeHTTP(w, r)
			return
		}
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

// ContentType is the type a bundle file is served with.
func ContentType(name string) string { return contentType(name) }

func contentType(name string) string {
	switch {
	case name == "" || strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".webmanifest"):
		return "application/manifest+json"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	}
	return "text/javascript; charset=utf-8"
}
