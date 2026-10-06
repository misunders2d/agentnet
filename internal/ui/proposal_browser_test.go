package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestBrowserProposalMatchesGo checks the browser's proposal shape guard
// (wire.mjs checkV2) against the shared Go vectors
// (envelope.TestProposalShapeVectors, written fresh by the Go test).
// TODO(integrate:P3): the engine's do_it/proposed parity joins this test
// once P3's message views land.
func TestBrowserProposalMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	path := filepath.Join(t.TempDir(), "vectors.json")
	gen := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "-count=1", "-run", "^TestProposalShapeVectors$", "github.com/misunders2d/agentnet/internal/envelope")
	gen.Env = append(os.Environ(), "AGENTNET_PROPOSAL_VECTORS="+path)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("Go vectors: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name  string          `json:"name"`
		Inner json.RawMessage `json:"inner"`
		Valid bool            `json:"valid"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil || len(vectors) < 14 {
		t.Fatalf("vectors: %d, %v", len(vectors), err)
	}
	input, _ := json.Marshal(map[string]any{"vectors": vectors})
	cmd := exec.Command(node, "testdata/proposal_browser_check.mjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got struct {
		Shapes map[string]bool `json:"shapes"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, v := range vectors {
		if got.Shapes[v.Name] != v.Valid {
			t.Errorf("shared vector %s: browser valid=%v, Go valid=%v", v.Name, got.Shapes[v.Name], v.Valid)
		}
	}
}
