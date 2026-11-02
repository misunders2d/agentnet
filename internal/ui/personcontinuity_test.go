package ui

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
)

func TestMEL736OneBrowserParity(t *testing.T) {
	var records []string
	for i, id := range []string{"faeb3e71bb3aac661d198083239baa6f", "59c89e536113a047e24569888bcc106a", protocol.NewID()} {
		k, e := identity.Generate()
		if e != nil {
			t.Fatal(e)
		}
		r := protocol.PersonRoster{Person: id, Label: "Bohdan", Email: "bohdan@mellanni.com", Devices: []identity.Public{k.Public([]string{"bohdan/lost", "bohdan/new", "bohdan/third"}[i])}}
		r.Sign(k.Sign)
		raw, _ := json.Marshal(r)
		records = append(records, string(raw))
	}
	w := startEngineNode(t, t.TempDir())
	v := w.ok(map[string]any{"op": "mel736OneProbes", "rosters": records})
	if v["passed"] != true {
		t.Fatal(v)
	}
}
