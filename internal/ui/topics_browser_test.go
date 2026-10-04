package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// TestBrowserTopicsMatchGo runs the browser engine's topic checks
// (testdata/topics_engine_check.mjs): its tunables must be the Go client's
// constants, its derivation must give the shared vectors' answers (the
// client's TestTopicDerivationVectors holds Go to the same file), and its
// /api/topics and /api/topic/* routes must behave as the daemon's.
func TestBrowserTopicsMatchGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	vectors, err := os.ReadFile("../client/testdata/topic_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"vectors": json.RawMessage(vectors),
		"constants": map[string]any{
			"archiveAfter": int64(client.TopicArchiveAfter / time.Second),
			"pageDefault":  client.TopicPageDefault,
			"pageMax":      client.TopicPageMax,
			"titleMax":     client.TopicTitleMax,
		},
	})
	cmd := exec.Command(node, "testdata/topics_engine_check.mjs")
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	var got struct {
		Checks int `json:"checks"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil || got.Checks < 60 {
		t.Fatalf("checks %d, %v\n%s%s", got.Checks, err, stdout.Bytes(), stderr.Bytes())
	}
}
