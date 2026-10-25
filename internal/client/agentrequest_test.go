package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestQueuedAgentRequestsKeepExactIDsAndIndependentTargets(t *testing.T) {
	stub := installAgentStub(t)
	Harnesses["agentstub2"] = Harnesses["agentstub"]
	t.Cleanup(func() { delete(Harnesses, "agentstub2") })
	named := map[string][]protocol.AgentRecord{}
	w, carol, packet, stops := groupTurnsFixture(t, func(a *Agent) {
		for _, h := range []string{"agentstub", "agentstub2"} {
			r, err := a.CreateLocalAgent("Same label", Responder{Harness: h, Dir: stub.dir})
			if err != nil {
				t.Fatal(err)
			}
			named[a.Address] = append(named[a.Address], r)
		}
	})
	if err := w.bob.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	invite := func(host *Agent, record protocol.AgentRecord) ParticipationInfo {
		t.Helper()
		p, err := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, host.Address, record.ID, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "exact named invitation", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
		if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
		eventually(t, "exact named acceptance", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
		return stateAt(t, w.alice, p.PID)
	}
	first := invite(w.bob, named[w.bob.Address][0])
	second := invite(w.bob, named[w.bob.Address][1])
	if first.AgentID == second.AgentID || first.Host.Address != second.Host.Address {
		t.Fatal("named identities collapsed")
	}
	offline := p6Member(t, w.alice, carol, packet.State.Conv)
	stops[carol]()
	start := time.Now()
	off, err := w.alice.QueueAgentRequest(tctx(t), protocol.NewID(), offline.PID, envelope.KindQuestion, "offline request", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := protocol.NewID()
	var sends [2]ConvSent
	var submitErrors [2]error
	var wg sync.WaitGroup
	for i := range sends {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sends[i], submitErrors[i] = w.alice.QueueAgentRequest(tctx(t), id, first.PID, envelope.KindQuestion, "first exact request", nil)
		}(i)
	}
	wg.Wait()
	for _, e := range submitErrors {
		if e != nil {
			t.Fatal(e)
		}
	}
	if sends[0].ID != sends[1].ID || sends[0].LID != id || sends[1].LID != id {
		t.Fatalf("retry changed IDs: %+v", sends)
	}
	other, err := w.alice.QueueAgentRequest(tctx(t), protocol.NewID(), second.PID, envelope.KindQuestion, "second exact request", nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("queued submits waited on offline target: %s", elapsed)
	}
	for _, sent := range []ConvSent{sends[0], other} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := w.alice.AwaitAgentRequest(ctx, sent.LID, 30*time.Second, nil)
		cancel()
		if err != nil || result.Answer == nil || result.Answer.Status != envelope.StatusDone {
			t.Fatalf("correlated result: %+v %v", result, err)
		}
	}
	for _, changed := range []struct {
		pid, kind, body string
		receiver        *ReplyReceiver
	}{
		{second.PID, envelope.KindQuestion, "first exact request", nil},
		{first.PID, envelope.KindTask, "first exact request", nil},
		{first.PID, envelope.KindQuestion, "changed body", nil},
		{first.PID, envelope.KindQuestion, "first exact request", &ReplyReceiver{Kind: "human"}},
	} {
		if _, err := w.alice.QueueAgentRequest(tctx(t), id, changed.pid, changed.kind, changed.body, changed.receiver); !errors.Is(err, ErrAgentRequestConflict) {
			t.Fatalf("changed retry: %v", err)
		}
	}
	before := stub.runs()
	if prior, err := w.alice.QueueAgentRequest(tctx(t), id, first.PID, envelope.KindQuestion, "first exact request", nil); err != nil || prior.ID != sends[0].ID {
		t.Fatalf("answered retry: %+v %v", prior, err)
	}
	if stub.runs() != before || before != 2 {
		t.Fatalf("executions=%d, before=%d", stub.runs(), before)
	}
	result, err := w.alice.AwaitAgentRequest(tctx(t), off.ID, 10*time.Millisecond, nil)
	if err != nil || !result.TimedOut || result.Answer != nil {
		t.Fatalf("offline wait=%+v %v", result, err)
	}
	stops[w.alice]()
	result, err = w.alice.AwaitAgentRequest(tctx(t), id, time.Second, nil)
	if err != nil || result.Answer == nil {
		t.Fatalf("stored answer after daemon stop=%+v %v", result, err)
	}
}

func TestQueuedAgentRequestReceiverAndFilesStayImmutable(t *testing.T) {
	stub := installAgentStub(t)
	w, conv, _, stopBob := agentWorld(t)
	setResponder(t, w.bob, "agentstub", stub.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)
	local, err := w.alice.CreateLocalAgent("local return", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	stopBob()
	id := protocol.NewID()
	receiver := &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "continue original local plan", Mode: envelope.KindQuestion}
	sent, err := w.alice.QueueAgentRequest(tctx(t), id, pid, envelope.KindQuestion, "retained receiver", receiver)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := w.alice.QueueAgentRequest(tctx(t), id, pid, envelope.KindQuestion, "retained receiver", receiver); err != nil || replay.ID != sent.ID || replay.LID != id {
		t.Fatalf("exact non-nil receiver replay=%+v %v", replay, err)
	}
	for _, changed := range []*ReplyReceiver{
		nil,
		{Kind: "human"},
		{Kind: receiver.Kind, AgentID: local.ID, Instructions: "changed local plan", Mode: receiver.Mode},
		{Kind: receiver.Kind, AgentID: local.ID, Instructions: receiver.Instructions, Mode: envelope.KindTask},
	} {
		if _, err := w.alice.QueueAgentRequest(tctx(t), id, pid, envelope.KindQuestion, "retained receiver", changed); !errors.Is(err, ErrAgentRequestConflict) {
			t.Fatalf("changed receiver=%+v: %v", changed, err)
		}
	}
	file := filepath.Join(t.TempDir(), "original.txt")
	if err := os.WriteFile(file, []byte("original attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	fileID := protocol.NewID()
	withFile, err := w.alice.AskAgentWithReceiver(WithQueuedSend(tctx(t), fileID), pid, envelope.KindQuestion, "file-bearing request", nil, OutgoingFile{Path: file})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.QueueAgentRequest(tctx(t), fileID, pid, envelope.KindQuestion, "file-bearing request", nil); !errors.Is(err, ErrAgentRequestConflict) {
		t.Fatalf("file-bearing ID reused: %+v %v", withFile, err)
	}
}

func TestQueuedLocalOnlyAgentRequestWaitAndRetainedMetadata(t *testing.T) {
	stub := installAgentStub(t)
	// Hold the native answer until the first wait has observed the pending
	// local-only job; a fast completed reply would miss the original defect.
	script := "#!/bin/sh\ncat > \"$AGENT_LOG.last\"\nprintf '=== run\\n' >> \"$AGENT_LOG\"\nwhile ! test -f \"$AGENT_LOG.release\"; do sleep 0.05; done\nprintf 'the deploy failed at step 3\\nemotion: calm\\n'\n"
	if err := os.WriteFile(Harnesses["agentstub"].bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	w, _, packet, _ := groupTurnsFixture(t)
	local, err := w.alice.CreateLocalAgent("local agent", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, w.alice.Address, local.ID, nil, nil, "")
	if err != nil || !p.Claimable() {
		t.Fatalf("local participation=%+v %v", p, err)
	}
	id := protocol.NewID()
	sent, err := w.alice.QueueAgentRequest(tctx(t), id, p.PID, envelope.KindQuestion, "local-only request", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the real locally authored job without its remote envelopes. This
	// isolates the retained inbox branch; it does not qualify the separate
	// existing no-recipient invitation/send behavior of a one-person group.
	if _, err := w.alice.store.db.Exec(`DELETE FROM outbox WHERE lid=?`, id); err != nil {
		t.Fatal(err)
	}
	retained, err := w.alice.AgentRequest(id)
	if err != nil || len(retained.Sent.Copies) != 0 {
		t.Fatalf("retained local-only request=%+v %v", retained, err)
	}
	result, err := w.alice.AwaitAgentRequest(tctx(t), id, time.Millisecond, nil)
	if err != nil || !result.TimedOut || result.Answer != nil {
		t.Fatalf("pending local-only push wait=%+v %v", result, err)
	}
	if err := os.WriteFile(stub.log+".release", nil, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = w.alice.AwaitAgentRequest(tctx(t), id, 30*time.Second, nil)
	wantBody := mentionAnchor("person", p.Host.Person, p.Host.Label) + "\n\nthe deploy failed at step 3"
	if err != nil || result.Answer == nil || result.Answer.Body != wantBody || result.Answer.From != w.alice.Address || result.Answer.AgentID != local.ID || result.Answer.Conv != packet.State.Conv || result.Answer.Status != envelope.StatusDone {
		t.Fatalf("local-only push wait=%+v %v", result, err)
	}
	if replay, err := w.alice.QueueAgentRequest(tctx(t), id, p.PID, envelope.KindQuestion, "local-only request", nil); err != nil || replay.ID != sent.ID || replay.LID != id || stub.runs() != 1 {
		t.Fatalf("local-only exact replay=%+v runs=%d %v", replay, stub.runs(), err)
	}
	// Existing local metadata must prevent dropping an original attachment.
	if _, err := w.alice.store.db.Exec(`INSERT INTO attachments(message_id,blob_id,name,size,sha256,ct_size,ct_sha256) VALUES(?,'local-file','original.txt',1,'digest',1,'cipher-digest')`, sent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.QueueAgentRequest(tctx(t), id, p.PID, envelope.KindQuestion, "local-only request", nil); !errors.Is(err, ErrAgentRequestConflict) {
		t.Fatalf("local file-bearing ID reused: %v", err)
	}
	// A retained request never borrows another author's/key's local record,
	// nor revives a turn erased by the person, even with a matching answer.
	for _, column := range []string{"sender", "verified_by"} {
		var original string
		if err := w.alice.store.db.QueryRow(`SELECT `+column+` FROM inbox WHERE id=?`, sent.ID).Scan(&original); err != nil {
			t.Fatal(err)
		}
		if _, err := w.alice.store.db.Exec(`UPDATE inbox SET `+column+`='foreign' WHERE id=?`, sent.ID); err != nil {
			t.Fatal(err)
		}
		if answer, _, err := w.alice.convReply(packet.State.Conv, id); !errors.Is(err, ErrNoMessage) || answer != nil {
			t.Fatalf("foreign %s accepted: %+v %v", column, answer, err)
		}
		if _, err := w.alice.store.db.Exec(`UPDATE inbox SET `+column+`=? WHERE id=?`, original, sent.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.alice.store.db.Exec(`INSERT INTO conv_erased(conv,key,lid,deletion) VALUES(?,?,?,?)`, packet.State.Conv, w.alice.Self().Fingerprint(), id, protocol.NewID()); err != nil {
		t.Fatal(err)
	}
	if answer, _, err := w.alice.convReply(packet.State.Conv, id); !errors.Is(err, ErrNoMessage) || answer != nil {
		t.Fatalf("erased request accepted: %+v %v", answer, err)
	}
	if _, err := w.alice.store.db.Exec(`DELETE FROM conv_erased WHERE conv=? AND lid=?`, packet.State.Conv, id); err != nil {
		t.Fatal(err)
	}
	terminal := []string{stateJobFailed, stateCancelled, stateInterrupt, stateDeclined, stateResolved}
	for _, state := range terminal {
		if _, err := w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, state, sent.ID); err != nil {
			t.Fatal(err)
		}
		if got, err := w.alice.AwaitAgentRequest(tctx(t), id, time.Second, nil); err != nil || got.Answer == nil {
			t.Fatalf("stored answer lost to %s: %+v %v", state, got, err)
		}
	}
	// Synthetic terminal state without a stored output must stop honestly,
	// rather than claim the request remains pending until the wait expires.
	if _, err := w.alice.store.db.Exec(`DELETE FROM outbox WHERE conv=? AND lid=(SELECT lid FROM outbox WHERE id=?)`, packet.State.Conv, result.Answer.ID); err != nil {
		t.Fatal(err)
	}
	for _, state := range terminal {
		if _, err := w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, state, sent.ID); err != nil {
			t.Fatal(err)
		}
		public, _, _ := statusOf(state)
		if got, err := w.alice.AwaitAgentRequest(tctx(t), id, time.Second, nil); err != nil || got.Answer != nil || got.Stopped == nil || got.Stopped.State != public || got.TimedOut {
			t.Fatalf("terminal %s wait: %+v %v", state, got, err)
		}
	}
}

func TestQueuedCorrelationIsSingleUseAcrossConversations(t *testing.T) {
	s := securityStore(t)
	a := &Agent{store: s, Address: "owner/desk"}
	id := protocol.NewID()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, conv := range []string{protocol.NewID(), protocol.NewID()} {
		go func(conv string) {
			<-start
			tx, err := s.db.Begin()
			if err != nil {
				results <- err
				return
			}
			defer tx.Rollback()
			err = a.queuedClaim(WithQueuedSend(context.Background(), id), conv, nil)(tx, protocol.NewID())
			if err == nil {
				_, err = tx.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid) VALUES(?,'peer/desk','same request','ciphertext','queued',1,?,?)`, protocol.NewID(), conv, id)
			}
			if err == nil {
				err = tx.Commit()
			}
			results <- err
		}(conv)
	}
	close(start)
	var success, collision int
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrSendAlreadyKept) {
			collision++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || collision != 1 {
		t.Fatalf("success=%d collision=%d", success, collision)
	}
}
