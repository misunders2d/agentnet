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

// One deterministic retained-ledger discriminator/regression, independent of
// scheduling/platform speed. Only field names, hashes and IDs are logged.
func TestHistoryCatchupRedactedDependencyRetainsExactLedger(t *testing.T) {
	w, phone, conv, ids := historyCatchupFixture(t, 1)
	a := w.alice
	if _, e := a.historyCatchupPage(tctx(t), phone.Self()); e != nil {
		t.Fatal(e)
	}
	source := func(id string) historySourceRow {
		t.Helper()
		r, e := a.historySourceRows(a.store.db, "dir='in' AND id=?", "id", 1, id)
		if e != nil || len(r) != 1 {
			t.Fatalf("source count=%d error=%v", len(r), e)
		}
		return r[0]
	}
	original := source(ids[0])
	target := &envelope.Ref{ID: original.in.LID, Fingerprint: original.key}
	control := func(sub, body string) string {
		t.Helper()
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: a.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: body, Conv: conv, LID: protocol.NewID(), Sub: sub, Ref: target}
		recipient, _ := a.Self().Recipient()
		env, sealErr := envelope.Seal(in, w.bob.id.Sign, recipient)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		if e := a.verifyAndStore(tctx(t), env); e != nil {
			t.Fatal(e)
		}
		return env.ID
	}
	revision := control(envelope.SubRevision, `{"rev":1,"text":"synthetic revision"}`)
	if _, e := a.historyCatchupPage(tctx(t), phone.Self()); e != nil {
		t.Fatal(e)
	}
	before := map[string]historySourceRow{original.in.ID: original, revision: source(revision)}
	saved := map[string]string{}
	for id, row := range before {
		c, e := a.prepareHistorySource(phone.Self(), row)
		if e != nil || c == nil {
			t.Fatalf("prepare original id=%s error=%v", id, e)
		}
		h, e := historyCopyHash(*c)
		if e != nil {
			t.Fatal(e)
		}
		saved[id] = h
	}
	retraction := control(envelope.SubRetraction, `{"reason":""}`)
	for id, old := range before {
		fresh := source(id)
		changed := []string{}
		oldMap, newMap := map[string]any{}, map[string]any{}
		raw, _ := json.Marshal(old.in)
		_ = json.Unmarshal(raw, &oldMap)
		raw, _ = json.Marshal(fresh.in)
		_ = json.Unmarshal(raw, &newMap)
		for field, v := range oldMap {
			if !reflect.DeepEqual(v, newMap[field]) {
				changed = append(changed, field)
			}
		}
		if len(changed) != 1 || changed[0] != "body" || fresh.in.Body != "" {
			t.Fatalf("unexpected changed fields id=%s fields=%v", id, changed)
		}
		c, e := a.prepareHistorySource(phone.Self(), fresh)
		if e != nil || c == nil {
			t.Fatalf("redacted source id=%s error=%v", id, e)
		}
		hash, e := historyCopyHash(*c)
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("source_id=%s changed_fields=%v original_hash=%s redacted_hash=%s", id, changed, saved[id], hash)
		if _, e = historyCopyPresent(a.store.db, phone.Self(), *c); !errors.Is(e, errHistoryCatchupConflict) {
			t.Fatal("strict ledger accepted changed source")
		}
	}
	// The original strict comparator must keep refusing redacted content, while
	// catch-up advances the already-covered dependencies and queues the deletion.
	if _, e := a.historyCatchupPage(tctx(t), phone.Self()); e != nil {
		t.Fatalf("retraction catch-up blocked: %v", e)
	}
	for id, row := range before {
		var ledger string
		if e := a.store.db.QueryRow(`SELECT hash FROM history_copies WHERE recipient_fp=? AND conv=? AND author=? AND lid=?`, phone.Self().Fingerprint(), conv, row.key, row.in.LID).Scan(&ledger); e != nil || ledger != saved[id] {
			t.Fatalf("immutable ledger changed id=%s error=%v", id, e)
		}
	}
	var count int
	if e := a.store.db.QueryRow(`SELECT count(*) FROM history_copies WHERE recipient_fp=? AND conv=? AND author=? AND lid=?`, phone.Self().Fingerprint(), conv, w.bob.Self().Fingerprint(), source(retraction).in.LID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("deletion not queued count=%d error=%v", count, e)
	}
}
