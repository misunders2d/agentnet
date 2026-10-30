package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// End is a durable local decision; recipient delivery cannot hold its HTTP
// response. The same signed human end path serves DMs and groups.
func TestLiveHumanEndCommitsBeforeDelivery(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	type barrier struct {
		entered, release chan struct{}
		once             sync.Once
	}
	var held atomic.Pointer[barrier]
	dir, _, alice := platformHub(t, ctx, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if b := held.Load(); b != nil && r.Method == "POST" && r.URL.Path == "/v1/messages" && r.Header.Get(protocol.HeaderAgent) == "admin/laptop" {
				b.once.Do(func() { close(b.entered) })
				select {
				case <-b.release:
				case <-r.Context().Done():
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	cert, err := os.ReadFile(filepath.Join(dir, "tls.crt"))
	if err != nil {
		t.Fatal(err)
	}
	join := func(name string) *client.Agent {
		t.Helper()
		code, err := alice.Invite(ctx, name, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		inv, err := protocol.DecodeInvite(code)
		if err != nil {
			t.Fatal(err)
		}
		inv.CertPEM = string(cert)
		a, err := client.Join(ctx, t.TempDir(), inv.Encode(), name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	bob, guest, outsider := join("desk"), join("guest"), join("outside")
	for _, a := range []*client.Agent{alice, bob, guest, outsider} {
		if _, err := a.CreatePerson(ctx, a.Address); err != nil {
			t.Fatal(err)
		}
	}
	stopA, stopB, stopGuest := runDaemon(t, alice), runDaemon(t, bob), runDaemon(t, guest)
	var dm string
	waitFor(t, "member DM", func() bool { dm, err = alice.CreateDM(ctx, bob.Address); return err == nil })
	group := liveTwoMemberGroup(t, alice, bob)
	wait := func(what string, fn func() bool) { waitFor(t, what, fn) }
	visits := []client.ParticipationInfo{
		inviteGuest(t, alice, guest, dm, wait),
		inviteGuest(t, alice, guest, group, wait),
	}
	var agent client.ParticipationInfo
	waitFor(t, "ordinary agent invitation", func() bool {
		agent, err = alice.InviteAgent(ctx, dm, bob.Address, nil, nil, "")
		return err == nil
	})
	live := NewLive(alice)
	if _, err := live.ChangeHuman(ctx, GuestAction{Action: "end", PID: agent.PID}); err == nil || !strings.Contains(err.Error(), "human participation") {
		t.Fatalf("human end accepted agent role: %v", err)
	}
	for _, p := range visits {
		if _, err := NewLive(outsider).ChangeHuman(ctx, GuestAction{Action: "end", PID: p.PID}); err == nil {
			t.Fatal("uninvited person ended another conversation's guest")
		}
		current, err := alice.Participation(p.PID)
		if err != nil || !current.HumanActive() || current.Dismissal != "" {
			t.Fatalf("refused caller changed guest: %+v %v", current, err)
		}
	}
	// Recipient devices stay offline, so every end copy must remain in custody
	// after the barrier is released. No helper can receive or run new work.
	stopA()
	stopB()
	stopGuest()
	handler := New(live, "127.0.0.1:8123", testToken).Handler()
	for i, p := range visits {
		t.Run([]string{"DM", "group"}[i], func(t *testing.T) {
			b := &barrier{entered: make(chan struct{}), release: make(chan struct{})}
			held.Store(b)
			var once sync.Once
			release := func() { once.Do(func() { close(b.release) }) }
			defer func() { release(); held.Store(nil) }()
			body, _ := json.Marshal(GuestAction{PID: p.PID})
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8123/api/dm/guest/end", strings.NewReader(string(body)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "http://127.0.0.1:8123")
			r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); handler.ServeHTTP(w, r); done <- w }()
			select {
			case <-b.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("human end never reached blocked recipient delivery")
			}
			current, err := alice.Participation(p.PID)
			if err != nil || current.State != client.PartDismissed || current.Dismissal == "" {
				t.Fatalf("end not durably stored before delivery: %+v %v", current, err)
			}
			var response *httptest.ResponseRecorder
			select {
			case response = <-done:
			case <-time.After(time.Second):
				release()
				<-done
				t.Fatal("human end response waited for delivery after signed local dismissal")
			}
			if response.Code != http.StatusOK {
				t.Fatalf("end HTTP%d: %s", response.Code, response.Body)
			}
			var view GuestView
			if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if view.PID != p.PID || view.State != client.PartDismissed || view.CanSend || view.CanEnd || view.CanLeave || !view.AudiencePending || !strings.Contains(view.StateText, "Ended here") {
				t.Fatalf("end response overstated remote delivery: %+v", view)
			}
			release()
			if err := alice.FlushOutbox(ctx); err != nil {
				t.Fatal(err)
			}
			msgs, err := alice.ConversationMessages(p.Conv)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, m := range msgs {
				if m.PID != p.PID || m.Sub != envelope.SubEvent {
					continue
				}
				ev, err := protocol.ParseParticipationEvent([]byte(m.Body))
				if err != nil || ev.Type != protocol.EventDismiss {
					continue
				}
				if err := ev.Verify(alice.Self().SignKey); err != nil || ev.Hash() != current.Dismissal || len(m.Copies) != 2 {
					t.Fatalf("signed end/copies not retained: copies=%d %v", len(m.Copies), err)
				}
				for _, copy := range m.Copies {
					if copy.ID == "" || copy.State != protocol.StateCustody {
						t.Fatalf("offline end copy not kept in custody: %+v", copy)
					}
				}
				found = true
			}
			if !found {
				t.Fatal("signed end missing from local conversation")
			}
			_, pending := guestOf(t, live, p.Conv, p.PID)
			if !pending.AudiencePending || pending.CanSend {
				t.Fatalf("custody is not proof all peers stored the end: %+v", pending)
			}
		})
	}
}

// Inviting a guest into a group is a durable local decision too: the app's
// response must not wait for recipient delivery, which the daemon finishes.
func TestLiveGuestInviteCommitsBeforeDelivery(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	type barrier struct {
		entered, release chan struct{}
		once             sync.Once
	}
	var held atomic.Pointer[barrier]
	var inviter atomic.Value
	dir, _, alice := platformHub(t, ctx, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if b := held.Load(); b != nil && r.Method == "POST" && r.URL.Path == "/v1/messages" && r.Header.Get(protocol.HeaderAgent) == inviter.Load() {
				b.once.Do(func() { close(b.entered) })
				select {
				case <-b.release:
				case <-r.Context().Done():
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	inviter.Store(alice.Address)
	cert, err := os.ReadFile(filepath.Join(dir, "tls.crt"))
	if err != nil {
		t.Fatal(err)
	}
	join := func(name string) *client.Agent {
		t.Helper()
		code, err := alice.Invite(ctx, name, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		inv, err := protocol.DecodeInvite(code)
		if err != nil {
			t.Fatal(err)
		}
		inv.CertPEM = string(cert)
		a, err := client.Join(ctx, t.TempDir(), inv.Encode(), name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	bob, guest := join("desk"), join("guest")
	for _, a := range []*client.Agent{alice, bob, guest} {
		if _, err := a.CreatePerson(ctx, a.Address); err != nil {
			t.Fatal(err)
		}
	}
	runDaemon(t, alice)
	runDaemon(t, bob)
	runDaemon(t, guest)
	var dm string
	waitFor(t, "member DM", func() bool { dm, err = alice.CreateDM(ctx, bob.Address); return err == nil })
	inviteGuest(t, alice, guest, dm, func(what string, fn func() bool) { waitFor(t, what, fn) }) // the guest's person and capabilities are known
	group := liveTwoMemberGroup(t, alice, bob)

	b := &barrier{entered: make(chan struct{}), release: make(chan struct{})}
	held.Store(b)
	var once sync.Once
	release := func() { once.Do(func() { close(b.release) }) }
	defer func() { release(); held.Store(nil) }()
	handler := New(NewLive(alice), "127.0.0.1:8123", testToken).Handler()
	body, _ := json.Marshal(GuestAction{Conv: group, Host: guest.Address})
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8123/api/dm/guest/invite", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:8123")
	r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); handler.ServeHTTP(w, r); done <- w }()
	select {
	case <-b.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("guest invite never reached blocked recipient delivery")
	}
	var response *httptest.ResponseRecorder
	select {
	case response = <-done:
	case <-time.After(time.Second):
		release()
		<-done
		t.Fatal("guest invite response waited for recipient delivery")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("invite HTTP%d: %s", response.Code, response.Body)
	}
	var view GuestView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if current, err := alice.Participation(view.PID); err != nil || current.State != client.PartInvited || current.Conv != group {
		t.Fatalf("invite not stored before delivery: %+v %v", current, err)
	}
	release()
	waitFor(t, "the guest holds the group invitation", func() bool {
		got, err := guest.Participation(view.PID)
		return err == nil && got.State == client.PartInvited
	})
}
