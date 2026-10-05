package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// TestBrowserReportSupersedeMatchesGo checks the engine's review notice
// superseding and one-time cleanup against the shared Go vectors
// (client.TestReviewSupersedeVectors, written fresh by the Go test), and its
// report counts, deciders, conv items and decision results across reports
// (MEL-532).
func TestBrowserReportSupersedeMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	path := filepath.Join(t.TempDir(), "vectors.json")
	gen := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "-count=1", "-run", "^TestReviewSupersedeVectors$", "github.com/misunders2d/agentnet/internal/client")
	gen.Env = append(os.Environ(), "AGENTNET_SUPERSEDE_VECTORS="+path)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("Go vectors: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name        string `json:"name"`
		Open        []int  `json:"open"`
		CleanupOpen []int  `json:"cleanup_open"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) < 7 {
		t.Fatalf("vectors: %d, %v", len(cases), err)
	}
	input, _ := json.Marshal(map[string]json.RawMessage{"cases": raw})
	cmd := exec.Command(node, "testdata/report_supersede_engine_check.mjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got struct {
		Verdicts map[string]struct {
			Open        []int `json:"open"`
			CleanupOpen []int `json:"cleanup_open"`
		} `json:"verdicts"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, c := range cases {
		v := got.Verdicts[c.Name]
		if !slices.Equal(v.Open, c.Open) || !slices.Equal(v.CleanupOpen, c.CleanupOpen) {
			t.Errorf("%s: browser open %v cleanup %v, Go open %v cleanup %v", c.Name, v.Open, v.CleanupOpen, c.Open, c.CleanupOpen)
		}
	}
}
