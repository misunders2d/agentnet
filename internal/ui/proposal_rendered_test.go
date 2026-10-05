package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

// Opt-in rendered P23 regression across all skins, desktop and touch phone.
// Real local signed transports; no model, installed identity or production.
func TestProposalConfirmationRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT")
	}
	for _, skin := range []string{"comic", "classic", "zoom"} {
		for _, width := range []string{"1440", "390"} {
			t.Run(skin+width, func(t *testing.T) {
				alice, bob, live := liveWorld(t)
				run, stop := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { alice.Run(run, client.RunOptions{}); close(done) }()
				t.Cleanup(func() { stop(); <-done })
				q, err := bob.SendMessage(t.Context(), client.Outgoing{To: alice.Address, Kind: envelope.KindQuestion, Body: "Should we update the changelog?"})
				if err != nil {
					t.Fatal(err)
				}
				body := "Update CHANGELOG.md.\nThen verify the version entry."
				p, err := alice.SendMessage(t.Context(), client.Outgoing{To: bob.Address, Kind: envelope.KindAnswer, Status: envelope.StatusProposal, ReplyTo: q.ID, Body: body})
				if err != nil {
					t.Fatal(err)
				}
				waitTopic(t, bob, p.ID)
				if !bob.CanConfirmProposal(p.ID) {
					t.Fatal("stored proposal is not confirmable")
				}
				thread, err := live.Thread(q.ID)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, m := range thread.Messages {
					if m.ID == p.ID && slices.Contains(m.Actions, DoIt) {
						found = true
					}
				}
				if !found {
					t.Fatal("native view omitted Do it")
				}
				var server *Server
				ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
				defer ts.Close()
				server = New(live, ts.Listener.Addr().String(), testToken)
				ts.Start()
				cmd := exec.Command("node", "testdata/proposal_rendered_check.cjs")
				cmd.Env = append(os.Environ(), "P23_URL="+ts.URL+"/?t="+testToken, "P23_SKIN="+skin, "P23_WIDTH="+width, "P23_PEER="+alice.Address)
				out, err := cmd.CombinedOutput()
				if err != nil || !strings.Contains(string(out), "P23 rendered proposal PASS") {
					t.Fatalf("%v\n%s", err, out)
				}
				// Rendering's double click is one durable task, with the exact bytes.
				deadline := time.Now().Add(10 * time.Second)
				for {
					inbox, e := alice.Inbox(false, false)
					if e != nil {
						t.Fatal(e)
					}
					count := 0
					for _, m := range inbox {
						if m.Kind == envelope.KindTask && m.ReplyTo == p.ID {
							count++
							if m.Body != body {
								t.Fatal("proposal bytes changed")
							}
						}
					}
					if count == 1 {
						break
					}
					if count > 1 {
						t.Fatal("double click sent two tasks")
					}
					if time.Now().After(deadline) {
						t.Fatal("confirmed task never arrived")
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Log(string(out))
			})
		}
	}
}

// Inert conversation provider: this proves native skin bindings only. The
// real signed conversation/approval/execution path is covered by client tests.
type proposalRenderedFixture struct {
	*Fixture
	confirmations int
}

func (f *proposalRenderedFixture) Act(a Action) (string, error) {
	if a.Do != DoIt {
		return f.Fixture.Act(a)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.dms {
		for i := range d.msgs {
			m := &d.msgs[i]
			if m.ID == a.ID {
				if slices.Contains(m.Actions, DoIt) {
					m.Actions = nil
					f.confirmations++
					f.bump()
				}
				return "Task saved; sending to the same agent.", nil
			}
		}
	}
	return "", NotFound("no proposal")
}
func TestProposalConversationRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in rendered conversation check")
	}
	for _, skin := range []string{"comic", "classic", "zoom"} {
		for _, width := range []string{"1440", "390"} {
			t.Run(skin+width, func(t *testing.T) {
				base := NewFixture(time.Now)
				if _, _, err := base.CreatePerson("Alice"); err != nil {
					t.Fatal(err)
				}
				conv, err := base.NewDM("vitalii/laptop")
				if err != nil {
					t.Fatal(err)
				}
				f := &proposalRenderedFixture{Fixture: base}
				base.mu.Lock()
				for _, d := range base.dms {
					if d.id == conv {
						d.msgs = []DMMessage{
							{ID: "question", LID: "question", Dir: "out", From: base.me.Address, Kind: KindQuestion, Body: "Should we update the changelog?", At: time.Now()},
							{ID: "proposal", LID: "proposal", Dir: "in", From: d.peer.Address, Kind: KindAnswer, Status: envelope.StatusProposal, Body: "Update CHANGELOG.md.\nThen verify the version entry.", ReplyTo: "question", PID: "participation", AgentID: "builder", VerifiedAgent: true, Actions: []string{DoIt}, At: time.Now()},
						}
					}
				}
				base.mu.Unlock()
				var server *Server
				ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
				defer ts.Close()
				server = New(f, ts.Listener.Addr().String(), testToken)
				ts.Start()
				cmd := exec.Command("node", "testdata/proposal_rendered_check.cjs")
				cmd.Env = append(os.Environ(), "P23_URL="+ts.URL+"/?t="+testToken, "P23_SKIN="+skin, "P23_WIDTH="+width, "P23_PEER=Vitalii", "P23_MODE=conversation")
				out, err := cmd.CombinedOutput()
				if err != nil || !strings.Contains(string(out), "P23 rendered proposal PASS") {
					t.Fatalf("%v\n%s", err, out)
				}
				f.mu.Lock()
				count := f.confirmations
				f.mu.Unlock()
				if count != 1 {
					t.Fatalf("confirmations %d", count)
				}
				t.Log(string(out))
			})
		}
	}
}
