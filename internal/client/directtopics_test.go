package client

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestDirectTopicCompletedRequestAndFilesNeverReplay(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	stopAlice, _ := runWith(t, w, w.alice, RunOptions{})
	runWith(t, w, w.bob, RunOptions{})
	path, _ := writeFile(t, t.TempDir(), "retained-original.bin", 1973)
	request, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask, Body: "completed work with original file", Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, request.ID, stateAwaiting)
	if err = w.bob.Accept(request.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, request.ID, stateAnswered)
	eventually(t, "exact result reaches sender", func() bool { _, ok := findReply(w.alice, request.ID); return ok })
	stopAlice()
	// Model a reader holding the final result and an earlier running report,
	// while the independently delivered terminal report is unavailable.
	if _, err = w.alice.store.db.Exec(`DELETE FROM inbox WHERE ref_id=? AND sub='status' AND json_extract(body,'$.state')!='running'`, request.ID); err != nil {
		t.Fatal(err)
	}
	before, err := w.alice.Conversation(request.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Messages) != 2 || len(before.Messages[0].Attachments) != 1 || st.count() != 1 {
		t.Fatalf("completed original fixture: %+v runs=%d", before, st.count())
	}
	destination, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "existing related work"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{request.ID}
	review, err := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Peer: w.bob.Address, IDs: ids, Topic: destination.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.ApplyTopicOrganization(tctx(t), review); err != nil {
		t.Fatal(err)
	}
	w.alice.convSync(tctx(t))
	w.bob.convSync(tctx(t))
	after, err := w.alice.Conversation(request.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Messages, after.Messages) {
		t.Fatal("move changed original messages, authorship, files or execution reports")
	}
	display, err := w.alice.DisplayConversation(destination.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range display.Messages {
		if m.ID == request.ID && m.Exec != nil && !topicExecutionClosed(m.Exec.State) {
			t.Fatalf("retained result outside display topic no longer settles completion: %+v", m.Exec)
		}
	}
	if topic := topicOf(t, w.alice, request.ID); topic.Pending {
		t.Fatal("moving a completed request without its result makes it pending again")
	}
	var state string
	var attempts int
	if err = w.bob.store.db.QueryRow(`SELECT state,attempts FROM inbox WHERE id=?`, request.ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != stateAnswered || attempts != 1 || st.count() != 1 {
		t.Fatalf("completed work replayed: state=%s attempts=%d runs=%d", state, attempts, st.count())
	}
}

func TestDirectTopicMovePreservesOriginalsAndExecution(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	a := w.alice
	peer := w.bob.Address
	sequence := int64(1000)
	send := func(text, reply string) string {
		t.Helper()
		m, e := a.SendMessage(tctx(t), Outgoing{To: peer, Kind: envelope.KindMessage, Body: text, ReplyTo: reply})
		if e != nil {
			t.Fatal(e)
		}
		sequence++
		if _, e = a.store.db.Exec(`UPDATE outbox SET created_at=?,created_ms=? WHERE id=?`, sequence, sequence*1000, m.ID); e != nil {
			t.Fatal(e)
		}
		return m.ID
	}
	first := send("earlier source", "")
	child := send("unselected child", first)
	destination := send("existing destination", "")
	unrelated := send("unrelated", "")
	before, err := a.Conversation(first, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.RenameTopic(peer, destination, "Keep destination name"); err != nil {
		t.Fatal(err)
	}
	review, err := a.PreviewTopicOrganization(TopicOrganizationRequest{Peer: peer, IDs: []string{first}, Topic: destination})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.ApplyTopicOrganization(tctx(t), review)
	if err != nil {
		t.Fatal(err)
	}
	if replay, e := a.ApplyTopicOrganization(tctx(t), review); e != nil || replay.ID != result.ID {
		t.Fatalf("retry %+v %v", replay, e)
	}
	changed := review
	changed.Topic = unrelated
	if _, e := a.ApplyTopicOrganization(tctx(t), changed); !errors.Is(e, ErrTopicOrganizationChanged) {
		t.Fatalf("changed retry: %v", e)
	}
	after, err := a.Conversation(first, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("organizational move changed original conversation or execution metadata")
	}
	display, err := a.DisplayConversation(destination, 0, 0)
	if err != nil || len(display.Messages) != 2 {
		t.Fatalf("destination messages=%+v err=%v", display, err)
	}
	if display.Messages[0].ID != first || display.Messages[1].ID != destination {
		t.Fatal("chronological originals not retained")
	}
	if topic := topicOf(t, a, first); topic.ID != destination || topic.Title != "Keep destination name" {
		t.Fatalf("destination identity/name changed: %+v", topic)
	}
	if topic := topicOf(t, a, child); topic.ID == destination {
		t.Fatal("unselected descendant followed moved ancestor")
	}
	if topic := topicOf(t, a, unrelated); topic.ID != unrelated {
		t.Fatal("unrelated topic moved")
	}
	// New replies follow their signed ancestry, never a display assignment.
	later := send("later reply", first)
	if topic := topicOf(t, a, later); topic.ID == destination {
		t.Fatal("later reply inherited display move")
	}
	stale, err := a.PreviewTopicOrganization(TopicOrganizationRequest{Peer: peer, IDs: []string{unrelated}, Topic: destination})
	if err != nil {
		t.Fatal(err)
	}
	send("arrival during review", destination)
	if _, err = a.ApplyTopicOrganization(tctx(t), stale); !errors.Is(err, ErrTopicOrganizationChanged) {
		t.Fatalf("stale review: %v", err)
	}
	if n := count(t, a, "inbox WHERE sub='direct-topic-sync'"); n != 0 {
		t.Fatal("placement created a visible message")
	}
	undo := TopicOrganizationUndo{Peer: peer, LID: result.ID, ID: protocol.NewID()}
	if _, err = a.UndoTopicOrganization(tctx(t), undo); err != nil {
		t.Fatal(err)
	}
	if _, err = a.UndoTopicOrganization(tctx(t), undo); err != nil {
		t.Fatalf("undo retry: %v", err)
	}
	if topic := topicOf(t, a, first); topic.ID != first || topic.Count != 3 {
		t.Fatalf("undo did not restore original source: %+v", topic)
	}
	if topic := topicOf(t, a, destination); topic.Count != 2 {
		t.Fatalf("undo moved later destination arrivals: %+v", topic)
	}
	fresh, err := a.PreviewTopicOrganization(TopicOrganizationRequest{Peer: peer, IDs: []string{first}, Topic: destination})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ApplyTopicOrganization(tctx(t), fresh); err != nil {
		t.Fatal(err)
	}
	undo.ID = protocol.NewID()
	if _, err = a.UndoTopicOrganization(tctx(t), undo); !errors.Is(err, ErrTopicOrganizationChanged) {
		t.Fatalf("undo overwrote later placement: %v", err)
	}
}

func TestDirectTopicSyncOwnHumanMissingHistoryAndIsolation(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	m, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "source"})
	if err != nil {
		t.Fatal(err)
	}
	destination, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "destination"})
	if err != nil {
		t.Fatal(err)
	}
	review, err := a.PreviewTopicOrganization(TopicOrganizationRequest{Peer: w.bob.Address, IDs: []string{m.ID}, Topic: destination.ID, Merge: m.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ApplyTopicOrganization(tctx(t), review); err != nil {
		t.Fatal(err)
	}
	if _, err = a.syncDirectTopics(); err != nil {
		t.Fatal(err)
	}
	deliverDeviceHistory(t, a, phone, envelope.SubDirectTopicSync)
	if n := count(t, phone, "direct_topic_placements"); n != 1 {
		t.Fatalf("missing-history inert placement count=%d", n)
	}
	if n := count(t, phone, "inbox WHERE sub='direct-topic-sync'"); n != 0 {
		t.Fatal("carrier became message")
	}
	for range 4 {
		if _, err = a.deviceHistoryPage(phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	deliverDeviceHistory(t, a, phone, envelope.SubDeviceHistory)
	if topic := topicOf(t, phone, m.ID); topic.ID != destination.ID || topic.Count != 2 {
		t.Fatalf("placement before original lost: %+v", topic)
	}
	if page, err := phone.Topics(TopicQuery{Peer: w.bob.Address}); err != nil || page.Matched != 1 {
		t.Fatalf("merge did not reduce topic count: %+v %v", page, err)
	}
	me, _, _ := a.store.selfPerson(a.Address)
	p := protocol.DirectTopicPlacement{Peer: w.bob.Address, ID: m.ID, Author: a.Self().Fingerprint(), Topic: m.ID, Revision: 100, Writer: w.bob.Self().Fingerprint(), Destination: protocol.DirectTopicDestination{Host: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint()}}
	raw, _ := json.Marshal(protocol.DirectTopicSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, Placements: []protocol.DirectTopicPlacement{p}})
	forged := craft(t, w.bob, phone, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubDirectTopicSync, Replica: true, Body: string(raw)})
	if err = phone.verifyAndStore(tctx(t), forged); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, phone, forged.ID) == "" {
		t.Fatal("foreign person organized own direct chat")
	}
	if topic := topicOf(t, phone, m.ID); topic.ID != destination.ID {
		t.Fatal("foreign placement changed history")
	}
}

func TestDirectTopicSyncOwnEndpointsUseOriginalIdentity(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	phone := linked(t, w.alice)
	send := func(body string) string {
		t.Helper()
		v, err := w.alice.SendMessage(tctx(t), Outgoing{To: phone.Address, Kind: envelope.KindMessage, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "own endpoint stores original", func() bool { _, err := phone.TopicOf(v.ID); return err == nil })
		return v.ID
	}
	source, destination, third := send("original source"), send("existing destination"), send("other existing topic")
	// History production fills the outgoing author metadata asynchronously.
	// Wait for that presentation input before capturing the move's CAS review.
	eventually(t, "original author metadata ready on both endpoints", func() bool {
		for _, id := range []string{source, destination, third} {
			var author string
			if err := w.alice.store.db.QueryRow(`SELECT author_fp FROM device_thread_links WHERE storage='out' AND id=?`, id).Scan(&author); err != nil || author != w.alice.Self().Fingerprint() {
				return false
			}
			if _, err := phone.TopicOf(id); err != nil {
				return false
			}
		}
		return true
	})
	review, err := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Peer: phone.Address, IDs: []string{source}, Topic: destination})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.ApplyTopicOrganization(tctx(t), review); err != nil {
		t.Fatal(err)
	}
	eventually(t, "organization reaches opposite native endpoint", func() bool {
		topic, err := phone.TopicOf(source)
		return err == nil && topic.ID == destination && topic.Count == 2
	})
	review, err = phone.PreviewTopicOrganization(TopicOrganizationRequest{Peer: w.alice.Address, IDs: []string{source}, Topic: third})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = phone.ApplyTopicOrganization(tctx(t), review); err != nil {
		t.Fatal(err)
	}
	eventually(t, "opposite endpoint choice supersedes original view", func() bool {
		topic, err := w.alice.TopicOf(source)
		return err == nil && topic.ID == third && topic.Count == 2
	})
	if n := count(t, w.alice, "direct_topic_placements"); n != 1 {
		t.Fatalf("one original gained per-view conflicting registers: %d", n)
	}
}

func TestDirectTopicNewTopicRestartLinkedAndUndo(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	file, _ := writeFile(t, t.TempDir(), "original-decision.bin", 397)
	sequence := int64(1200)
	send := func(body, reply string, files ...string) string {
		t.Helper()
		m, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: body, ReplyTo: reply, Files: files})
		if err != nil {
			t.Fatal(err)
		}
		sequence++
		if _, err = a.store.db.Exec(`UPDATE outbox SET created_at=?,created_ms=? WHERE id=?`, sequence, sequence*1000, m.ID); err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	first := send("original build decision", "", file)
	child := send("original follow-up", first)
	second := send("related independent exchange", "")
	original, err := a.Conversation(first, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := a.PreviewTopicOrganization(TopicOrganizationRequest{Peer: w.bob.Address, IDs: []string{first}, New: true, Title: "Selected decision"})
	if err != nil {
		t.Fatal(err)
	}
	if partial.Topic != partial.Operation {
		t.Fatal("new topic allocated a second identity")
	}
	if _, err = a.ApplyTopicOrganization(tctx(t), partial); err != nil {
		t.Fatal(err)
	}
	if topic := topicOf(t, a, child); topic.ID != child || topic.Count != 1 {
		t.Fatalf("unselected remainder followed moved root: %+v", topic)
	}
	if v, err := a.DisplayConversation(partial.Topic, 0, 0); err != nil || len(v.Messages) != 1 || v.Messages[0].ID != first {
		t.Fatalf("synthetic navigation %+v %v", v, err)
	}
	if _, err = a.UndoTopicOrganization(tctx(t), TopicOrganizationUndo{Peer: w.bob.Address, LID: partial.Operation, ID: protocol.NewID()}); err != nil {
		t.Fatal(err)
	}
	review, err := a.PreviewTopicOrganization(TopicOrganizationRequest{Peer: w.bob.Address, IDs: []string{first, child, second}, New: true, Title: "  Build   decisions  "})
	if err != nil {
		t.Fatal(err)
	}
	if review.Title != "Build decisions" || review.Topic != review.Operation {
		t.Fatalf("new topic identity/title %+v", review)
	}
	if _, err = a.ApplyTopicOrganization(tctx(t), review); err != nil {
		t.Fatal(err)
	}
	if page, err := a.Topics(TopicQuery{Peer: w.bob.Address}); err != nil || page.Matched != 1 || page.Topics[0].ID != review.Topic || page.Topics[0].Title != "Build decisions" || page.Topics[0].Count != 3 {
		t.Fatalf("consolidation %+v %v", page, err)
	}
	after, err := a.Conversation(first, 0, 0)
	if err != nil || !reflect.DeepEqual(original, after) {
		t.Fatalf("original conversation changed: %v", err)
	}
	if scope, err := a.attentionScope(review.Topic); err != nil || len(scope) != 3 {
		t.Fatalf("synthetic read scope %+v %v", scope, err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if sent, err := a.ApplyTopicOrganization(tctx(t), review); err != nil || sent.ID != review.Operation {
		t.Fatalf("restart exact retry %+v %v", sent, err)
	}
	if _, err = a.syncDirectTopics(); err != nil {
		t.Fatal(err)
	}
	deliverDeviceHistory(t, a, phone, envelope.SubDirectTopicSync)
	if _, err = a.syncTopicTitles(); err != nil {
		t.Fatal(err)
	}
	deliverDeviceHistory(t, a, phone, envelope.SubTopicSync)
	for range 4 {
		if _, err = a.deviceHistoryPage(phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	deliverDeviceHistory(t, a, phone, envelope.SubDeviceHistory)
	if topic := topicOf(t, phone, first); topic.ID != review.Topic || topic.Count != 3 || topic.Title != review.Title {
		t.Fatalf("linked new topic %+v", topic)
	}
	if display, err := phone.DisplayConversation(review.Topic, 0, 0); err != nil || len(display.Messages) != 3 || len(display.Messages[0].Attachments) != 1 {
		t.Fatalf("linked synthetic navigation/files %+v %v", display, err)
	}
	// Undo an operation whose source is itself synthetic, even after it empties.
	next, err := a.PreviewTopicOrganization(TopicOrganizationRequest{Peer: w.bob.Address, IDs: []string{first, child, second}, New: true, Title: "Another name"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ApplyTopicOrganization(tctx(t), next); err != nil {
		t.Fatal(err)
	}
	if _, err = a.UndoTopicOrganization(tctx(t), TopicOrganizationUndo{Peer: w.bob.Address, LID: next.Operation, ID: protocol.NewID()}); err != nil {
		t.Fatal(err)
	}
	if topic := topicOf(t, a, first); topic.ID != review.Topic || topic.Count != 3 {
		t.Fatalf("synthetic source not restored %+v", topic)
	}
	// The later organizational choice correctly prevents overwriting it.
	if _, err = a.UndoTopicOrganization(tctx(t), TopicOrganizationUndo{Peer: w.bob.Address, LID: review.Operation, ID: protocol.NewID()}); !errors.Is(err, ErrTopicOrganizationChanged) {
		t.Fatalf("old undo overwrote later placement: %v", err)
	}
}

func TestDirectTopicNamedAgentBoundary(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	runAgent(t, w.bob)
	waitNamedAgentCaps(t, w.bob)
	one, two := protocol.NewID(), protocol.NewID()
	send := func(agent string) string {
		t.Helper()
		v, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "exact original agent", Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: agent}})
		if err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	first, other, destination := send(one), send(two), send(one)
	for _, request := range []TopicOrganizationRequest{{Peer: w.bob.Address, IDs: []string{first}, Topic: other}, {Peer: w.bob.Address, IDs: []string{first, other}, New: true, Title: "Mixed agents"}} {
		if _, err := w.alice.PreviewTopicOrganization(request); !errors.Is(err, ErrTopicOrganizationChanged) {
			t.Fatalf("mixed-agent review accepted: %v", err)
		}
	}
	review, err := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Peer: w.bob.Address, IDs: []string{first, destination}, New: true, Title: "Same exact agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.ApplyTopicOrganization(tctx(t), review); err != nil {
		t.Fatal(err)
	}
	// Even an authenticated own placement cannot mix immutable destinations.
	owner, _ := w.alice.ownTopicWriter(w.alice.store.db)
	tx, err := w.alice.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = saveDirectTopicPlacement(tx, owner, protocol.DirectTopicPlacement{Peer: w.bob.Address, ID: other, Author: w.alice.Self().Fingerprint(), Topic: review.Topic, Revision: 100, Writer: w.alice.Self().Fingerprint(), Synthetic: true, Destination: *review.Destination}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if topic := topicOf(t, w.alice, other); topic.ID != other || topic.AgentID != two {
		t.Fatalf("foreign agent placement changed destination %+v", topic)
	}
	if topic := topicOf(t, w.alice, first); topic.ID != review.Topic || topic.Count != 2 || topic.AgentID != one {
		t.Fatalf("new-topic destination flipped %+v", topic)
	}
}

func TestDirectTopicConcurrentNewChoicesConverge(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	message, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "one original exchange"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err = a.deviceHistoryPage(phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	deliverDeviceHistory(t, a, phone, envelope.SubDeviceHistory)
	left, err := a.PreviewTopicOrganization(TopicOrganizationRequest{Peer: w.bob.Address, IDs: []string{message.ID}, New: true, Title: "Laptop choice"})
	if err != nil {
		t.Fatal(err)
	}
	right, err := phone.PreviewTopicOrganization(TopicOrganizationRequest{Peer: w.bob.Address, IDs: []string{message.ID}, New: true, Title: "Phone choice"})
	if err != nil {
		t.Fatal(err)
	}
	// Both offline choices commit before either device observes the other.
	if _, err = a.ApplyTopicOrganization(tctx(t), left); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.ApplyTopicOrganization(tctx(t), right); err != nil {
		t.Fatal(err)
	}
	if _, err = a.syncDirectTopics(); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.syncDirectTopics(); err != nil {
		t.Fatal(err)
	}
	deliverDeviceHistory(t, phone, a, envelope.SubDirectTopicSync)
	deliverDeviceHistory(t, a, phone, envelope.SubDirectTopicSync)
	// Duplicate/reordered delivery cannot resurrect the losing new topic.
	deliverDeviceHistory(t, phone, a, envelope.SubDirectTopicSync)
	deliverDeviceHistory(t, a, phone, envelope.SubDirectTopicSync)
	winner := left.Topic
	if phone.Self().Fingerprint() > a.Self().Fingerprint() {
		winner = right.Topic
	}
	for _, device := range []*Agent{a, phone} {
		if topic := topicOf(t, device, message.ID); topic.ID != winner || topic.Count != 1 {
			t.Fatalf("concurrent choice did not converge: %+v want=%s", topic, winner)
		}
		if display, err := device.DisplayConversation(winner, 0, 0); err != nil || len(display.Messages) != 1 || display.Messages[0].ID != message.ID {
			t.Fatalf("winning synthetic navigation %+v %v", display, err)
		}
		if page, err := device.Topics(TopicQuery{Peer: w.bob.Address}); err != nil || page.Matched != 1 {
			t.Fatalf("losing synthetic topic remained visible %+v %v", page, err)
		}
	}
}
