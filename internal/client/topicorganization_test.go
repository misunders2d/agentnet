package client

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
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

func TestTopicOrganizationDestinationTitle(t *testing.T) {
	id := func(c string) string { return strings.Repeat(c, 32) }
	key := "11111111-22222222-33333333-44444444"
	conv, from, to := strings.Repeat("c", 64), id("a"), id("b")
	source := ConvMessage{ID: id("1"), LID: id("1"), Key: key, Topic: from, Body: "Group note to edit", Sent: 1}
	destination := ConvMessage{ID: id("2"), LID: id("2"), Key: key, Topic: to, Body: "Stock count topic start", Sent: 2}
	move := ConvMessage{ID: id("3"), LID: id("3"), Key: key, Topic: to, Sent: 3, TopicEvent: &envelope.TopicEvent{Action: "move", Moves: []envelope.TopicMove{{LID: source.LID, Author: key, Topic: from}}}}
	for _, merge := range []bool{false, true} {
		event := move
		if merge {
			e := *move.TopicEvent
			e.Action, e.Merge = "merge", from
			event.TopicEvent = &e
		}
		for _, promoted := range []bool{false, true} {
			msgs := []ConvMessage{source, destination, event}
			if promoted {
				msgs[1].Topic = ""
				msgs[1].LID, msgs[1].ID = to, to
				msgs = append(msgs, ConvMessage{ID: id("4"), LID: id("4"), Topic: to, Sent: 2, TopicEvent: &envelope.TopicEvent{Action: "create"}})
			}
			original, _ := json.Marshal(msgs)
			for _, private := range []string{"", "Private destination name"} {
				locals := map[string]topicLocal{to: {Title: private}}
				for replay := 0; replay < 2; replay++ {
					var retained []ConvMessage
					if err := json.Unmarshal(original, &retained); err != nil {
						t.Fatal(err)
					}
					if replay == 1 {
						for i, j := 0, len(retained)-1; i < j; i, j = i+1, j-1 {
							retained[i], retained[j] = retained[j], retained[i]
						}
					}
					found := false
					for _, topic := range summarizeChatTopics(conv, retained, locals, 10) {
						if topic.ID != to {
							continue
						}
						found = true
						want := destination.Body
						if private != "" {
							want = private
						}
						if topic.Title != want || private != "" && topic.AutoTitle != destination.Body {
							t.Errorf("merge=%v promoted=%v replay=%d private=%q: destination title=%q auto=%q, want %q", merge, promoted, replay, private, topic.Title, topic.AutoTitle, want)
						}
					}
					if !found {
						t.Fatal("destination disappeared")
					}
					if ChatTopicAssignments(retained)[source.LID] != to {
						t.Fatal("title preservation changed selected placement")
					}
				}
			}
			after, _ := json.Marshal(msgs)
			if string(after) != string(original) {
				t.Fatal("projection rewrote original bytes or audience")
			}
		}
	}
	// An empty/new destination has no original anchor; its selected first
	// message still supplies the automatic title. Main retains its own rules.
	for _, topic := range []string{to, ""} {
		empty := move
		empty.Topic = topic
		msgs := []ConvMessage{source, empty}
		ts := summarizeChatTopicView(conv, msgs, nil, 10, topic == "")
		if len(ts) != 1 || ts[0].Title != source.Body || ts[0].ID != topic {
			t.Fatalf("empty/Main destination fallback changed: %+v", ts)
		}
	}
	back := ConvMessage{ID: id("5"), LID: id("5"), Key: key, Topic: from, Sent: 4, ReplyTo: move.LID, TopicEvent: &envelope.TopicEvent{Action: "move", Moves: []envelope.TopicMove{{LID: source.LID, Author: key, Topic: to}}}}
	for _, topic := range summarizeChatTopics(conv, []ConvMessage{back, move, destination, source}, nil, 10) {
		if topic.ID == to && (topic.Title != destination.Body || topic.Count != 1) {
			t.Fatalf("inverse move changed destination title: %+v", topic)
		}
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
	namedSent, e := w.alice.ApplyTopicOrganization(ctx, named)
	if e != nil {
		t.Fatal(e)
	}
	if raw := convMsgByID(t, w.alice, conv, namedSent.ID); raw.Body != TopicOrganizationNotice(newTopic, "Named destination") {
		t.Fatalf("explicit human title changed in outgoing notice: %q", raw.Body)
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
	me, own, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil {
		t.Fatal("own roster prerequisite lookup", err)
	}
	checks := map[string]bool{
		"own-person":           own,
		"sender-current":       me.has(w.alice.Address, w.alice.Self().Fingerprint()),
		"reader-current":       me.has(host.Address, host.Self().Fingerprint()),
		"explicit-human-roles": len(me.roster.HumanKeys) > 0,
		"reader-agent":         !me.roster.Human(host.Self().Fingerprint()),
	}
	for name, device := range map[string]*Agent{"sender": w.alice, "reader": host} {
		pin, found, err := pinnedKey(w.alice.store.db, device.Address)
		if err != nil {
			t.Fatal(name+" pin prerequisite lookup", err)
		}
		checks[name+"-pin-absent-or-matching"] = !found || sameKeys(pin, device.Self())
		var pending int
		if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, device.Address).Scan(&pending); err != nil {
			t.Fatal(name+" pending prerequisite lookup", err)
		}
		checks[name+"-no-pending-key"] = pending == 0
		revoked, err := revokedDeviceKnown(w.alice.store.db, device.Address, device.Self().Fingerprint())
		if err != nil {
			t.Fatal(name+" revocation prerequisite lookup", err)
		}
		checks[name+"-not-revoked"] = !revoked
	}
	t.Logf("legacy history prerequisites: %v", checks)
	for name, pass := range checks {
		if !pass {
			t.Fatal("legacy history prerequisite failed: " + name)
		}
	}
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
	t.Run("history-role-boundaries", testTopicOrganizationHistoryRoles)
	t.Run("agent-producer-history", testTopicOrganizationAgentProducerHistory)
}

func testTopicOrganizationHistoryRoles(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	a := w.alice
	conv := newDM(t, a, w.bob)
	stop() // Source pages below run without a daemon changing their outbox.
	seed, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "ordinary inert history", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	topic := convMsgByID(t, a, conv, seed.ID).Topic
	old, err := a.RenameSharedTopic(tctx(t), conv, topic, "old")
	if err != nil {
		t.Fatal(err)
	}
	winner, err := a.RenameSharedTopic(tctx(t), conv, topic, "winner")
	if err != nil {
		t.Fatal(err)
	}
	for _, lid := range []string{old.LID, winner.LID} {
		if _, err := a.store.db.Exec(`INSERT INTO conv_erased(conv,lid,key,deletion) VALUES(?,?,?,?)`, conv, lid, a.Self().Fingerprint(), protocol.NewID()); err != nil {
			t.Fatal(err)
		}
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		t.Fatalf("source roster: %v %v", ok, err)
	}
	// These helpers create and verify signed roster steps, including each
	// reader's join signature. Neither reader runs a delivery daemon.
	roster, agentKey := p7Added(t, w, me.roster, "admin/history-agent", false)
	p7PinStep(t, a, roster)
	roster, humanKey := p7Added(t, w, roster, "admin/history-human", true)
	p7PinStep(t, a, roster)
	agent := &Agent{id: agentKey, Address: "admin/history-agent"}
	human := &Agent{id: humanKey, Address: "admin/history-human"}
	var agentItems map[protocol.GroupHistoryRef]bool
	for _, control := range []struct {
		name   string
		reader *Agent
		pin    bool
	}{{"agent-without-pin", agent, false}, {"agent-matching-pin", agent, true}, {"human-winner", human, true}} {
		t.Run(control.name, func(t *testing.T) {
			reader := control.reader
			if control.pin {
				if err := a.store.pin(reader.Self()); err != nil {
					t.Fatal(err)
				}
			}
			if pin, found, err := pinnedKey(a.store.db, reader.Address); err != nil || found != control.pin || found && !sameKeys(pin, reader.Self()) {
				t.Fatalf("reader pin control found=%v matching=%v want-present=%v err=%v", found, found && sameKeys(pin, reader.Self()), control.pin, err)
			}
			if _, err := a.historyPageFor(reader.Self(), historyPos{}); err != nil {
				t.Fatalf("legacy page for %s: %v", control.name, err)
			}
			items := historyCopiedItems(t, a, reader)
			ordinary, recovered := false, false
			for _, item := range items {
				if item.LID == seed.LID {
					if item.From != a.Address || item.FromKey != a.Self().Fingerprint() || item.Body != "ordinary inert history" {
						t.Fatal("ordinary history changed its original human author or body")
					}
					ordinary = true
				}
				if item.LID == old.LID {
					t.Fatal("obsolete erased title recovered")
				}
				if item.LID == winner.LID {
					if item.Body != "" {
						t.Fatal("metadata recovery restored a body")
					}
					recovered = true
				}
			}
			if !ordinary || recovered != (reader == human) {
				t.Fatalf("reader=%s ordinary=%v erased-winner=%v", control.name, ordinary, recovered)
			}
			if control.name == "agent-without-pin" {
				agentItems = make(map[protocol.GroupHistoryRef]bool)
				for _, item := range items {
					agentItems[historyRef(conv, item)] = true
				}
			} else if reader == agent {
				refs := make(map[protocol.GroupHistoryRef]bool)
				for _, item := range items {
					refs[historyRef(conv, item)] = true
				}
				if !reflect.DeepEqual(refs, agentItems) {
					t.Fatal("matching pin changed the exact selected source set")
				}
			}
		})
	}
	if err := topicOrganizationAuthor(a.store.db, conv, agent.Address, agent.Self().Fingerprint()); err == nil {
		t.Fatal("ordinary history granted human organization authority")
	}
	if _, err := a.ownHistorySourceRows(a.store.db, agent.Self(), "conv=?", "id", 10, conv); !errors.Is(err, errHistoryRecoveryAuthority) {
		t.Fatalf("agent entered human erased recovery: %v", err)
	}
	if rows, err := a.historySourceRows(a.store.db, "conv=? AND lid IN (?,?)", "id", 10, conv, old.LID, winner.LID); err != nil || len(rows) != 0 {
		t.Fatalf("generic selected/guest selector recovered erased rows: %d %v", len(rows), err)
	}
	if err := currentOwnLegacyHistory(a.store.db, a.Self(), human.Self()); err != nil {
		t.Fatalf("valid own human reader lost generic history authority: %v", err)
	}
	if rows, err := a.ownHistorySourceRows(a.store.db, human.Self(), "conv=? AND lid=?", "id", 1, conv, winner.LID); err != nil || len(rows) != 1 || rows[0].in.LID != winner.LID {
		t.Fatalf("valid human pair lost its separate erased-winner path: %d %v", len(rows), err)
	}
	replacement, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := json.Marshal(replacement.Public(agent.Address))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"changed-pin", "pending", "revoked", "frozen", "removed", "missing-role", "sender-pending", "db-error"} {
		t.Run(mode, func(t *testing.T) {
			tx, err := a.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			switch mode {
			case "changed-pin":
				_, err = tx.Exec(`UPDATE peers SET public=? WHERE address=?`, changed, agent.Address)
			case "pending":
				_, err = tx.Exec(`UPDATE peers SET pending=public WHERE address=?`, agent.Address)
			case "revoked":
				_, err = tx.Exec(`INSERT INTO config(k,v) VALUES(?,?)`, revokedDevicePrefix+agent.Address+"/"+agent.Self().Fingerprint(), agent.Address)
			case "frozen":
				_, err = tx.Exec(`UPDATE persons SET state='conflict' WHERE state='self'`)
			case "removed":
				_, err = tx.Exec(`UPDATE persons SET record=json_set(record,'$.devices',json('[]')) WHERE state='self'`)
			case "missing-role":
				_, err = tx.Exec(`UPDATE persons SET record=json_remove(record,'$.human_keys') WHERE state='self'`)
			case "sender-pending":
				if _, err = tx.Exec(`INSERT OR IGNORE INTO peers(address,public,pinned_at) VALUES(?,?,0)`, a.Address, mustJSON(a.Self())); err == nil {
					_, err = tx.Exec(`UPDATE peers SET pending=public WHERE address=?`, a.Address)
				}
			case "db-error":
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := currentOwnLegacyHistory(tx, a.Self(), agent.Self()); err == nil {
				t.Fatal("invalid agent history authority accepted")
			} else if mode == "db-error" && errors.Is(err, errHistoryRecoveryAuthority) {
				t.Fatal("database error was converted to a role fallback")
			}
		})
	}
}

func testTopicOrganizationAgentProducerHistory(t *testing.T) {
	w := newWorld(t, "")
	stopOwner := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	owner := w.alice
	conv := newDM(t, owner, w.bob)
	producer, awaited, _ := linkPhone(t, owner, "history-producer")
	if err := owner.ApproveAgentLink(tctx(t), pendingLink(t, owner).ID); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	stopProducer := runAgent(t, producer)
	seed, err := owner.SendConv(tctx(t), conv, ConvOutgoing{Body: "original human history for every role pair", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	topic := convMsgByID(t, owner, conv, seed.ID).Topic
	old, err := owner.RenameSharedTopic(tctx(t), conv, topic, "old")
	if err != nil {
		t.Fatal(err)
	}
	winner, err := owner.RenameSharedTopic(tctx(t), conv, topic, "winner")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "real linked agent retains the human originals before erasure", func() bool {
		rows, err := producer.historySourceRows(producer.store.db, "conv=? AND lid IN (?,?,?)", "id", 10, conv, seed.LID, old.LID, winner.LID)
		if err != nil || len(rows) != 3 {
			return false
		}
		title, present, err := producer.SharedTopicTitle(conv, topic)
		return err == nil && present && title.LID == winner.LID && title.Writer == owner.Self().Fingerprint()
	})
	stopProducer() // Keep this real agent's store and identity; stop its producer.
	for _, source := range []*Agent{owner, producer} {
		for _, lid := range []string{old.LID, winner.LID} {
			if _, err := source.store.db.Exec(`INSERT INTO conv_erased(conv,lid,key,deletion) VALUES(?,?,?,?)`, conv, lid, owner.Self().Fingerprint(), protocol.NewID()); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Actual joins give each reader a separate store/key and the owner's signed
	// role decision. The stopped producer verifies those same roster steps.
	human, awaited, _ := linkPhone(t, owner, "history-human-reader")
	if err := owner.DecideLink(tctx(t), pendingLink(t, owner).ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	me, ok, err := owner.store.selfPerson(owner.Address)
	if err != nil || !ok {
		t.Fatalf("human reader roster: %v %v", ok, err)
	}
	p7PinStep(t, producer, me.roster)
	agent, awaited, _ := linkPhone(t, owner, "history-agent-reader")
	if err := owner.ApproveAgentLink(tctx(t), pendingLink(t, owner).ID); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	me, ok, err = owner.store.selfPerson(owner.Address)
	if err != nil || !ok {
		t.Fatalf("agent reader roster: %v %v", ok, err)
	}
	p7PinStep(t, producer, me.roster)
	stopOwner()
	current, ok, err := producer.store.selfPerson(producer.Address)
	if err != nil || !ok || current.roster.Human(producer.Self().Fingerprint()) || !current.has(producer.Address, producer.Self().Fingerprint()) || !current.roster.Human(human.Self().Fingerprint()) || current.roster.Human(agent.Self().Fingerprint()) {
		t.Fatalf("actual agent producer/reader role prerequisites failed: own=%v err=%v", ok, err)
	}
	for _, source := range []struct {
		name string
		a    *Agent
	}{{"human", owner}, {"agent", producer}} {
		for _, reader := range []struct {
			name string
			a    *Agent
		}{{"human", human}, {"agent", agent}} {
			t.Run(source.name+"-to-"+reader.name, func(t *testing.T) {
				if _, err := source.a.historyPageFor(reader.a.Self(), historyPos{}); err != nil {
					t.Fatalf("actual legacy page: %v", err)
				}
				ordinary, recovered := false, false
				for _, item := range historyCopiedItems(t, source.a, reader.a) {
					if item.LID == seed.LID {
						if item.From != owner.Address || item.FromKey != owner.Self().Fingerprint() || item.Body != "original human history for every role pair" {
							t.Fatal("history reattributed or changed the human original")
						}
						ordinary = true
					}
					if item.LID == old.LID {
						t.Fatal("obsolete erased title recovered")
					}
					if item.LID == winner.LID {
						if item.FromKey != owner.Self().Fingerprint() || item.Body != "" {
							t.Fatal("winner changed its original human author or body")
						}
						recovered = true
					}
				}
				wantWinner := source.name == "human" && reader.name == "human"
				if !ordinary || recovered != wantWinner {
					t.Fatalf("ordinary=%v erased-winner=%v want-winner=%v", ordinary, recovered, wantWinner)
				}
			})
		}
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
