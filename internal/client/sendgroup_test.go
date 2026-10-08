package client

import (
	"context"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func groupRequestFixture() envelope.Inner {
	return envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), Conv: protocol.NewID(), From: "alice/laptop", To: "bob/laptop", TS: 1, Kind: envelope.KindQuestion, Body: "same human body", Origin: envelope.OriginUI, PID: protocol.NewID(), Target: &envelope.Target{Address: "bob/laptop", Fingerprint: strings.Repeat("a", 64)}, SendGroup: protocol.NewID()}
}
func TestSendGroupMetadataConflictDoesNotChangePayloadAuthority(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	stops[w.alice]()
	_, path := paths(w.alice.home)
	s := w.alice.store
	var err error
	in := groupRequestFixture()
	in.Conv = packet.State.Conv
	key := strings.Repeat("b", 64)
	if _, err = s.addConvInbox(in, key, "", false, nil); err != nil {
		t.Fatal(err)
	}
	hash := contentHash(in)
	without := in
	without.SendGroup = ""
	if hash != contentHash(without) {
		t.Fatal("presentation changed logical payload")
	}
	without.ID = protocol.NewID()
	if got, err := s.addConvInbox(without, key, "", false, nil); err != nil || got != admittedAgain {
		t.Fatalf("stripped copy conflicted: %s %v", got, err)
	}
	if group, err := storedSendGroup(s.db, "in", in.ID); err != nil || group != in.SendGroup {
		t.Fatalf("absence erased known group: %s %v", group, err)
	}
	conflict := in
	conflict.ID = protocol.NewID()
	conflict.SendGroup = protocol.NewID()
	if got, err := s.addConvInbox(conflict, key, "", false, nil); err != nil || got != admittedAgain {
		t.Fatalf("presentation conflict rejected payload: %s %v", got, err)
	}
	s.db.Close()
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if got, err := s.addConvInbox(in, key, "", false, nil); err != nil || got != admittedAgain {
		t.Fatalf("repeat: %s %v", got, err)
	}
	var group string
	var disabled bool
	if err = s.db.QueryRow(`SELECT send_group,send_group_conflict FROM inbox WHERE id=?`, in.ID).Scan(&group, &disabled); err != nil || group != "" || !disabled {
		t.Fatalf("conflict not sticky after restart: %s %v %v", group, disabled, err)
	}
	bad := in
	bad.ID = protocol.NewID()
	bad.Target = &envelope.Target{Address: "mallory/laptop", Fingerprint: strings.Repeat("c", 64)}
	if got, err := s.addConvInbox(bad, key, "", false, nil); err != nil || got != admitConflict {
		t.Fatalf("group bypassed changed target: %s %v", got, err)
	}
	bad = in
	bad.ID = protocol.NewID()
	bad.Body = "changed body"
	if got, err := s.addConvInbox(bad, key, "", false, nil); err != nil || got != admitConflict {
		t.Fatalf("group bypassed changed body: %s %v", got, err)
	}
}

func TestSendGroupHistoryAndDirectOrderPreservesConflict(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	stops[w.alice]()
	s := w.alice.store
	in := groupRequestFixture()
	in.Conv = packet.State.Conv
	key := strings.Repeat("b", 64)
	if _, err := s.addHistoryInbox(in, 1, key, w.bob.Address, protocol.NewID(), false, nil); err != nil {
		t.Fatal(err)
	}
	direct := in
	direct.ID = protocol.NewID()
	direct.SendGroup = ""
	if _, err := s.addConvInbox(direct, key, "", false, nil); err != nil {
		t.Fatal(err)
	}
	if group, err := storedSendGroup(s.db, "in", direct.ID); err != nil || group != in.SendGroup {
		t.Fatalf("direct stripped copy erased historical group: %q %v", group, err)
	}
	other := in
	other.ID = protocol.NewID()
	other.SendGroup = protocol.NewID()
	if _, err := s.addHistoryInbox(other, 1, key, w.bob.Address, protocol.NewID(), false, nil); err != nil {
		t.Fatal(err)
	}
	if group, err := storedSendGroup(s.db, "in", direct.ID); err != nil || group != "" {
		t.Fatalf("history conflict did not disable: %q %v", group, err)
	}
	if _, err := s.addConvInbox(in, key, "", false, nil); err != nil {
		t.Fatal(err)
	}
	if group, err := storedSendGroup(s.db, "in", direct.ID); err != nil || group != "" {
		t.Fatalf("later direct restored conflict: %q %v", group, err)
	}
}

func TestSendGroupOldPeerAndLinkedHistory(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	a := w.alice
	p := p6Member(t, a, w.bob, packet.State.Conv)
	// Remove only optional presentation support: all execution/room caps stay.
	signCapsAfter(t, w.bob, without(ownCaps, protocol.CapSendGroup))
	group := protocol.NewID()
	child := protocol.NewID()
	sent, err := a.AskAgent(WithHumanSendGroup(WithQueuedSend(tctx(t), child), group), p.PID, envelope.KindQuestion, "one human send")
	if err != nil {
		t.Fatal(err)
	}
	if sent.LID != child {
		t.Fatal("group replaced child logical ID")
	}
	var raw, canonical string
	var wire bool
	if err = a.store.db.QueryRow(`SELECT envelope,send_group,wire_send_group FROM outbox WHERE id=?`, sent.ID).Scan(&raw, &canonical, &wire); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	inner, err := envelope.Open(env, w.bob.id, w.bob.Address, a.Self())
	if err != nil {
		t.Fatal(err)
	}
	if inner.SendGroup != "" || canonical != group || wire {
		t.Fatalf("old peer not negotiated: inner=%q local=%q wire=%v", inner.SendGroup, canonical, wire)
	}
	eventually(t, "old peer receives independently authorized request", func() bool { return p6HasLID(t, w.bob, child) })
	child2 := protocol.NewID()
	sent2, err := a.AskAgent(WithHumanSendGroup(WithQueuedSend(tctx(t), child2), group), p.PID, envelope.KindQuestion, "one human send")
	if err != nil || sent2.LID != child2 || sent2.LID == sent.LID {
		t.Fatalf("requests merged: %+v %v", sent2, err)
	}
	signCapsAfter(t, w.bob, ownCaps)
	newSent, e := a.AskAgent(WithHumanSendGroup(WithQueuedSend(tctx(t), protocol.NewID()), group), p.PID, envelope.KindQuestion, "new reader request")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.store.db.QueryRow(`SELECT envelope,wire_send_group FROM outbox WHERE id=?`, newSent.ID).Scan(&raw, &wire); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal([]byte(raw), &env); e != nil {
		t.Fatal(e)
	}
	groupedInner, e := envelope.Open(env, w.bob.id, w.bob.Address, a.Self())
	if e != nil || groupedInner.SendGroup != group || !wire {
		t.Fatalf("new reader signed metadata lost: %q %v %v", groupedInner.SendGroup, wire, e)
	}
	phone, await, _ := linkPhone(t, a, "group-history")
	req := pendingLink(t, a)
	stops[a]()
	if err = a.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	// The phone explicitly advertises sg1 before history construction.
	if !a.sendGroupSupported(context.Background(), phone.Self()) {
		t.Fatal("phone fixture did not advertise sg1")
	}
	if _, err = a.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.store.db.Query(`SELECT body,envelope FROM outbox WHERE recipient=? AND sub='history'`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var body string
		var h HistoryItem
		if err = rows.Scan(&body, &raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(body), &h); err != nil {
			t.Fatal(err)
		}
		if e := json.Unmarshal([]byte(raw), &env); e != nil {
			t.Fatal(e)
		}
		sealedHistory, e := envelope.Open(env, phone.id, phone.Address, a.Self())
		if e != nil {
			t.Fatal(e)
		}
		var signedHistory HistoryItem
		if e = json.Unmarshal([]byte(sealedHistory.Body), &signedHistory); e != nil {
			t.Fatal(e)
		}
		if signedHistory.SendGroup != h.SendGroup {
			t.Fatal("stored metadata differs from signed history")
		}
		if h.LID == child || h.LID == child2 {
			if h.SendGroup != group {
				t.Fatalf("history lost local group: %+v", h)
			}
			found[h.LID] = true
		}
	}
	rows.Close()
	if len(found) != 2 {
		t.Fatalf("history collapsed independent children: %+v", found)
	}
}

func TestSendGroupMigrationLegacyRemainsUngrouped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.db")
	step := -1
	for i, sql := range schema {
		if sql == sendGroupSchema {
			step = i
			break
		}
	}
	if step < 0 {
		t.Fatal("send group schema missing")
	}
	db, err := sqlitedb.Open(path, schema[:step])
	if err != nil {
		t.Fatal(err)
	}
	id := protocol.NewID()
	if _, err = db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at) VALUES(?,'bob/laptop',1,'message','legacy body',1)`, id); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	var group string
	var conflict bool
	if err = s.db.QueryRow(`SELECT send_group,send_group_conflict FROM inbox WHERE id=?`, id).Scan(&group, &conflict); err != nil || group != "" || conflict {
		t.Fatalf("legacy grouping invented: %q %v %v", group, conflict, err)
	}
	if (protocol.CapsRecord{Caps: []string{protocol.CapRoom}}).Reads(protocol.CapSendGroup) {
		t.Fatal("legacy room implies optional grouping")
	}
}
