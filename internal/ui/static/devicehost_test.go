package static

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The relay's page opens its views only when device.mjs gives loader.js
// every engine method loader.js calls on it (window.agentnetEngine):
// a missing driveService left the page at "Opening AgentNet…".
func TestDeviceEngineHasWhatLoaderUses(t *testing.T) {
	loader, _ := fs.ReadFile(Files, "loader.js")
	device, _ := fs.ReadFile(Files, "device.mjs")
	m := regexp.MustCompile(`window\.agentnetEngine = \{([^\n]*)\};`).FindSubmatch(device)
	if m == nil {
		t.Fatal("device.mjs sets no window.agentnetEngine")
	}
	given := map[string]bool{}
	for _, k := range regexp.MustCompile(`(\w+):`).FindAllSubmatch(m[1], -1) {
		given[string(k[1])] = true
	}
	used := regexp.MustCompile(`\bbrowser\.(\w+)\(`).FindAllSubmatch(loader, -1)
	if len(used) == 0 {
		t.Fatal("loader.js calls nothing on the browser engine")
	}
	for _, u := range used {
		if name := string(u[1]); !given[name] {
			t.Errorf("loader.js calls browser.%s, which device.mjs does not give (%s)", name, strings.TrimSpace(string(m[1])))
		}
	}
}
