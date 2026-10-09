package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The device approving a link need not hold every already-accepted turn.
// Another current human device must contribute its own snapshot when it
// learns the verified roster, without needing to approve the link itself.
func TestHistoryCurrentOwnDeviceSuppliesMissingSnapshot(t *testing.T) {
	w, approver, source, packet := groupHistoryLinkedFixture(t)
	stopApprover := runAgent(t, approver)
	stopSource := runAgent(t, source)
	publishGroupFixtureCaps(t, source, true)
	groupGovernanceAwait(t, packet, source)
	dm := newDM(t, w.bob, source)
	stopSource()

	_, dmRoot := rootOf(t, w.bob, dm)
	groupRoot, err := json.Marshal(packet.Root)
	if err != nil {
		t.Fatal(err)
	}
	originals := []envelope.Inner{
		{Conv: packet.State.Conv, Root: groupRoot, Body: "group turn only this laptop received"},
		{Conv: dm, Root: dmRoot, Body: "person turn only this laptop received"},
	}
	for i := range originals {
		in := &originals[i]
		in.ID, in.LID = protocol.NewID(), protocol.NewID()
		in.Kind, in.Origin = envelope.KindMessage, envelope.OriginUI
		// A real signed, encrypted original reaches B while A is absent from
		// this delivery. No inbox row or history cursor is fabricated.
		if err := source.verifyAndStore(tctx(t), craft(t, w.bob, source, *in)); err != nil {
			t.Fatal(err)
		}
		if got := inboxCount(t, source, "id=?", in.ID); got != 1 {
			t.Fatalf("source did not accept signed original: count=%d held=%s", got, heldReason(t, source, in.ID))
		}
		if got := inboxCount(t, approver, "lid=?", in.LID); got != 0 {
			t.Fatal("approver unexpectedly holds the missing original")
		}
	}

	phone, awaited, _ := linkPhone(t, approver, "other-laptop-phone")
	request := pendingLink(t, approver)
	stopApprover()
	if err := approver.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	approver.historyStep(tctx(t))
	var supplied int
	if err := approver.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, phone.Address).Scan(&supplied); err != nil || supplied != 0 {
		t.Fatalf("approver supplied history it did not have: count=%d error=%v", supplied, err)
	}

	// The same reference carried by a members push makes B follow A's
	// signed chain. A connection already schedules this history upkeep.
	me, _, err := approver.Person()
	if err != nil {
		t.Fatal(err)
	}
	source.convWork.take()
	source.observeRef(tctx(t), &protocol.PersonRef{ID: me.Person, Seq: me.Seq, Hash: me.Roster})
	own, ok, err := source.store.selfPerson(source.Address)
	if err != nil || !ok || !own.has(phone.Address, phone.Self().Fingerprint()) || !own.roster.Human(source.Self().Fingerprint()) || !own.roster.Human(phone.Self().Fingerprint()) {
		t.Fatalf("source lacks the exact verified current own-human roster: %v", err)
	}
	if source.convWork.bits.Load()&convHistory == 0 {
		t.Fatal("verified own-roster change did not wake history upkeep")
	}
	source.convSync(tctx(t))
	var jobs, copies int
	if err := source.store.db.QueryRow(`SELECT count(*) FROM history_jobs WHERE device=? AND fingerprint=?`, phone.Address, phone.Self().Fingerprint()).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := source.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, phone.Address).Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || copies != len(originals) {
		t.Fatalf("current own laptop supplied no missing snapshot after verified roster/history wake: jobs=%d history=%d want=1/%d; approver history=%d", jobs, copies, len(originals), supplied)
	}

	rows, err := source.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub IN ('history','group-proof','group-context') ORDER BY rowid`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	var envs []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, env := range envs {
		groupGovernanceDeliver(t, source, phone, env)
	}
	phone.retryProof(tctx(t))
	for _, env := range envs {
		if state, err := phone.store.disposition(env.ID); err != nil || state != protocol.StateDelivered {
			t.Fatalf("admitted history carrier has no durable delivery receipt: %q %v", state, err)
		}
	}
	for _, in := range originals {
		messages, err := phone.ConversationMessages(in.Conv)
		if err != nil || len(messages) != 1 {
			t.Fatalf("phone lacks exact missing history: count=%d error=%v", len(messages), err)
		}
		m := messages[0]
		if m.ID != in.ID || m.LID != in.LID || m.Claimed != w.bob.Self().Fingerprint() || !m.History || !m.Replica || m.SyncedFrom != source.Address || m.Job != "" || m.State != "" {
			t.Fatalf("history lost original attribution or gained execution state: %+v", m)
		}
	}
	var before string
	if err := source.store.db.QueryRow(`SELECT pos FROM history_jobs WHERE device=?`, phone.Address).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// A quarantine receipt proves that the receiver retained the ciphertext.
	// Reconnect must not mint a fresh carrier for the same logical item.
	var carrierCount int
	if err := source.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub IN ('history','group-proof','group-context')`, phone.Address).Scan(&carrierCount); err != nil {
		t.Fatal(err)
	}
	if _, err := source.store.db.Exec(`UPDATE outbox SET state='quarantined' WHERE recipient=? AND sub IN ('history','group-proof','group-context')`, phone.Address); err != nil {
		t.Fatal(err)
	}
	source.convWork.historyDeferred = nil
	source.historyStep(tctx(t))
	reopened, err := Open(source.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.historyStep(tctx(t))
	var after string
	if err := reopened.store.db.QueryRow(`SELECT pos FROM history_jobs WHERE device=?`, phone.Address).Scan(&after); err != nil || after != before {
		t.Fatalf("repeat/restart changed snapshot cursor: %v", err)
	}
	if err := reopened.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, phone.Address).Scan(&copies); err != nil || copies != len(originals) {
		t.Fatalf("repeat/restart duplicated history: copies=%d error=%v", copies, err)
	}
	var afterCarriers int
	if err := reopened.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub IN ('history','group-proof','group-context')`, phone.Address).Scan(&afterCarriers); err != nil || afterCarriers != carrierCount {
		t.Fatalf("quarantine/restart generated duplicate ciphertext: before=%d after=%d error=%v", carrierCount, afterCarriers, err)
	}
	for _, env := range envs {
		groupGovernanceDeliver(t, reopened, phone, env)
	}
	for _, in := range originals {
		if got := inboxCount(t, phone, "lid=?", in.LID); got != 1 {
			t.Fatalf("duplicate history inserted %d originals", got)
		}
	}
	// A queued copy must retain the narrower authority after restart. The
	// legacy group/DM membership checks alone also admit agent-role keys.
	historyRecoveryChangeRole(t, reopened, phone, "sender")
	for _, env := range envs {
		if allowed, err := reopened.mayDeliver(env); err != nil || allowed {
			t.Fatalf("automatic queued history escaped changed human role: allowed=%v error=%v", allowed, err)
		}
	}
}

func TestHistoryDiscoveryCurrentAuthority(t *testing.T) {
	for _, change := range []string{"source agent", "recipient agent", "removed", "foreign", "frozen", "changed key", "pending recipient", "pending source"} {
		t.Run(change, func(t *testing.T) {
			a, recipient, _, _ := historyRecoveryFixture(t)
			switch change {
			case "source agent":
				historyRecoveryChangeRole(t, a, recipient, "reader")
			case "recipient agent":
				historyRecoveryChangeRole(t, a, recipient, "sender")
			case "removed":
				own, _, _ := a.store.selfPerson(a.Address)
				next := own.roster
				next.Seq++
				next.Prev, next.By, next.Join = own.roster.Hash(), a.Self().Fingerprint(), nil
				next.Devices = slices.DeleteFunc(slices.Clone(next.Devices), func(d identity.Public) bool { return d.Address == recipient.Address })
				next.HumanKeys = []string{a.Self().Fingerprint()}
				next.Sign(a.id.Sign)
				if _, err := a.store.pinChain(next.Person, [][]byte{[]byte(mustJSON(next))}, a.Self(), false); err != nil {
					t.Fatal(err)
				}
			case "foreign", "frozen":
				state := personPinned
				if change == "frozen" {
					state = personConflict
				}
				if _, err := a.store.db.Exec(`UPDATE persons SET state=?`, state); err != nil {
					t.Fatal(err)
				}
			default:
				id, err := identity.Generate()
				if err != nil {
					t.Fatal(err)
				}
				address := recipient.Address
				if change == "pending source" {
					address = a.Address
					if err := a.store.pin(a.Self()); err != nil {
						t.Fatal(err)
					}
				}
				if change == "changed key" {
					err = a.store.pin(id.Public(address))
				} else {
					err = a.store.setPending(id.Public(address))
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := a.reconcileHistory(); err != nil {
				t.Fatal(err)
			}
			if n := count(t, a, "history_jobs"); n != 0 {
				t.Fatalf("ineligible device gained %d history jobs", n)
			}
			if _, err := a.store.config(discoveredHistory + recipient.Address); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("ineligible device gained discovery guard: %v", err)
			}
		})
	}
}

func TestHistoryDiscoveryKeepsExistingJobsAndRollsBack(t *testing.T) {
	for _, state := range []string{"running", "done", "ended"} {
		t.Run(state, func(t *testing.T) {
			a, recipient, _, _ := historyRecoveryFixture(t)
			pos := `{"conv":"kept","ms":123,"id":"original"}`
			if _, err := a.store.db.Exec(`INSERT INTO history_jobs(device,fingerprint,pos,convs_total,state,created_at,updated_at) VALUES(?,?,?,1,?,10,20)`, recipient.Address, "old-fingerprint", pos, state); err != nil {
				t.Fatal(err)
			}
			if err := a.reconcileHistory(); err != nil {
				t.Fatal(err)
			}
			var gotPos, gotState, fp string
			var created, updated int
			if err := a.store.db.QueryRow(`SELECT fingerprint,pos,state,created_at,updated_at FROM history_jobs WHERE device=?`, recipient.Address).Scan(&fp, &gotPos, &gotState, &created, &updated); err != nil || fp != "old-fingerprint" || gotPos != pos || gotState != state || created != 10 || updated != 20 {
				t.Fatalf("existing job altered: %s %s %s %d %d error=%v", fp, gotPos, gotState, created, updated, err)
			}
			if _, err := a.store.config(discoveredHistory + recipient.Address); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("existing explicit job policy changed: %v", err)
			}
		})
	}
	a, recipient, _, _ := historyRecoveryFixture(t)
	if _, err := a.store.db.Exec(`CREATE TRIGGER fail_discovered_history BEFORE INSERT ON config WHEN NEW.k LIKE 'own-human-history/%' BEGIN SELECT RAISE(ABORT,'fixture guard write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileHistory(); err == nil {
		t.Fatal("guard write failure ignored")
	}
	if n := count(t, a, "history_jobs"); n != 0 {
		t.Fatal("unguarded job survived rollback")
	}
	if _, err := a.store.db.Exec(`DROP TRIGGER fail_discovered_history`); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileHistory(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, a, "history_jobs"); n != 1 {
		t.Fatalf("guarded job missing after retry: %d", n)
	}
	if err := a.discoveredHistoryCheck(a.store.db, recipient.Address); err != nil {
		t.Fatal(err)
	}
}

// A's sealed history can arrive after B finished C's snapshot. A is now
// offline; B must continue the person's history even when the old turn's
// conversation sorts before C's existing snapshot cursor.
func TestHistoryLateOwnCopyConvergesAfterCompletedSnapshot(t *testing.T) {
	w, first, source, packet := groupHistoryLinkedFixture(t)
	stopFirst := runAgent(t, first)
	stopSource := runAgent(t, source)
	publishGroupFixtureCaps(t, source, true)
	groupGovernanceAwait(t, packet, source)
	dms := []string{newDM(t, w.bob, source), newDM(t, w.bob, source)}
	slices.Sort(dms)
	stopFirst()
	_, highRoot := rootOf(t, w.bob, dms[1])
	high := envelope.Inner{ID: protocol.NewID(), LID: protocol.NewID(), Conv: dms[1], Root: highRoot, Kind: envelope.KindMessage, Body: "already received newest conversation", Origin: envelope.OriginUI}
	if err := source.verifyAndStore(tctx(t), craft(t, w.bob, source, high)); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, source, "id=?", high.ID); n != 1 {
		t.Fatal("source lacks original newest conversation")
	}
	_, lowRoot := rootOf(t, w.bob, dms[0])
	late := []envelope.Inner{
		{Conv: dms[0], Root: lowRoot, Body: "older person history delayed before phone link"},
		{Conv: packet.State.Conv, Root: json.RawMessage(mustJSON(packet.Root)), Body: "older group history delayed before phone link"},
	}
	var delayed []outCopy
	for i := range late {
		in := &late[i]
		in.ID, in.LID, in.Kind, in.Origin, in.TS = protocol.NewID(), protocol.NewID(), envelope.KindMessage, envelope.OriginUI, time.Now().Add(-24*time.Hour).Unix()
		env := craft(t, w.bob, first, *in)
		if err := first.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		original, err := envelope.Open(env, first.id, first.Address, w.bob.Self())
		if err != nil {
			t.Fatal(err)
		}
		var at int64
		if err := first.store.db.QueryRow(`SELECT received_ms FROM inbox WHERE id=?`, in.ID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		copy, err := first.historyCopy(source.Self(), in.Conv, in.Root, itemOf(original, w.bob.Self().Fingerprint(), at))
		if err != nil {
			t.Fatal(err)
		}
		delayed = append(delayed, copy)
		if n := inboxCount(t, source, "lid=?", in.LID); n != 0 {
			t.Fatal("delayed original arrived before the phone snapshot")
		}
	}

	phone, awaited, _ := linkPhone(t, source, "late-history-phone")
	request := pendingLink(t, source)
	stopSource()
	if err := source.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	if _, err := source.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	var state, before string
	if err := source.store.db.QueryRow(`SELECT state,pos FROM history_jobs WHERE device=?`, phone.Address).Scan(&state, &before); err != nil || state != "done" {
		t.Fatalf("phone snapshot has not finished: %s %v", state, err)
	}
	var pos historyPos
	if err := json.Unmarshal([]byte(before), &pos); err != nil || pos.Conv != dms[1] || late[0].Conv >= pos.Conv {
		t.Fatalf("late conversation is not before the completed cursor: %s %v", before, err)
	}
	reopened, err := Open(source.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for i, copy := range delayed {
		// Admission of an already sealed, reordered own replica. A's daemon
		// remains offline and cannot discover or backfill C itself.
		if err := reopened.verifyAndStore(tctx(t), copy.env); err != nil {
			t.Fatal(err)
		}
		if n := inboxCount(t, reopened, "id=? AND claimed_fp=? AND replica=1", late[i].ID, w.bob.Self().Fingerprint()); n != 1 {
			t.Fatalf("late own history was not validly admitted: count=%d held=%s", n, heldReason(t, reopened, copy.env.ID))
		}
	}
	// The same upkeep a reconnect schedules; no cursor reset or broad replay.
	reopened.convWork.due(convHistory | convRetry)
	reopened.convSync(tctx(t))
	var after string
	if err := reopened.store.db.QueryRow(`SELECT pos FROM history_jobs WHERE device=?`, phone.Address).Scan(&after); err != nil || before != after {
		t.Fatalf("completed snapshot cursor changed: %v", err)
	}
	rows, err := reopened.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='history'`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var copies int
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		in, err := envelope.Open(env, phone.id, phone.Address, reopened.Self())
		if err != nil {
			t.Fatal(err)
		}
		var item HistoryItem
		if err := json.Unmarshal([]byte(in.Body), &item); err != nil {
			t.Fatal(err)
		}
		for _, original := range late {
			if item.LID == original.LID && item.FromKey == w.bob.Self().Fingerprint() {
				copies++
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if copies != len(late) {
		t.Fatalf("late accepted own history stranded behind done snapshot: onward copies=%d want=%d; one conversation sorts before old cursor", copies, len(late))
	}
}
