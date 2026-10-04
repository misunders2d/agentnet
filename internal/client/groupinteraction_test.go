package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupInteractionControlsThreePeople(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	sent, err := w.alice.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "original group text"})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, "ordinary group original", func() bool { return len(groupTurns(t, a, packet.State.Conv)) == 1 })
	}
	ref, err := w.alice.RefOf(packet.State.Conv, sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = carol.Revise(tctx(t), ref, "other person's edit"); err == nil {
		t.Fatal("foreign author gained revision authority")
	}
	revision, err := w.alice.Revise(tctx(t), ref, "corrected group text")
	if err != nil {
		t.Fatal(err)
	}
	reaction, err := w.bob.React(tctx(t), ref, "👍", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic revision=%+v reaction=%+v", revision, reaction)
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		rows, e := a.store.db.Query(`SELECT sub,state,coalesce(error,'') FROM outbox WHERE conv=? AND ref_id IS NOT NULL`, packet.State.Conv)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var sub, state, why string
			if e = rows.Scan(&sub, &state, &why); e != nil {
				t.Fatal(e)
			}
			t.Logf("synthetic control %s %s %s %s", a.Address, sub, state, why)
		}
		rows.Close()
	}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		eventually(t, "same group revision/reaction", func() bool {
			rows := groupTurns(t, a, packet.State.Conv)
			return len(rows) == 1 && rows[0].Shown(rows[0].Body) == "corrected group text" && len(rows[0].Reactions) == 1
		})
	}
	if _, err = w.alice.Retract(tctx(t), ref, ""); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		eventually(t, "group tombstone converges", func() bool {
			rows := groupTurns(t, a, packet.State.Conv)
			return len(rows) == 1 && rows[0].Deleted && rows[0].Body == "" && rows[0].Text == ""
		})
		var work int
		if err = a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE state IN (?,?)`, stateAgentWaiting, stateRunning).Scan(&work); err != nil || work != 0 {
			t.Fatalf("human control created work: %d %v", work, err)
		}
	}
}

func TestGroupInteractionVisitorDepartureRestart(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	host := proofReader(t, w, "departure-visitor")
	fakeNotify(host)
	stop := runAgent(t, host)
	publishGroupFixtureCaps(t, host, true)
	record, err := host.CreateLocalAgent("Departure reviewer", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := carol.InviteNamedAgent(tctx(t), packet.State.Conv, host.Address, record.ID, nil, nil, "current membership disclosure only")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor invitation", func() bool {
		// The invitation can arrive before the group context it needs is
		// decryptable here: that is pending (agentVerdict waits on it), not failure.
		info, err := host.Participation(p.PID)
		if errors.Is(err, ErrGroupContextPending) || errors.Is(err, ErrNoParticipation) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return info.State == PartInvited
	})
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor acceptance", func() bool { return stateAt(t, carol, p.PID).Claimable() })
	// A valid later head retaining every epoch must preserve the invitation.
	next, err := w.alice.RenameGroup(tctx(t), packet.State.Conv, "Updated current disclosure")
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, next, host, carol, w.bob)
	if !stateAt(t, host, p.PID).Claimable() {
		t.Fatal("valid later head ended same-epoch PID")
	}
	task, err := carol.AskAgent(tctx(t), p.PID, envelope.KindTask, "held before departure")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, host, task.ID, stateAwaiting)
	if stub.runs() != 0 {
		t.Fatal("unapproved task executed")
	}
	stop()
	home := host.home
	label, name, _ := protocol.SplitAddress(host.Address)
	var old protocol.Profile
	if err = host.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &old); err != nil {
		t.Fatal(err)
	}
	host.Close()
	host, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	if !stateAt(t, host, p.PID).Claimable() {
		t.Fatal("restart lost verified original proof/context/PID")
	}
	var before int
	if err = carol.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND pid=? AND sub=?`, packet.State.Conv, p.PID, envelope.SubGroupContext).Scan(&before); err != nil {
		t.Fatal(err)
	}
	leave, err := carol.LeaveGroup(tctx(t), packet.State.Conv)
	if err != nil || leave.Withdrawal == nil {
		t.Fatalf("ordinary departure %v", err)
	}
	var n int
	if err = carol.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND pid=? AND sub=?`, packet.State.Conv, p.PID, envelope.SubGroupContext).Scan(&n); err != nil || n != before+1 {
		t.Fatalf("exact departure visitor context not atomic %d %v", n, err)
	}
	fakeNotify(host)
	runAgent(t, host)
	eventually(t, "new authenticated host session", func() bool {
		var current protocol.Profile
		if host.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &current) != nil {
			return false
		}
		for _, id := range current.Sessions {
			if !slices.Contains(old.Sessions, id) {
				return true
			}
		}
		return false
	})
	publishGroupFixtureCaps(t, host, true)
	eventually(t, "visitor pins exact departure", func() bool {
		pins, e := host.groupWithdrawals(packet.State.Conv)
		return e == nil && groupWithdrawalPinned(pins, *leave.Withdrawal)
	})
	// Accepting it is refused, saying why (BUG-23): nothing would run it.
	if err = host.Accept(task.ID); !errors.Is(err, ErrNothingRuns) {
		t.Fatalf("accept of a task after its inviter's departure: %v", err)
	}
	if s, _ := host.store.jobState(task.ID); s == stateAccepted || s == stateRunning {
		t.Fatalf("a refused accept left the task %s", s)
	}
	// Accepted before the departure: the worker does not run it either.
	if _, err = host.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateAccepted, task.ID); err != nil {
		t.Fatal(err)
	}
	host.NoteChange() // as a stored change does: the worker looks again from the start
	host.wakeWorker()
	waitState(t, host, task.ID, stateNotRun)
	if stub.runs() != 0 {
		t.Fatal("received departure allowed queued task claim")
	}
	if _, err = carol.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "after departure"); err == nil {
		t.Fatal("departed inviter sent request")
	}
	if err = carol.RecoverGroupWithdrawals(tctx(t)); err != nil {
		t.Fatal(err)
	}
	carol.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND pid=? AND sub=?`, packet.State.Conv, p.PID, envelope.SubGroupContext).Scan(&n)
	if n != before+1 {
		t.Fatal("recovery duplicated visitor departure")
	}
	var sibling int
	if err = host.store.db.QueryRow(`SELECT count(*) FROM history_jobs`).Scan(&sibling); err != nil || sibling != 0 {
		t.Fatal("visitor disclosure created own history export")
	}
}

// New same-key consent deliberately has a distinct signed admission. No local
// retry fence, host role or accepted task grant may be reinterpreted across it.
func groupInteractionRejoin(t *testing.T, admin, person *Agent, packet GroupContext) GroupContext {
	t.Helper()
	self, _, _ := person.store.selfPerson(person.Address)
	admission, err := person.SignGroupAdmission(packet.Root, packet.State.Seq+1, packet.State.Hash(), nil)
	if err != nil {
		t.Fatal(err)
	}
	next := packet
	next.Proof = nil
	next.State.Seq++
	next.State.Prev = packet.State.Hash()
	next.State.Members = append(slices.Clone(packet.State.Members), protocol.GroupMember{ConvMember: protocol.ConvMember{Person: self.roster.Person, Roster: self.roster.Hash()}, Admission: admission})
	slices.SortFunc(next.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	next, err = admin.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := admin.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.PublishGroup(tctx(t), commit, next); err != nil {
		t.Fatal(err)
	}
	return next
}

func TestGroupInteractionEpochRetryFences(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	msg, err := w.alice.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "recipient epoch original"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "recipient original", func() bool { return len(groupTurns(t, w.bob, packet.State.Conv)) == 1 })
	ref, err := w.alice.RefOf(packet.State.Conv, msg.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.Revise(tctx(t), ref, "recipient epoch edit"); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub=?`, w.bob.Address, envelope.SubRevision).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var queued envelope.Envelope
	if err = json.Unmarshal(raw, &queued); err != nil {
		t.Fatal(err)
	}
	bob, _, _ := w.bob.store.selfPerson(w.bob.Address)
	record, err := w.bob.CreateLocalAgent("Epoch member", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	pid, err := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, w.bob.Address, record.ID, nil, []string{carol.Self().Fingerprint()}, "epoch bound")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "epoch invitation", func() bool { return stateAt(t, w.bob, pid.PID).State == PartInvited })
	if _, err = w.bob.AcceptParticipation(tctx(t), pid.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "epoch acceptance", func() bool { return stateAt(t, w.alice, pid.PID).Claimable() })
	next, err := w.alice.RemoveGroupMember(tctx(t), packet.State.Conv, bob.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, next, carol)
	if view := stateAt(t, w.alice, pid.PID); view.Claimable() || view.Invite != "" {
		t.Fatal("removed member host silently became visitor")
	}
	next = groupInteractionRejoin(t, w.alice, w.bob, next)
	groupGovernanceAwait(t, next, w.bob, carol)
	if view := stateAt(t, w.alice, pid.PID); view.Claimable() || view.Invite != "" {
		t.Fatal("same-key host rejoin revived PID")
	}
	if _, err = w.alice.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, stateQueued, queued.ID); err != nil {
		t.Fatal(err)
	}
	if handled, allowed, e := w.alice.mayDeliverGroupControl(queued); e != nil || !handled || allowed {
		t.Fatalf("recipient rejoin revived queued control %v %v %v", handled, allowed, e)
	}
	var stablePID, taskKeyPID, inviterPID ParticipationInfo
	for i, setup := range []struct {
		inviter  *Agent
		taskKeys []string
	}{{w.alice, nil}, {w.alice, []string{carol.Self().Fingerprint()}}, {carol, nil}} {
		inv, e := setup.inviter.InviteNamedAgent(tctx(t), packet.State.Conv, w.bob.Address, record.ID, nil, setup.taskKeys, "exact epoch scope")
		if e != nil {
			t.Fatal(e)
		}
		eventually(t, "fresh member invite", func() bool { return stateAt(t, w.bob, inv.PID).State == PartInvited })
		if _, e = w.bob.AcceptParticipation(tctx(t), inv.PID); e != nil {
			t.Fatal(e)
		}
		eventually(t, "fresh member accepted", func() bool { return stateAt(t, setup.inviter, inv.PID).Claimable() })
		switch i {
		case 0:
			stablePID = inv
		case 1:
			taskKeyPID = inv
		case 2:
			inviterPID = inv
		}
	}
	task, err := carol.AskAgent(tctx(t), stablePID.PID, envelope.KindTask, "requester epoch original task")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if stub.runs() != 0 {
		t.Fatal("unapproved task ran before epoch change")
	}
	// The sender's epoch has its own fence; a current recipient cannot vouch it.
	msg, err = carol.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "sender epoch original"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err = carol.RefOf(packet.State.Conv, msg.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = carol.Revise(tctx(t), ref, "sender epoch edit"); err != nil {
		t.Fatal(err)
	}
	if err = carol.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub=?`, w.alice.Address, envelope.SubRevision).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &queued)
	cp, _, _ := carol.store.selfPerson(carol.Address)
	next, err = w.alice.RemoveGroupMember(tctx(t), packet.State.Conv, cp.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, next, w.bob)
	next = groupInteractionRejoin(t, w.alice, carol, next)
	groupGovernanceAwait(t, next, carol, w.bob)
	if stateAt(t, w.alice, taskKeyPID.PID).Claimable() {
		t.Fatal("same-key task grant admission revived")
	}
	if stateAt(t, w.bob, inviterPID.PID).Claimable() {
		t.Fatal("same-key inviter admission revived")
	}
	if !stateAt(t, w.bob, stablePID.PID).Claimable() {
		t.Fatal("unrelated requester change ended stable invite")
	}
	// Accepting it is refused, saying why (BUG-23): nothing would run it.
	if err = w.bob.Accept(task.ID); !errors.Is(err, ErrNothingRuns) || !strings.Contains(err.Error(), "group admission changed") {
		t.Fatalf("accept of a task whose requester's admission changed: %v", err)
	}
	if s, _ := w.bob.store.jobState(task.ID); s == stateAccepted || s == stateRunning {
		t.Fatalf("a refused accept left the task %s", s)
	}
	// Accepted before the admission changed: the worker does not run it either.
	if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateAccepted, task.ID); err != nil {
		t.Fatal(err)
	}
	w.bob.NoteChange() // as a stored change does: the worker looks again from the start
	w.bob.wakeWorker()
	waitState(t, w.bob, task.ID, stateNotRun)
	if stub.runs() != 0 {
		t.Fatal("same-key requester rejoin revived queued task")
	}
	if _, err = carol.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, stateQueued, queued.ID); err != nil {
		t.Fatal(err)
	}
	if handled, allowed, e := carol.mayDeliverGroupControl(queued); e != nil || !handled || allowed {
		t.Fatalf("sender rejoin revived queued control %v %v %v", handled, allowed, e)
	}
}

func TestGroupInteractionControlReorderedOriginal(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	stops[w.bob]()
	msg, err := w.alice.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "immutable original"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := w.alice.RefOf(packet.State.Conv, msg.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.Revise(tctx(t), ref, "visible revised original"); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub=?`, w.bob.Address, envelope.SubRevision).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var control envelope.Envelope
	json.Unmarshal(raw, &control)
	if len(groupTurns(t, w.bob, packet.State.Conv)) != 0 {
		t.Fatal("fixture original unexpectedly delivered before reorder")
	}
	if err = w.bob.accept(tctx(t), control); err != nil {
		t.Fatal(err)
	}
	var why string
	if err = w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, control.ID).Scan(&why); err != nil || why != reasonProof {
		t.Fatalf("unknown original failed to hold %s %v", why, err)
	}
	var original envelope.Envelope
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND lid=? AND coalesce(sub,'')=''`, w.bob.Address, msg.LID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &original)
	if err = w.bob.accept(tctx(t), original); err != nil {
		t.Fatal(err)
	}
	w.bob.retryProof(tctx(t))
	m := groupTurns(t, w.bob, packet.State.Conv)
	if len(m) != 1 || m[0].Shown(m[0].Body) != "visible revised original" {
		t.Fatal("exact original did not release held control")
	}
	wrong := envelope.Inner{Conv: packet.State.Conv, Ref: &envelope.Ref{ID: original.ID, Fingerprint: w.alice.Self().Fingerprint()}}
	if err = w.bob.groupControlTarget(w.bob.store.db, wrong); err == nil {
		t.Fatal("physical ID substituted for exact LID")
	}
}

func TestGroupInteractionVisitorReorderedCarriers(t *testing.T) {
	stub := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	host := proofReader(t, w, "reordered-visitor")
	stop := runAgent(t, host)
	publishGroupFixtureCaps(t, host, true)
	record, err := host.CreateLocalAgent("Reordered reviewer", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	stop()
	p, err := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	read := func(sub string) envelope.Envelope {
		var raw []byte
		var env envelope.Envelope
		if e := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND pid=? AND sub=? ORDER BY rowid DESC LIMIT 1`, host.Address, p.PID, sub).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if e := json.Unmarshal(raw, &env); e != nil {
			t.Fatal(e)
		}
		return env
	}
	contextEnv, proofEnv, eventEnv := read(envelope.SubGroupContext), read(envelope.SubGroupProof), read(envelope.SubEvent)
	if err = host.accept(tctx(t), contextEnv); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err = host.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, contextEnv.ID).Scan(&reason); err != nil || reason != reasonProof {
		t.Fatalf("context without invitation not held %s %v", reason, err)
	}
	if _, err = host.GroupContext(packet.State.Conv); err == nil {
		t.Fatal("uninvited context installed")
	}
	if err = host.accept(tctx(t), eventEnv); err != nil {
		t.Fatal(err)
	}
	// A future original public record alone cannot install or create work.
	last, err := groupProofRecord(w.alice.store.db, packet.State.Conv, packet.Root.Creator.Fingerprint, packet.State.Seq)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := w.alice.groupLifecycleCopy(packet.Root, envelope.SubGroupProof, protocol.GroupCarrier{V: 1, Seq: last.Seq, Hash: last.Hash}, protocol.GroupJournalPage{Records: []protocol.GroupCommit{last}}, host.Self(), p.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.addConvOutbox([]outCopy{copy}, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	groupGovernanceDeliver(t, w.alice, host, copy.env)
	if err = host.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, copy.env.ID).Scan(&reason); err != nil || reason != reasonProof {
		t.Fatalf("partial original prefix not held %s %v", reason, err)
	}
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err == nil {
		t.Fatal("incomplete prefix accepted PID")
	}
	home := host.home
	host.Close()
	host, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	if err = host.accept(tctx(t), proofEnv); err != nil {
		t.Fatal(err)
	}
	host.retryProof(tctx(t))
	view, err := host.Participation(p.PID)
	if err != nil || view.State != PartInvited {
		t.Fatalf("durable reordered invitation %s %v", view.State, err)
	}
	if err = host.accept(tctx(t), contextEnv); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = host.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, contextEnv.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("context replay duplicated durable row")
	}
	if stub.runs() != 0 {
		t.Fatal("proof/context/replay executed work")
	}
	// Wrong PID is shape-valid but has no local disclosure authorization.
	inner, err := envelope.Open(contextEnv, host.id, host.Address, w.alice.Self())
	if err != nil {
		t.Fatal(err)
	}
	inner.ID, inner.LID, inner.PID = protocol.NewID(), protocol.NewID(), protocol.NewID()
	recipient, _ := host.Self().Recipient()
	forged, err := envelope.Seal(inner, w.alice.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.accept(tctx(t), forged); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, host, `id=?`, forged.ID) != 0 {
		t.Fatal("unrelated PID admitted visitor context")
	}
}

func TestGroupInteractionVisitorLargeCurrentContext(t *testing.T) {
	stub := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	for range 50 {
		if _, err := w.alice.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "PRIVATE_ROOM_CONTENT_NOT_DISCLOSED"}); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := w.alice.SelectGroupHistory(tctx(t), packet.State.Conv, GroupHistorySelection{Last: 50})
	if err != nil || len(refs) != 50 {
		t.Fatalf("large signed selection %d %v", len(refs), err)
	}
	joiner := proofReader(t, w, "large-context-member")
	runAgent(t, joiner)
	publishGroupFixtureCaps(t, joiner, true)
	person, _, _ := joiner.store.selfPerson(joiner.Address)
	inv, err := w.alice.InviteGroup(tctx(t), packet.State.Conv, person.roster.Person, refs)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "large explicit membership proposal", func() bool {
		rows, e := joiner.GroupInvitations()
		return e == nil && len(rows) == 1 && rows[0].ID == inv.ID
	})
	if err = joiner.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "large signed current membership", func() bool {
		p, e := w.alice.GroupContext(packet.State.Conv)
		if e != nil {
			return false
		}
		if _, present := p.State.Member(person.roster.Person); !present {
			return false
		}
		packet = p
		return true
	})
	raw, _ := json.Marshal(packet)
	if len(raw) <= 8<<10 {
		t.Fatalf("current context fixture too small: %d", len(raw))
	}
	host := proofReader(t, w, "large-context-visitor")
	runAgent(t, host)
	publishGroupFixtureCaps(t, host, true)
	record, err := host.CreateLocalAgent("Large current reviewer", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, host.Address, record.ID, nil, nil, "member list disclosure only")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "large bounded visitor context verified", func() bool { view, e := host.Participation(p.PID); return e == nil && view.State == PartInvited })
	groupGovernanceAwait(t, packet, host)
	var encoded []byte
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND pid=? AND sub=?`, host.Address, p.PID, envelope.SubGroupContext).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= 8<<10 {
		t.Fatalf("current context inlined instead of bounded attachment %d", len(encoded))
	}
	for _, m := range groupTurns(t, host, packet.State.Conv) {
		if strings.Contains(m.Body, "PRIVATE_ROOM_CONTENT_NOT_DISCLOSED") {
			t.Fatal("current membership carrier disclosed selected room plaintext")
		}
	}
	if stub.runs() != 0 {
		t.Fatal("large signed current context executed work")
	}
}

func TestGroupInteractionTypingActualAudience(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	scope := protocol.TypingScope{Conv: packet.State.Conv}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		eventually(t, "typing current native transport", func() bool { view, e := a.Typing(scope); return e == nil && view.Current && view.Supported })
	}
	result, err := w.bob.SendTyping(tctx(t), scope, true)
	if err != nil || result.Submitted != 2 {
		t.Fatalf("actual group typing audience: %+v %v", result, err)
	}
	for _, a := range []*Agent{w.alice, carol} {
		eventually(t, "human group typing arrives", func() bool { return typingCount(t, a, scope) == 1 })
	}
	if _, err = w.bob.SendTyping(tctx(t), scope, false); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, carol} {
		eventually(t, "human group typing stops", func() bool { return typingCount(t, a, scope) == 0 })
	}
	// Delayed signed arrival exercises the real TTL using the native ingress.
	applyTyping(w.alice, typingWire(t, carol, w.alice, scope, true, time.Now().Add(-4500*time.Millisecond).UnixMilli()))
	if typingCount(t, w.alice, scope) != 1 {
		t.Fatal("delayed signed group typing absent before expiry")
	}
	eventually(t, "group typing signed TTL expires", func() bool { return typingCount(t, w.alice, scope) == 0 })
	person, _, _ := w.bob.store.selfPerson(w.bob.Address)
	next, err := w.alice.RemoveGroupMember(tctx(t), packet.State.Conv, person.roster.Person)
	if err != nil {
		t.Fatal(err)
	}
	groupGovernanceAwait(t, next, carol)
	applyTyping(w.alice, typingWire(t, w.bob, w.alice, scope, true, time.Now().UnixMilli()))
	if typingCount(t, w.alice, scope) != 0 {
		t.Fatal("removed group member shown typing")
	}
	if rows, e := w.alice.ConversationMessages(packet.State.Conv); e != nil || len(rows) != 0 {
		t.Fatalf("ephemeral typing persisted: %v %v", rows, e)
	}
}

func TestGroupInteractionLinkedControlHistory(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	sent, err := w.alice.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "linked original"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "original before link", func() bool { return len(groupTurns(t, w.bob, packet.State.Conv)) == 1 })
	ref, err := w.alice.RefOf(packet.State.Conv, sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.Revise(tctx(t), ref, "linked revision"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "revision before link", func() bool {
		m := groupTurns(t, w.bob, packet.State.Conv)
		return len(m) == 1 && m[0].Shown(m[0].Body) == "linked revision"
	})
	phone, await, _ := linkPhone(t, w.bob, "phone")
	request := pendingLink(t, w.bob)
	if err = w.bob.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	copies, err := w.alice.groupDeliveryCopies(tctx(t), packet)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.addConvOutbox(copies, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "linked verified context", func() bool { _, e := phone.GroupContext(packet.State.Conv); return e == nil })
	// Snapshot uses original inbox receive stamp, not the producer's local composite.
	eventually(t, "linked control history converges", func() bool {
		m := groupTurns(t, phone, packet.State.Conv)
		return len(m) == 1 && m[0].Shown(m[0].Body) == "linked revision"
	})
	stops[w.bob]()
	zero, _ := json.Marshal(historyPos{})
	if _, err = w.bob.store.db.Exec(`UPDATE history_jobs SET state='running',pos=? WHERE device=?`, string(zero), phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.store.db.Exec(`CREATE TRIGGER group_snapshot_transient BEFORE INSERT ON outbox WHEN NEW.sub='history' BEGIN SELECT RAISE(ABORT,'synthetic transient insert'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.historyPageFor(phone.Self(), historyPos{}); err == nil || !strings.Contains(err.Error(), "synthetic transient insert") {
		t.Fatalf("transient snapshot failure lost: %v", err)
	}
	var after []byte
	var jobState string
	if err = w.bob.store.db.QueryRow(`SELECT pos,state FROM history_jobs WHERE device=?`, phone.Address).Scan(&after, &jobState); err != nil || !bytes.Equal(after, zero) || jobState != "running" {
		t.Fatalf("failed snapshot advanced cursor: %s %s %v", after, jobState, err)
	}
	if _, err = w.bob.store.db.Exec(`DROP TRIGGER group_snapshot_transient`); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	var body string
	if err = w.bob.store.db.QueryRow(`SELECT body FROM outbox WHERE recipient=? AND sub=? AND json_extract(body,'$.sub')=?`, phone.Address, envelope.SubHistory, envelope.SubRevision).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var item HistoryItem
	if err = json.Unmarshal([]byte(body), &item); err != nil {
		t.Fatal(err)
	}
	item.FromKey = w.bob.Self().Fingerprint()
	if e := phone.groupControlHistoryCheck(phone.store.db, packet.Root, w.bob.Self(), item); e == nil {
		t.Fatal("forged original control author admitted")
	}
	if _, err = w.alice.Retract(tctx(t), ref, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "linked live tombstone", func() bool {
		m := groupTurns(t, phone, packet.State.Conv)
		return len(m) == 1 && m[0].Deleted && m[0].Body == ""
	})
}

func TestGroupInteractionNamedMemberAndVisitor(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		fakeNotify(a)
	}
	member, err := w.bob.CreateLocalAgent("Member builder", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	host := proofReader(t, w, "outside")
	fakeNotify(host)
	runAgent(t, host)
	publishGroupFixtureCaps(t, host, true)
	visitor, err := host.CreateLocalAgent("Outside reviewer", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(t.TempDir(), "selected.txt")
	if err = os.WriteFile(selected, []byte("GROUP_SELECTED_BYTES"), 0600); err != nil {
		t.Fatal(err)
	}
	sent, err := w.alice.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "selected group context", Files: []OutgoingFile{{Path: selected}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "GROUP_UNSELECTED_SECRET"}); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, w.bob.Address, member.ID, []string{sent.LID}, nil, "member scope")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "member invitation", func() bool { view, e := w.bob.Participation(p.PID); return e == nil && view.State == PartInvited })
	if _, err = w.bob.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "member acceptance", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "member question")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		answer := replyAt(t, a, packet.State.Conv, q.ID)
		if answer.AgentID != member.ID || answer.PID != p.PID {
			t.Fatalf("member output attribution %+v", answer)
		}
	}
	if strings.Contains(stub.last(), "GROUP_UNSELECTED_SECRET") {
		t.Fatal("unselected member context leaked")
	}
	v, err := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, host.Address, visitor.ID, []string{sent.LID}, nil, "visitor scope")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor original proof/current context", func() bool {
		view, e := host.Participation(v.PID)
		return e == nil && view.State == PartInvited && view.External
	})
	if _, err = host.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "ambient outsider"}); err == nil {
		t.Fatal("visitor ordinary room send")
	}
	if _, err = host.GroupMembers(packet.State.Conv); err == nil {
		t.Fatal("visitor gained member projection authority")
	}
	if _, err = host.AcceptParticipation(tctx(t), v.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor acceptance", func() bool { return stateAt(t, w.alice, v.PID).Claimable() })
	q, err = w.alice.AskAgent(tctx(t), v.PID, envelope.KindQuestion, "visitor question")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob, carol, host} {
		answer := replyAt(t, a, packet.State.Conv, q.ID)
		if answer.AgentID != visitor.ID || answer.PID != v.PID {
			t.Fatalf("visitor output attribution %+v", answer)
		}
	}
	if !strings.Contains(stub.last(), "GROUP_SELECTED_BYTES") || strings.Contains(stub.last(), "GROUP_UNSELECTED_SECRET") || strings.Contains(stub.last(), "member question") {
		t.Fatalf("visitor grant isolation %s", stub.last())
	}
	if stub.runs() != 2 {
		t.Fatalf("wrong independent execution count %d", stub.runs())
	}
	rootRaw, _ := json.Marshal(packet.Root)
	for _, bad := range []struct {
		signer            *Agent
		pid, agent, reply string
	}{
		{host, p.PID, visitor.ID, q.ID},
		{host, v.PID, member.ID, q.ID},
		{w.bob, v.PID, visitor.ID, q.ID},
		{host, v.PID, visitor.ID, protocol.NewID()},
	} {
		forged := craft(t, bad.signer, w.alice, envelope.Inner{Conv: packet.State.Conv, Root: rootRaw, LID: protocol.NewID(), PID: bad.pid, Kind: envelope.KindAnswer, AgentID: bad.agent, ReplyTo: bad.reply, Body: "forged unrelated output"})
		if err = w.alice.accept(tctx(t), forged); err != nil {
			t.Fatal(err)
		}
		if inboxCount(t, w.alice, `id=?`, forged.ID) != 0 {
			t.Fatal("raw signed cross-PID/agent/host/request output admitted")
		}
	}
	views, err := host.Conversations()
	if err != nil {
		t.Fatal(err)
	}
	visitorRole := false
	for _, view := range views {
		if view.ID == packet.State.Conv {
			visitorRole = view.Role == "visitor" && view.Frozen == ""
		}
	}
	if !visitorRole {
		t.Fatal("verified visitor projection missing")
	}
	ref, err := host.RefOf(packet.State.Conv, q.ID, "in")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.React(tctx(t), ref, "👍", false); err == nil {
		t.Fatal("visitor reaction gained room authority")
	}
	if _, err = host.SendTyping(tctx(t), protocol.TypingScope{Conv: packet.State.Conv}, true); err == nil {
		t.Fatal("visitor typing gained room authority")
	}
	if _, err = host.groupStatusScope(host.store.db, ref, w.bob.Address, w.bob.Self().Fingerprint()); err == nil {
		t.Fatal("member key forged visitor status")
	}
	badRef := ref
	badRef.ID = protocol.NewID()
	if _, err = host.groupStatusScope(host.store.db, badRef, host.Address, host.Self().Fingerprint()); err == nil {
		t.Fatal("unknown request forged status")
	}
	// A task needs this host's separate conscious execution acceptance.
	task, err := w.alice.AskAgent(tctx(t), v.PID, envelope.KindTask, "explicit visitor task")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, host, task.ID, stateAwaiting)
	if stub.runs() != 2 {
		t.Fatal("host participation acceptance executed an unapproved task")
	}
	host.noteStatus(task.ID)
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		eventually(t, "authenticated awaiting status", func() bool {
			for _, m := range groupTurns(t, a, packet.State.Conv) {
				if m.LID == task.LID && m.Exec != nil && m.Exec.Host == host.Address && m.Exec.State == "awaiting" {
					return true
				}
			}
			return false
		})
	}
	if err = host.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob, carol, host} {
		replyAt(t, a, packet.State.Conv, task.ID)
	}
	if stub.runs() != 3 {
		t.Fatalf("explicit task count %d", stub.runs())
	}
	if err = host.Accept(task.ID); err == nil {
		t.Fatal("completed task silently rerun")
	}
	follow, err := w.alice.AskAgent(tctx(t), v.PID, envelope.KindQuestion, "visitor follow-up")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, packet.State.Conv, follow.ID)
	if stub.runs() != 4 {
		t.Fatal("follow-up reran original")
	}
	memberTask, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindTask, "immutable group task baseline")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, memberTask.ID, stateAwaiting)
	memberRef, err := w.alice.RefOf(packet.State.Conv, memberTask.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.Revise(tctx(t), memberRef, "changed visible group task"); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.Retract(tctx(t), memberRef, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "request tombstone never executes", func() bool {
		for _, m := range groupTurns(t, w.bob, packet.State.Conv) {
			if m.LID == memberTask.LID {
				return m.Deleted && m.Body == "immutable group task baseline"
			}
		}
		return false
	})
	if stub.runs() != 4 {
		t.Fatal("request edit/deletion executed a job")
	}
	if err = w.bob.Accept(memberTask.ID); err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, packet.State.Conv, memberTask.ID)
	if stub.runs() != 5 || !strings.HasSuffix(stub.last(), "immutable group task baseline\n") {
		t.Fatal("admitted request baseline changed or rerun")
	}
	phone, await, _ := linkPhone(t, host, "visitor-phone")
	if err = host.DecideLink(tctx(t), pendingLink(t, host).ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	if _, err = host.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	var leak int
	if err = host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND recipient=?`, packet.State.Conv, phone.Address).Scan(&leak); err != nil || leak != 0 {
		t.Fatalf("visitor sibling disclosure %d %v", leak, err)
	}
	if _, err = phone.GroupContext(packet.State.Conv); err == nil {
		t.Fatal("visitor sibling gained group context")
	}
	if err = host.RequestFile(tctx(t), sent.ID, 0); err == nil {
		t.Fatal("visitor used generic file fetch for ambient source")
	}
	if _, err = host.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Kind: envelope.KindAnswer, PID: p.PID, AgentID: visitor.ID, ReplyTo: q.ID, Body: "cross PID forged output"}); err == nil {
		t.Fatal("cross PID output admitted")
	}
	if _, err = w.bob.DismissParticipation(tctx(t), v.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor dismissed", func() bool { return stateAt(t, host, v.PID).State == PartDismissed })
	if _, err = w.alice.AskAgent(tctx(t), v.PID, envelope.KindQuestion, "dismissed"); err == nil {
		t.Fatal("dismissed future work")
	}
	if _, err = host.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Kind: envelope.KindAnswer, PID: v.PID, AgentID: visitor.ID, ReplyTo: q.ID, Body: "late output"}); err == nil {
		t.Fatal("dismissed output")
	}
}
