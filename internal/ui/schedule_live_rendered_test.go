package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// One real schedule through the rendered controls, an actual store restart and
// wall-clock dispatch. The broader UI permutations use the cheaper DOM fixture.
func TestScheduleLiveRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: installed Playwright required; takes up to three minutes")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	hubHome, home := filepath.Join(t.TempDir(), "hub"), filepath.Join(t.TempDir(), "alice")
	testhub.Start(t, hubHome, "127.0.0.1:0", "")
	a, err := client.Join(t.Context(), home, testhub.BootstrapCode(t, hubHome), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	stop := runDaemon(t, a)
	t.Cleanup(func() { stop(); a.Close() })
	code, err := a.Invite(t.Context(), "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.Join(t.Context(), filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	stopBob := runDaemon(t, b)
	t.Cleanup(func() { stopBob(); b.Close() })
	for _, person := range []struct {
		a     *client.Agent
		label string
	}{{a, "Alice"}, {b, "Bob"}} {
		if _, err := person.a.CreatePerson(t.Context(), person.label); err != nil {
			t.Fatal(err)
		}
	}
	var conv string
	waitFor(t, "verified schedule DM", func() bool { conv, err = a.CreateDM(t.Context(), b.Address); return err == nil })
	if _, err = a.SendConv(t.Context(), conv, client.ConvOutgoing{Body: "Fictional schedule test chat"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "recipient holds schedule DM", func() bool { _, err := b.ConversationMessages(conv); return err == nil })

	var current atomic.Pointer[Server]
	ts := httptest.NewUnstartedServer(nil)
	newServer := func() *Server { return New(NewLive(a), ts.Listener.Addr().String(), testToken) }
	current.Store(newServer())
	// These guarded commands exist only in this disposable test server. Browser
	// schedule actions themselves all pass through the production Host API.
	control := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var command struct{ Do string }
		if json.NewDecoder(r.Body).Decode(&command) != nil {
			http.Error(w, "invalid test command", 400)
			return
		}
		var result any
		var err error
		switch command.Do {
		case "push":
			result, err = b.SendConv(r.Context(), conv, client.ConvOutgoing{Body: "Schedule edit push signal"})
		case "restart":
			old := current.Load()
			stop()
			if err = a.Close(); err == nil {
				a, err = client.Open(home)
			}
			if err == nil {
				current.Store(newServer())
				stop = runDaemon(t, a)
				old.Restarting()
				result = map[string]bool{"reopened": true}
			}
		case "received":
			result, err = b.ConversationMessages(conv)
		default:
			http.Error(w, "unknown test command", 400)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
	privateComic := os.Getenv("AGENTNET_SCHEDULE_COMIC")
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server := current.Load()
		if r.URL.Path == "/__schedule_fixture" {
			server.Guard(control).ServeHTTP(w, r)
			return
		}
		if privateComic != "" && strings.HasPrefix(r.URL.Path, "/assets/skins/comic/") {
			name := strings.TrimPrefix(r.URL.Path, "/assets/skins/comic/")
			if name == "entry.mjs" || name == "style.css" || name == "document.css" {
				if name == "entry.mjs" {
					w.Header().Set("Content-Type", "text/javascript")
				}
				http.ServeFile(w, r, filepath.Join(privateComic, name))
				return
			}
		}
		server.Handler().ServeHTTP(w, r)
	})
	ts.Start()
	t.Cleanup(ts.Close)
	cmd := exec.CommandContext(t.Context(), "node", "testdata/schedule_live_rendered.cjs")
	cmd.Env = append(os.Environ(), "SCHEDULE_URL="+ts.URL+"/?t="+testToken, "SCHEDULE_CONV="+conv)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "live schedule PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
