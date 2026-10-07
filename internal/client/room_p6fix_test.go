package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestP6FixAnswerAudienceAndStoredReply(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	from := p6Member(t, w.alice, w.alice, conv)
	to := p6Member(t, w.alice, w.bob, conv)
	if err := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	root, err := w.alice.AskAgent(tctx(t), from.PID, envelope.KindQuestion, "SOURCE_REQUEST")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, root.LID); err != nil {
		t.Fatal(err)
	}
	asked, err := carol.AskAgent(tctx(t), to.PID, envelope.KindQuestion, "THIRD_PARTY_REPORT")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, asked.LID, stateAwaiting)
	eventually(t, "source sees request id", func() bool {
		c, e := w.alice.agentContext(stateAt(t, w.alice, from.PID), "", defaultContextBytes)
		return e == nil && strings.Contains(strings.Join(c.lines, "\n"), asked.LID)
	})
	if err = w.bob.Accept(asked.LID); err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, w.alice, conv, asked.LID)
	if got, e := w.alice.RoomReply(root.LID, asked.LID); e != nil || got == nil || got.Body != answer.Body {
		t.Fatalf("third-party reply: %+v %v", got, e)
	}
	own, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{PID: from.PID, Kind: envelope.KindAnswer, ReplyTo: root.LID, Origin: envelope.OriginAgentPrefix + "fixture", Emotion: "neutral", Body: "OWN_ANSWER"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.alice.agentContext(stateAt(t, w.alice, from.PID), "", defaultContextBytes)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(c.lines, "\n")
	for _, want := range []string{"Verified group agent (PID " + to.PID, "You (the agent), answer: OWN_ANSWER", "question for you: SOURCE_REQUEST", asked.LID, "reply to " + root.LID} {
		if !strings.Contains(lines, want) {
			t.Fatalf("missing %q in %s (own=%s)", want, lines, own.LID)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: strings.Repeat("x", 30000)}); err != nil {
			t.Fatal(err)
		}
	}
	if got, e := w.alice.RoomReply(root.LID, asked.LID); e != nil || got == nil {
		t.Fatalf("reply outside prompt window: %+v %v", got, e)
	}
	if err = os.WriteFile(Harnesses["agentstub"].bin, []byte("#!/bin/sh\ncat >/dev/null\nhead -c 70000 /dev/zero | tr '\\000' x\nprintf '\\nemotion: neutral\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	large, err := carol.AskAgent(tctx(t), to.PID, envelope.KindQuestion, "large answer")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, large.LID, stateAwaiting)
	if err = w.bob.Accept(large.LID); err != nil {
		t.Fatal(err)
	}
	big := replyAt(t, w.alice, conv, large.LID)
	if len(big.Body) < maxOutput || !strings.Contains(big.Body, "[output truncated]") {
		t.Fatalf("not a capped answer: len=%d", len(big.Body))
	}
	if got, e := w.alice.RoomReply(root.LID, large.LID); e != nil || got == nil || got.Body != big.Body {
		t.Fatalf("capped answer lookup: %+v %v", got, e)
	}
	refused, err := carol.AskAgent(tctx(t), to.PID, envelope.KindQuestion, "declined request")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, refused.LID, stateAwaiting)
	if _, err = w.bob.Decline(tctx(t), refused.LID, "owner declined this request"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "declined status received", func() bool {
		_, e := w.alice.RoomReply(root.LID, refused.LID)
		return e != nil && strings.Contains(e.Error(), "declined")
	})
	for i := 0; i < 3; i++ {
		if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: strings.Repeat("z", 30000)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, e := w.alice.RoomReply(root.LID, refused.LID); e == nil || !strings.Contains(e.Error(), "declined") {
		t.Fatalf("terminal status outside prompt window: %v", e)
	}
	if _, err = w.alice.DismissParticipation(tctx(t), to.PID); err != nil {
		t.Fatal(err)
	}
	if got, e := w.alice.RoomReply(root.LID, asked.LID); e != nil || got == nil {
		t.Fatalf("stored reply after removal: %+v %v", got, e)
	}
}

func TestP6FixQueuedRoomAskIsNotACompletedOutput(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	from := p6Member(t, w.alice, w.alice, conv)
	to := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "source at target", func() bool { return stateAt(t, w.bob, from.PID).Claimable() })
	for _, stop := range stops {
		stop()
	}
	root, err := w.alice.AskAgent(tctx(t), from.PID, envelope.KindQuestion, "running source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, root.LID); err != nil {
		t.Fatal(err)
	}
	child, err := w.alice.SendRoomAsk(tctx(t), root.LID, to.PID, envelope.KindQuestion, "queued nested request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.store.db.Exec(`UPDATE outbox SET state=? WHERE lid=?`, stateQueued, child.LID); err != nil {
		t.Fatal(err)
	}
	if n, err := w.alice.holdEndedOutputs(""); err != nil || n != 0 {
		t.Fatalf("nested request treated as completed output: held=%d err=%v", n, err)
	}
	var n int
	if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND state<>?`, child.LID, stateQueued).Scan(&n); err != nil || n != 0 {
		t.Fatalf("queued nested copies changed: n=%d err=%v", n, err)
	}
	env := groupTurnEnvelope(t, w.alice, child.ID)
	if handled, allowed, err := w.alice.mayDeliverGroupParticipation(env); err != nil || !handled || !allowed {
		t.Fatalf("running origin refused: handled=%v allowed=%v err=%v", handled, allowed, err)
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelled, root.LID); err != nil {
		t.Fatal(err)
	}
	if _, allowed, err := w.alice.mayDeliverGroupParticipation(env); err != nil || allowed {
		t.Fatalf("stopped origin still allowed: allowed=%v err=%v", allowed, err)
	}
}

func TestP6FixRemovedAudienceDoesNotKillOtherAnswer(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	removed := p6Member(t, w.alice, w.alice, conv)
	target := p6Member(t, w.alice, w.bob, conv)
	if err := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	ask, err := carol.AskAgent(tctx(t), target.PID, envelope.KindQuestion, "answer survives another agent leaving")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, ask.LID, stateAwaiting)
	if _, err = w.alice.DismissParticipation(tctx(t), removed.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "target sees removed scope", func() bool { return stateAt(t, w.bob, removed.PID).State == PartDismissed })
	if err = w.bob.Accept(ask.LID); err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, carol, conv, ask.LID)
	if answer.Human == nil || slices.ContainsFunc(answer.Human.Audience, func(s envelope.HumanScope) bool { return s.PID == removed.PID }) {
		t.Fatalf("stale answer audience: %+v", answer.Human)
	}
}

func TestP6FixLateJoinMembershipAndShareVisibility(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	// Seal while Dave is absent and no agent has accepted future context.
	stops[carol]()
	injectFaults(carol).add("POST", "/v1/messages", 2, false)
	private, err := carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "UNSELECTED_BEFORE_DAVE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(private.Copies) != 2 {
		t.Fatalf("pre-join sealed recipients: %+v", private.Copies)
	}
	var n int
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND lid=?`, conv, private.LID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("turn reached host before late join: %d %v", n, err)
	}
	// The sender knows its exact sealed roster; this evidence must survive.
	if err = carol.store.db.QueryRow(`SELECT count(*) FROM room_turn_readers WHERE conv=? AND lid=?`, conv, private.LID).Scan(&n); err != nil || n != len(packet.State.Members) {
		t.Fatalf("sender sealed reader proof: %d %v", n, err)
	}
	member := p6Member(t, w.alice, w.bob, conv)
	dave := proofReader(t, w, "dave")

	runAgent(t, dave)
	publishGroupFixtureCaps(t, dave, true)
	next := groupInteractionRejoin(t, w.alice, dave, packet)
	eventually(t, "late join has original membership", func() bool { p, e := dave.Participation(member.PID); return e == nil && p.Claimable() && p.Member })
	same, err := dave.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "")
	if err != nil || same.PID != member.PID {
		t.Fatalf("late join duplicate: %+v %v", same, err)
	}
	eventually(t, "host knows late join", func() bool { p, e := w.bob.GroupContext(conv); return e == nil && p.State.Seq == next.State.Seq })
	if err = carol.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pre-join turn reaches host after admission", func() bool {
		return w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND lid=?`, conv, private.LID).Scan(&n) == nil && n == 1
	})
	if err = dave.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND lid=?`, conv, private.LID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("late member received pre-join turn: %d %v", n, err)
	}
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM room_context WHERE conv=? AND pid=? AND lid=?`, conv, member.PID, private.LID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pre-agent turn became ambient context: %d %v", n, err)
	}
	davePerson, _, err := dave.store.selfPerson(dave.Address)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM room_turn_readers WHERE conv=? AND lid=? AND person=?`, conv, private.LID, davePerson.info.Person).Scan(&n); err != nil || n != 0 {
		t.Fatalf("late member gained unproven reader authority: %d %v", n, err)
	}
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM room_turn_readers WHERE conv=? AND lid=?`, conv, private.LID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("received fan must prove only author and host: %d %v", n, err)
	}
	m, err := dave.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	me, _, err := dave.store.selfPerson(dave.Address)
	if err != nil {
		t.Fatal(err)
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: member.PID, Type: protocol.EventShare, Prev: member.Invite, TS: time.Now().Unix(), Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: dave.Address, Fingerprint: dave.Self().Fingerprint()}, Host: &protocol.ParticipationHost{Person: member.Host.Person, Address: member.Host.Address, Fingerprint: member.Host.Fingerprint, AgentID: member.AgentID}, Grant: []protocol.GrantRef{{LID: private.LID, Fingerprint: carol.Self().Fingerprint()}}, Audience: protocol.AudienceRoom}
	if err = m.bindGroupInvite(&ev); err != nil {
		t.Fatal(err)
	}
	ev.Sign(dave.id.Sign)
	if err = dave.recordAndSend(tctx(t), ev); err != nil {
		t.Fatal(err)
	}
	eventually(t, "forged share recorded without access", func() bool {
		p, e := w.bob.Participation(member.PID)
		return e == nil && slices.Contains(p.Shares, ev.Hash()) && !slices.Contains(p.Grant, ev.Grant[0])
	})
	if err = w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	ask, err := dave.AskAgent(tctx(t), member.PID, envelope.KindQuestion, "late join question")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, ask.LID, stateAwaiting)
	if err = w.bob.Accept(ask.LID); err != nil {
		t.Fatal(err)
	}
	replyAt(t, dave, conv, ask.LID)
	if strings.Contains(stub.last(), "UNSELECTED_BEFORE_DAVE") {
		t.Fatal("share granted unseen earlier history")
	}
}

func TestP6FixCapturedTurnsOnlyAndEpochFence(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	early, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "BEFORE_MEMBERSHIP"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "earlier turn held", func() bool {
		return inboxHas(t, w.alice, early.Copies[0].ID) || strings.Contains(strings.Join(convBodies(t, w.alice, conv), "\n"), "BEFORE_MEMBERSHIP")
	})
	from := p6Member(t, w.alice, w.alice, conv)
	tx, err := w.alice.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = recordRoomContext(tx, envelope.Inner{Conv: conv, LID: early.LID, Kind: envelope.KindMessage}, w.bob.Self().Fingerprint(), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM room_context WHERE conv=? AND pid=? AND lid=?`, conv, from.PID, early.LID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("late earlier turn granted: %d %v", n, err)
	}
	// Bob can capture only consent he has received, not Alice's local state.
	eventually(t, "sender knows accepted membership", func() bool {
		p := stateAt(t, w.bob, from.PID)
		return p.Claimable() && p.Invite == from.Invite && p.Decision == from.Decision
	})
	future, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "AFTER_MEMBERSHIP"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "future turn captured", func() bool {
		var count int
		e := w.alice.store.db.QueryRow(`SELECT count(*) FROM room_context WHERE conv=? AND pid=? AND lid=?`, conv, from.PID, future.LID).Scan(&count)
		return e == nil && count == 1
	})
	target := p6Member(t, w.alice, w.bob, conv)
	ask, err := w.alice.AskAgent(tctx(t), target.PID, envelope.KindQuestion, "saved copy epoch")
	if err != nil {
		t.Fatal(err)
	}
	var saved ConvCopy
	for _, copy := range ask.Copies {
		if copy.To == carol.Address {
			saved = copy
		}
	}
	if saved.ID == "" {
		t.Fatal("no third member copy")
	}
	env := groupTurnEnvelope(t, w.alice, saved.ID)
	person, _, _ := carol.store.selfPerson(carol.Address)
	removed, err := w.alice.RemoveGroupMember(tctx(t), conv, person.info.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupInteractionRejoin(t, w.alice, carol, removed)
	if _, err = w.alice.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, stateQueued, saved.ID); err != nil {
		t.Fatal(err)
	}
	if handled, allowed, e := w.alice.mayDeliverGroupParticipation(env); e != nil || !handled || allowed {
		t.Fatalf("old admission copied: %v %v %v", handled, allowed, e)
	}
}

func TestP6FixOutsideAdmissionAndRemovalRights(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	outside := proofReader(t, w, "outside")
	runAgent(t, outside)
	publishGroupFixtureCaps(t, outside, true)
	if _, err := w.bob.InviteAgent(tctx(t), conv, outside.Address, nil, nil, ""); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("non-admin outside add: %v", err)
	}
	member := p6Member(t, w.bob, w.alice, conv)
	if !member.Claimable() {
		t.Fatal("non-admin could not add member-hosted agent")
	}
	visitor := p6Member(t, w.alice, outside, conv)
	if !visitor.External || !visitor.Member {
		t.Fatalf("not an outside member: %+v", visitor)
	}
	eventually(t, "other member sees outside agent", func() bool { return stateAt(t, carol, visitor.PID).Claimable() })
	if _, err := carol.DismissParticipation(tctx(t), visitor.PID); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("unrelated member removed outside agent: %v", err)
	}
	// Pinned signatures alone cannot grant an ordinary member admin rights.
	events, err := w.alice.store.participationEvents(conv, visitor.PID)
	if err != nil {
		t.Fatal(err)
	}
	var original protocol.ParticipationEvent
	for _, ev := range events {
		if ev.Type == protocol.EventInvite {
			original = ev
		}
	}
	bob, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.bob.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	original.PID = protocol.NewID()
	original.Author = protocol.EventAuthor{Person: bob.info.Person, Roster: bob.info.Roster, Address: w.bob.Address, Fingerprint: bob.info.Fingerprint, GroupAdmission: m.keyEpoch(bob.info.Fingerprint)}
	original.Sign(w.bob.id.Sign)
	if ok, err := m.verifyInviteEpoch(w.bob.store.db, original); err != nil || ok {
		t.Fatalf("non-admin signed invitation counted: %v %v", ok, err)
	}
	if _, err = outside.DismissParticipation(tctx(t), visitor.PID); err != nil {
		t.Fatalf("outside host owner removal: %v", err)
	}
	eventually(t, "host removal reaches group", func() bool { return stateAt(t, w.alice, visitor.PID).State == PartDismissed })
	visitor = p6Member(t, w.alice, outside, conv)
	if _, err = w.alice.DismissParticipation(tctx(t), visitor.PID); err != nil {
		t.Fatalf("admin removal: %v", err)
	}
}

func TestP6FixLocalAndNestedHarnessUseRealCLI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "agentnet")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/agentnet")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %v %s", err, out)
	}
	stub := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	rootScript := filepath.Join(t.TempDir(), "root.sh")
	childScript := filepath.Join(t.TempDir(), "child.sh")
	log := filepath.Join(t.TempDir(), "room-env")
	t.Setenv("P6_CLI", bin)
	t.Setenv("P6_ENV_LOG", log)
	t.Setenv("P6_RELEASE", log+".release")
	t.Setenv(RoomRequestEnv, "inherited-must-be-stripped")
	for name, script := range map[string]string{"p6root": rootScript, "p6child": childScript} {
		Harnesses[name] = harness{bin: script, stdin: true}
		t.Cleanup(func() { delete(Harnesses, name) })
	}
	os.WriteFile(rootScript, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'root:%s:%s\\n' \"$AGENTNET_ROOM_REQUEST\" \"$AGENTNET_REQUEST_ID\" >>\"$P6_ENV_LOG\"\nwhile [ ! -f \"$P6_ENV_LOG.ask\" ]; do sleep 0.02; done\n\"$P6_CLI\" --home \"$AGENTNET_HOME\" room ask --pid \"$P6_CHILD_PID\" CHILD_REQUEST\n"), 0700)
	os.WriteFile(childScript, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'child:%s:%s\\n' \"$AGENTNET_ROOM_REQUEST\" \"$AGENTNET_REQUEST_ID\" >>\"$P6_ENV_LOG\"\nwhile [ ! -f \"$P6_RELEASE\" ]; do sleep 0.02; done\n\"$P6_CLI\" --home \"$AGENTNET_HOME\" room ask --pid \"$P6_REMOTE_PID\" REMOTE_REQUEST\n"), 0700)
	named, err := w.alice.CreateLocalAgent("nested", Responder{Harness: "p6child", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	from := p6Member(t, w.alice, w.alice, conv)
	child, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, named.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "named local member", func() bool { return stateAt(t, w.alice, child.PID).Claimable() })
	remote := p6Member(t, w.alice, w.bob, conv)
	t.Setenv("P6_CHILD_PID", child.PID)
	t.Setenv("P6_REMOTE_PID", remote.PID)
	if err = w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.SetResponder(&Responder{Harness: "p6root", Dir: stub.dir, Timeout: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	ask, err := w.alice.AskAgent(tctx(t), from.PID, envelope.KindQuestion, "ROOT_REQUEST")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "parent is running before nested ask", func() bool { return jobState(t, w.alice, ask.ID) == stateRunning })
	if resume, err := w.alice.PauseForAppUpdate(); err == nil {
		resume()
		t.Fatal("update admitted while parent was preparing nested ask")
	}
	// The accepted idle-switch state must block unrelated roots without
	// deadlocking exact descendants of work that already owns the idle fence.
	w.alice.update.Lock()
	w.alice.update.pending = &UpdateRequest{ID: "nested-pending-update", To: "v9.9.9"}
	w.alice.update.Unlock()
	if w.alice.runNext(tctx(t), nil) {
		t.Fatal("pending update admitted a new root job")
	}
	if err := os.WriteFile(log+".ask", nil, 0600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "nested local job exists", func() bool {
		var n int
		e := w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE pid=? AND state IN (?,?) AND local=1`, child.PID, stateAgentWaiting, stateRunning).Scan(&n)
		return e == nil && n > 0
	})
	// The real daemon must claim the same-host child while its parent waits;
	// no test-only second runNext may hide serial scheduler deadlock.
	eventually(t, "parent and nested local jobs both running", func() bool {
		var n int
		err := w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE pid IN (?,?) AND state=? AND local=1`, from.PID, child.PID, stateRunning).Scan(&n)
		return err == nil && n == 2
	})
	if w.alice.updatePending() == nil {
		t.Fatal("nested claim discarded pending update")
	}
	w.alice.update.Lock()
	w.alice.update.pending = nil
	w.alice.update.Unlock()
	if resume, err := w.alice.PauseForAppUpdate(); err == nil {
		resume()
		t.Fatal("whole-app update allowed while parent and child jobs were running")
	}
	if err := os.WriteFile(log+".release", nil, 0600); err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, ask.LID)
	eventually(t, "nested runs release whole-app idle fence", func() bool {
		resume, err := w.alice.PauseForAppUpdate()
		if err != nil {
			return false
		}
		resume()
		return true
	})
	// A cancelled waiting parent must cancel its already-running child, never
	// detach it into fresh authority or emit the remote follow-up.
	if err := os.Remove(log + ".release"); err != nil {
		t.Fatal(err)
	}
	second, err := w.alice.AskAgent(tctx(t), from.PID, envelope.KindQuestion, "CANCEL_PARENT")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "second parent and child run naturally", func() bool {
		var n int
		e := w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE pid IN (?,?) AND state=? AND local=1`, from.PID, child.PID, stateRunning).Scan(&n)
		return e == nil && n == 2
	})
	eventually(t, "second child harness started", func() bool {
		data, err := os.ReadFile(log)
		return err == nil && len(strings.Split(strings.TrimSpace(string(data)), "\n")) == 4
	})
	if err := w.alice.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cancelled parent stops local descendant", func() bool {
		var n int
		e := w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE pid IN (?,?) AND state=? AND local=1`, from.PID, child.PID, stateRunning).Scan(&n)
		return e == nil && n == 0 && jobState(t, w.alice, second.ID) == stateCancelled
	})
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("env runs %q", data)
	}
	for _, line := range lines {
		parts := strings.Split(line, ":")
		if len(parts) != 3 || !protocol.ValidID(parts[1]) || parts[2] != "" {
			t.Fatalf("local room env/progress binding: %q", line)
		}
	}
}
