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

func TestP12SendRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in rendered send check")
	}
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewDM("vitalii/laptop"); err != nil {
		t.Fatal(err)
	}
	var server *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	defer ts.Close()
	server = New(f, ts.Listener.Addr().String(), testToken)
	ts.Start()
	cmd := exec.Command("node", "testdata/send_rendered_check.cjs")
	cmd.Env = append(os.Environ(), "P12_URL="+ts.URL+"/?t="+testToken)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "P12 rendered send PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
func TestQueuedEngineSend(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command("node", "testdata/queued_send_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
