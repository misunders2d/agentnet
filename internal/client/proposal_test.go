package client

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A question whose answer needs an action the run may not take comes back
// as a proposal: an answer with status proposal whose body is exactly the
// task the agent wrote under its marker (MEL-521). Nothing runs.
func TestRunJobProposal(t *testing.T) {
	st := installStub(t, "propose")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "is the changelog up to date?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	eventually(t, "the proposal", func() bool {
		var ok bool
		m, ok = findReply(w.alice, q.ID)
		return ok
	})
	if m.Kind != envelope.KindAnswer || m.Status != envelope.StatusProposal || m.Body != "Update CHANGELOG.md: add the 0.8.0 entry." {
		t.Fatalf("proposal %+v", m)
	}
	if row := inboxRow(t, w.bob, q.ID); row.State != stateAnswered || row.Detail != "" {
		t.Fatalf("the question on the host: %+v", row)
	}
	// The question's run was asked to propose, and to keep needs-human for
	// decisions only the person makes.
	data := mustRead(t, st.log+".stdin")
	if !strings.Contains(data, proposeMarker) || !strings.Contains(data, "decide something only they can") {
		t.Fatalf("prompt:\n%s", data)
	}
	// The asker's execution view settles on it: the proposal is its last word.
	if e := execOf(t, w.alice, q.ID); e != nil && e.State != envelope.StatusProposal && e.State != "queued" && e.State != "running" {
		t.Fatalf("exec %+v", e)
	}
}

// A cut-off proposal is never sent: the person sees it instead.
func TestTruncatedProposalNeedsHuman(t *testing.T) {
	st := installStub(t, "proposebig")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateNeedHuman)
	if row := inboxRow(t, w.bob, q.ID); !strings.Contains(row.Detail, "cut off") {
		t.Fatalf("detail %q", row.Detail)
	}
	time.Sleep(200 * time.Millisecond)
	if m, ok := findReply(w.alice, q.ID); ok {
		t.Fatalf("a cut-off proposal was sent: %+v", m)
	}
}

// Only a question's own run proposes: a task run that writes the marker is
// handed to the person.
func TestProposalFromTaskNeedsHuman(t *testing.T) {
	st := installStub(t, "propose")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "do it", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateNeedHuman)
	if row := inboxRow(t, w.bob, task.ID); !strings.Contains(row.Detail, "only a question's own answer can carry") || !strings.Contains(row.Detail, "CHANGELOG") {
		t.Fatalf("detail %q", row.Detail)
	}
}

// A conversation fans out with physical per-recipient request IDs. Replies
// name the executor's copy; all copies still describe this one local turn.
func proposalReplyAt(t *testing.T, a *Agent, conv, id string) ConvMessage {
	t.Helper()
	ids := map[string]bool{id: true}
	var lid string
	if err := a.store.db.QueryRow(`SELECT lid FROM outbox WHERE id=?`, id).Scan(&lid); err == nil {
		ids[lid] = true
		copies, err := a.SentCopies(lid)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range copies {
			ids[c.ID] = true
		}
	}
	var p ConvMessage
	eventually(t, "proposal/result at "+a.Address, func() bool {
		var n int
		p, n = convMsg(t, a, conv, func(m ConvMessage) bool { return ids[m.ReplyTo] })
		return n == 1
	})
	return p
}

// Conversation proposals retain the read-only run and normal recipient task
// approval, exact provenance and same-chat results. Concurrent taps send once.
func TestAgentProposalInConversation(t *testing.T) {
	for _, own := range []bool{false, true} {
		t.Run(fmt.Sprint("own=", own), func(t *testing.T) {
			st := installAgentStub(t)
			w, conv, _, _ := agentWorld(t)
			setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
			st.mode("propose")
			pid := participate(t, w, conv, nil, nil)
			asker := w.alice
			if own {
				asker = w.bob
			}
			q, err := asker.AskAgent(tctx(t), pid, envelope.KindQuestion, "why did the deploy fail?")
			if err != nil {
				t.Fatal(err)
			}
			p := proposalReplyAt(t, asker, conv, q.ID)
			if p.Status() != envelope.StatusProposal || p.Body != "Restart the deploy from step 3." || !asker.CanConfirmProposal(p.ID) {
				t.Fatalf("proposal %+v", p)
			}
			if !strings.Contains(st.last(), proposeMarker) {
				t.Fatal("question prompt omits proposals")
			}
			st.mode("closed")
			var sends [2]SendResult
			var errs [2]error
			var wg sync.WaitGroup
			for i := range sends {
				wg.Add(1)
				go func() { defer wg.Done(); sends[i], errs[i] = asker.ConfirmProposal(tctx(t), p.ID) }()
			}
			wg.Wait()
			if errs[0] != nil || errs[1] != nil || sends[0].ID != sends[1].ID {
				t.Fatalf("double tap %+v %v", sends, errs)
			}
			task := sends[0]
			if asker.CanConfirmProposal(p.ID) {
				t.Fatal("confirmed proposal still actionable")
			}
			if !own {
				waitState(t, w.bob, task.ID, stateAwaiting)
			}
			v, err := w.bob.ProposalOf(task.ID)
			if err != nil || v == nil || v.Proposal != p.Body || v.Question != "why did the deploy fail?" || v.ConfirmedBy != asker.Address {
				t.Fatalf("provenance %+v %v", v, err)
			}
			if !own {
				if err := w.bob.Accept(task.ID); err != nil {
					t.Fatal(err)
				}
			}
			result := proposalReplyAt(t, asker, conv, task.ID)
			if result.Kind != envelope.KindResult || result.PID != pid || result.Body != "Finished the work" {
				t.Fatalf("result %+v", result)
			}
			if st.runs() != 2 || !strings.Contains(st.last(), "chose Do it") {
				t.Fatalf("runs %d prompt %s", st.runs(), st.last())
			}
		})
	}
}

// proposalAsked runs a question from alice to bob's proposing agent and
// returns the proposal alice received.
func proposalAsked(t *testing.T) (*world, *stub, Message, string) {
	t.Helper()
	st := installStub(t, "propose")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "is the changelog up to date?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	var p Message
	eventually(t, "the proposal", func() bool {
		var ok bool
		p, ok = findReply(w.alice, q.ID)
		return ok && p.Status == envelope.StatusProposal
	})
	return w, st, p, q.ID
}

// Do it sends exactly the stored proposal as a task replying to it; the
// host sees where it comes from, runs it only with the usual approval, and
// a second confirmation never runs twice (MEL-521).
func TestConfirmProposal(t *testing.T) {
	w, st, p, q := proposalAsked(t)
	sent, err := w.alice.ConfirmProposal(tctx(t), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var body, replyTo, kind string
	w.alice.store.db.QueryRow(`SELECT body, reply_to, coalesce(kind, json_extract(envelope, '$.kind')) FROM outbox WHERE id = ?`, sent.ID).Scan(&body, &replyTo, &kind)
	if body != p.Body || replyTo != p.ID || kind != envelope.KindTask {
		t.Fatalf("task %q %q %q", body, replyTo, kind)
	}
	if again, err := w.alice.ConfirmProposal(tctx(t), p.ID); err != nil || again.ID != sent.ID {
		t.Fatalf("confirmed twice: %+v %v", again, err)
	}
	waitState(t, w.bob, sent.ID, stateAwaiting) // the usual approval: alice may not task bob
	v, err := w.bob.ProposalOf(sent.ID)
	if err != nil || v == nil || v.QuestionID != q || v.ConfirmedBy != w.alice.Address || v.Proposal != p.Body || v.Question != "is the changelog up to date?" {
		t.Fatalf("provenance %+v %v", v, err)
	}
	// Another confirmation of the same proposal (another of alice's
	// devices, a retry): not run twice, and alice learns so.
	dup := receiverDirect(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: p.Body, ReplyTo: p.ID})
	if err := w.bob.verifyAndStore(tctx(t), dup); err != nil {
		t.Fatal(err)
	}
	if m := inboxRow(t, w.bob, dup.ID); m.State != stateNotRun || !strings.Contains(m.Detail, sent.ID) {
		t.Fatalf("second confirmation %+v", m)
	}
	// Accepted: the run is told what it carries out.
	if err := w.bob.Accept(sent.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run", func() bool {
		return strings.Contains(mustRead(t, st.log+".stdin"), "Authority is the asker's ordinary task approval")
	})
}

// A task with the proposal's text from anyone but its addressee is an
// ordinary task: no provenance, and it uses up nothing.
func TestProposalOnlyFromAddressee(t *testing.T) {
	w, _, p, _ := proposalAsked(t)
	carol := mustJoin(t, t.TempDir()+"/carol", w.aliceInvites("carol"), "desk")
	forged := receiverDirect(t, carol, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: p.Body, ReplyTo: p.ID})
	if err := w.bob.store.pin(carol.Self()); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.verifyAndStore(tctx(t), forged); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.bob.ProposalOf(forged.ID); v != nil {
		t.Fatalf("a third party's task carries provenance %+v", v)
	}
	sent, err := w.alice.ConfirmProposal(tctx(t), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, sent.ID, stateAwaiting) // not not_run: the forgery used up nothing
}

// A proposal edited or deleted after it was made, or one that is not a
// proposal, is not confirmed.
func TestEditedProposalNotConfirmable(t *testing.T) {
	w, _, p, q := proposalAsked(t)
	if _, err := w.alice.ConfirmProposal(tctx(t), q); err == nil {
		t.Fatal("a question was confirmed as a proposal")
	}
	if _, err := w.alice.store.db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state, verified_by, sub, received_ms, ref_id, ref_fp) VALUES(?, ?, 1, 'message', '{}', 1, '', ?, ?, 1, ?, ?)`,
		strings.Repeat("e", 32), w.bob.Address, w.bob.Self().Fingerprint(), envelope.SubRevision, p.ID, w.bob.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.ConfirmProposal(tctx(t), p.ID); err == nil || !strings.Contains(err.Error(), "edited or deleted") {
		t.Fatalf("an edited proposal: %v", err)
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE coalesce(kind, json_extract(envelope, '$.kind')) = ? AND reply_to = ?`, envelope.KindTask, p.ID).Scan(&n)
	if n != 0 {
		t.Fatal("a task was sent")
	}
}

// Owner D6/D9: the approved human phone (ordinary link, not --native)
// confirms its own agent's proposal, in the same DM, with no second accept.
func TestOwnPhoneConfirmsProposalOnce(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	phone := linkedVia(t, w.bob, "phone", func(ctx context.Context, id string) error { return w.bob.DecideLink(ctx, id, true) })
	pid := participate(t, w, conv, nil, nil)
	eventually(t, "phone receives the active participation", func() bool { return stateAt(t, phone, pid).Claimable() })
	st.mode("propose")
	q, err := phone.AskAgent(tctx(t), pid, envelope.KindQuestion, "should we restart?")
	if err != nil {
		t.Fatal(err)
	}
	p := proposalReplyAt(t, phone, conv, q.ID)
	st.mode("closed")
	task, err := phone.ConfirmProposal(tctx(t), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := proposalReplyAt(t, phone, conv, task.ID)
	if result.Kind != envelope.KindResult {
		t.Fatalf("result %+v", result)
	}
	again, err := phone.ConfirmProposal(tctx(t), p.ID)
	if err != nil || again.ID != task.ID || st.runs() != 2 {
		t.Fatalf("phone retry %+v %v runs=%d", again, err, st.runs())
	}
	if strings.Contains(st.last(), "--question-mode") {
		t.Fatal("task used question harness")
	}
}

// The native host enforces the same approved-human rule for device threads.
func TestOwnPhoneDeviceProposal(t *testing.T) {
	st := installStub(t, "propose")
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	phone := linkedVia(t, w.alice, "phone", func(ctx context.Context, id string) error { return w.alice.DecideLink(ctx, id, true) })
	if err := w.alice.Approve(phone.Address); err != nil {
		t.Fatal(err)
	}
	q, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "is the changelog current?"})
	if err != nil {
		t.Fatal(err)
	}
	var p Message
	eventually(t, "phone proposal", func() bool {
		var ok bool
		p, ok = findReply(phone, q.ID)
		return ok && p.Status == envelope.StatusProposal
	})
	task, err := phone.ConfirmProposal(tctx(t), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The task stub proposes again; the task does run but that second proposal
	// is handed to the person. No approval is needed to start this one run.
	waitState(t, w.alice, task.ID, stateNeedHuman)
	if st.count() != 2 {
		t.Fatalf("runs %d", st.count())
	}
}

func TestProposalRefusesAgentHostAndRemovedPhone(t *testing.T) {
	for _, role := range []string{"agent-host", "removed", "frozen", "pending"} {
		t.Run(role, func(t *testing.T) {
			st := installAgentStub(t)
			w, conv, _, _ := agentWorld(t)
			setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
			approve := func(ctx context.Context, id string) error { return w.bob.DecideLink(ctx, id, true) }
			if role == "agent-host" {
				approve = w.bob.ApproveAgentLink
			}
			phone := linkedVia(t, w.bob, "phone", approve)
			pid := participate(t, w, conv, nil, nil)
			eventually(t, "linked device participation", func() bool { return stateAt(t, phone, pid).Claimable() })
			st.mode("propose")
			q, err := phone.AskAgent(tctx(t), pid, envelope.KindQuestion, "should we restart?")
			if err != nil {
				t.Fatal(err)
			}
			p := proposalReplyAt(t, phone, conv, q.ID)
			switch role {
			case "removed":
				if err := w.bob.RemoveDevice(tctx(t), phone.Address); err != nil {
					t.Fatal(err)
				}
				// The Hub revokes the phone before it can fetch its replacement
				// roster. Apply the owner's signed step locally, as recovery/history
				// does, to test a known removed key without inventing new polling.
				current, _, err := w.bob.store.selfPerson(w.bob.Address)
				if err != nil {
					t.Fatal(err)
				}
				p7PinStep(t, phone, current.roster)
			case "frozen":
				if _, err := phone.store.db.Exec(`UPDATE persons SET state=? WHERE state=?`, personConflict, personSelf); err != nil {
					t.Fatal(err)
				}
			case "pending":
				if _, err := phone.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.bob.Address); err != nil {
					t.Fatal(err)
				}
			}
			if phone.CanConfirmProposal(p.ID) {
				t.Fatal("unsafe proposal offered Do it")
			}
			if _, err := phone.ConfirmProposal(tctx(t), p.ID); err == nil {
				t.Fatal("unsafe device confirmed proposal")
			}
			if st.runs() != 1 {
				t.Fatalf("unexpected task run: %d", st.runs())
			}
		})
	}
}

func TestGroupTopicProposalConfirmation(t *testing.T) {
	st := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	record, err := w.bob.CreateLocalAgent("Deploy helper", Responder{Harness: "agentstub", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	pid, err := w.alice.InviteNamedAgent(tctx(t), conv, w.bob.Address, record.ID, nil, nil, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "group agent invitation", func() bool { return stateAt(t, w.bob, pid.PID).State == PartInvited })
	if _, err = w.bob.AcceptParticipation(tctx(t), pid.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "group agent active", func() bool { return stateAt(t, w.alice, pid.PID).Claimable() })
	st.mode("propose")
	q, err := w.alice.AskAgentInTopic(tctx(t), pid.PID, envelope.KindQuestion, "should we restart?", "new", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := proposalReplyAt(t, w.alice, conv, q.ID)
	if p.Status() != envelope.StatusProposal || p.Topic == "" {
		t.Fatalf("group proposal %+v", p)
	}
	st.mode("closed")
	task, err := w.alice.ConfirmProposal(tctx(t), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The named host still waits for its owner's approval.
	waitState(t, w.bob, task.ID, stateAwaiting)
	if err = w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	result := proposalReplyAt(t, w.alice, conv, task.ID)
	if result.Kind != envelope.KindResult || result.Topic != p.Topic || st.runs() != 2 {
		t.Fatalf("group result %+v runs=%d", result, st.runs())
	}
}

// The same own-device confirmation rule holds for an accepted human guest,
// after its existing invitation/audience/key checks. No guest task grant is added.
func TestOwnPhoneGuestProposal(t *testing.T) {
	w, host, conv, _, st := humanWorld(t)
	phone := linkedVia(t, host, "phone", func(ctx context.Context, id string) error { return host.DecideLink(ctx, id, true) })
	humanTestCaps(t, phone)
	human, err := w.alice.InviteHuman(tctx(t), conv, phone.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone human invite", func() bool { return stateAt(t, phone, human.PID).State == PartInvited })
	if _, err = phone.AcceptParticipation(tctx(t), human.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone guest accepted", func() bool { return stateAt(t, w.alice, human.PID).HumanActive() })
	agent, err := w.alice.InviteAgent(tctx(t), conv, host.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "own external agent invite", func() bool { return stateAt(t, host, agent.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), agent.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone sees own agent", func() bool { return stateAt(t, phone, agent.PID).Claimable() })
	if err = host.Approve(phone.Address); err != nil {
		t.Fatal(err)
	}
	st.mode("propose")
	q, err := phone.AskAgent(tctx(t), agent.PID, envelope.KindQuestion, "should we restart?")
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalReplyAt(t, phone, conv, q.ID)
	st.mode("closed")
	task, err := phone.ConfirmProposal(tctx(t), proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := proposalReplyAt(t, phone, conv, task.ID)
	if result.Kind != envelope.KindResult || st.runs() != 2 {
		t.Fatalf("result %+v runs=%d", result, st.runs())
	}
}
