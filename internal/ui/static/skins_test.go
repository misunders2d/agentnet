package static

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func skinFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "notebook")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"skin.json": `{"api":1,"id":"notebook","name":"Notebook","entry":"entry.mjs","style":"style.css","files":["entry.mjs","style.css"]}`,
		"entry.mjs": `export function mount(root, host) { root.textContent = host.platform; }`,
		"style.css": `body { color: black; }`, "private.txt": "never serve this",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}
func TestInstalledSkinSnapshot(t *testing.T) {
	home := skinFixture(t)
	handler := Skins(home)
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		return w
	}
	w := get("/assets/skins/index.json")
	var list []Skin
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 2 || list[0].ID != "comic" || list[1].ID != "notebook" || list[1].Digest == "" { // Comic, then the package
		t.Fatalf("catalog: %s %v", w.Body, err)
	}
	entry := "/assets/skins/notebook/entry.mjs"
	old := get(entry)
	if old.Code != 200 || !strings.Contains(old.Header().Get("Content-Type"), "javascript") {
		t.Fatal(old.Code, old.Header())
	}
	if err := os.WriteFile(filepath.Join(home, "notebook", "entry.mjs"), []byte("new code"), 0600); err != nil {
		t.Fatal(err)
	}
	if get(entry).Body.String() != old.Body.String() {
		t.Fatal("installed code changed under the consent digest")
	}
	next := httptest.NewRecorder()
	Skins(home).ServeHTTP(next, httptest.NewRequest("GET", entry, nil))
	if next.Body.String() != "new code" || next.Header().Get("ETag") == old.Header().Get("ETag") {
		t.Fatal("restart did not update the snapshot")
	}
	for _, p := range []string{"/assets/skins/notebook/private.txt", "/assets/skins/notebook/../private.txt", "/assets/skins/notebook/%2e%2e/private.txt", "/assets/skins/notebook/", "/assets/skins/notebook/not-declared.js"} {
		if get(p).Code != 404 {
			t.Fatal("served undeclared path", p)
		}
	}
	cached := httptest.NewRecorder()
	r := httptest.NewRequest("GET", entry, nil)
	r.Header.Set("If-None-Match", old.Header().Get("ETag"))
	handler.ServeHTTP(cached, r)
	if cached.Code != 304 || cached.Body.Len() != 0 {
		t.Fatal("conditional response", cached.Code)
	}
}
func TestSkinRejectsUnsafePackages(t *testing.T) {
	for _, test := range []string{"api", "id", "oversize", "traversal", "undeclared-entry", "symlink", "writable"} {
		t.Run(test, func(t *testing.T) {
			home := skinFixture(t)
			dir := filepath.Join(home, "notebook")
			manifest := filepath.Join(dir, "skin.json")
			raw, _ := os.ReadFile(manifest)
			switch test {
			case "api":
				raw = []byte(strings.Replace(string(raw), `"api":1`, `"api":2`, 1))
			case "id":
				raw = []byte(strings.Replace(string(raw), `"id":"notebook"`, `"id":"other"`, 1))
			case "oversize":
				if err := os.WriteFile(filepath.Join(dir, "entry.mjs"), make([]byte, (4<<20)+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				raw = []byte(strings.ReplaceAll(string(raw), "entry.mjs", "../entry.mjs"))
			case "undeclared-entry":
				raw = []byte(strings.Replace(string(raw), `"entry":"entry.mjs"`, `"entry":"secret.js"`, 1))
			case "symlink":
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation requires Windows privilege")
				}
				target := filepath.Join(t.TempDir(), "outside.mjs")
				os.WriteFile(target, []byte("secret"), 0600)
				os.Remove(filepath.Join(dir, "entry.mjs"))
				if err := os.Symlink(target, filepath.Join(dir, "entry.mjs")); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if runtime.GOOS == "windows" {
					t.Skip("Windows uses ACLs")
				}
				if err := os.Chmod(filepath.Join(dir, "entry.mjs"), 0666); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(manifest, raw, 0600); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			Skins(home).ServeHTTP(w, httptest.NewRequest("GET", "/assets/skins/index.json", nil))
			var list []Skin
			json.Unmarshal(w.Body.Bytes(), &list)
			if len(list) != 1 || list[0].ID != "comic" { // only the built-in skin
				t.Fatalf("unsafe package listed: %s", w.Body)
			}
		})
	}
}

func TestRelayInstalledSkin(t *testing.T) {
	home := skinFixture(t)
	w := httptest.NewRecorder()
	Relay(home).ServeHTTP(w, httptest.NewRequest("GET", "/assets/skins/notebook/entry.mjs", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "mount") || w.Header().Get("Content-Security-Policy") != relayCSP {
		t.Fatal(w.Code, w.Header(), w.Body)
	}
}

func TestBrowserLocalSkinDigest(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	dir := skinFixture(t)
	root, err := os.OpenRoot(filepath.Join(dir, "notebook"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	skin, _, err := readSkin(root, "notebook")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "../testdata/local_skins_check.mjs", filepath.Join(dir, "notebook"), skin.Digest).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
