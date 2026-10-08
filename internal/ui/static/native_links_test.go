package static

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The opener's default click script prevents _blank navigation before its
// command is rejected by our bounded capabilities. Links must reach the
// existing Rust navigation handler without adding general opener authority.
func TestNativeLinksUseBoundedNavigation(t *testing.T) {
	desktop := filepath.Join("..", "..", "..", "desktop", "src-tauri")
	source, err := os.ReadFile(filepath.Join(desktop, "src", "main.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{".open_js_links_on_click(false)", ".on_new_window(", "allow_navigation(&h, &url)", "open_outside(app, url);"} {
		if !strings.Contains(string(source), required) {
			t.Errorf("native external links require %s", required)
		}
	}
	capability, err := os.ReadFile(filepath.Join(desktop, "capabilities", "default.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Permissions []json.RawMessage `json:"permissions"`
	}
	if err := json.Unmarshal(capability, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, permission := range decoded.Permissions {
		if strings.Contains(string(permission), "opener:") {
			t.Fatal("external navigation must not grant general opener commands to the page")
		}
	}
}
