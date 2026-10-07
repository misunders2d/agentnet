package client

import (
	"encoding/json"
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

// beforeDue reports whether an alert queued by a message sent at or after
// sent cannot be due yet: it is due alertGrace after its admission, stored
// in whole ms (so up to 1ms sooner), and the alert loop may show and drop
// it from then on. A "not shown yet" check holds only while this is true;
// read it after the look, since a loaded runner (Windows CI) may spend the
// whole grace first.
func beforeDue(sent time.Time) bool {
	return time.Now().Before(sent.Add(alertGrace - time.Millisecond))
}

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
	if err := w.bob.SetAlertPrefs(AlertPrefs{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "chat alerts without sender grant"})
	if pendingAlerts(t, w.bob) != 1 {
		t.Fatal("default-unmuted chat needs no sender grant")
	}
	allowAliceAt(t, w)
	queued := time.Now()
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "one"})
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "two"})
	pending := pendingAlerts(t, w.bob)
	inGrace := beforeDue(queued) // both turns were admitted before the alert could be due
	if inGrace && pending != 1 {
		t.Fatal("not one pending alert for the conversation")
	}
	eventually(t, "the alert", func() bool { return n.count() >= 1 })
	time.Sleep(600 * time.Millisecond)
	n.mu.Lock()
	body, argv := n.bodies[0], n.argvs[0]
	n.mu.Unlock()
	if body != "AgentNet: New activity" || !slices.Equal(argv, []string{"open-page", conv}) {
		t.Fatalf("alert: %q %v", body, argv)
	}
	if !inGrace {
		t.Logf("the two sends outlasted the grace (%v past it): \"two\" may have queued its own alert, so \"once\" is not observable this run", time.Since(queued.Add(alertGrace)))
		return
	}
	if n.count() != 1 || pendingAlerts(t, w.bob) != 0 {
		t.Fatalf("alerts: shown %d, pending %d", n.count(), pendingAlerts(t, w.bob))
	}
}

// The local page's presentation of the newest message cancels the alert;
// of an older one does not. Participation records never alert; an
// invited agent's answer does.
func TestDesktopAlertPresentationAndKinds(t *testing.T) {
	w, conv, n, _ := alertWorld(t, time.Second)
	allowAliceAt(t, w)
	queued := time.Now()
	first := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "first"})
	second := sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "second"})
	if err := w.bob.AlertPresented(conv, []string{first}); err != nil {
		t.Fatal(err)
	}
	if pendingAlerts(t, w.bob) != 1 && beforeDue(queued) {
		t.Fatal("an older message's presentation cancelled the alert")
	}
	if err := w.bob.AlertPresented(conv, []string{first, second}); err != nil {
		t.Fatal(err)
	}
	if pendingAlerts(t, w.bob) != 0 {
		t.Fatal("presenting the newest message did not cancel the alert")
	}
	// Presented before it was due, the alert is never shown; a loaded
	// runner may have shown it (once) before the presentation.
	shownBefore := 0
	if !beforeDue(queued) {
		shownBefore = 1
	}
	// Bob invites alice's agent: alice's invite record reaches bob quietly.
	p, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the invite", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
	time.Sleep(1500 * time.Millisecond)
	if n.count() > shownBefore {
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
	queued := time.Now()
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "open DM"})
	var pendingConv string
	w.bob.store.db.QueryRow(`SELECT conv FROM alerts`).Scan(&pendingConv)
	// The muted DM never queues one; the open DM's waits until it is due.
	if p := pendingAlerts(t, w.bob); p > 1 || p == 1 && pendingConv != conv || p == 0 && beforeDue(queued) {
		t.Fatalf("pending %d for %s", p, pendingConv)
	}
	if err := w.bob.SetAlertPrefs(AlertPrefs{}); err != nil {
		t.Fatal(err)
	}
	if pendingAlerts(t, w.bob) != 0 {
		t.Fatal("turning alerts off kept a pending alert")
	}
	shownBefore := 0 // turned off before it was due, the open DM's alert is never shown
	if !beforeDue(queued) {
		shownBefore = 1
	}
	allowAliceAt(t, w)
	queued = time.Now()
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "then frozen"})
	freezeAlice(t, w)
	if !beforeDue(queued) { // frozen only after it could be due: it may be shown
		shownBefore++
	}
	time.Sleep(1500 * time.Millisecond)
	eventually(t, "the frozen sender's alert dropped when due", func() bool { return pendingAlerts(t, w.bob) == 0 })
	if n.count() > shownBefore {
		t.Fatalf("a frozen sender's alert: shown %d", n.count())
	}
}

// BUG-38: alerts allowed for a person cover every current device of that
// person, not only the device whose key was allowed; a key the person does
// not hold still alerts nothing (TestDesktopAlertOnceAfterGrace).
func TestDesktopAlertCoversEveryDeviceOfAnAllowedPerson(t *testing.T) {
	w, conv, n, _ := alertWorld(t, time.Second)
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "while off, from the laptop"})
	allowAliceAt(t, w)
	phone := linked(t, w.alice)
	eventually(t, "the DM on alice's phone", func() bool { return len(convBodies(t, phone, conv)) > 0 })
	sendAt(t, phone, w.bob, conv, ConvOutgoing{Body: "from alice's phone"})
	eventually(t, "an alert for alice's phone", func() bool { return n.count() == 1 })
}

// A pending alert survives a restart and is shown once after it.
func TestDesktopAlertAcrossRestart(t *testing.T) {
	w, conv, n, stopBob := alertWorld(t, 2*time.Second)
	allowAliceAt(t, w)
	queued := time.Now()
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "before the restart"})
	stopBob()
	if (pendingAlerts(t, w.bob) != 1 || n.count() != 0) && beforeDue(queued) {
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

func TestDesktopGroupAlertWithoutDM(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	if err := w.bob.SetAlertPrefs(AlertPrefs{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	sent, err := w.alice.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "group member activity"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "group activity queued without DM or sender grant", func() bool { return pendingAlerts(t, w.bob) == 1 })
	var raw string
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE lid=? AND recipient=?`, sent.LID, w.bob.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &env); err != nil || !env.Attn || env.Chan != protocol.NotifyChannel(packet.State.Conv, w.bob.Self().Fingerprint()) {
		t.Fatalf("group push hint missing: %v", err)
	}
	if err = w.bob.SetAlertPrefs(AlertPrefs{Enabled: true, Mutes: []string{packet.State.Conv}}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.AlertPresented(packet.State.Conv, []string{env.ID}); err != nil {
		t.Fatal(err)
	}
	sendAt(t, w.alice, w.bob, packet.State.Conv, ConvOutgoing{Body: "muted group activity"})
	if pendingAlerts(t, w.bob) != 0 {
		t.Fatal("explicit group mute ignored")
	}
}

func TestChatAlertQuietMigration(t *testing.T) {
	w, conv, _, _ := alertWorld(t, time.Hour)
	sendAt(t, w.alice, w.bob, conv, ConvOutgoing{Body: "old quiet chat"})
	if err := w.bob.SetAlertPrefs(AlertPrefs{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.store.db.Exec(chatAlertDefaultsSchema); err != nil {
		t.Fatal(err)
	}
	p, err := w.bob.AlertPrefs()
	if err != nil || !slices.Contains(p.Mutes, conv) {
		t.Fatalf("old quiet chat not preserved: %+v %v", p, err)
	}
	next := newDM(t, w.alice, w.bob)
	sendAt(t, w.alice, w.bob, next, ConvOutgoing{Body: "same person, later root"})
	p, err = w.bob.AlertPrefs()
	if err != nil || !slices.Contains(p.Mutes, next) {
		t.Fatalf("same person chat lost quiet preference: %+v %v", p, err)
	}
	if err := w.bob.SetAlertPrefs(AlertPrefs{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	p, err = w.bob.AlertPrefs()
	if err != nil || len(p.Mutes) != 0 {
		t.Fatalf("unmute left quiet anchors: %+v %v", p, err)
	}
}
