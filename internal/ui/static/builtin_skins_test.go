package static

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func catalogOf(t *testing.T, dir string) []map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	Skins(dir).ServeHTTP(w, httptest.NewRequest("GET", "/assets/skins/index.json", nil))
	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("catalog: %s %v", w.Body, err)
	}
	return list
}

// Comic is a package like any other: listed first, with its manifest's
// entry, style, document rules and files, and a digest of its bytes.
func TestBuiltinSkinCatalog(t *testing.T) {
	for _, dir := range []string{"", t.TempDir()} {
		list := catalogOf(t, dir)
		if len(list) != 1 || list[0]["id"] != "comic" || list[0]["name"] != "Comic" || list[0]["entry"] != "entry.mjs" || list[0]["style"] != "style.css" ||
			list[0]["document"] != "document.css" || len(list[0]["digest"].(string)) != 64 {
			t.Fatalf("catalog %q: %v", dir, list)
		}
		if _, ok := list[0]["builtin"]; ok {
			t.Fatal("the catalog carries a trust word: trust is the host's own list")
		}
	}
	if !slices.Equal(BuiltinSkins, []string{"comic"}) {
		t.Fatalf("built-in skins %v", BuiltinSkins)
	}
}

// What the program serves for the built-in package is byte for byte what
// web/build.sh recorded in web/SHA256SUMS, and every file the manifest
// declares is served; nothing else under the package is.
func TestBuiltinSkinServedBytesMatchBuild(t *testing.T) {
	sums, err := os.ReadFile(filepath.Join("..", "web", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		sum, name, ok := strings.Cut(l, "  ")
		if !ok {
			t.Fatalf("bad SHA256SUMS line %q", l)
		}
		listed[name] = sum
	}
	var m Skin
	raw, err := os.ReadFile(filepath.Join("skins", "comic", "skin.json"))
	if err != nil || json.Unmarshal(raw, &m) != nil {
		t.Fatalf("manifest: %v", err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != listed["skin.json"] {
		t.Error("skins/comic/skin.json does not match web/SHA256SUMS: run web/build.sh")
	}
	if len(listed) != len(m.Files)+1 {
		t.Errorf("SHA256SUMS lists %d files, the manifest declares %d (+ skin.json)", len(listed), len(m.Files))
	}
	for _, relay := range []bool{false, true} {
		for _, name := range m.Files {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/assets/skins/comic/"+name, nil)
			if relay {
				Relay().ServeHTTP(w, r)
			} else {
				Skins("").ServeHTTP(w, r)
			}
			if w.Code != 200 || fmt.Sprintf("%x", sha256.Sum256(w.Body.Bytes())) != listed[name] {
				t.Errorf("relay=%v %s: %d, served bytes differ from SHA256SUMS", relay, name, w.Code)
			}
		}
	}
	// The package directory holds exactly the declared files.
	filepath.WalkDir(filepath.Join("skins", "comic"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel := strings.TrimPrefix(filepath.ToSlash(p), "skins/comic/")
			if _, ok := listed[rel]; !ok {
				t.Errorf("%s is in the package but not built by web/build.sh", p)
			}
		}
		return err
	})
}

// No installed package may take a built-in skin's id, one to come, or the
// old name of the default: such a directory is skipped and the built-in
// bytes stay what is served.
func TestReservedSkinIDs(t *testing.T) {
	home := t.TempDir()
	for _, id := range ReservedSkinIDs {
		dir := filepath.Join(home, id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]string{
			"skin.json": `{"api":1,"id":"` + id + `","name":"Impostor","entry":"entry.mjs","files":["entry.mjs"]}`,
			"entry.mjs": `export function mount(root) { root.textContent = "impostor"; }`,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	list := catalogOf(t, home)
	if len(list) != 1 || list[0]["name"] != "Comic" {
		t.Fatalf("a reserved id was listed: %v", list)
	}
	w := httptest.NewRecorder()
	Skins(home).ServeHTTP(w, httptest.NewRequest("GET", "/assets/skins/comic/entry.mjs", nil))
	if strings.Contains(w.Body.String(), "impostor") {
		t.Fatal("an installed package replaced the built-in Comic")
	}
	for _, id := range []string{"comic", "classic", "zoom", "default"} {
		if !slices.Contains(ReservedSkinIDs, id) {
			t.Errorf("%s is not reserved", id)
		}
	}
}

// A manifest's own words never become trust or a digest: the catalog has
// the digest of the bytes, and no "builtin" field at all.
func TestSkinManifestCannotClaimTrust(t *testing.T) {
	home := skinFixture(t)
	manifest := filepath.Join(home, "notebook", "skin.json")
	if err := os.WriteFile(manifest, []byte(`{"api":1,"id":"notebook","name":"Notebook","entry":"entry.mjs","style":"style.css","files":["entry.mjs","style.css"],"builtin":true,"digest":"00"}`), 0600); err != nil {
		t.Fatal(err)
	}
	list := catalogOf(t, home)
	if len(list) != 2 || list[1]["id"] != "notebook" || len(list[1]["digest"].(string)) != 64 {
		t.Fatalf("catalog %v", list)
	}
	if _, ok := list[1]["builtin"]; ok {
		t.Fatal("a package claimed built-in trust")
	}
}

// The document rules (fonts) are a declared CSS file like the stylesheet.
func TestSkinDocumentRulesDeclared(t *testing.T) {
	for _, c := range []struct {
		manifest string
		ok       bool
	}{
		{`{"api":1,"id":"notebook","name":"N","entry":"entry.mjs","document":"style.css","files":["entry.mjs","style.css"]}`, true},
		{`{"api":1,"id":"notebook","name":"N","entry":"entry.mjs","document":"fonts.css","files":["entry.mjs","style.css"]}`, false},
		{`{"api":1,"id":"notebook","name":"N","entry":"entry.mjs","document":"entry.mjs","files":["entry.mjs","style.css"]}`, false},
		{`{"api":1,"id":"notebook","name":"N","entry":"entry.mjs","files":["entry.mjs","style.css","style.css"]}`, false},
	} {
		home := skinFixture(t)
		if err := os.WriteFile(filepath.Join(home, "notebook", "skin.json"), []byte(c.manifest), 0600); err != nil {
			t.Fatal(err)
		}
		if got := len(catalogOf(t, home)) == 2; got != c.ok {
			t.Errorf("%s: listed %v, want %v", c.manifest, got, c.ok)
		}
	}
}
