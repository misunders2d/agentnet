package static

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
)

// A skin is a complete interface package: a manifest (skin.json), its entry
// module and every file it uses. AgentNet is the core (the daemon and the
// documented skin contract, docs/UI_SKINS.md); every interface is a skin
// on top of it, loaded the same way. The built-in ones are embedded in this
// program (skins/<id>/); installed ones are explicitly installed, trusted
// UI code, not sandboxed plugins. Installed packages are snapshotted on
// server start: replacing files cannot swap code underneath a user's
// selection. Restart to install or update a package.
type Skin struct {
	API      int      `json:"api"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Entry    string   `json:"entry,omitempty"`
	Style    string   `json:"style,omitempty"`
	Document string   `json:"document,omitempty"` // document-level rules (@font-face, @property), adopted by the host
	Files    []string `json:"files,omitempty"`
	Digest   string   `json:"digest,omitempty"`
}
type skinAsset struct {
	data       []byte
	mime, etag string
}

// BuiltinSkins are the packages embedded in this program, in the order the
// catalog offers them; the first is the default. Their trust comes from
// this fixed list (and loader.js's copy of it), never from a manifest.
var BuiltinSkins = []string{"comic"}

// ReservedSkinIDs are the ids no installed or browser-local package may
// use: the built-in skins, those to come, and the old name of the default.
var ReservedSkinIDs = []string{"comic", "classic", "zoom", "default"}

var skinID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var skinTypes = map[string]string{
	".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json", ".png": "image/png",
	".webp": "image/webp", ".svg": "image/svg+xml", ".woff2": "font/woff2",
}

// Skins serves the built-in packages and the files declared by valid
// installed packages under directory, at /assets/skins/<id>/, with the
// catalog at /assets/skins/index.json. Missing or unsafe installed packages
// are omitted; the built-in ones are always there. The caller must put this
// handler behind its normal origin/authentication gate.
func Skins(directory string) http.Handler {
	var catalog []Skin
	assets := map[string]skinAsset{}
	add := func(skin Skin, files map[string]skinAsset) {
		catalog = append(catalog, skin)
		for name, a := range files {
			assets["/assets/skins/"+skin.ID+"/"+name] = a
		}
	}
	for _, id := range BuiltinSkins {
		skin, files, err := builtinSkin(id)
		if err != nil {
			panic("static: built-in skin " + id + ": " + err.Error()) // a build error
		}
		add(skin, files)
	}
	totalBytes := 0
	if directory != "" {
		root, err := os.OpenRoot(directory)
		if err == nil {
			defer root.Close()
			info, e := root.Stat(".")
			if e == nil && skinOwned(info) {
				entries, _ := fs.ReadDir(root.FS(), ".")
				installed := 0
				for _, entry := range entries {
					if installed >= 32 {
						break
					}
					if !entry.IsDir() || slices.Contains(ReservedSkinIDs, entry.Name()) || !skinID.MatchString(entry.Name()) {
						continue
					}
					dir, err := root.OpenRoot(entry.Name())
					if err != nil {
						continue
					}
					skin, files, err := readSkin(dir, entry.Name())
					dir.Close()
					if err != nil {
						continue
					}
					size := 0
					for _, a := range files {
						size += len(a.data)
					}
					if totalBytes+size > 64<<20 {
						continue
					}
					totalBytes += size
					installed++
					add(skin, files)
				}
			}
		}
	}
	index, _ := json.Marshal(catalog)
	assets["/assets/skins/index.json"] = makeSkinAsset(index, "application/json")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a, ok := assets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", a.mime)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", a.etag)
		if r.Header.Get("If-None-Match") == a.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodGet {
			w.Write(a.data)
		}
	})
}
func makeSkinAsset(data []byte, mime string) skinAsset {
	sum := sha256.Sum256(data)
	return skinAsset{data: data, mime: mime, etag: `"` + hex.EncodeToString(sum[:]) + `"`}
}

// builtinSkin reads an embedded package (skins/<id>/) through the same
// validation as an installed one.
func builtinSkin(id string) (Skin, map[string]skinAsset, error) {
	pkg, err := fs.Sub(files, "skins/"+id)
	if err != nil {
		return Skin{}, nil, err
	}
	return parseSkin(func(name string, limit int64) ([]byte, error) {
		if !fs.ValidPath(name) || strings.ContainsAny(name, `\:%`) {
			return nil, errors.New("invalid path")
		}
		b, err := fs.ReadFile(pkg, name)
		if err == nil && int64(len(b)) > limit {
			return nil, errors.New("file too large")
		}
		return b, err
	}, id)
}

// readSkin reads an installed package from root: every path component must
// belong to the serving user, be unwritable by others and not be a link.
func readSkin(root *os.Root, id string) (Skin, map[string]skinAsset, error) {
	info, err := root.Stat(".")
	if err != nil || !skinOwned(info) {
		return Skin{}, nil, errors.New("unsafe directory")
	}
	return parseSkin(func(name string, limit int64) ([]byte, error) {
		if !fs.ValidPath(name) || strings.ContainsAny(name, `\:%`) {
			return nil, errors.New("invalid path")
		}
		// Reject links and writable intermediate directories as well as files.
		parts := strings.Split(name, "/")
		for i := range parts {
			info, e := root.Lstat(strings.Join(parts[:i+1], "/"))
			if e != nil || info.Mode()&os.ModeSymlink != 0 || !skinOwned(info) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
				return nil, errors.New("unsafe file")
			}
		}
		f, e := root.Open(name)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || !skinOwned(info) || info.Size() > limit {
			return nil, errors.New("invalid file")
		}
		b, e := io.ReadAll(io.LimitReader(f, limit+1))
		if int64(len(b)) > limit {
			return nil, errors.New("file too large")
		}
		return b, e
	}, id)
}

// parseSkin validates a package's manifest and files, read through read,
// and computes its consent digest: the manifest's bytes, then each declared
// file's name and SHA-256 (local-skins.mjs computes the same).
func parseSkin(read func(name string, limit int64) ([]byte, error), id string) (Skin, map[string]skinAsset, error) {
	var m Skin
	raw, err := read("skin.json", 16<<10)
	if err != nil {
		return m, nil, err
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return m, nil, err
	}
	m.Digest = "" // computed here, never taken from the manifest
	css := func(name string) bool {
		return name == "" || (slices.Contains(m.Files, name) && path.Ext(name) == ".css")
	}
	if m.API != 1 || m.ID != id || strings.TrimSpace(m.Name) == "" || len(m.Name) > 80 || len(m.Files) == 0 || len(m.Files) > 32 ||
		!slices.Contains(m.Files, m.Entry) || (path.Ext(m.Entry) != ".js" && path.Ext(m.Entry) != ".mjs") || !css(m.Style) || !css(m.Document) {
		return m, nil, errors.New("invalid manifest")
	}
	files := map[string]skinAsset{}
	hash := sha256.New()
	hash.Write(raw)
	total := 0
	seen := map[string]bool{}
	for _, name := range m.Files {
		mime := skinTypes[path.Ext(name)]
		if mime == "" || name == "skin.json" || seen[name] {
			return m, nil, errors.New("unsupported file")
		}
		seen[name] = true
		data, e := read(name, 4<<20)
		if e != nil {
			return m, nil, e
		}
		total += len(data)
		if total > 16<<20 {
			return m, nil, errors.New("skin too large")
		}
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		sum := sha256.Sum256(data)
		hash.Write(sum[:])
		files[name] = makeSkinAsset(data, mime)
	}
	m.Digest = hex.EncodeToString(hash.Sum(nil))
	manifest, _ := json.Marshal(m)
	files["skin.json"] = makeSkinAsset(manifest, "application/json")
	return m, files, nil
}
