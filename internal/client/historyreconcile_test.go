package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"testing"

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
