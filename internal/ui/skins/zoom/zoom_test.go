package zoom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestStandaloneZoomPackage(t *testing.T) {
	var m struct {
		API              int `json:"api"`
		ID, Entry, Style string
		Files            []string
	}
	raw, err := os.ReadFile("skin.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.API != 1 || m.ID != "zoom" || m.Entry != "entry.mjs" || m.Style != "style.css" {
		t.Fatalf("bad manifest: %s", raw)
	}
	output := "../../static/skins/zoom"
	shipped, err := os.ReadFile(filepath.Join(output, "skin.json"))
	if err != nil || string(shipped) != string(raw) {
		t.Fatalf("manifest differs: %v", err)
	}
	declared := map[string]bool{"skin.json": true}
	for _, name := range m.Files {
		if declared[name] {
			t.Fatalf("duplicate file %s", name)
		}
		declared[name] = true
	}
	checks := regexp.MustCompile(`(?:window|globalThis)\.(?:agentnet\w*|fetch)|\bfetch\s*\(|\bEventSource\b|document\.(?:querySelector|getElementById|body|head|documentElement)|\.\./(?:comic|classic|_shared)/`)
	imports := regexp.MustCompile(`(?m)^import .*? from ["']([^"']+)["']|\bimport\(\s*["']([^"']+)["']`)
	for _, name := range m.Files {
		if strings.Contains(name, "..") || filepath.IsAbs(name) {
			t.Fatal("unsafe asset", name)
		}
		sourcePath := filepath.Join("src", name)
		if name == "topics.mjs" {
			sourcePath = filepath.Join("..", "shared", name)
		}
		source, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		built, err := os.ReadFile(filepath.Join(output, name))
		if err != nil || string(built) != string(source) {
			t.Fatalf("%s differs from source: %v", name, err)
		}
		if strings.HasSuffix(name, ".mjs") {
			if match := checks.Find(source); match != nil {
				t.Fatalf("%s reaches outside contract: %s", name, match)
			}
			for _, m := range imports.FindAllSubmatch(source, -1) {
				target := string(m[1])
				if target == "" {
					target = string(m[2])
				}
				if !strings.HasPrefix(target, "./") || !declared[strings.TrimPrefix(target, "./")] {
					t.Fatalf("%s has undeclared/nonlocal import %s", name, target)
				}
			}
			if node, err := exec.LookPath("node"); err == nil {
				if out, err := exec.Command(node, "--check", sourcePath).CombinedOutput(); err != nil {
					t.Fatalf("%s syntax: %v: %s", name, err, out)
				}
			}
		}
	}
	listing, err := os.ReadFile("SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(listing)), "\n") {
		want, name, ok := strings.Cut(line, "  ")
		if !ok || !declared[name] || seen[name] {
			t.Fatalf("invalid digest inventory %s", line)
		}
		seen[name] = true
		data, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			t.Fatalf("digest mismatch %s", name)
		}
	}
	if len(seen) != len(declared) {
		t.Fatalf("incomplete digest inventory")
	}
}
