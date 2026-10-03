package client

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// privateBytesAt counts rows of a's store holding s anywhere a participation
// record, turn or captured audience could carry it.
func privateBytesAt(t *testing.T, a *Agent, s string) int {
	t.Helper()
	n := 0
	for _, q := range []string{
		`SELECT count(*) FROM participation_events WHERE instr(event, ?) > 0`,
		`SELECT count(*) FROM inbox WHERE instr(coalesce(body,''), ?) > 0 OR instr(coalesce(human,''), ?) > 0`,
		`SELECT count(*) FROM outbox WHERE instr(coalesce(body,''), ?) > 0 OR instr(coalesce(human,''), ?) > 0`,
	} {
		var c int
		args := []any{s}
		if strings.Count(q, "?") == 2 {
			args = append(args, s)
		}
		if err := a.store.db.QueryRow(q, args...).Scan(&c); err != nil {
			t.Fatal(err)
		}
		n += c
	}
	return n
}

// guestAssistant: Bob adds his own assistant (private note, granted context,
// Alice's task key), Alice invites Carol, Carol accepts and learns the
// assistant from its public scope only.
func guestAssistant(t *testing.T) (w *world, carol *Agent, conv string, lids []string, stub *agentStub, ap, hp ParticipationInfo) {
	t.Helper()
	w, carol, conv, lids, stub = humanWorld(t)
	setResponder(t, w.bob, "agentstub", stub.dir, time.Minute)
	var err error
	if ap, err = w.bob.InviteAgent(tctx(t), conv, w.bob.Address, lids[:1], []string{w.alice.Self().Fingerprint()}, "PRIVATE_ASSISTANT_NOTE_C2"); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.AcceptParticipation(tctx(t), ap.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "assistant active at Alice", func() bool { return stateAt(t, w.alice, ap.PID).Claimable() })
	if hp, err = w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "PRIVATE_GUEST_NOTE_C2"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Carol invited", func() bool { return stateAt(t, carol, hp.PID).State == PartInvited })
	if _, err = carol.AcceptParticipation(tctx(t), hp.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Carol active at both originals", func() bool {
		return stateAt(t, w.alice, hp.PID).HumanActive() && stateAt(t, w.bob, hp.PID).HumanActive()
	})
	eventually(t, "Carol learns the assistant", func() bool {
		p, err := carol.Participation(ap.PID)
		return err == nil && p.Claimable()
	})
	return
}

// A guest may address an already-added member-hosted assistant only under its
// host's own permissions: a question waits until the host accepts it once or
// approves that guest; a task needs an exact-key grant or a one-time accept,
// never the members' task keys; a captured request stops once the guest ends.
// Everyone in the captured audience sees request and reply; nobody but the
// invite's holders sees its note, grant or task keys.
func TestHumanGuestAsksMemberHostedAssistant(t *testing.T) {
	w, carol, conv, lids, stub, ap, hp := guestAssistant(t)
	for _, private := range []string{"PRIVATE_ASSISTANT_NOTE_C2", `"task_keys"`, `"grant"`, lids[0]} {
		if n := privateBytesAt(t, carol, private); n != 0 {
			t.Fatalf("guest store holds the assistant invitation's %q (%d)", private, n)
		}
	}
	var invites int
	carol.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE pid=? AND type=?`, ap.PID, protocol.EventInvite).Scan(&invites)
	if invites != 0 {
		t.Fatal("guest holds the assistant's private invitation")
	}
	if _, err := carol.SendConv(tctx(t), conv, ConvOutgoing{PID: hp.PID, Body: "ambient guest chatter"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ambient turn at Bob", func() bool { return humanBodyCount(t, w.bob, conv, "ambient guest chatter") == 1 })

	// Unapproved question: inert everywhere, waiting for Bob.
	q, err := carol.AskAgent(tctx(t), ap.PID, envelope.KindQuestion, "guest question one")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "question awaits Bob", func() bool { return jobState(t, w.bob, q.LID) == stateAwaiting })
	if stub.runs() != 0 {
		t.Fatal("unapproved guest question ran")
	}
	eventually(t, "request seen at Alice", func() bool { return humanBodyCount(t, w.alice, conv, "guest question one") == 1 })
	if inboxCount(t, w.alice, `conv=? AND body=? AND state IN (?,?,?)`, conv, "guest question one", stateAgentWaiting, stateRunning, stateAwaiting) != 0 {
		t.Fatal("request not inert at Alice")
	}
	// Explicit one-time acceptance runs it; the reply reaches the whole audience.
	if err := w.bob.Accept(q.LID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{carol, w.alice, w.bob} {
		ans := replyAt(t, a, conv, q.LID)
		if ans.Kind != envelope.KindAnswer || ans.PID != ap.PID || ans.From != w.bob.Address || ans.Key != w.bob.Self().Fingerprint() {
			t.Fatalf("reply at %s: %+v", a.Address, ans)
		}
	}
	prompt := stub.last()
	if strings.Count(prompt, "guest question one") != 1 || !strings.Contains(prompt, "deploy failed at step 3") ||
		strings.Contains(prompt, "ambient guest chatter") || strings.Contains(prompt, "lunch") {
		t.Fatalf("assistant context is not granted context plus the addressed request:\n%s", prompt)
	}

	// Bob's standing approval of Carol: her next question runs directly.
	if err := w.bob.Approve(carol.Address); err != nil {
		t.Fatal(err)
	}
	q2, err := carol.AskAgent(tctx(t), ap.PID, envelope.KindQuestion, "guest question two")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, carol, conv, q2.LID)

	// Alice's task key is no authority for Carol: her task waits.
	task, err := carol.AskAgent(tctx(t), ap.PID, envelope.KindTask, "guest task one")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest task awaits Bob", func() bool { return jobState(t, w.bob, task.LID) == stateAwaiting })
	runs := stub.runs()

	// The same captured request under another key is no guest's.
	var human string
	w.bob.store.db.QueryRow(`SELECT human FROM inbox WHERE id=?`, q2.LID).Scan(&human)
	var h envelope.HumanTurn
	if err := json.Unmarshal([]byte(human), &h); err != nil {
		t.Fatal(err)
	}
	_, raw, _, _ := w.bob.store.conversation(conv)
	forged := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: carol.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion,
		Body: "forged key", Conv: conv, Root: raw, PID: ap.PID, Human: &h, Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint()}}
	forged.LID = forged.ID
	if _, err := w.bob.store.addConvInbox(forged, "0000beef-0000beef-0000beef-0000beef", stateAgentWaiting, false, nil); err != nil {
		t.Fatal(err)
	}
	notifyDaemon(w.bob.home)
	time.Sleep(300 * time.Millisecond)
	if s := jobState(t, w.bob, forged.ID); s != stateAgentWaiting || stub.runs() != runs {
		t.Fatalf("request under another key: %s, runs %d", s, stub.runs())
	}

	// Carol leaves; Bob's later one-time acceptance cannot run her task.
	if _, err := carol.DismissParticipation(tctx(t), hp.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Bob observes the leave", func() bool { return stateAt(t, w.bob, hp.PID).State == PartDismissed })
	if err := w.bob.Accept(task.LID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ended guest's task not run", func() bool { return jobState(t, w.bob, task.LID) == stateNotRun })
	if stub.runs() != runs {
		t.Fatal("ended guest's task ran")
	}
	if _, err := carol.AskAgent(tctx(t), ap.PID, envelope.KindQuestion, "after leave"); err == nil {
		t.Fatal("ended guest addressed the assistant")
	}

	// The same conversation is private again: the assistant's reply to a member
	// goes to the originals only.
	own, err := w.alice.AskAgent(tctx(t), ap.PID, envelope.KindQuestion, "private member question")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, own.ID)
	time.Sleep(300 * time.Millisecond)
	if humanBodyCount(t, carol, conv, "private member question") != 0 {
		t.Fatal("post-end request reached the departed guest")
	}
	if _, n := convMsg(t, carol, conv, func(m ConvMessage) bool { return m.ReplyTo == own.ID || m.ReplyTo == own.LID }); n != 0 {
		t.Fatal("post-end reply reached the departed guest")
	}
}

// The same with an outside-hosted assistant: the outside host learns the guest
// only from her public scope and acceptance carried on her request, decides
// under its own permissions, and learns her end; the guest learns the
// assistant from its public scope. Neither holds the other's invitation.
func TestHumanGuestAsksOutsideHostedAssistant(t *testing.T) {
	w, carol, conv, lids, stub := humanWorld(t)
	charlie := mustJoin(t, filepath.Join(t.TempDir(), "charlie"), w.aliceInvites("charlie"), "host")
	runAgent(t, charlie)
	persons(t, charlie)
	fakeNotify(charlie)
	humanTestCaps(t, charlie)
	record, err := charlie.CreateLocalAgent("Builder", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := charlie.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	ap, err := w.alice.InviteNamedAgent(tctx(t), conv, charlie.Address, record.ID, lids[:1], []string{w.bob.Self().Fingerprint()}, "PRIVATE_OUTSIDE_NOTE_C2")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "outside invite", func() bool { p, err := charlie.Participation(ap.PID); return err == nil && p.State == PartInvited })
	if _, err := charlie.AcceptParticipation(tctx(t), ap.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "outside assistant active", func() bool { return stateAt(t, w.alice, ap.PID).Claimable() && stateAt(t, w.bob, ap.PID).Claimable() })
	hp, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "PRIVATE_GUEST_NOTE_C2")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "Carol invited", func() bool { return stateAt(t, carol, hp.PID).State == PartInvited })
	if _, err := carol.AcceptParticipation(tctx(t), hp.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Carol learns the outside assistant", func() bool {
		p, err := carol.Participation(ap.PID)
		return err == nil && p.Claimable() && p.External && p.AgentID == record.ID
	})

	q, err := carol.AskAgent(tctx(t), ap.PID, envelope.KindQuestion, "guest asks outside")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "question awaits Charlie", func() bool { return jobState(t, charlie, q.LID) == stateAwaiting })
	if stub.runs() != 0 {
		t.Fatal("unapproved guest question ran")
	}
	if err := charlie.Accept(q.LID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{carol, w.alice, w.bob} {
		ans := replyAt(t, a, conv, q.LID)
		if ans.Kind != envelope.KindAnswer || ans.PID != ap.PID || ans.AgentID != record.ID || ans.From != charlie.Address || ans.Key != charlie.Self().Fingerprint() {
			t.Fatalf("outside reply at %s: %+v", a.Address, ans)
		}
	}
	for _, c := range []struct {
		a       *Agent
		private []string
	}{{carol, []string{"PRIVATE_OUTSIDE_NOTE_C2", `"task_keys"`, `"grant"`, lids[0]}}, {charlie, []string{"PRIVATE_GUEST_NOTE_C2"}}} {
		for _, private := range c.private {
			if n := privateBytesAt(t, c.a, private); n != 0 {
				t.Fatalf("%s holds another participation's private %q (%d)", c.a.Address, private, n)
			}
		}
	}
	var invites int
	charlie.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE pid=? AND type=?`, hp.PID, protocol.EventInvite).Scan(&invites)
	if invites != 0 {
		t.Fatal("outside host holds the guest's private invitation")
	}

	// Bob's task key is no authority for Carol; her end stops her waiting task.
	task, err := carol.AskAgent(tctx(t), ap.PID, envelope.KindTask, "guest task outside")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest task awaits Charlie", func() bool { return jobState(t, charlie, task.LID) == stateAwaiting })
	runs := stub.runs()
	if _, err := carol.DismissParticipation(tctx(t), hp.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "outside host learns the guest's end", func() bool {
		p, err := charlie.Participation(hp.PID)
		return err == nil && p.State == PartDismissed
	})
	if err := charlie.Accept(task.LID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ended guest's task not run", func() bool { return jobState(t, charlie, task.LID) == stateNotRun })
	if stub.runs() != runs {
		t.Fatal("ended guest's task ran")
	}
}
