package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestBrowserRequestCompletionExactReply(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	request := Message{ID: "request", Dir: "out", To: "own/host", Kind: KindQuestion,
		Target: &envelope.Target{Address: "own/host", Fingerprint: "own-key", AgentID: "chosen"}}
	answer := Message{ID: "answer", Dir: "out", To: "own/host", Kind: KindAnswer, ReplyTo: request.ID, AgentID: "chosen"}
	incoming := Message{ID: "incoming", Dir: "in", From: "remote/desk", To: "own/host", Kind: KindQuestion}
	manual := Message{ID: "manual", Dir: "out", To: "remote/desk", Kind: KindAnswer, ReplyTo: incoming.ID}
	dtos, err := json.Marshal(map[string]any{"request": request, "answer": answer, "incoming": incoming, "manual": manual, "local": "own/host"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), node, "testdata/requestcompletion_engine_check.mjs")
	cmd.Env = append(os.Environ(), "AGENTNET_COMPLETION_DTOS="+string(dtos))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestBrowserOutboxProgressPreservesCustody(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/outboxprogress_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// The rendered fixture consumes the public projection produced by the signed
// browser regression; no fabricated receipt or model call is involved.
func TestRequestCompletionComicRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	projection := filepath.Join(t.TempDir(), "completion.json")
	cmd := exec.CommandContext(t.Context(), "node", "testdata/requestcompletion_engine_check.mjs")
	cmd.Env = append(os.Environ(), "AGENTNET_COMPLETION_PROJECTION="+projection)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("signed projection: %v\n%s", err, out)
	}
	cmd = exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_COMPLETION_PROJECTION="+projection, "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
