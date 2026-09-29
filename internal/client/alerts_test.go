package client

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// alertWorld: alice and bob with persons and a DM; bob's daemon runs with
// its notifications recorded and a click command for the local page.
func alertWorld(t *testing.T, grace time.Duration) (w *world, conv string, n *notes, stopBob func()) {
	t.Helper()
	old := alertGrace
	alertGrace = grace
	t.Cleanup(func() { alertGrace = old })
	w = newWorld(t, "")
	n = fakeNotify(w.bob)
	runAgent(t, w.alice)
	stopBob, _ = runWith(t, w, w.bob, RunOptions{OpenConv: func(conv string) []string { return []string{"open-page", conv} }})
	persons(t, w.alice, w.bob)
	conv = newDM(t, w.alice, w.bob)
	return w, conv, n, stopBob
}

func allowAliceAt(t *testing.T, w *world, mutes ...string) {
	t.Helper()
	p := AlertPrefs{Enabled: true, Mutes: mutes,
		Senders: []protocol.NotifySender{{Address: w.alice.Address, Fingerprint: w.alice.id.Public(w.alice.Address).Fingerprint()}}}
	if err := w.bob.SetAlertPrefs(p); err != nil {
		t.Fatal(err)
	}
}

func sendAt(t *testing.T, from, to *Agent, conv string, m ConvOutgoing) string {
	t.Helper()
	sent, err := from.SendConv(tctx(t), conv, m)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the message at "+to.Address, func() bool { return inboxCount(t, to, `id = ?`, sent.ID) == 1 })
	return sent.ID
}

func pendingAlerts(t *testing.T, a *Agent) int { return inboxlessCount(t, a, "alerts") }

func inboxlessCount(t *testing.T, a *Agent, table string) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Off by default; on, a DM turn from an allowed exact key alerts once,
// after the grace, with fixed text and a click on that conversation;
// later messages of it within the grace add to that one alert. The grace
// outlasts two sends on a slow runner (Windows CI), so "two" lands within
// it; an alert wrongly queued while off is still pending at the check.
func TestDesktopAlertOnceAfterGrace(t *testing.T) {
	w, conv, n, _ := alertWorld(t, 3*time.Second)
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "while off"})
	time.Sleep(600 * time.Millisecond)
	if n.count() != 0 || pendingAlerts(t, w.bob) != 0 {
		t.Fatal("alerted while alerts are off")
	}
	if err := w.bob.SetAlertPrefs(AlertPrefs{Enabled: true, Senders: []protocol.NotifySender{{Address: w.alice.Address, Fingerprint: "0000beef-0000beef-0000beef-0000beef"}}}); err != nil {
		t.Fatal(err)
	}
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "another key allowed"})
	if pendingAlerts(t, w.bob) != 0 {
		t.Fatal("alert for a sender allowed under another key")
	}
	allowAliceAt(t, w)
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "one"})
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "two"})
	if pendingAlerts(t, w.bob) != 1 {
		t.Fatal("not one pending alert for the conversation")
	}
	eventually(t, "the alert", func() bool { return n.count() == 1 })
	time.Sleep(600 * time.Millisecond)
	n.mu.Lock()
	body, argv := n.bodies[0], n.argvs[0]
	n.mu.Unlock()
	if n.count() != 1 || body != "AgentNet: New activity" || !slices.Equal(argv, []string{"open-page", conv}) || pendingAlerts(t, w.bob) != 0 {
		t.Fatalf("alerts: %d %q %v", n.count(), body, argv)
	}
}

// The local page's presentation of the newest message cancels the alert;
// of an older one does not. Participation records never alert; an
// invited agent's answer does.
func TestDesktopAlertPresentationAndKinds(t *testing.T) {
	w, conv, n, _ := alertWorld(t, time.Second)
	allowAliceAt(t, w)
	first := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "first"})
	second := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "second"})
	if err := w.bob.AlertPresented(conv, []string{first}); err != nil {
		t.Fatal(err)
	}
	if pendingAlerts(t, w.bob) != 1 {
		t.Fatal("an older message's presentation cancelled the alert")
	}
	if err := w.bob.AlertPresented(conv, []string{first, second}); err != nil {
		t.Fatal(err)
	}
	if pendingAlerts(t, w.bob) != 0 {
		t.Fatal("presenting the newest message did not cancel the alert")
	}
	// Bob invites alice's agent: alice's invite record reaches bob quietly.
	p, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the invite", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
	time.Sleep(1500 * time.Millisecond)
	if n.count() != 0 {
		t.Fatalf("%d alerts for presented messages or a participation record", n.count())
	}
	in := envelope.Inner{V: envelope.Version2, Sub: "", Kind: envelope.KindAnswer, Origin: "agent:claude", PID: p.PID}
	if !asksAttention(in) {
		t.Fatal("an agent's answer does not ask for attention")
	}
	in.Sub = envelope.SubEvent
	if asksAttention(in) {
		t.Fatal("a record asks for attention")
	}
}

// Two DMs with one person mute separately; turning alerts off drops the
// pending ones; a sender frozen before the deadline is not shown.
func TestDesktopAlertMuteOffAndFreeze(t *testing.T) {
	w, conv, n, _ := alertWorld(t, time.Second)
	other := newDM(t, w.alice, w.bob)
	allowAliceAt(t, w, other)
	sendAt(t, w.alice, w.bob, other, ConvOutgoing{Body: "muted DM"})
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "open DM"})
	var pendingConv string
	w.bob.store.db.QueryRow(`SELECT conv FROM alerts`).Scan(&pendingConv)
	if pendingAlerts(t, w.bob) != 1 || pendingConv != conv {
		t.Fatalf("pending %d for %s", pendingAlerts(t, w.bob), pendingConv)
	}
	if err := w.bob.SetAlertPrefs(AlertPrefs{}); err != nil {
		t.Fatal(err)
	}
	if pendingAlerts(t, w.bob) != 0 {
		t.Fatal("turning alerts off kept a pending alert")
	}
	allowAliceAt(t, w)
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "then frozen"})
	freezeAlice(t, w)
	time.Sleep(1500 * time.Millisecond)
	if n.count() != 0 || pendingAlerts(t, w.bob) != 0 {
		t.Fatalf("a frozen sender's alert: shown %d, pending %d", n.count(), pendingAlerts(t, w.bob))
	}
}

// A pending alert survives a restart and is shown once after it.
func TestDesktopAlertAcrossRestart(t *testing.T) {
	w, conv, n, stopBob := alertWorld(t, 2*time.Second)
	allowAliceAt(t, w)
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "before the restart"})
	stopBob()
	if pendingAlerts(t, w.bob) != 1 || n.count() != 0 {
		t.Fatal("the alert did not wait in the store")
	}
	runAgent(t, w.bob)
	eventually(t, "the alert after the restart", func() bool { return n.count() == 1 })
	time.Sleep(time.Second)
	if n.count() != 1 {
		t.Fatalf("shown %d times", n.count())
	}
}

func TestAlertPrefsValidated(t *testing.T) {
	w := newWorld(t, "")
	for name, p := range map[string]AlertPrefs{
		"bad key":          {Senders: []protocol.NotifySender{{Address: "a/b", Fingerprint: "x"}}},
		"repeated sender":  {Senders: []protocol.NotifySender{{Address: "a/b", Fingerprint: "0000beef-0000beef-0000beef-0000beef"}, {Address: "a/b", Fingerprint: "0000beef-0000beef-0000beef-0000beef"}}},
		"bad conversation": {Mutes: []string{"nope"}},
	} {
		if w.bob.SetAlertPrefs(p) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := w.bob.AlertPresented(strings.Repeat("a", 64), []string{"x"}); err == nil {
		t.Fatal("a bad id accepted")
	}
	if p, err := w.bob.AlertPrefs(); err != nil || p.Enabled || len(p.Senders) != 0 {
		t.Fatalf("default prefs: %+v %v", p, err)
	}
}
