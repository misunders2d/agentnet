package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func syncedProposalFixture(t *testing.T) (*world, *Agent, string) {
	t.Helper()
	w, phone, _, _ := historyCatchupFixture(t, 0)
	request, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "Prepare the report?"})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindAnswer, Status: envelope.StatusProposal, ReplyTo: request.ID, Body: "Write the full report in Russian.\nSave report.md."})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	var env envelope.Envelope
	if err = w.bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, proposal.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if _, err = w.alice.deviceHistoryPage(phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := w.alice.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='device-history' ORDER BY rowid`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	var copies []envelope.Envelope
	for rows.Next() {
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		copies = append(copies, env)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, env := range copies {
		groupGovernanceDeliver(t, w.alice, phone, env)
	}
	return w, phone, proposal.ID
}

func TestSyncedProposalCanBeConfirmedByOwnHuman(t *testing.T) {
	w, phone, id := syncedProposalFixture(t)
	if !w.alice.CanConfirmProposal(id) {
		t.Fatal("original proposal is not confirmable")
	}
	if !phone.CanConfirmProposal(id) {
		_, err := phone.confirmableProposal(id)
		t.Fatalf("authenticated own-device proposal cannot be confirmed on the linked human device: %v", err)
	}
}

func TestProposalOriginalRevisedSiblingChoice(t *testing.T) {
	stub := installStub(t, "answer")
	w, phone, id := syncedProposalFixture(t)
	var sent [2]SendResult
	var errs [2]error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sent[0], errs[0] = w.alice.ConfirmProposal(tctx(t), id) }()
	go func() {
		defer wg.Done()
		sent[1], errs[1] = phone.ConfirmRevisedProposal(tctx(t), id, "Write the full report in English.\nSave report.md.")
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("choice %d: %v", i, err)
		}
	}
	if sent[0].ID == sent[1].ID {
		t.Fatal("different own device identities were conflated")
	}
	eventually(t, "exact host choice and duplicate", func() bool {
		var choices, duplicates int
		if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM proposal_choices WHERE ref=?`, id).Scan(&choices); err != nil {
			t.Fatal(err)
		}
		if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE reply_to=? AND state=?`, id, stateNotRun).Scan(&duplicates); err != nil {
			t.Fatal(err)
		}
		return choices == 1 && duplicates == 1
	})
	var winner string
	if err := w.bob.store.db.QueryRow(`SELECT task FROM proposal_choices WHERE ref=?`, id).Scan(&winner); err != nil {
		t.Fatal(err)
	}
	if row := inboxRow(t, w.bob, winner); row.State != stateAwaiting {
		t.Fatalf("confirmation bypassed ordinary foreign task permission: %s", row.State)
	}
	v, err := w.bob.ProposalOf(winner)
	if err != nil || v == nil || v.Proposal != "Write the full report in Russian.\nSave report.md." || v.Question != "Prepare the report?" || v.Edited != (winner == sent[1].ID) {
		t.Fatalf("provenance %+v %v", v, err)
	}
	// Sender reload retains the exact chosen bytes; a changed retry cannot
	// silently change them or mint another task.
	phone.Close()
	phone, err = Open(phone.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	again, err := phone.ConfirmRevisedProposal(tctx(t), id, "Write the full report in English.\nSave report.md.")
	if err != nil || again.ID != sent[1].ID {
		t.Fatalf("restart retry %+v %v", again, err)
	}
	if _, err = phone.ConfirmRevisedProposal(tctx(t), id, "Different task"); err == nil || !strings.Contains(err.Error(), "different text") {
		t.Fatalf("changed retry: %v", err)
	}
	if _, err = phone.ConfirmProposal(tctx(t), id); err == nil {
		t.Fatal("original silently replaced an edited choice")
	}
	setResponder(t, w.bob, "stub", stub.dir, time.Minute)
	if err = w.bob.Accept(winner); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, winner, stateAnswered)
	if stub.count() != 1 {
		t.Fatalf("proposal choice ran %d times", stub.count())
	}
}

func TestOwnRevisedProposalUsesOrdinaryTaskPermission(t *testing.T) {
	st := installStub(t, "propose")
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	phone := linkedVia(t, w.alice, "phone", func(ctx context.Context, id string) error { return w.alice.DecideLink(ctx, id, true) })
	if err := w.alice.Approve(phone.Address); err != nil {
		t.Fatal(err)
	}
	request, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "Which report?"})
	if err != nil {
		t.Fatal(err)
	}
	var p Message
	eventually(t, "own proposal", func() bool {
		var ok bool
		p, ok = findReply(phone, request.ID)
		return ok && p.Status == envelope.StatusProposal
	})
	task, err := phone.ConfirmRevisedProposal(tctx(t), p.ID, "Write the report in English.")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.alice, task.ID, stateAwaiting)
	var holds bool
	if err = w.alice.store.db.QueryRow(`SELECT `+ownProposalHolds+` FROM inbox WHERE id=?`, task.ID).Scan(&holds); err != nil || holds {
		t.Fatalf("revised task inherited exact proposal exception: %v %v", holds, err)
	}
	v, err := w.alice.ProposalOf(task.ID)
	if err != nil || v == nil || !v.Edited || v.Task != "Write the report in English." || v.Proposal != p.Body {
		t.Fatalf("edited provenance: %+v %v", v, err)
	}
	if st.count() != 1 {
		t.Fatalf("edited task ran without normal permission: runs=%d", st.count())
	}
	if err = w.alice.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.alice, task.ID, stateNeedHuman)
	if st.count() != 2 {
		t.Fatalf("accepted revised task runs=%d", st.count())
	}
}

func TestProposalChoiceIgnoresUnboundReplies(t *testing.T) {
	s := securityStore(t)
	if _, err := s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES('question','asker/desk',1,'question','Original question',1,'answered','asker-key');
 INSERT INTO outbox(id,recipient,body,envelope,state,created_at,kind,status,reply_to) VALUES('proposal','asker/desk','Exact task','{}','delivered',1,'answer','proposal','question')`); err != nil {
		t.Fatal(err)
	}
	base := envelope.Inner{From: "asker/desk", To: "host/desk", Kind: envelope.KindTask, ReplyTo: "proposal", Body: "Edited task"}
	base.ID = proposalTaskID("asker-key", "", base.ReplyTo, base.To)
	for _, tc := range []struct {
		name   string
		change func(*envelope.Inner)
		key    string
	}{
		{"ordinary reply", func(n *envelope.Inner) { n.ID = "ordinary" }, "asker-key"},
		{"wrong key", func(*envelope.Inner) {}, "changed-key"},
		{"wrong question", func(n *envelope.Inner) { n.ReplyTo = "other"; n.ID = proposalTaskID("asker-key", "", n.ReplyTo, n.To) }, "asker-key"},
		{"wrong executor", func(n *envelope.Inner) {
			n.Target = &envelope.Target{Address: "host/desk", Fingerprint: "host-key", AgentID: "other"}
		}, "asker-key"},
		{"wrong topic", func(n *envelope.Inner) { n.Topic = "other" }, "asker-key"},
		{"replica", func(n *envelope.Inner) { n.Replica = true }, "asker-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := base
			tc.change(&n)
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			proposal, _, err := claimProposalChoice(tx, n, tc.key)
			if err != nil || proposal != "" {
				t.Fatalf("unbound task reserved a choice: %q %v", proposal, err)
			}
			var count int
			if err = tx.QueryRow(`SELECT count(*) FROM proposal_choices`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid choice persisted: %d %v", count, err)
			}
		})
	}
}

func TestRevisedProposalWaitsForHostChoiceCapability(t *testing.T) {
	w, phone, id := syncedProposalFixture(t)
	signCapsAfter(t, w.bob, without(ownCaps, protocol.CapOwnSyncV3))
	task, err := phone.ConfirmRevisedProposal(tctx(t), id, "Write in English.")
	if err != nil {
		t.Fatal(err)
	}
	if got := outboxState(t, phone, task.ID); got != stateConvWaiting {
		t.Fatalf("old host task state=%s", got)
	}
	var delivered int
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, task.ID).Scan(&delivered); err != nil || delivered != 0 {
		t.Fatalf("new choice reached old host: %d %v", delivered, err)
	}
	signCapsAfter(t, w.bob, ownCaps)
	phone.releaseConv(tctx(t), []string{protocol.FeatureEnv2, protocol.FeaturePerson, protocol.FeatureCaps})
	if err = phone.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if got := outboxState(t, phone, task.ID); got != protocol.StateDelivered && got != protocol.StateCustody {
		t.Fatalf("upgraded exact choice state=%s", got)
	}
}

func TestSyncedProposalChoiceRemovesActionAndKeepsProvenance(t *testing.T) {
	w, phone, id := syncedProposalFixture(t)
	task, err := phone.ConfirmRevisedProposal(tctx(t), id, "Write the full report in English.\nSave report.md.")
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if _, err = phone.deviceHistoryPage(w.alice.Self()); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := phone.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='device-history' ORDER BY rowid`, w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	var copies []envelope.Envelope
	for rows.Next() {
		var raw string
		var env envelope.Envelope
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		copies = append(copies, env)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, env := range copies {
		groupGovernanceDeliver(t, phone, w.alice, env)
	}
	if w.alice.CanConfirmProposal(id) {
		t.Fatal("synced explicit choice left original proposal actionable")
	}
	if _, err = w.alice.ConfirmProposal(tctx(t), id); err == nil {
		t.Fatal("linked choice allowed another send")
	}
	v, err := w.alice.ProposalOf(task.ID)
	if err != nil || v == nil || !v.Edited || v.Task != "Write the full report in English.\nSave report.md." || v.ConfirmedBy != phone.Address || v.Asker != w.alice.Address {
		t.Fatalf("synced choice provenance: %+v %v", v, err)
	}
	var replica bool
	var state string
	if err = w.alice.store.db.QueryRow(`SELECT replica,state FROM inbox WHERE id=?`, task.ID).Scan(&replica, &state); err != nil || !replica || state != "" {
		t.Fatalf("choice copy became executable: replica=%v state=%q err=%v", replica, state, err)
	}
}

func TestOwnHostAndPhoneProposalChoiceRunsOnce(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	phone := linkedVia(t, w.bob, "phone", func(ctx context.Context, id string) error { return w.bob.DecideLink(ctx, id, true) })
	pid := participate(t, w, conv, nil, nil)
	eventually(t, "phone participation", func() bool { return stateAt(t, phone, pid).Claimable() })
	st.mode("propose")
	q, err := w.bob.AskAgent(tctx(t), pid, envelope.KindQuestion, "which deploy?")
	if err != nil {
		t.Fatal(err)
	}
	local := proposalReplyAt(t, w.bob, conv, q.ID)
	var requestLID string
	if err = w.bob.store.db.QueryRow(`SELECT lid FROM outbox WHERE id=?`, q.ID).Scan(&requestLID); err != nil {
		t.Fatal(err)
	}
	remote := proposalReplyAt(t, phone, conv, requestLID)
	if !phone.CanConfirmProposal(remote.ID) {
		_, e := phone.confirmableProposal(remote.ID)
		t.Fatalf("sibling cannot confirm own host's question: %v", e)
	}
	st.mode("closed")
	var wg sync.WaitGroup
	var errs [2]error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = w.bob.ConfirmRevisedProposal(tctx(t), local.ID, "Restart the deploy from step 2.")
	}()
	go func() { defer wg.Done(); _, errs[1] = phone.ConfirmProposal(tctx(t), remote.ID) }()
	wg.Wait()
	for _, e := range errs {
		if e != nil && !strings.Contains(e.Error(), "already confirmed") {
			t.Fatal(e)
		}
	}
	var winner string
	eventually(t, "host choice", func() bool {
		e := w.bob.store.db.QueryRow(`SELECT task FROM proposal_choices WHERE conv=? AND ref=?`, conv, local.LID).Scan(&winner)
		return e == nil
	})
	waitState(t, w.bob, winner, stateAnswered)
	if st.runs() != 2 {
		t.Fatalf("question and one chosen task expected, runs=%d", st.runs())
	}
	if w.bob.CanConfirmProposal(local.ID) {
		t.Fatal("host winner remains actionable")
	}
}

func TestProposalChoiceSurvivesHostRestart(t *testing.T) {
	home := t.TempDir()
	seedFixtureStore(t, home)
	s, err := openStore(filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.db.Close() }()
	if _, err = s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES('question','asker/desk',1,'question','Why?',1,'answered','asker-key');
 INSERT INTO outbox(id,recipient,body,envelope,state,created_at,kind,status,reply_to) VALUES('proposal','asker/desk','Original task','{}','delivered',1,'answer','proposal','question')`); err != nil {
		t.Fatal(err)
	}
	insert := func(in envelope.Inner) {
		t.Helper()
		tx, e := s.db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e = insertInner(tx, in, "asker-key"); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	insert(envelope.Inner{ID: "legacy-original", From: "asker/desk", To: "host/desk", Kind: envelope.KindTask, ReplyTo: "proposal", Body: "Original task"})
	s.db.Close()
	s, err = openStore(filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	id := proposalTaskID("asker-key", "", "proposal", "host/desk")
	insert(envelope.Inner{ID: id, From: "asker/desk", To: "host/desk", Kind: envelope.KindTask, ReplyTo: "proposal", Body: "Edited task"})
	var chosen, state string
	if err = s.db.QueryRow(`SELECT task FROM proposal_choices WHERE ref='proposal'`).Scan(&chosen); err != nil || chosen != "legacy-original" {
		t.Fatalf("restart choice=%q %v", chosen, err)
	}
	if err = s.db.QueryRow(`SELECT state FROM inbox WHERE id=?`, id).Scan(&state); err != nil || state != stateNotRun {
		t.Fatalf("restart duplicate=%q %v", state, err)
	}
}

func TestProposalReplyKeepsLegacyAskerBinding(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)
	signCapsAfter(t, w.alice, without(ownCaps, protocol.CapOwnSyncV3))
	st.mode("propose")
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "Legacy question")
	if err != nil {
		t.Fatal(err)
	}
	p := proposalReplyAt(t, w.alice, conv, q.ID)
	var physical string
	if err = w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND kind='question' AND sender=?`, conv, w.alice.Address).Scan(&physical); err != nil {
		t.Fatal(err)
	}
	if p.ReplyTo != physical {
		t.Fatalf("legacy asker lost physical source: %s != %s", p.ReplyTo, physical)
	}
	st.mode("closed")
	task, err := w.alice.ConfirmProposal(tctx(t), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if err = w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAnswered)
	if st.runs() != 2 {
		t.Fatalf("legacy exact confirmation runs=%d", st.runs())
	}
	signCapsAfter(t, w.alice, ownCaps)
	st.mode("propose")
	newQ, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "Current question")
	if err != nil {
		t.Fatal(err)
	}
	newP := proposalReplyAt(t, w.alice, conv, newQ.ID)
	var logical string
	if err = w.alice.store.db.QueryRow(`SELECT lid FROM outbox WHERE id=?`, newQ.ID).Scan(&logical); err != nil {
		t.Fatal(err)
	}
	if newP.ReplyTo != logical {
		t.Fatal("verified current asker did not receive shared logical proposal context")
	}
}
