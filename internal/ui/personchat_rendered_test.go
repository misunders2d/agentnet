package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// One person has one chat row; separately signed conversations remain
// selectable, including an empty conversation created on another device.
func TestPersonChatRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Tester"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := PersonView{Person: "person-casey", Label: "Casey", Address: "casey/desk", State: "pinned"}
	f.threads, f.listed = nil, nil
	f.dms = []*fxDM{
		{id: "casey-old", peer: p, created: now.Add(-time.Hour), msgs: []DMMessage{{ID: "old-message", Dir: "in", From: p.Address, Kind: "message", Body: "Older laptop message", At: now.Add(-time.Hour)}}},
		{id: "casey-new", peer: p, created: now.Add(-time.Minute), msgs: []DMMessage{{ID: "new-message", Dir: "out", From: f.me.Address, Kind: "message", Body: "New phone message", At: now.Add(-time.Minute)}}},
		{id: "casey-empty", peer: p, created: now},
	}
	var s *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(f, ts.Listener.Addr().String(), testToken)
	ts.Start()
	cmd := exec.Command(node, "testdata/person_chat_rendered.cjs")
	cmd.Env = append(os.Environ(), "PERSON_CHAT_URL="+ts.URL+"/?t="+testToken)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "person chat rendered PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
