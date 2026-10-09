package client

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestHistoryContributionReviewedGroup(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	ctx := tctx(t)
	destination := packet.State.Conv
	source := newDM(t, w.alice, w.bob)
	first, e := w.alice.SendConv(ctx, source, ConvOutgoing{Body: "Never disclose omitted parent"})
	if e != nil {
		t.Fatal(e)
	}
	selected, e := w.alice.SendConv(ctx, source, ConvOutgoing{Body: "Selected quoted @agent task text", ReplyTo: first.LID})
	if e != nil {
		t.Fatal(e)
	}
	var before string
	if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, selected.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	r, e := w.alice.PreviewHistoryContribution(HistoryContributionRequest{Source: source, IDs: []string{selected.ID}, Destination: destination})
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Items) != 1 || len(r.Audience) != 3 || strings.Contains(r.Body, "Never disclose") || !strings.Contains(r.Body, "parent not shared") || !strings.Contains(r.Body, "agentnet:message/"+selected.ID+"?conv="+source) {
		t.Fatalf("bad exact inert review: %+v", r)
	}
	sent, e := w.alice.ApplyHistoryContribution(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, "only selected inert imported context", func() bool {
			m, n := convMsg(t, a, destination, func(m ConvMessage) bool { return m.LID == sent.LID })
			return n == 1 && m.Body == r.Body && m.Kind == envelope.KindMessage && m.PID == "" && m.Target == nil && m.ReplyTo == ""
		})
	}
	var after string
	if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, selected.ID).Scan(&after); e != nil {
		t.Fatal(e)
	}
	if before != after {
		t.Fatal("contribution mutated source")
	}
	again, e := w.alice.ApplyHistoryContribution(ctx, r)
	if e != nil || again.LID != sent.LID {
		t.Fatalf("same operation resent: %v", e)
	}
	modified := r
	modified.Body += " extra"
	if _, e = w.alice.ApplyHistoryContribution(ctx, modified); !errors.Is(e, ErrHistoryContributionChanged) {
		t.Fatalf("changed retry accepted: %v", e)
	}
	phone := linked(t, w.alice)
	eventually(t, "original import reaches own linked device", func() bool {
		m, _ := phone.ConversationMessages(destination)
		for _, x := range m {
			if x.LID == sent.LID {
				return true
			}
		}
		return false
	})
	if again, e = phone.ApplyHistoryContribution(ctx, r); e != nil || again.LID != sent.LID || again.State != "stored" {
		t.Fatalf("linked exact operation duplicated: %+v %v", again, e)
	}
}

func TestHistoryContributionChangedSourceAndAudience(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	ctx := tctx(t)
	destination := packet.State.Conv
	source := newDM(t, w.alice, w.bob)
	selected, e := w.alice.SendConv(ctx, source, ConvOutgoing{Body: "Reviewed version"})
	if e != nil {
		t.Fatal(e)
	}
	request := HistoryContributionRequest{Source: source, IDs: []string{selected.ID}, Destination: destination}
	r, e := w.alice.PreviewHistoryContribution(request)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.Revise(ctx, ControlRef{Conv: source, ID: selected.LID, Fingerprint: w.alice.Self().Fingerprint()}, "Current edited version"); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyHistoryContribution(ctx, r); !errors.Is(e, ErrHistoryContributionChanged) {
		t.Fatalf("stale source version not canonical: %v", e)
	}
	r, e = w.alice.PreviewHistoryContribution(request)
	if e != nil || !strings.Contains(r.Body, "Current edited version") {
		t.Fatalf("edited version not reviewed: %v", e)
	}
	if _, e = w.alice.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, carol.Address); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyHistoryContribution(ctx, r); e == nil {
		t.Fatal("changed audience key disclosed")
	}
	if _, e = w.alice.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, carol.Address); e != nil {
		t.Fatal(e)
	}
	request.Topic = protocol.NewID()
	request.NewTopic = true
	request.Title = "Contributed topic"
	r, e = w.alice.PreviewHistoryContribution(request)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyHistoryContribution(ctx, r); e != nil {
		t.Fatal(e)
	}
	topics, e := w.alice.ChatTopics(destination)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, x := range topics {
		if x.ID == request.Topic {
			found = x.Title == request.Title && x.Count == 1
		}
	}
	if !found {
		t.Fatal("named contribution topic missing")
	}
	mainRequest := HistoryContributionRequest{Source: source, IDs: []string{selected.ID}, Destination: destination}
	mainReview, e := w.alice.PreviewHistoryContribution(mainRequest)
	if e != nil {
		t.Fatal(e)
	}
	// Seed an ordinary Main message so the existing Main preference applies.
	if _, e = w.alice.SendConv(ctx, destination, ConvOutgoing{Body: "Existing Main destination"}); e != nil {
		t.Fatal(e)
	}
	mainReview, e = w.alice.PreviewHistoryContribution(mainRequest)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ChangeChatMainTopic(ctx, destination, "archive", "", 0); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyHistoryContribution(ctx, mainReview); !errors.Is(e, ErrHistoryContributionChanged) {
		t.Fatalf("archived Main should invalidate review: %v", e)
	}
	if _, e = w.alice.PreviewHistoryContribution(mainRequest); e == nil {
		t.Fatal("archived Main destination accepted")
	}
	deletedRequest := HistoryContributionRequest{Source: source, IDs: []string{selected.ID}, Destination: destination, Topic: request.Topic}
	deletedReview, e := w.alice.PreviewHistoryContribution(deletedRequest)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.Retract(tctx(t), ControlRef{Conv: source, ID: selected.LID, Fingerprint: w.alice.Self().Fingerprint()}, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyHistoryContribution(tctx(t), deletedReview); !errors.Is(e, ErrHistoryContributionChanged) {
		t.Fatalf("deleted source did not invalidate old review: %v", e)
	}
	if _, e = w.alice.PreviewHistoryContribution(deletedRequest); !errors.Is(e, ErrNoMessage) {
		t.Fatalf("deleted source reviewed: %v", e)
	}
}

func TestTopicOrganizationPendingPinClaim(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	ctx := tctx(t)
	first, e := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Source", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	target, e := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Target", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	targetMsg, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == target.LID })
	to := targetMsg.Topic
	review, e := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{first.LID}, Topic: to})
	if e != nil {
		t.Fatal(e)
	}
	old := beforeOutbox
	beforeOutbox = func() {
		if _, e := w.alice.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.bob.Address); e != nil {
			t.Fatal(e)
		}
	}
	defer func() { beforeOutbox = old }()
	if _, e = w.alice.ApplyTopicOrganization(ctx, review); e == nil {
		t.Fatal("pin changed after final preview but move committed")
	}
	msgs, _ := w.alice.ConversationMessages(conv)
	if ChatTopicAssignments(msgs)[first.LID] == to {
		t.Fatal("pending recipient pin changed local assignment")
	}
}

func TestHistoryContributionSelectedFilesAndGuestAuthority(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	ctx := tctx(t)
	destination := packet.State.Conv
	source := newDM(t, w.alice, w.bob)
	chosen, data := writeFile(t, t.TempDir(), "chosen.bin", 4096)
	omitted, _ := writeFile(t, t.TempDir(), "PRIVATE_OMITTED_FILE.bin", 512)
	selected, e := w.alice.SendConv(ctx, source, ConvOutgoing{Body: "Only selected bytes", Files: []OutgoingFile{{Path: chosen}, {Path: omitted}}})
	if e != nil {
		t.Fatal(e)
	}
	guest := proofReader(t, w, "contributionguest")
	runAgent(t, guest)
	publishGroupFixtureCaps(t, guest, true)
	for _, a := range []*Agent{w.alice, w.bob, carol, guest} {
		roomReader(t, a)
	}
	ctx = tctx(t) // Each action keeps the standard bound; fixture setup may exceed it under race.
	invite, e := w.alice.InviteHuman(ctx, destination, guest.Address, nil, "Current audience only")
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "contribution guest invited", func() bool { return stateAt(t, guest, invite.PID).State == PartInvited })
	request := HistoryContributionRequest{Source: source, IDs: []string{selected.ID}, Destination: destination, Files: []HistoryContributionFileRef{{ID: selected.ID, Index: 0}}}
	pending, e := w.alice.PreviewHistoryContribution(request)
	if e != nil {
		t.Fatal(e)
	}
	if len(pending.Audience) != 3 || !pending.AudiencePending {
		t.Fatalf("pending invitee received audience promise: %+v", pending.Audience)
	}
	ctx = tctx(t)
	if _, e = guest.AcceptParticipation(ctx, invite.PID); e != nil {
		t.Fatal(e)
	}
	eventually(t, "contribution guest accepted", func() bool { return stateAt(t, w.alice, invite.PID).HumanActive() })
	ctx = tctx(t)
	if _, e = w.alice.ApplyHistoryContribution(ctx, pending); !errors.Is(e, ErrHistoryContributionChanged) {
		t.Fatalf("accepted audience changed without review: %v", e)
	}
	review, e := w.alice.PreviewHistoryContribution(request)
	if e != nil {
		t.Fatal(e)
	}
	if len(review.Audience) != 4 || strings.Contains(review.Body, "PRIVATE_OMITTED_FILE") || !strings.Contains(review.Body, "1 source file(s) omitted") {
		t.Fatal("selection or accepted audience changed")
	}
	sent, e := w.alice.ApplyHistoryContribution(ctx, review)
	if e != nil {
		t.Fatal(e)
	}
	for _, a := range []*Agent{w.bob, carol, guest} {
		var imported ConvMessage
		eventually(t, "selected encrypted attachment delivered", func() bool {
			m, n := convMsg(t, a, destination, func(m ConvMessage) bool { return m.LID == sent.LID })
			imported = m
			return n == 1 && len(m.Attachments) == 1
		})
		reader, _, e := a.OpenFileFrom(tctx(t), imported.Dir, imported.ID, 0)
		if e != nil {
			t.Fatal(e)
		}
		opened, e := io.ReadAll(reader)
		reader.Close()
		if e != nil || !bytes.Equal(opened, data) {
			t.Fatalf("exact selected file bytes: %v", e)
		}
	}
	ctx = tctx(t)
	seedReview, e := w.alice.PreviewHistoryContribution(request)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.SendConv(WithQueuedSend(ctx, seedReview.Operation), destination, ConvOutgoing{Body: seedReview.Body, Topic: seedReview.Topic}); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyHistoryContribution(ctx, seedReview); !errors.Is(e, ErrHistoryContributionChanged) {
		t.Fatalf("unrelated same-body message falsely proved selected files imported: %v", e)
	}
	imported, _ := convMsg(t, guest, destination, func(m ConvMessage) bool { return m.LID == sent.LID })
	denied, e := guest.PreviewHistoryContribution(HistoryContributionRequest{Source: destination, IDs: []string{imported.ID}, Destination: source})
	if e == nil || len(denied.Items) != 0 || !strings.Contains(e.Error(), "full member") {
		t.Fatalf("limited source guest could reshare: %+v %v", denied, e)
	}
	fresh, e := w.alice.PreviewHistoryContribution(request)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(w.alice.keptPath(fresh.Items[0].Files[0].SHA256)); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyHistoryContribution(ctx, fresh); !errors.Is(e, ErrHistoryContributionChanged) {
		t.Fatalf("missing selected bytes should invalidate review: %v", e)
	}
	if _, e = w.alice.PreviewHistoryContribution(request); e == nil {
		t.Fatal("unavailable chosen file previewed")
	}
	var copies int
	if e = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=?`, fresh.Operation).Scan(&copies); e != nil || copies != 0 {
		t.Fatalf("missing bytes left a disclosed copy: %d %v", copies, e)
	}
}

func TestHistoryContributionCommitTopicAndPinRecheck(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	ctx := tctx(t)
	destination := packet.State.Conv
	source := newDM(t, w.alice, w.bob)
	selected, e := w.alice.SendConv(ctx, source, ConvOutgoing{Body: "Commit exact selection"})
	if e != nil {
		t.Fatal(e)
	}
	target, e := w.alice.SendConv(ctx, destination, ConvOutgoing{Body: "Destination", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	targetMessage, _ := convMsg(t, w.alice, destination, func(m ConvMessage) bool { return m.LID == target.LID })
	request := HistoryContributionRequest{Source: source, IDs: []string{selected.ID}, Destination: destination, Topic: targetMessage.Topic}
	for _, boundary := range []string{"pin", "topic"} {
		t.Run(boundary, func(t *testing.T) {
			review, e := w.alice.PreviewHistoryContribution(request)
			if e != nil {
				t.Fatal(e)
			}
			previous := beforeOutbox
			beforeOutbox = func() {
				if boundary == "pin" {
					_, e = w.alice.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.bob.Address)
				} else {
					_, e = w.alice.ChangeChatTopic(ctx, destination, targetMessage.Topic, "archive", "", 0)
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			_, e = w.alice.ApplyHistoryContribution(ctx, review)
			beforeOutbox = previous
			if !errors.Is(e, ErrHistoryContributionChanged) {
				t.Fatalf("%s changed after final preview: %v", boundary, e)
			}
			var count int
			if e = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=?`, review.Operation).Scan(&count); e != nil || count != 0 {
				t.Fatalf("stale contribution committed: %d %v", count, e)
			}
			if boundary == "pin" {
				if _, e = w.alice.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.bob.Address); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestHistoryContributionCausalOrderAndAgentAttribution(t *testing.T) {
	parent, child := protocol.NewID(), protocol.NewID()
	ordered, e := contributionOrder([]ConvMessage{{ID: child, LID: child, ReplyTo: parent, Sent: 1}, {ID: parent, LID: parent, Sent: 2}})
	if e != nil || len(ordered) != 2 || ordered[0].LID != parent {
		t.Fatalf("clock skew broke causal order: %+v %v", ordered, e)
	}
	stub := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	ctx := tctx(t)
	setResponder(t, w.bob, "agentstub", stub.dir, time.Minute)
	source := newDM(t, w.alice, w.bob)
	p := p6Member(t, w.alice, w.bob, source)
	asked, e := w.alice.AskAgent(ctx, p.PID, envelope.KindTask, "Original task only")
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, w.bob, asked.ID, stateAwaiting)
	if e = w.bob.Accept(asked.ID); e != nil {
		t.Fatal(e)
	}
	reply := replyAt(t, w.alice, source, asked.LID)
	review, e := w.alice.PreviewHistoryContribution(HistoryContributionRequest{Source: source, IDs: []string{reply.ID, asked.ID}, Destination: packet.State.Conv})
	if e != nil {
		t.Fatal(e)
	}
	if review.Items[0].LID != asked.LID || !strings.Contains(review.Body, "Agent ") || !strings.Contains(review.Body, "host person:") {
		t.Fatalf("agent output was attributed as human: %+v", review.Items)
	}
	sent, e := w.alice.ApplyHistoryContribution(ctx, review)
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "task and agent result become ordinary inert quote", func() bool {
		m, n := convMsg(t, w.bob, packet.State.Conv, func(m ConvMessage) bool { return m.LID == sent.LID })
		return n == 1 && m.Kind == envelope.KindMessage && m.PID == "" && m.Target == nil && m.AgentID == ""
	})
	if stub.runs() != 1 {
		t.Fatal("history contribution reran original work")
	}
}
