package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupHistoryWitnessRetiredOwnEndForwarder(t *testing.T) {
	w, a, old, packet := groupHistoryLinkedFixture(t)
	stopA := runAgent(t, a)
	stopOld := runAgent(t, old)
	publishGroupFixtureCaps(t, old, true)
	groupGovernanceAwait(t, packet, old)
	part := p6Member(t, w.bob, w.bob, packet.State.Conv)
	eventually(t, "old own phone sees accepted member", func() bool { return stateAt(t, old, part.PID).Claimable() })
	if _, err := w.bob.DismissParticipation(tctx(t), part.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "old own phone sees original signed end", func() bool { return stateAt(t, old, part.PID).State == PartDismissed })
	events, err := old.store.participationEvents(packet.State.Conv, part.PID)
	if err != nil {
		t.Fatal(err)
	}
	var end protocol.ParticipationEvent
	for _, ev := range events {
		if ev.Type == protocol.EventDismiss {
			end = ev
		}
	}
	if end.Author.Address != w.bob.Address {
		t.Fatal("fixture end lacks independent original author")
	}
	members, err := old.dmMembers(packet.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	features, err := old.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = old.shareRoomDismissal(tctx(t), members, end, a.Self(), features); err != nil {
		t.Fatal(err)
	}
	if err = old.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "original end forwarded by old own human", func() bool {
		return inboxCount(t, a, `sender=? AND pid=? AND sub='event' AND json_extract(body,'$.type')='dismiss'`, old.Address, part.PID) > 0
	})
	var id string
	if err = a.store.db.QueryRow(`SELECT id FROM inbox WHERE sender=? AND pid=? AND sub='event' AND json_extract(body,'$.type')='dismiss' LIMIT 1`, old.Address, part.PID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	stopOld()
	if err = a.RemoveDevice(tctx(t), old.Address); err != nil {
		t.Fatal(err)
	}
	phone, await, _ := linkPhone(t, a, "end-history-reader")
	link := pendingLink(t, a)
	stopA()
	if err = a.DecideLink(tctx(t), link.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	rows, err := a.historySourceRows(a.store.db, "dir='in' AND id=?", "conv,ms,id", 1, id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("source: %d %v", len(rows), err)
	}
	copy, err := a.prepareHistorySource(phone.Self(), rows[0])
	if err != nil || copy == nil {
		t.Fatalf("retired own end transport blocked inert history: %v", err)
	}
	var item HistoryItem
	if err = json.Unmarshal([]byte(copy.in.Body), &item); err != nil || item.GroupHistory == nil {
		t.Fatalf("missing exact historical witness: %v", err)
	}
	if _, err = a.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	groupHistoryDeliverCommittedFixture(t, a, phone, packet.State.Seq, false)
	var before int
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=? AND pid=?`, packet.State.Conv, part.PID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = phone.accept(tctx(t), copy.env); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, phone, `id=? AND replica=1 AND state='' AND group_history IS NOT NULL`, id) != 1 {
		t.Fatal("inert retired transport history missing")
	}
	var after int
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=? AND pid=?`, packet.State.Conv, part.PID).Scan(&after); err != nil || after != before {
		t.Fatalf("history changed live event ledger: %d/%d %v", before, after, err)
	}
	live, err := phone.dmMembers(packet.State.Conv)
	if err != nil || live.device(old.Address, old.Self().Fingerprint()) {
		t.Fatal("retired transport gained live membership")
	}
	for _, mode := range []string{"unknown-transport", "forged-end", "wrong-parent", "removed-outer-forwarder", "no-witness"} {
		t.Run(mode, func(t *testing.T) {
			var bad HistoryItem
			if err := json.Unmarshal([]byte(copy.in.Body), &bad); err != nil {
				t.Fatal(err)
			}
			forwarder := a.Self()
			switch mode {
			case "unknown-transport":
				bad.FromKey = w.bob.Self().Fingerprint()
			case "removed-outer-forwarder":
				forwarder = old.Self()
			case "no-witness":
				bad.GroupHistory = nil
			case "forged-end", "wrong-parent":
				ev := end
				if mode == "wrong-parent" {
					ev.Prev = strings.Repeat("f", 64)
					ev.Sign(w.bob.id.Sign)
				} else {
					ev.Sig = append([]byte(nil), ev.Sig...)
					ev.Sig[0] ^= 1
				}
				raw, _ := json.Marshal(ev)
				bad.Body = string(raw)
			}
			if _, err := phone.groupParticipationHistoryCheck(phone.store.db, packet.Root, forwarder, bad); err == nil {
				t.Fatal("invalid historical end accepted")
			}
		})
	}
	if err = phone.store.pin(old.Self()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"pending", "changed"} {
		t.Run(mode+"-original-transport-pin", func(t *testing.T) {
			tx, err := phone.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			query := `UPDATE peers SET pending=? WHERE address=?`
			if mode == "changed" {
				query = `UPDATE peers SET public=? WHERE address=?`
			}
			raw, err := json.Marshal(w.bob.id.Public(old.Address))
			if err != nil {
				t.Fatal(err)
			}
			res, err := tx.Exec(query, string(raw), old.Address)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("transport pin fixture: %d %v", n, err)
			}
			if _, err = phone.groupParticipationHistoryCheck(tx, packet.Root, a.Self(), item); err == nil {
				t.Fatal("changed original transport pin accepted")
			}
		})
	}
}

func TestGroupHistoryWitnessCapturedAudienceAfterHostReadmission(t *testing.T) {
	w, sibling, packet, stops := groupTurnsFixture(t)
	a, stopA := w.alice, stops[w.alice]
	primary := p6Member(t, w.bob, w.bob, packet.State.Conv)
	secondary := p6Member(t, sibling, sibling, packet.State.Conv)
	eventually(t, "both captured participants active", func() bool { return stateAt(t, a, primary.PID).Claimable() && stateAt(t, a, secondary.PID).Claimable() })
	ask, err := a.AskAgent(tctx(t), primary.PID, envelope.KindQuestion, "exact captured audience history")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "request stored by exact host", func() bool { return inboxCount(t, w.bob, `conv=? AND lid=?`, packet.State.Conv, ask.LID) > 0 })
	person, ok, err := sibling.store.selfPerson(sibling.Address)
	if err != nil || !ok {
		t.Fatalf("sibling person: %v", err)
	}
	removed, err := a.RemoveGroupMember(tctx(t), packet.State.Conv, person.info.Person)
	if err != nil {
		t.Fatal(err)
	}
	current := groupInteractionRejoin(t, a, sibling, removed)
	groupGovernanceAwait(t, current, a, w.bob, sibling)
	if !stateAt(t, a, primary.PID).Claimable() || stateAt(t, a, secondary.PID).Claimable() {
		t.Fatal("fixture must leave primary valid but captured sibling unavailable live")
	}
	phone, await, _ := linkPhone(t, a, "captured-history-reader")
	link := pendingLink(t, a)
	stopA()
	if err = a.DecideLink(tctx(t), link.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	rows, err := a.historySourceRows(a.store.db, "dir='out' AND conv=? AND lid=?", "conv,ms,id", 1, packet.State.Conv, ask.LID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("source: %d %v", len(rows), err)
	}
	original, err := a.historySourceItem(a.store.db, rows[0])
	if err != nil || original.Human == nil || len(original.Human.Audience) != 2 {
		t.Fatalf("captured audience absent: %v", err)
	}
	copy, err := a.prepareHistorySource(phone.Self(), rows[0])
	if err != nil || copy == nil {
		t.Fatalf("exact captured consent blocked historical witness fallback: %v", err)
	}
	var item HistoryItem
	if err = json.Unmarshal([]byte(copy.in.Body), &item); err != nil || item.GroupHistory == nil {
		t.Fatalf("missing exact witness: %v", err)
	}
	if _, err = a.groupParticipationHistoryCheck(a.store.db, packet.Root, a.Self(), item); err != nil {
		t.Fatal(err)
	}
	captured, _ := json.Marshal(original.Human)
	kept, _ := json.Marshal(item.Human)
	if string(captured) != string(kept) {
		t.Fatal("captured consent rewritten")
	}
	if err = humanTurnAuthorization(a.store.db, original.inner(packet.State.Conv), a.Address, a.Self().Fingerprint(), a.Address, a.Self().Fingerprint(), false); err == nil {
		t.Fatal("historical witness fallback authorized a live captured turn")
	}
	if _, err = a.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	groupHistoryDeliverCommittedFixture(t, a, phone, current.State.Seq, false)
	var before int
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=?`, packet.State.Conv).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = phone.accept(tctx(t), copy.env); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, phone, `id=? AND replica=1 AND state='' AND group_history IS NOT NULL`, item.ID) != 1 {
		t.Fatal("captured consent history missing at new own reader")
	}
	var after int
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=?`, packet.State.Conv).Scan(&after); err != nil || after != before {
		t.Fatalf("witnessed consent changed live events: %d/%d %v", before, after, err)
	}
	item.Human.Audience[0].Decision = strings.Repeat("f", 64)
	if _, err = a.groupParticipationHistoryCheck(a.store.db, packet.Root, a.Self(), item); err == nil {
		t.Fatal("changed captured decision accepted")
	}
	if stateAt(t, a, secondary.PID).Claimable() {
		t.Fatal("captured sibling regained live authority")
	}
}
