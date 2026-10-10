package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func historyCatchupFixture(t *testing.T, n int) (*world, *Agent, string, []string) {
	t.Helper()
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	stop()
	_, root := rootOf(t, w.bob, conv)
	var ids []string
	for i := 0; i < n; i++ {
		in := envelope.Inner{Kind: envelope.KindMessage, Body: fmt.Sprintf("row-%03d", i), Conv: conv, Root: root, LID: protocol.NewID(), Origin: envelope.OriginUI}
		env := craft(t, w.bob, w.alice, in)
		if err := w.alice.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if _, err := w.alice.store.db.Exec(`UPDATE inbox SET received_ms=? WHERE id=?`, 1000+i, env.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, env.ID)
	}
	stop = runAgent(t, w.alice)
	phone, awaited, _ := linkPhone(t, w.alice, "catchup-phone")
	request := pendingLink(t, w.alice)
	stop()
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	return w, phone, conv, ids
}

func historyCopiedItems(t *testing.T, a, phone *Agent) []HistoryItem {
	t.Helper()
	rows, err := a.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='history' ORDER BY rowid`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var items []HistoryItem
	for rows.Next() {
		var raw string
		var env envelope.Envelope
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		in, err := envelope.Open(env, phone.id, phone.Address, a.Self())
		if err != nil {
			t.Fatal(err)
		}
		var item HistoryItem
		if err := decodeStrict([]byte(in.Body), &item); err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestHistoryCatchupRecentTailRestart(t *testing.T) {
	w, phone, conv, ids := historyCatchupFixture(t, 57)
	a := w.alice
	more, err := a.historyCatchupPage(tctx(t), phone.Self())
	if err != nil || !more {
		t.Fatalf("first page: %v %v", more, err)
	}
	items := historyCopiedItems(t, a, phone)
	if len(items) != historyPage {
		t.Fatalf("first bounded page: %d", len(items))
	}
	if items[0].Body != "row-056" || items[len(items)-1].Body != "row-007" {
		t.Fatalf("not newest page: first=%q last=%q", items[0].Body, items[len(items)-1].Body)
	}
	a.Close()
	a, err = Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, root := rootOf(t, w.bob, conv)
	late := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Body: "old timestamp arrives during backfill", Conv: conv, Root: root, LID: protocol.NewID(), Origin: envelope.OriginUI})
	if err := a.verifyAndStore(tctx(t), late); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE inbox SET received_ms=1 WHERE id=?`, late.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	items = historyCopiedItems(t, a, phone)
	if len(items) <= historyPage || items[historyPage].Body != "old timestamp arrives during backfill" {
		t.Fatal("old timestamp waited behind older backfill")
	}
	var receiptSeq int64
	for rounds := 0; ; rounds++ {
		// Production remains bounded until this reader retains the first page.
		// Admit the signed carriers before supplying their proven receipts.
		queued, err := a.store.queued()
		if err != nil {
			t.Fatal(err)
		}
		for _, env := range queued {
			if env.To != phone.Address {
				continue
			}
			if err = phone.verifyAndStore(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			state, err := phone.store.disposition(env.ID)
			if err != nil || state != protocol.StateDelivered {
				t.Fatalf("phone did not admit history: %s %v", state, err)
			}
			receiptSeq++
			raw, _ := json.Marshal(protocol.ReceiptEvent{Seq: receiptSeq, ID: env.ID, State: state})
			if err = a.dispatch(tctx(t), "receipt", string(raw)); err != nil {
				t.Fatal(err)
			}
		}
		more, err := a.historyCatchupPage(tctx(t), phone.Self())
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		if rounds > 5 {
			t.Fatal("unchanged history did not settle")
		}
	}
	items = historyCopiedItems(t, a, phone)
	if len(items) != len(ids)+1 {
		t.Fatalf("missing/duplicate exact copies: %d", len(items))
	}
	var jobs int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE attempts>0`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("history ran work: %d %v", jobs, err)
	}
	before := len(items)
	if more, err := a.historyCatchupPage(tctx(t), phone.Self()); more || err != nil {
		t.Fatalf("settled wake: %v %v", more, err)
	}
	if got := len(historyCopiedItems(t, a, phone)); got != before {
		t.Fatalf("unchanged wake resent history: %d", got)
	}
	var pos string
	if err := a.store.db.QueryRow(`SELECT pos FROM history_jobs WHERE device=?`, phone.Address).Scan(&pos); err != nil || pos != `{"conv":"","ms":0,"id":""}` {
		t.Fatalf("legacy position overwritten: %s %v", pos, err)
	}
}

func TestHistoryCatchupExpiredCopyAndConflictingSource(t *testing.T) {
	w, phone, _, ids := historyCatchupFixture(t, 1)
	a := w.alice
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := a.store.db.QueryRow(`SELECT carrier FROM history_copies WHERE recipient_fp=?`, phone.Self().Fingerprint()).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE outbox SET state='expired' WHERE id=?`, original); err != nil {
		t.Fatal(err)
	}
	a.convWork.historyDeferred = nil
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	var replacement string
	if err := a.store.db.QueryRow(`SELECT carrier FROM history_copies WHERE recipient_fp=?`, phone.Self().Fingerprint()).Scan(&replacement); err != nil || replacement == original {
		t.Fatalf("expired copy not replaced: %s %v", replacement, err)
	}
	if got := len(historyCopiedItems(t, a, phone)); got != 2 {
		t.Fatalf("replacement count: %d", got)
	}
	if _, err := a.store.db.Exec(`UPDATE outbox SET state='expired' WHERE id=?`, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE inbox SET body='conflicting source bytes' WHERE id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	a.convWork.historyDeferred = nil
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); !errors.Is(err, errHistoryCatchupConflict) {
		t.Fatalf("conflicting logical bytes accepted: %v", err)
	}
	if got := len(historyCopiedItems(t, a, phone)); got != 2 {
		t.Fatalf("conflict queued another copy: %d", got)
	}
}

func TestHistoryCatchupMissingContextDoesNotBlockOtherChat(t *testing.T) {
	w, approver, source, packet := groupHistoryLinkedFixture(t)
	stopApprover := runAgent(t, approver)
	stopSource := runAgent(t, source)
	publishGroupFixtureCaps(t, source, true)
	groupGovernanceAwait(t, packet, source)
	dm := newDM(t, w.bob, source)
	stopSource()
	var groupPayload []byte
	if err := source.store.db.QueryRow(`SELECT payload FROM group_context WHERE conv=?`, packet.State.Conv).Scan(&groupPayload); err != nil {
		t.Fatal(err)
	}
	_, dmRoot := rootOf(t, w.bob, dm)
	for _, in := range []envelope.Inner{{Conv: dm, Root: dmRoot, Body: "healthy DM"}, {Conv: packet.State.Conv, Root: json.RawMessage(mustJSON(packet.Root)), Body: "group awaits context"}} {
		in.Kind, in.LID, in.Origin = envelope.KindMessage, protocol.NewID(), envelope.OriginUI
		if err := source.verifyAndStore(tctx(t), craft(t, w.bob, source, in)); err != nil {
			t.Fatal(err)
		}
	}
	phone, awaited, _ := linkPhone(t, approver, "context-catchup-phone")
	request := pendingLink(t, approver)
	stopApprover()
	if err := approver.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	me, _, err := approver.Person()
	if err != nil {
		t.Fatal(err)
	}
	source.observeRef(tctx(t), &protocol.PersonRef{ID: me.Person, Seq: me.Seq, Hash: me.Roster})
	if err := source.reconcileHistory(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.store.db.Exec(`DELETE FROM group_context WHERE conv=?`, packet.State.Conv); err != nil {
		t.Fatal(err)
	}
	if _, err := source.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	items := historyCopiedItems(t, source, phone)
	if len(items) != 1 || items[0].Body != "healthy DM" {
		t.Fatalf("bad group blocked healthy chat: %+v", items)
	}
	var pending int
	if err := source.store.db.QueryRow(`SELECT count(*) FROM history_deferred WHERE recipient_fp=?`, phone.Self().Fingerprint()).Scan(&pending); err != nil || pending < 1 {
		t.Fatalf("missing source silently discarded: %d %v", pending, err)
	}
	if more, err := source.historyCatchupPage(tctx(t), phone.Self()); err != nil || more {
		t.Fatalf("unchanged proof retries forever: %v %v", more, err)
	}
	source.Close()
	source, err = Open(source.home)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.store.db.Exec(`INSERT INTO group_context(conv,payload) VALUES(?,?)`, packet.State.Conv, groupPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := source.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	items = historyCopiedItems(t, source, phone)
	if len(items) != 2 {
		t.Fatalf("restored context never rechecked: %d", len(items))
	}
}

func TestHistoryCatchupAuthorityAndAtomicPage(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 3)
	a := w.alice
	if err := a.store.pin(phone.Self()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	a.historyStep(tctx(t))
	if items := historyCopiedItems(t, a, phone); len(items) != 0 {
		t.Fatal("pending human key fell back to legacy queue authority")
	}
	if _, err := a.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`CREATE TRIGGER test_history_abort BEFORE INSERT ON outbox WHEN NEW.sub='history' BEGIN SELECT RAISE(ABORT,'synthetic history queue interruption'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err == nil {
		t.Fatal("interrupted queue reported success")
	}
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM history_copies`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("ledger escaped failed queue transaction: %d %v", n, err)
	}
	var pos, phase string
	if err := a.store.db.QueryRow(`SELECT pos,phase FROM history_catchup WHERE device=?`, phone.Address).Scan(&pos, &phase); err != nil || phase != "recent" || pos != `{"conv":"","ms":0,"id":""}` {
		t.Fatalf("failed page advanced progress: %s %s %v", phase, pos, err)
	}
	if _, err := a.store.db.Exec(`DROP TRIGGER test_history_abort`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	historyRecoveryChangeRole(t, a, phone, "sender")
	a.historyStep(tctx(t))
	if items := historyCopiedItems(t, a, phone); len(items) != 3 {
		t.Fatalf("role change replayed legacy snapshot: %d", len(items))
	}
	var raw string
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub='history' LIMIT 1`, phone.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	if allowed, err := a.mayDeliver(env); err != nil || allowed {
		t.Fatalf("queued catch-up escaped current-human delivery fence: %v %v", allowed, err)
	}
}

func TestHistoryCatchupConcurrentPagesDoNotRegress(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 57)
	a := w.alice
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := a.historyCatchupPage(tctx(t), phone.Self()); done <- err }()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := len(historyCopiedItems(t, a, phone)); got != 57 {
		t.Fatalf("concurrent pages lost or repeated originals: %d", got)
	}
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if got := len(historyCopiedItems(t, a, phone)); got != 57 {
		t.Fatalf("serialized page regressed cursor: %d", got)
	}
	if _, err := a.store.db.Exec(`UPDATE history_jobs SET state='ended' WHERE device=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); !errors.Is(err, errHistoryRecoveryAuthority) {
		t.Fatalf("ended job restarted: %v", err)
	}
}

func TestHistoryCatchupNewestReplyIncludesOlderDependency(t *testing.T) {
	w, phone, conv, ids := historyCatchupFixture(t, 57)
	a := w.alice
	_, root := rootOf(t, w.bob, conv)
	latest := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Body: "newest reply", Conv: conv, Root: root, LID: protocol.NewID(), ReplyTo: ids[0], Origin: envelope.OriginUI})
	if err := a.verifyAndStore(tctx(t), latest); err != nil {
		t.Fatal(err)
	}
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	items := historyCopiedItems(t, a, phone)
	if len(items) != historyPage+1 || items[0].ID != ids[0] || items[1].ID != latest.ID {
		t.Fatalf("newest reply dependency not queued first: %d copies", len(items))
	}
	rows, err := a.historySourceRows(a.store.db, "dir='in' AND id=?", "conv,ms,id", 1, latest.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("source: %v %v", rows, err)
	}
	copy, err := a.prepareHistorySource(phone.Self(), rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if present, err := historyCopyPresent(a.store.db, phone.Self(), *copy); err != nil || !present {
		t.Fatalf("exact durable copy absent: %v %v", present, err)
	}
	if _, err := a.store.db.Exec(`UPDATE outbox SET recipient_fp='unrelated' WHERE id=(SELECT carrier FROM history_copies WHERE recipient_fp=? AND lid=?)`, phone.Self().Fingerprint(), rows[0].in.LID); err != nil {
		t.Fatal(err)
	}
	if present, err := historyCopyPresent(a.store.db, phone.Self(), *copy); err != nil || present {
		t.Fatalf("mismatched carrier counted as durable: %v %v", present, err)
	}
}
