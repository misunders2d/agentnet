package ui

import (
	"context"
	"database/sql"
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
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Opt-in rendered send lifecycle (MEL-748) in Comic, desktop and touch phone:
// the real page, host routes, signed transport and durable queue in a group,
// with the person-chain request that cancelled messages in the incident held
// past its time budget, then the sending itself held while the chat goes on,
// then both answered. No model, installed identity or production.
func TestQueuedSendRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT")
	}
	for _, width := range []string{"1440", "390"} {
		t.Run(width, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "bob")
			alice, bob, live, hub := liveWorldHubAt(t, home)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
			defer cancel()
			for _, a := range []*client.Agent{alice, bob} {
				if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
					t.Fatal(err)
				}
			}
			run, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { alice.Run(run, client.RunOptions{}); close(done) }()
			defer func() { stop(); <-done }()
			waitDirectory(t, live, "peer connected", func(d Directory) bool { return d.Status == DirectoryListed && d.Current })
			conv := liveTwoMemberGroup(t, alice, bob)
			if err := alice.FlushOutbox(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "first inbound"}); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
				msgs, _ := bob.ConversationMessages(conv)
				found := false
				for _, m := range msgs {
					found = found || m.Body == "first inbound"
				}
				if found {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("first message never arrived")
				}
			}
			var held, heldPost atomic.Bool
			held.Store(true)
			defer held.Store(false)
			defer heldPost.Store(false)
			hub.SetHold(func(r *http.Request) {
				if r.Header.Get(protocol.HeaderAgent) != bob.Address {
					return
				}
				switch {
				case strings.HasPrefix(r.URL.Path, "/v1/persons/"):
					for held.Load() {
						time.Sleep(20 * time.Millisecond)
					}
				case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
					for heldPost.Load() {
						time.Sleep(20 * time.Millisecond)
					}
				}
			})
			defer hub.SetHold(nil)
			// A row that really stopped: what a refusal leaves, in the store's own
			// shape. It is the worker's next row, not its current one, so no
			// attempt races this write.
			side, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(10000)")
			if err != nil {
				t.Fatal(err)
			}
			defer side.Close()
			var server *Server
			ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/__test/inbound":
					if _, e := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "later inbound"}); e != nil {
						http.Error(w, e.Error(), 500)
					}
				case "/__test/stop":
					res, e := side.Exec(`UPDATE scheduled_sends SET state='needs_review',detail='This person cannot receive messages now.',diagnostic='GET https://hub.example/v1/persons/abc/chain?after=1: refused',rev=rev+1 WHERE body=? AND immediate=1 AND state='pending'`, r.URL.Query().Get("body"))
					n := int64(0)
					if e == nil {
						n, e = res.RowsAffected()
					}
					if e != nil || n != 1 {
						http.Error(w, "row not stopped", 500)
						return
					}
					bob.NoteChange()
				case "/__test/open-rosters": // the stall moves on to the sending itself
					heldPost.Store(true)
					held.Store(false)
				case "/__test/open":
					heldPost.Store(false)
					held.Store(false)
				default:
					server.Handler().ServeHTTP(w, r)
				}
			}))
			defer ts.Close()
			server = New(live, ts.Listener.Addr().String(), testToken)
			ts.Start()
			cmd := exec.Command("node", "testdata/queued_send_rendered.cjs")
			cmd.Env = append(os.Environ(), "Q_URL="+ts.URL+"/?t="+testToken, "Q_WIDTH="+width, "Q_CHAT=Harbor planning")
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "queued send rendered PASS") {
				t.Fatalf("%v\n%s", err, out)
			}
			t.Logf("%s", out)
			// What the other person holds: each message once, in order; never the cancelled one.
			want := []string{"kept through a stall", "stopped then retried"}
			for deadline := time.Now().Add(40 * time.Second); ; time.Sleep(50 * time.Millisecond) {
				msgs, _ := alice.ConversationMessages(conv)
				var got []string
				for _, m := range msgs {
					switch m.Body {
					case "cancel this one":
						t.Fatal("the cancelled message went out")
					case want[0], want[1]:
						got = append(got, m.Body)
					}
				}
				if len(got) > len(want) {
					t.Fatalf("duplicates reached the peer: %q", got)
				}
				if len(got) == len(want) {
					if got[0] != want[0] {
						t.Fatalf("out of order: %q", got)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the peer holds only %q", got)
				}
			}
		})
	}
}
