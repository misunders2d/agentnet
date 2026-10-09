package client

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTopicOrganizationMovesOnlyReviewedMessages(t *testing.T) {
	id := func(c string) string { return strings.Repeat(c, 32) }
	key := "11111111-22222222-33333333-44444444"
	other := "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"
	from, to := id("a"), id("b")
	var event envelope.TopicEvent
	if err := json.Unmarshal([]byte(`{"action":"move","moves":[{"lid":"`+id("1")+`","author":"`+key+`","hash":"`+strings.Repeat("c", 64)+`","topic":"`+from+`"},{"lid":"`+id("4")+`","author":"`+other+`","hash":"`+strings.Repeat("d", 64)+`"}]}`), &event); err != nil {
		t.Fatal(err)
	}
	msgs := []ConvMessage{
		{ID: id("1"), LID: id("1"), Key: key, Topic: from, Kind: envelope.KindTask, Sent: 1},
		{ID: id("2"), LID: id("2"), Key: other, ReplyTo: id("1"), Sent: 2},
		{ID: id("3"), LID: id("3"), Key: key, Topic: from, Sent: 3},
		{ID: id("4"), LID: id("4"), Key: other, Sent: 4},
		{ID: id("5"), LID: id("5"), Key: key, Topic: to, TopicEvent: &event, Sent: 5},
		// An answer produced later retains the request's signed topic/scope.
		{ID: id("6"), LID: id("6"), Key: other, Topic: from, ReplyTo: id("1"), Kind: envelope.KindResult, Sent: 6},
	}
	before, _ := json.Marshal(msgs)
	want := map[string]string{id("1"): to, id("2"): from, id("3"): from, id("4"): to, id("5"): to, id("6"): from}
	if got := ChatTopicAssignments(msgs); !reflect.DeepEqual(got, want) {
		t.Fatalf("selected-only assignments = %v, want %v", got, want)
	}
	after, _ := json.Marshal(msgs)
	if string(before) != string(after) {
		t.Fatal("organization rewrote original message, reply or execution metadata")
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	if got := ChatTopicAssignments(msgs); !reflect.DeepEqual(got, want) {
		t.Fatal("assignment depends on arrival order")
	}
}

func TestTopicOrganizationCiphertextOnlyHistoryCopy(t *testing.T) {
	organization, err := topicOrganizationCopy(nil, "", envelope.SubHistory, "")
	if err != nil || organization {
		t.Fatalf("generic ciphertext-only history copy blocked: %v", err)
	}
}

func TestTopicOrganizationReviewedDMJourney(t *testing.T) {
	w, conv, _ := dmFiles(t)
	ctx := tctx(t)
	send := func(body, topic, reply string) ConvSent {
		sent, err := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: body, Topic: topic, ReplyTo: reply})
		if err != nil {
			t.Fatal(err)
		}
		return sent
	}
	first := send("Selected original", "new", "")
	from := convMsgByID(t, w.alice, conv, first.ID).Topic
	child := send("Unselected reply", "", first.LID)
	destination := send("Destination", "new", "")
	to := convMsgByID(t, w.alice, conv, destination.ID).Topic
	main := send("Selected main-flow message", "", "")
	review, err := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{first.ID, main.ID}, Topic: to})
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Moves) != 2 || !protocol.ValidHash(review.Token) {
		t.Fatal("review lost exact selection")
	}
	moved, err := w.alice.ApplyTopicOrganization(ctx, review)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "reviewed move reaches other full member", func() bool {
		msgs, e := w.bob.ConversationMessages(conv)
		if e != nil {
			return false
		}
		assigned := ChatTopicAssignments(msgs)
		return assigned[first.LID] == to && assigned[main.LID] == to && assigned[child.LID] == from
	})
	original := convMsgByID(t, w.alice, conv, first.ID)
	if original.Topic != from || original.Body != "Selected original" {
		t.Fatal("display move rewrote the original")
	}
	if again, e := w.alice.ApplyTopicOrganization(ctx, review); e != nil || again.LID != moved.LID || len(again.Copies) != len(moved.Copies) {
		t.Fatalf("exact retry changed operation: %v", e)
	}
	changed := review
	changed.Topic = from
	if _, e := w.alice.ApplyTopicOrganization(ctx, changed); !errors.Is(e, ErrTopicOrganizationChanged) {
		t.Fatalf("changed retry: %v", e)
	}
	stale, err := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{child.ID}, Topic: to})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.ChangeChatTopic(ctx, conv, to, "done", "", 3); err != nil {
		t.Fatal(err)
	}
	if _, e := w.alice.ApplyTopicOrganization(ctx, stale); e == nil {
		t.Fatal("closed destination accepted stale preview")
	}
	// Workers derive their output topic from the exact stored request, rather
	// than the human composer's current display selection.
	originalTopic, err := topicReference(w.alice.store.db, conv, first.LID, w.alice.Self().Fingerprint())
	if err != nil || originalTopic != from {
		t.Fatalf("original request scope changed: %s %v", originalTopic, err)
	}
	if err = checkParticipationTopic(w.alice.store.db, &from, envelope.Inner{Conv: conv, Topic: to, ReplyTo: first.LID}); err == nil {
		t.Fatal("display move widened original topic authority")
	}
	late := send("Future reply keeps original scope", "", first.LID)
	msgs, err := w.alice.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	if ChatTopicAssignments(msgs)[late.LID] != from {
		t.Fatal("future reply inherited a display move")
	}
	phone := linked(t, w.alice)
	eventually(t, "organization on a newly linked device", func() bool {
		m, e := phone.ConversationMessages(conv)
		return e == nil && ChatTopicAssignments(m)[first.LID] == to && ChatTopicAssignments(m)[late.LID] == from
	})
}

func TestTopicOrganizationStaleVersionsAndMergeCutoff(t *testing.T) {
	w, conv, _ := dmFiles(t)
	ctx := tctx(t)
	send := func(body, topic string) ConvSent {
		s, e := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: body, Topic: topic})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	first := send("Source name", "new")
	from := convMsgByID(t, w.alice, conv, first.ID).Topic
	second := send("Second selected message", from)
	target := send("Destination name", "new")
	to := convMsgByID(t, w.alice, conv, target.ID).Topic
	request := TopicOrganizationRequest{Conv: conv, IDs: []string{first.ID}, Topic: to}
	stale, e := w.alice.PreviewTopicOrganization(request)
	if e != nil {
		t.Fatal(e)
	}
	ref := ControlRef{Conv: conv, ID: first.LID, Fingerprint: w.alice.Self().Fingerprint()}
	if _, e = w.alice.Revise(ctx, ref, "Edited source name"); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicOrganization(ctx, stale); e == nil {
		t.Fatal("stale edited version moved")
	}
	fresh, e := w.alice.PreviewTopicOrganization(request)
	if e != nil || fresh.Moves[0].Hash == stale.Moves[0].Hash {
		t.Fatalf("edited review did not change version: %v", e)
	}
	if _, e = w.alice.ApplyTopicOrganization(ctx, fresh); e != nil {
		t.Fatal(e)
	}
	stale, e = w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{second.ID}, Topic: to})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.Retract(ctx, ControlRef{Conv: conv, ID: second.LID, Fingerprint: ref.Fingerprint}, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicOrganization(ctx, stale); e == nil {
		t.Fatal("deleted selected message moved")
	}
	remaining := send("Reviewed merge cutoff", from)
	merge, e := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{remaining.ID}, Topic: to, Merge: from})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicOrganization(ctx, merge); e != nil {
		t.Fatal(e)
	}
	topics, e := w.alice.ChatTopics(conv)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, topic := range topics {
		if topic.ID == from {
			found = topic.Redirect == to && topic.Title != ""
		}
	}
	if !found {
		m, _ := w.alice.ConversationMessages(conv)
		t.Fatalf("merge lost source name or explicit destination link: topics=%+v redirects=%v messages=%+v", topics, ChatTopicRedirects(m), m)
	}
	if _, e = w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{first.ID}, Topic: from, Merge: to}); e == nil {
		t.Fatal("cyclic merge preview accepted")
	}
	late := send("Posted after reviewed cutoff", from)
	msgs, e := w.alice.ConversationMessages(conv)
	if e != nil || ChatTopicAssignments(msgs)[late.LID] != from {
		t.Fatalf("future source post moved: %v", e)
	}
	newTopic := protocol.NewID()
	named, e := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{late.ID}, Topic: newTopic, New: true, Title: "Named destination"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicOrganization(ctx, named); e != nil {
		t.Fatal(e)
	}
	topics, e = w.alice.ChatTopics(conv)
	if e != nil {
		t.Fatal(e)
	}
	found = false
	for _, topic := range topics {
		if topic.ID == newTopic {
			found = topic.Title == "Named destination" && topic.Count == 1
		}
	}
	if !found {
		t.Fatal("named destination did not retain reviewed title and exact message")
	}
	renamedReview, e := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{first.ID}, Topic: newTopic})
	if e != nil {
		t.Fatalf("named destination could not be reviewed again: %v", e)
	}
	if _, e = w.alice.ChangeChatTopic(ctx, conv, newTopic, "rename", "Changed destination", 0); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicOrganization(ctx, renamedReview); e == nil {
		t.Fatal("destination renamed after review accepted")
	}
}

func TestTopicOrganizationRejectsAgentHost(t *testing.T) {
	w, conv, _ := dmFiles(t)
	if _, e := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Existing original conversation"}); e != nil {
		t.Fatal(e)
	}
	host := linkedVia(t, w.alice, "worker", w.alice.ApproveAgentLink)
	eventually(t, "agent host receives inert original conversation", func() bool {
		_, e := membersIn(host.store.db, conv)
		return e == nil
	})
	if e := topicOrganizationAuthor(host.store.db, conv, host.Address, host.Self().Fingerprint()); e == nil {
		t.Fatal("own agent host obtained human organization authority")
	}
	if e := topicOrganizationAuthor(w.alice.store.db, conv, host.Address, host.Self().Fingerprint()); e == nil {
		t.Fatal("received agent-host key obtained human organization authority")
	}
	if e := topicOrganizationAuthor(w.alice.store.db, conv, w.alice.Address, w.alice.Self().Fingerprint()); e != nil {
		t.Fatalf("current original human lost organization authority: %v", e)
	}
}

func TestTopicOrganizationReviewedGroup(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	ctx, conv := tctx(t), packet.State.Conv
	first, e := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Group source", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	firstMsg, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == first.LID })
	from := firstMsg.Topic
	child, e := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Unselected group reply", ReplyTo: first.LID})
	if e != nil {
		t.Fatal(e)
	}
	destination, e := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Group destination", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	destinationMsg, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == destination.LID })
	to := destinationMsg.Topic
	review, e := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{first.LID}, Topic: to})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicOrganization(ctx, review); e != nil {
		t.Fatal(e)
	}
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, "same exact group organization", func() bool {
			m, e := a.ConversationMessages(conv)
			return e == nil && ChatTopicAssignments(m)[first.LID] == to && ChatTopicAssignments(m)[child.LID] == from
		})
	}
}
