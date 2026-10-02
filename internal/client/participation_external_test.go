package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func externalAgentWorld(t *testing.T) (*world, *Agent, string, []string, *agentStub, []protocol.AgentRecord, func()) {
	t.Helper()
	stub := installAgentStub(t)
	w, conv, lids, _ := agentWorld(t)
	charlie := mustJoin(t, filepath.Join(t.TempDir(), "charlie"), w.aliceInvites("charlie"), "host")
	stop := runAgent(t, charlie)
	persons(t, charlie)
	for _, a := range []*Agent{w.alice, w.bob, charlie} {
		fakeNotify(a)
		waitNamedAgentCaps(t, a)
	}
	var records []protocol.AgentRecord
	for _, label := range []string{"Builder", "Reviewer"} {
		record, err := charlie.CreateLocalAgent(label, Responder{Harness: "agentstub", Dir: stub.dir})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := charlie.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	return w, charlie, conv, lids, stub, records, stop
}

func TestExternalAgentEncryptedLifecycle(t *testing.T) {
	w, host, conv, lids, stub, records, _ := externalAgentWorld(t)
	selected := filepath.Join(t.TempDir(), "selected.txt")
	hidden := filepath.Join(t.TempDir(), "hidden.txt")
	os.WriteFile(selected, []byte("AUTHORIZED_FILE_BYTES_496\n"), 0600)
	os.WriteFile(hidden, []byte("SECRET_UNGRANTED_FILE_BYTES\n"), 0600)
	fileTurn, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "selected failure log", Files: []OutgoingFile{{Path: selected}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "hidden room file", Files: []OutgoingFile{{Path: hidden}}}); err != nil {
		t.Fatal(err)
	}
	grant := append(append([]string{}, lids[:2]...), fileTurn.LID)
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, grant, nil, "selected context only")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.bob, host} {
		eventually(t, "external invite at "+a.Address, func() bool {
			info, err := a.Participation(p.PID)
			return err == nil && info.State == PartInvited && info.External && info.AgentID == records[0].ID
		})
	}
	if _, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "before acceptance"); err == nil {
		t.Fatal("pending participation executes")
	}
	if stub.runs() != 0 {
		t.Fatal("invite executed")
	}
	rows, err := host.Conversations()
	if err != nil || len(rows) != 1 || rows[0].Role != "visitor" {
		t.Fatalf("visitor role %+v %v", rows, err)
	}
	root, raw, _, _ := host.store.conversation(conv)
	me, _, _ := host.store.selfPerson(host.Address)
	if _, member := root.Member(me.info.Person); member || len(root.Members) != 2 {
		t.Fatal("outside host became DM member")
	}
	if _, err = host.SendConv(tctx(t), conv, ConvOutgoing{Body: "ordinary outsider message"}); err == nil {
		t.Fatal("visitor ordinary send")
	}
	ctxView, err := host.ParticipationContext(p.PID, 0)
	if err != nil || ctxView.Missing != 0 || len(ctxView.Messages) != 3 {
		t.Fatalf("granted visitor context %+v %v", ctxView, err)
	}
	for _, m := range ctxView.Messages {
		if !m.History || m.Key != "" || m.Claimed == "" || m.ExcerptPID != p.PID || m.Job != "" {
			t.Fatalf("unverified excerpt authority %+v", m)
		}
	}
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		eventually(t, "active at "+a.Address, func() bool { return stateAt(t, a, p.PID).Claimable() })
	}
	current := filepath.Join(t.TempDir(), "current.txt")
	os.WriteFile(current, []byte("CURRENT_REQUEST_FILE_BYTES_489\n"), 0600)
	question, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "review selected context", OutgoingFile{Path: current})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob, host} {
		answer := replyAt(t, a, conv, question.ID)
		if answer.AgentID != records[0].ID || answer.From != host.Address || answer.Key != host.Self().Fingerprint() || answer.PID != p.PID {
			t.Fatalf("outside reply attribution %+v", answer)
		}
	}
	for _, text := range []string{"deploy failed at step 3", "logs are in the ticket", "AUTHORIZED_FILE_BYTES_496", "CURRENT_REQUEST_FILE_BYTES_489", "between", "external agent"} {
		if !strings.Contains(stub.last(), text) {
			t.Fatalf("missing %q in harness prompt %s", text, stub.last())
		}
	}
	for _, text := range []string{"lunch", "SECRET_UNGRANTED_FILE_BYTES", "hidden room file"} {
		if strings.Contains(stub.last(), text) {
			t.Fatalf("leaked %q", text)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(host.home, "opened"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agentnet-apx-") {
			t.Fatal("job retained plaintext")
		}
	}
	follow, err := w.bob.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "", OutgoingFile{Path: current, Name: "file-only follow-up.txt"})
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, follow.ID)
	if stub.runs() != 2 {
		t.Fatalf("runs %d", stub.runs())
	}
	if _, err = w.bob.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "host dismissed", func() bool { return stateAt(t, host, p.PID).State == PartDismissed })
	if _, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "dismissed"); err == nil {
		t.Fatal("dismissed ask")
	}
	if _, err = host.ParticipationContext(p.PID, 0); err == nil {
		t.Fatal("dismissed context")
	}
	if _, err = host.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindAnswer, Body: "late result", PID: p.PID, AgentID: records[0].ID, ReplyTo: question.ID}); err == nil {
		t.Fatal("late output")
	}
	// Host- and member-signed traffic still cannot substitute role/agent/ref.
	var invite protocol.ParticipationEvent
	events, _ := w.alice.store.participationEvents(conv, p.PID)
	for _, e := range events {
		if e.Type == protocol.EventInvite {
			invite = e
		}
	}
	item := HistoryItem{V: 1, From: w.alice.Address, FromKey: w.alice.Self().Fingerprint(), ID: protocol.NewID(), LID: protocol.NewID(), TS: invite.TS, Kind: envelope.KindMessage, Body: "cross-grant"}
	body, _ := json.Marshal(item)
	bad := craft(t, w.alice, host, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: raw, LID: protocol.NewID(), Sub: envelope.SubExcerpt, Replica: true, PID: p.PID, Body: string(body)})
	if err = host.accept(tctx(t), bad); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, host, `id=?`, bad.ID) != 0 {
		t.Fatal("ungranted excerpt admitted")
	}
	newInvite, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[1].ID, lids[:1], nil, "second distinct agent")
	if err != nil {
		t.Fatal(err)
	}
	if newInvite.PID == p.PID {
		t.Fatal("reused dismissed PID")
	}
	eventually(t, "second host invitation", func() bool { return stateAt(t, host, newInvite.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), newInvite.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second active", func() bool { return stateAt(t, w.alice, newInvite.PID).Claimable() })
	q2, err := w.alice.AskAgent(tctx(t), newInvite.PID, envelope.KindQuestion, "second isolated identity")
	if err != nil {
		t.Fatal(err)
	}
	a2 := replyAt(t, w.alice, conv, q2.ID)
	if a2.AgentID != records[1].ID || a2.PID != newInvite.PID || stub.runs() != 3 {
		t.Fatalf("distinct identity %+v runs %d", a2, stub.runs())
	}
	if strings.Contains(stub.last(), "AUTHORIZED_FILE_BYTES_496") || strings.Contains(stub.last(), "member follow-up without reinvite") {
		t.Fatal("other PID context leaked")
	}
	if _, err = host.DismissParticipation(tctx(t), newInvite.PID); err == nil {
		t.Fatal("external host gained dismiss authority")
	}
	if _, err = w.alice.InviteNamedAgent(tctx(t), conv, host.Address, protocol.NewID(), nil, nil, ""); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown named identity %v", err)
	}
}

func TestExternalAgentProofReplayAndQueuedGates(t *testing.T) {
	w, host, conv, lids, _, records, stopHost := externalAgentWorld(t)
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, lids[:1], nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "pending external host", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	stopHost() // all subsequent native admission/claim/output is deterministic
	_, raw, _, _ := host.store.conversation(conv)
	badDecision := protocol.ParticipationEvent{V: 1, Conv: conv, PID: p.PID, Type: protocol.EventAccept, Prev: strings.Repeat("fe", 32), Author: stateAuthor(t, host), TS: time.Now().Unix()}
	for _, signer := range []*Agent{host, w.alice} {
		ev := badDecision
		ev.Author = stateAuthor(t, signer)
		ev.Sign(signer.id.Sign)
		body, _ := json.Marshal(ev)
		wrong := craft(t, signer, host, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: raw, LID: protocol.NewID(), PID: p.PID, Sub: envelope.SubEvent, Body: string(body)})
		if err := host.accept(tctx(t), wrong); err != nil {
			t.Fatal(err)
		}
		if inboxCount(t, host, `id=?`, wrong.ID) != 0 {
			t.Fatal("wrong host/Prev decision admitted")
		}
	}
	target := &envelope.Target{Address: host.Address, Fingerprint: host.Self().Fingerprint(), AgentID: records[0].ID}
	request := craft(t, w.alice, host, envelope.Inner{Kind: envelope.KindQuestion, Conv: conv, Root: raw, LID: protocol.NewID(), PID: p.PID, Target: target, Body: "reordered request"})
	if err := host.accept(tctx(t), request); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := host.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, request.ID).Scan(&reason); err != nil || reason != reasonProof {
		t.Fatalf("pending reordered request reason %q %v", reason, err)
	}
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	for host.retryProof(tctx(t)) {
	}
	if inboxCount(t, host, `id=? AND state=?`, request.ID, stateAgentWaiting) != 1 {
		t.Fatal("request did not resume after acceptance proof")
	}
	if err = host.accept(tctx(t), request); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, host, `id=?`, request.ID) != 1 {
		t.Fatal("request replay duplicated input")
	}
	for _, in := range []envelope.Inner{
		{Kind: envelope.KindQuestion, Target: &envelope.Target{Address: host.Address, Fingerprint: host.Self().Fingerprint(), AgentID: records[1].ID}, Body: "wrong agent"},
		{Kind: envelope.KindQuestion, Target: &envelope.Target{Address: host.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: records[0].ID}, Body: "wrong host key"},
		{Kind: envelope.KindAnswer, AgentID: records[0].ID, ReplyTo: request.ID, Body: "member impersonates host"},
	} {
		in.Conv, in.Root, in.LID, in.PID = conv, raw, protocol.NewID(), p.PID
		bad := craft(t, w.alice, host, in)
		if err := host.accept(tctx(t), bad); err != nil {
			t.Fatal(err)
		}
		if inboxCount(t, host, `id=?`, bad.ID) != 0 {
			t.Fatal("wrong role/target admitted")
		}
	}
	h := HistoryItem{V: 1, ID: protocol.NewID(), LID: lids[1], From: w.alice.Address, FromKey: w.alice.Self().Fingerprint(), TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "ungranted excerpt"}
	b, _ := json.Marshal(h)
	ungranted := craft(t, w.alice, host, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: raw, LID: protocol.NewID(), PID: p.PID, Sub: envelope.SubExcerpt, Replica: true, Body: string(b)})
	if err := host.accept(tctx(t), ungranted); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, host, `id=?`, ungranted.ID) != 0 {
		t.Fatal("cross-ref context admitted")
	}
	// Actual restart reads existing proofs/inbox; none of them becomes a
	// second claim. A concurrent dismissal cannot land inside the claim tx.
	reopened, err := Open(host.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, dbPath := paths(host.home)
	other, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(100)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var during error
	beforeAgentClaim = func() {
		_, during = other.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, p.Inviter.Person)
	}
	t.Cleanup(func() { beforeAgentClaim = func() {} })
	j, found, _, _, err := reopened.store.claimAgentPage("", host.Address, host.Self().Fingerprint(), 0, agentPage, func(q dbq, id string) (*ExecutorStamp, error) { return reopened.ResolveExecutorIn(q, id, nil) })
	beforeAgentClaim = func() {}
	if err != nil || !found || j.ID != request.ID || j.Executor == nil || j.Executor.AgentID != records[0].ID || during == nil {
		t.Fatalf("atomic named restarted claim %+v %v %v %v", j, found, err, during)
	}
	if _, found, _, _, err := reopened.store.claimAgentPage("", host.Address, host.Self().Fingerprint(), 0, agentPage); err != nil || found {
		t.Fatalf("duplicate claim %v %v", found, err)
	}
	// Freeze or withdraw the host itself: membership cannot substitute for
	// exact current host proof; restoring synthetic proof retains the input.
	if _, err := host.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, p.Host.Person); err != nil {
		t.Fatal(err)
	}
	if why := host.agentStop(j); why == "" {
		t.Fatal("frozen host still executes")
	}
	if _, err := host.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personSelf, p.Host.Person); err != nil {
		t.Fatal(err)
	}
	// Queue both recipient-encrypted outputs: one immutable job owns both.
	injected := injectFaults(reopened)
	injected.add("POST", "/v1/messages", 2, false)
	reopened.finishAgent(tctx(t), j, &j.Executor.Responder, envelope.StatusDone, "bounded answer\nemotion: calm")
	var queued int
	if err := host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE pid=? AND origin LIKE 'agent:%' AND state=?`, p.PID, stateQueued).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("queued output fan %d %v", queued, err)
	}
	var delivered int
	reopened.hub.http.Transport = postHook{reopened.hub.http.Transport, func(env envelope.Envelope) {
		if env.Kind != envelope.KindAnswer {
			return
		}
		delivered++
		if delivered == 1 {
			info := stateAt(t, host, p.PID)
			ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: p.PID, Type: protocol.EventDismiss, Prev: info.Decision, Author: stateAuthor(t, w.alice), TS: time.Now().Unix()}
			ev.Sign(w.alice.id.Sign)
			body, _ := json.Marshal(ev)
			end := craft(t, w.alice, host, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: raw, LID: protocol.NewID(), PID: p.PID, Sub: envelope.SubEvent, Body: string(body)})
			if err := host.accept(tctx(t), end); err != nil {
				t.Error(err)
			}
		}
	}}
	if err := reopened.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("post-dismiss output copies delivered %d", delivered)
	}
	var blocked int
	host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE pid=? AND origin LIKE 'agent:%' AND state=?`, p.PID, stateNotDelivered).Scan(&blocked)
	if blocked != 1 {
		t.Fatalf("blocked outputs %d", blocked)
	}
	if err := host.accept(tctx(t), request); err != nil {
		t.Fatal(err)
	}
	if _, found, _, _, err := reopened.store.claimAgentPage("", host.Address, host.Self().Fingerprint(), 0, agentPage); err != nil || found {
		t.Fatal("completed/dismissed work reclaimed")
	}
	// A signed older host keeps agi1 but loses apx1: no fresh invitation or
	// capability downgrade is possible, even with its catalog intact.
	label, name, _ := protocol.SplitAddress(host.Address)
	var prof protocol.Profile
	if err := host.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil || len(prof.Sessions) != 1 {
		t.Fatalf("profile %v %v", prof.Sessions, err)
	}
	caps := slices.DeleteFunc(slices.Clone(ownCaps), func(s string) bool { return s == protocol.CapExternalParticipation })
	cap := protocol.CapsRecord{Address: host.Address, Session: prof.Sessions[0], Caps: caps, TS: time.Now().Unix() + 100}
	cap.Sign(host.id.Sign)
	if err := host.hub.do(tctx(t), "PUT", "/v1/caps", cap, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, nil, nil, ""); !errors.Is(err, errAgentIdentityUnsupported) {
		t.Fatalf("old external host downgrade %v", err)
	}
}

func TestExternalAgentRequestCorrelationAndOrdering(t *testing.T) {
	w, host, conv, _, _, records, stopHost := externalAgentWorld(t)
	var parts []ParticipationInfo
	for _, record := range records {
		p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "external invitation", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
		if _, err := host.AcceptParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
		for _, a := range []*Agent{w.alice, w.bob} {
			eventually(t, "accepted external identity", func() bool { return stateAt(t, a, p.PID).Claimable() })
		}
		parts = append(parts, p)
	}
	stopHost() // no job consumes these requests; admission and retries stay explicit
	q1, err := w.alice.AskAgent(tctx(t), parts[0].PID, envelope.KindQuestion, "first agent request")
	if err != nil {
		t.Fatal(err)
	}
	q2, err := w.bob.AskAgent(tctx(t), parts[1].PID, envelope.KindQuestion, "second agent request")
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range []*Agent{w.alice, w.bob} {
		rows, err := from.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND kind=?`, host.Address, envelope.KindQuestion)
		if err != nil {
			t.Fatal(err)
		}
		var copies []envelope.Envelope
		for rows.Next() {
			var raw string
			var env envelope.Envelope
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &env); err != nil {
				t.Fatal(err)
			}
			copies = append(copies, env)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		for _, env := range copies {
			if err := host.accept(tctx(t), env); err != nil {
				t.Fatal(err)
			}
		}
	}
	if q1.ID != q1.LID || q2.ID != q2.LID {
		t.Fatal("audience cannot resolve executable request ID by shared logical ID")
	}
	eventually(t, "display copy of first request", func() bool {
		m, n := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == q1.LID })
		return n == 1 && m.ID != q1.ID && m.Replica
	})
	_, raw, _, _ := host.store.conversation(conv)
	output := envelope.Inner{Kind: envelope.KindAnswer, Conv: conv, Root: raw, LID: protocol.NewID(), PID: parts[1].PID,
		AgentID: records[1].ID, ReplyTo: q1.ID, Body: "second agent claiming first request"}
	if _, err := host.SendConv(tctx(t), conv, ConvOutgoing{Kind: output.Kind, PID: output.PID, AgentID: output.AgentID, ReplyTo: output.ReplyTo, Body: output.Body}); err == nil {
		t.Fatal("cross-PID output queued by exact host")
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		bad := craft(t, host, a, output)
		if err := a.accept(tctx(t), bad); err != nil {
			t.Fatal(err)
		}
		if heldReason(t, a, bad.ID) != reasonInvalid || inboxCount(t, a, `id=?`, bad.ID) != 0 {
			t.Fatal("cross-PID output admitted in same DM")
		}
	}
	// A same-logical request copied under a new physical ID stays one input.
	target := &envelope.Target{Address: host.Address, Fingerprint: host.Self().Fingerprint(), AgentID: records[0].ID}
	copy := craft(t, w.alice, host, envelope.Inner{Kind: envelope.KindQuestion, Conv: conv, Root: raw, LID: q1.LID, PID: parts[0].PID, Target: target, Body: "first agent request", Origin: envelope.OriginUI})
	if err := host.accept(tctx(t), copy); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, host, `lid=?`, q1.LID) != 1 {
		t.Fatal("physical fan replay duplicated request")
	}
	if _, err := host.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindAnswer, PID: parts[0].PID, AgentID: records[0].ID, ReplyTo: q1.ID, Body: "first correct answer"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		replyAt(t, a, conv, q1.ID)
	}
	// Output arriving before a request remains proof_pending. A legitimate
	// audience copy later binds it by LID, then retry admits it exactly once.
	delayedID := protocol.NewID()
	delayed := craft(t, host, w.alice, envelope.Inner{Kind: envelope.KindAnswer, Conv: conv, Root: raw, LID: protocol.NewID(), PID: parts[0].PID,
		AgentID: records[0].ID, ReplyTo: delayedID, Body: "answer before audience request"})
	if err := w.alice.accept(tctx(t), delayed); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, w.alice, delayed.ID) != reasonProof || inboxCount(t, w.alice, `id=?`, delayed.ID) != 0 {
		t.Fatal("missing request accepted instead of held for proof")
	}
	request := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindQuestion, Conv: conv, Root: raw, LID: delayedID, PID: parts[0].PID, Target: target, Replica: true, Body: "delayed display request"})
	if err := w.alice.accept(tctx(t), request); err != nil {
		t.Fatal(err)
	}
	for w.alice.retryProof(tctx(t)) {
	}
	if inboxCount(t, w.alice, `id=?`, delayed.ID) != 1 {
		t.Fatal("ordered proof retry failed")
	}
	if err := w.alice.accept(tctx(t), delayed); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, w.alice, `id=?`, delayed.ID) != 1 {
		t.Fatal("output retry duplicated message")
	}
	wrongLaterID := protocol.NewID()
	wrongLater := craft(t, host, w.alice, envelope.Inner{Kind: envelope.KindAnswer, Conv: conv, Root: raw, LID: protocol.NewID(), PID: parts[0].PID,
		AgentID: records[0].ID, ReplyTo: wrongLaterID, Body: "answer awaiting mismatched request"})
	if err := w.alice.accept(tctx(t), wrongLater); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, w.alice, wrongLater.ID) != reasonProof {
		t.Fatal("absent request is not retryable proof")
	}
	wrongRequest := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindQuestion, Conv: conv, Root: raw, LID: wrongLaterID, PID: parts[1].PID,
		Target: &envelope.Target{Address: host.Address, Fingerprint: host.Self().Fingerprint(), AgentID: records[1].ID}, Replica: true, Body: "request actually belongs to other agent"})
	if err := w.alice.accept(tctx(t), wrongRequest); err != nil {
		t.Fatal(err)
	}
	for w.alice.retryProof(tctx(t)) {
	}
	if heldReason(t, w.alice, wrongLater.ID) != reasonInvalid || inboxCount(t, w.alice, `id=?`, wrongLater.ID) != 0 {
		t.Fatal("late mismatched request converted proof hold into accepted output")
	}
	// Persisted metadata is rechecked too, including host key/target and
	// ambiguous references under different human keys. A row chosen by
	// SQL order must never stand in for one unambiguous original request.
	members, err := w.alice.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.alice.participation(conv, parts[0].PID)
	if err != nil {
		t.Fatal(err)
	}
	bound := envelope.Inner{Kind: envelope.KindAnswer, Conv: conv, PID: parts[0].PID, AgentID: records[0].ID, ReplyTo: q1.ID}
	for _, test := range []struct {
		name, query string
		args        []any
	}{
		{"wrong target key", `UPDATE outbox SET target=? WHERE lid=?`, []any{targetJSON(&envelope.Target{Address: host.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: records[0].ID}), q1.LID}},
		{"wrong target address", `UPDATE outbox SET target=? WHERE lid=?`, []any{targetJSON(&envelope.Target{Address: w.bob.Address, Fingerprint: host.Self().Fingerprint(), AgentID: records[0].ID}), q1.LID}},
		{"wrong target agent", `UPDATE outbox SET target=? WHERE lid=?`, []any{targetJSON(&envelope.Target{Address: host.Address, Fingerprint: host.Self().Fingerprint(), AgentID: records[1].ID}), q1.LID}},
		{"ordinary turn", `UPDATE outbox SET kind=? WHERE lid=?`, []any{envelope.KindMessage, q1.LID}},
		{"wrong answer kind", `UPDATE outbox SET kind=? WHERE lid=?`, []any{envelope.KindTask, q1.LID}},
		{"control turn", `UPDATE outbox SET sub=? WHERE lid=?`, []any{envelope.SubEvent, q1.LID}},
		{"ambiguous human key", `INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,conv,lid,target,pid)
		 SELECT ?,?,created_at,kind,body,created_at,'',?,conv,lid,target,pid FROM outbox WHERE lid=? LIMIT 1`, []any{protocol.NewID(), w.bob.Address, w.bob.Self().Fingerprint(), q1.LID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := w.alice.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(test.query, test.args...); err != nil {
				t.Fatal(err)
			}
			if reason, err := externalOutputRequest(tx, bound, info, members, w.alice.Address, w.alice.Self().Fingerprint()); reason != reasonInvalid || err == nil {
				t.Fatalf("mismatched request proof accepted: %q %v", reason, err)
			}
		})
	}
	// Unknown legacy physical IDs are not guessed from a participation.
	legacy := craft(t, host, w.alice, envelope.Inner{Kind: envelope.KindAnswer, Conv: conv, Root: raw, LID: protocol.NewID(), PID: parts[0].PID,
		AgentID: records[0].ID, ReplyTo: protocol.NewID(), Body: "unresolved old physical reference"})
	if err := w.alice.accept(tctx(t), legacy); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, w.alice, legacy.ID) != reasonProof {
		t.Fatal("legacy request inferred by PID")
	}
	oldID := protocol.NewID()
	oldRequest := craft(t, w.alice, host, envelope.Inner{ID: oldID, Kind: envelope.KindQuestion, Conv: conv, Root: raw, LID: protocol.NewID(), PID: parts[0].PID, Target: target, Body: "old physical request retained"})
	if err := host.accept(tctx(t), oldRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := host.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindAnswer, PID: parts[0].PID, AgentID: records[0].ID, ReplyTo: oldID, Body: "known legacy physical request answer"}); err != nil {
		t.Fatalf("exact retained legacy request cannot be answered: %v", err)
	}
	// Already queued copies also revalidate the persisted request binding.
	faults := injectFaults(host)
	faults.add("POST", "/v1/messages", 2, false)
	queued, err := host.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindAnswer, PID: parts[1].PID, AgentID: records[1].ID, ReplyTo: q2.ID, Body: "queued second answer"})
	if err != nil || queued.State != stateQueued {
		t.Fatalf("queued output %+v %v", queued, err)
	}
	if _, err := host.store.db.Exec(`UPDATE outbox SET reply_to=? WHERE lid=?`, q1.ID, queued.LID); err != nil {
		t.Fatal(err)
	}
	if err := host.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var held int
	if err := host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND state=?`, queued.LID, stateNotDelivered).Scan(&held); err != nil || held != 2 {
		t.Fatalf("mismatched queued fan not blocked: %d %v", held, err)
	}
}

func TestExternalAgentLinkedHumanHistory(t *testing.T) {
	w, host, conv, _, stub, records, _ := externalAgentWorld(t)
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "outside host invited", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err := host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "external accepted before history", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "retain this agent lifecycle on human linked device")
	if err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, w.alice, conv, q.ID)
	phone, awaited, _ := linkPhone(t, w.alice, "external-history-phone")
	fakeNotify(phone)
	request := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	waitNamedAgentCaps(t, phone)
	eventually(t, "external signed acceptance and output linked history", func() bool {
		info, err := phone.Participation(p.PID)
		if err != nil || !info.Claimable() || !info.External || info.HostHere {
			return false
		}
		msgs, err := phone.ConversationMessages(conv)
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.LID == answer.LID {
				return m.AgentID == records[0].ID && m.PID == p.PID && m.ReplyTo == q.ID && m.History && m.Job == "" && m.SyncedFrom == w.alice.Address && m.Claimed == host.Self().Fingerprint()
			}
		}
		return false
	})
	if stub.runs() != 1 {
		t.Fatal("linked history reran external agent")
	}
	var gated, plaintext int
	if err := w.alice.store.db.QueryRow(`SELECT count(*), coalesce(sum(body!=''),0) FROM outbox WHERE recipient=? AND conv=? AND sub=? AND required_cap=?`, phone.Address, conv, envelope.SubHistory, protocol.CapExternalParticipation).Scan(&gated, &plaintext); err != nil || gated != 4 || plaintext != 0 {
		t.Fatalf("external lifecycle history requirement/privacy: copies=%d plaintext=%d err=%v", gated, plaintext, err)
	}
	_, raw, _, _ := w.alice.store.conversation(conv)
	for _, item := range []HistoryItem{
		{V: 1, ID: protocol.NewID(), LID: protocol.NewID(), From: host.Address, FromKey: host.Self().Fingerprint(), TS: time.Now().Unix(), Kind: envelope.KindAnswer, PID: p.PID, AgentID: records[1].ID, ReplyTo: q.ID, Body: "other named agent"},
		{V: 1, ID: protocol.NewID(), LID: protocol.NewID(), From: host.Address, FromKey: w.bob.Self().Fingerprint(), TS: time.Now().Unix(), Kind: envelope.KindAnswer, PID: p.PID, AgentID: records[0].ID, ReplyTo: q.ID, Body: "wrong host key"},
		{V: 1, ID: protocol.NewID(), LID: protocol.NewID(), From: host.Address, FromKey: host.Self().Fingerprint(), TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "ordinary outsider room history"},
	} {
		body, _ := json.Marshal(item)
		bad := craft(t, w.alice, phone, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: raw, LID: protocol.NewID(), Replica: true, Sub: envelope.SubHistory, Body: string(body)})
		if err := phone.accept(tctx(t), bad); err != nil {
			t.Fatal(err)
		}
		if inboxCount(t, phone, `lid=?`, item.LID) != 0 || heldReason(t, phone, bad.ID) != reasonInvalid {
			t.Fatal("outside history acquired unrelated host/agent authority")
		}
	}
	badDecision := protocol.ParticipationEvent{V: 1, Conv: conv, PID: p.PID, Type: protocol.EventAccept, Prev: strings.Repeat("fe", 32), Author: stateAuthor(t, host), TS: time.Now().Unix()}
	for _, signer := range []*Agent{host, w.bob} {
		ev := badDecision
		ev.Sign(signer.id.Sign)
		eventBody, _ := json.Marshal(ev)
		item := HistoryItem{V: 1, ID: protocol.NewID(), LID: protocol.NewID(), From: host.Address, FromKey: host.Self().Fingerprint(), TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: p.PID, Body: string(eventBody)}
		body, _ := json.Marshal(item)
		bad := craft(t, w.alice, phone, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: raw, LID: protocol.NewID(), Replica: true, Sub: envelope.SubHistory, Body: string(body)})
		if err := phone.accept(tctx(t), bad); err != nil {
			t.Fatal(err)
		}
		if inboxCount(t, phone, `lid=?`, item.LID) != 0 || heldReason(t, phone, bad.ID) != reasonInvalid {
			t.Fatal("wrong host signature/invite accepted through history")
		}
	}
	// A host's own linked sibling is not an invited execution address and
	// never becomes a human member by receiving the host's history carrier.
	hostPhone, hostAwaited, _ := linkPhone(t, host, "host-history-phone")
	hostRequest := pendingLink(t, host)
	if err := host.DecideLink(tctx(t), hostRequest.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-hostAwaited; out.err != nil {
		t.Fatal(out.err)
	}
	eventually(t, "visitor history snapshot skipped", func() bool {
		jobs, err := host.HistoryProgress()
		return err == nil && len(jobs) == 1 && jobs[0].State == "done"
	})
	var visitorCopies int
	if err := host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND conv=? AND sub=?`, hostPhone.Address, conv, envelope.SubHistory).Scan(&visitorCopies); err != nil || visitorCopies != 0 {
		t.Fatalf("visitor history queued to linked host: %d %v", visitorCopies, err)
	}
	item := HistoryItem{V: 1, ID: protocol.NewID(), LID: protocol.NewID(), From: w.alice.Address, FromKey: w.alice.Self().Fingerprint(), TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "unrelated room history"}
	body, _ := json.Marshal(item)
	leak := craft(t, host, hostPhone, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: raw, LID: protocol.NewID(), Replica: true, Sub: envelope.SubHistory, Body: string(body)})
	if err := hostPhone.accept(tctx(t), leak); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, hostPhone, `lid=?`, item.LID) != 0 {
		t.Fatal("linked outside host became room reader")
	}
}

func TestExternalAgentHistoryRequirementPreservesExcerpt(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	item := HistoryItem{V: 1, ID: protocol.NewID(), LID: protocol.NewID(), PID: protocol.NewID(), Sub: envelope.SubExcerpt}
	body, _ := json.Marshal(item)
	copy := outCopy{env: envelope.Envelope{ID: protocol.NewID(), To: "person/phone"},
		in: envelope.Inner{Conv: protocol.NewID(), LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubHistory, Body: string(body)}, state: stateQueued}
	if agentRequirement(copy.in) != protocol.CapExternalParticipation {
		t.Fatal("wrapped excerpt lost external capability")
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := insertCopies(tx, []outCopy{copy}); err != nil {
		t.Fatal(err)
	}
	var stored, required string
	if err := tx.QueryRow(`SELECT body,required_cap FROM outbox WHERE id=?`, copy.env.ID).Scan(&stored, &required); err != nil || stored != "" || required != protocol.CapExternalParticipation {
		t.Fatalf("wrapped excerpt queue privacy/capability: %q %q %v", stored, required, err)
	}
	copy.env.ID = protocol.NewID()
	copy.in.Body = `{"v":1,"agent_id":"1234"}`
	copy.required = protocol.CapExternalParticipation // known external original, even without an excerpt sub
	if err := insertCopies(tx, []outCopy{copy}); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT required_cap FROM outbox WHERE id=?`, copy.env.ID).Scan(&required); err != nil || required != protocol.CapExternalParticipation {
		t.Fatalf("explicit external history requirement downgraded: %q %v", required, err)
	}
}
