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

// TestBrowserAssistantReactionsMatchGo runs the browser engine's assistant
// reaction checks (testdata/reaction_engine_check.mjs) against the shared Go
// shape vectors, written fresh by envelope.TestAssistantReactionShapeVectors:
// every vector judged as Go judges it, plus the engine's admission,
// projection, removal and own-linked history checks.
func TestBrowserAssistantReactionsMatchGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	path := filepath.Join(t.TempDir(), "vectors.json")
	gen := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "-count=1", "-run", "^TestAssistantReactionShapeVectors$", "github.com/misunders2d/agentnet/internal/envelope")
	gen.Env = append(os.Environ(), "AGENTNET_REACTION_VECTORS="+path)
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
	if err := json.Unmarshal(raw, &vectors); err != nil || len(vectors) != 19 {
		t.Fatalf("vectors: %d, %v", len(vectors), err)
	}
	// The guest-audience vectors, written fresh by client.TestHumanReactionWireVectors.
	hgen := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "-count=1", "-v", "-run", "^TestHumanReactionWireVectors$", "github.com/misunders2d/agentnet/internal/client")
	hout, err := hgen.CombinedOutput()
	if err != nil {
		t.Fatalf("Go guest-audience vectors: %v\n%s", err, hout)
	}
	_, line, found := bytes.Cut(hout, []byte("HUMAN_REACTION_VECTOR_JSON "))
	line, _, _ = bytes.Cut(line, []byte("\n"))
	var human json.RawMessage
	if !found || json.Unmarshal(line, &human) != nil {
		t.Fatalf("guest-audience vectors missing:\n%s", hout)
	}
	input, _ := json.Marshal(map[string]any{"vectors": vectors, "human_vectors": human})
	cmd := exec.Command(node, "testdata/reaction_engine_check.mjs")
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	var got struct {
		Shapes map[string]bool `json:"shapes"`
		Checks int             `json:"checks"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
		t.Fatalf("%v\n%s", err, stdout.Bytes())
	}
	for _, v := range vectors {
		if valid, ok := got.Shapes[v.Name]; !ok || valid != v.Valid {
			t.Errorf("shared vector %s: browser valid=%v (judged %v), Go valid=%v", v.Name, valid, ok, v.Valid)
		}
	}
	if got.Checks < 40 {
		t.Fatalf("engine checks ran %d", got.Checks)
	}
	t.Logf("19 shared vectors agree; %d engine checks", got.Checks)
}
