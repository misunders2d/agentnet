package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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
		"vectors":           json.RawMessage(vectors),
		"execRunningMaxAge": int64(client.ExecRunningMaxAge / time.Second),
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

// TestMessengerTopicLimitsMatchGo pins the server limits the messenger's
// TOPICS block (web/src/model.ts) repeats to the Go client's constants, and
// keeps its page sizes within what one request may ask for.
func TestMessengerTopicLimitsMatchGo(t *testing.T) {
	src, err := os.ReadFile("web/src/model.ts")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const TOPICS = \{(.*?)\} as const;`).FindSubmatch(src)
	if block == nil {
		t.Fatal("no TOPICS block in model.ts")
	}
	value := func(name string) int {
		t.Helper()
		m := regexp.MustCompile(`(?m)^\s*` + name + `:\s*(\d+),`).FindSubmatch(block[1])
		if m == nil {
			t.Fatalf("TOPICS.%s missing", name)
		}
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}
	if got := value("titleMax"); got != client.TopicTitleMax {
		t.Errorf("TOPICS.titleMax %d, client.TopicTitleMax %d", got, client.TopicTitleMax)
	}
	if got := value("pageMax"); got != client.TopicPageMax {
		t.Errorf("TOPICS.pageMax %d, client.TopicPageMax %d", got, client.TopicPageMax)
	}
	for _, name := range []string{"pageSize", "chatSearchMax"} {
		if got := value(name); got < 1 || got > client.TopicPageMax {
			t.Errorf("TOPICS.%s %d is not a page the server lists (1..%d)", name, got, client.TopicPageMax)
		}
	}
}
