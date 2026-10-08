package client

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func historyRecoveryFixture(t *testing.T) (*Agent, *Agent, protocol.ConvRoot, HistoryItem) {
	t.Helper()
	a := enrolledAt(t, "https://history-fixture.invalid", "")
	t.Cleanup(func() { a.Close() })
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	sender := &Agent{id: id, Address: "member/other"}
	first := protocol.PersonRoster{Person: protocol.NewID(), Label: "History fixture", Devices: []identity.Public{a.Self()}}
	first.Sign(a.id.Sign)
	next := protocol.PersonRoster{Person: first.Person, Label: first.Label, Seq: 1, Prev: first.Hash(), By: a.Self().Fingerprint(), Devices: []identity.Public{a.Self(), sender.Self()}, HumanKeys: []string{a.Self().Fingerprint(), sender.Self().Fingerprint()}}
	next.Join = ed25519.Sign(sender.id.Sign, protocol.JoinBytes(next.Person, next.Seq, next.Prev, sender.Self()))
	next.Sign(a.id.Sign)
	if _, err = a.store.pinChain(first.Person, [][]byte{[]byte(mustJSON(first)), []byte(mustJSON(next))}, a.Self(), true); err != nil {
		t.Fatal(err)
	}
	if err = a.store.pin(sender.Self()); err != nil {
		t.Fatal(err)
	}
	if err = a.store.deleteConfig(historyRecoveryScan); err != nil {
		t.Fatal(err)
	}
	root := protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Creator: protocol.ConvCreator{Person: next.Person, Roster: next.Hash(), Address: a.Address, Fingerprint: a.Self().Fingerprint()}, Members: []protocol.ConvMember{{Person: next.Person, Roster: next.Hash()}}, Nonce: protocol.NewID(), Created: time.Now().Unix(), Realm: protocol.NewID(), Title: "History fixture", Admins: []string{next.Person}}
	root.Sign(a.id.Sign)
	item := HistoryItem{V: 1, ID: protocol.NewID(), LID: protocol.NewID(), From: sender.Address, FromKey: sender.Self().Fingerprint(), TS: time.Now().Unix(), Kind: envelope.KindMessage, PID: protocol.NewID(), Body: "inert history"}
	return a, sender, root, item
}

func historyRecoveryEnvelope(t *testing.T, sender, reader *Agent, root protocol.ConvRoot, item HistoryItem) envelope.Envelope {
	t.Helper()
	return craft(t, sender, reader, envelope.Inner{Conv: root.ID(), Root: json.RawMessage(mustJSON(root)), LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubHistory, Replica: true, Body: mustJSON(item)})
}

func historyRecoveryReason(t *testing.T, a *Agent, id, want string) {
	t.Helper()
	var reason string
	if err := a.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, id).Scan(&reason); err != nil || reason != want {
		t.Fatalf("carrier reason %q, want %q: %v", reason, want, err)
	}
}

func historyRecoveryChangeRole(t *testing.T, a *Agent, sender *Agent, who string) {
	t.Helper()
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		t.Fatalf("own roster: %v", err)
	}
	next := me.roster
	next.Seq++
	next.Prev, next.By, next.Join = me.roster.Hash(), a.Self().Fingerprint(), nil
	fp := sender.Self().Fingerprint()
	if who == "reader" {
		fp = a.Self().Fingerprint()
	}
	next.HumanKeys = slices.DeleteFunc(next.Humans(), func(key string) bool { return key == fp })
	next.Sign(a.id.Sign)
	if _, err := a.store.pinChain(next.Person, [][]byte{[]byte(mustJSON(next))}, a.Self(), false); err != nil {
		t.Fatal(err)
	}
}

func TestGroupHistoryRecoveryScanScope(t *testing.T) {
	a, sender, root, item := historyRecoveryFixture(t)
	eligible := historyRecoveryEnvelope(t, sender, a, root, item)
	control := item
	control.PID, control.Sub, control.Ref = "", envelope.SubRevision, &envelope.Ref{ID: protocol.NewID(), Fingerprint: sender.Self().Fingerprint()}
	eligibleControl := historyRecoveryEnvelope(t, sender, a, root, control)
	ordinary := item
	ordinary.PID = ""
	ordinaryEnv := historyRecoveryEnvelope(t, sender, a, root, ordinary)
	badSignature := historyRecoveryEnvelope(t, sender, a, root, item)
	badSignature.Sig[0] ^= 1
	malformed := craft(t, sender, a, envelope.Inner{Conv: root.ID(), Root: json.RawMessage(mustJSON(root)), LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubHistory, Replica: true, Body: "{}"})
	live := craft(t, sender, a, envelope.Inner{Conv: root.ID(), Root: json.RawMessage(mustJSON(root)), LID: protocol.NewID(), Kind: envelope.KindMessage, Body: "live remains held"})
	for _, env := range []envelope.Envelope{eligible, eligibleControl, ordinaryEnv, badSignature, malformed, live} {
		if err := a.store.holdAs(env, reasonInvalid); err != nil {
			t.Fatal(err)
		}
	}
	otherReasons := map[string]envelope.Envelope{}
	for _, reason := range []string{reasonProof, reasonConflict, reasonDuplicate, reasonKeyChanged} {
		env := historyRecoveryEnvelope(t, sender, a, root, item)
		otherReasons[reason] = env
		if err := a.store.holdAs(env, reason); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.recoverInvalidGroupHistory(); err != nil {
		t.Fatal(err)
	}
	for reason, env := range otherReasons {
		historyRecoveryReason(t, a, env.ID, reason)
		if _, err := a.store.config(historyRecoveryCarrier + env.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("reconsidered %s carrier: %v", reason, err)
		}
	}
	for _, env := range []envelope.Envelope{eligible, eligibleControl} {
		historyRecoveryReason(t, a, env.ID, reasonProof)
	}
	for _, env := range []envelope.Envelope{ordinaryEnv, badSignature, malformed, live} {
		historyRecoveryReason(t, a, env.ID, reasonInvalid)
	}
	late := historyRecoveryEnvelope(t, sender, a, root, item)
	if err := a.store.holdAs(late, reasonInvalid); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInvalidGroupHistory(); err != nil {
		t.Fatal(err)
	}
	historyRecoveryReason(t, a, late.ID, reasonInvalid)
	if got := inboxCount(t, a, "1=1"); got != 0 {
		t.Fatalf("scan inserted %d messages", got)
	}
}

func TestGroupHistoryRecoveryCurrentAuthority(t *testing.T) {
	for _, change := range []string{"sender agent host", "reader agent host", "frozen", "foreign", "wrong pin", "pending sender", "pending reader"} {
		t.Run(change, func(t *testing.T) {
			a, sender, root, item := historyRecoveryFixture(t)
			env := historyRecoveryEnvelope(t, sender, a, root, item)
			if err := a.store.holdAs(env, reasonInvalid); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "sender agent host":
				historyRecoveryChangeRole(t, a, sender, "sender")
			case "reader agent host":
				historyRecoveryChangeRole(t, a, sender, "reader")
			case "frozen", "foreign":
				state := personConflict
				if change == "foreign" {
					state = personPinned
				}
				if _, err := a.store.db.Exec(`UPDATE persons SET state=?`, state); err != nil {
					t.Fatal(err)
				}
			default:
				id, err := identity.Generate()
				if err != nil {
					t.Fatal(err)
				}
				address := sender.Address
				if change == "pending reader" {
					address = a.Address
					if err = a.store.pin(a.Self()); err != nil {
						t.Fatal(err)
					}
				}
				if change == "wrong pin" {
					err = a.store.pin(id.Public(address))
				} else {
					err = a.store.setPending(id.Public(address))
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := a.recoverInvalidGroupHistory(); err != nil {
				t.Fatal(err)
			}
			historyRecoveryReason(t, a, env.ID, reasonInvalid)
			if _, err := a.store.config(historyRecoveryCarrier + env.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("ineligible carrier marked: %v", err)
			}
		})
	}
}

func TestGroupHistoryRecoveryPendingProofRetainsHumanGuard(t *testing.T) {
	for _, who := range []string{"sender", "reader"} {
		t.Run(who, func(t *testing.T) {
			a, sender, root, item := historyRecoveryFixture(t)
			env := historyRecoveryEnvelope(t, sender, a, root, item)
			if err := a.store.holdAs(env, reasonInvalid); err != nil {
				t.Fatal(err)
			}
			if err := a.recoverInvalidGroupHistory(); err != nil {
				t.Fatal(err)
			}
			a.retryProof(tctx(t)) // no group context yet: ordinary retry remains pending
			historyRecoveryReason(t, a, env.ID, reasonProof)
			home := a.home
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			a, err = Open(home)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { a.Close() })
			historyRecoveryChangeRole(t, a, sender, who)
			a.retryProof(tctx(t))
			historyRecoveryReason(t, a, env.ID, reasonProof)
			if _, err := a.store.config(historyRecoveryCarrier + env.ID); err != nil {
				t.Fatalf("pending guard lost on restart: %v", err)
			}
			// Directly exercise the insertion boundary after the precheck, both
			// with a new row and an existing logical row. Normal ingress still
			// performs the full group/history proof checks before reaching here.
			for _, duplicate := range []bool{false, true} {
				if duplicate {
					if _, err = a.store.addHistoryInbox(item.inner(root.ID()), 0, item.FromKey, sender.Address, "unmarked-control", false, nil); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = a.store.addHistoryInbox(item.inner(root.ID()), 0, item.FromKey, sender.Address, env.ID, true, nil); err == nil {
					t.Fatalf("lost %s authority admitted history (duplicate %v)", who, duplicate)
				}
				historyRecoveryReason(t, a, env.ID, reasonProof)
			}
			if err = a.recoverInvalidGroupHistory(); err != nil {
				t.Fatal(err)
			}
			historyRecoveryReason(t, a, env.ID, reasonProof)
		})
	}
}

func TestGroupHistoryRecoveryWaitsForOwnReaderAndRollsBackStorageFailure(t *testing.T) {
	a, sender, root, item := historyRecoveryFixture(t)
	env := historyRecoveryEnvelope(t, sender, a, root, item)
	if err := a.store.holdAs(env, reasonInvalid); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE persons SET state=?`, personPinned); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInvalidGroupHistory(); err != nil {
		t.Fatal(err)
	}
	historyRecoveryReason(t, a, env.ID, reasonInvalid)
	if _, err := a.store.config(historyRecoveryScan); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("consumed scan before own reader ready: %v", err)
	}
	if _, err := a.store.db.Exec(`UPDATE persons SET state=?`, personSelf); err != nil {
		t.Fatal(err)
	}
	// A storage failure after staging a carrier must roll back its reason and
	// guard together with the completion marker, allowing the next normal retry.
	if _, err := a.store.db.Exec(`CREATE TRIGGER fail_history_scan BEFORE INSERT ON config WHEN NEW.k='group-history-invalid-recovery-v1' BEGIN SELECT RAISE(ABORT, 'fixture storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInvalidGroupHistory(); err == nil {
		t.Fatal("storage failure swallowed")
	}
	historyRecoveryReason(t, a, env.ID, reasonInvalid)
	if _, err := a.store.config(historyRecoveryCarrier + env.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partially committed recovery guard: %v", err)
	}
	if _, err := a.store.db.Exec(`DROP TRIGGER fail_history_scan`); err != nil {
		t.Fatal(err)
	}
	a.retryProof(tctx(t))
	historyRecoveryReason(t, a, env.ID, reasonProof)
	if _, err := a.store.config(historyRecoveryScan); err != nil {
		t.Fatalf("scan did not finish once own reader became ready: %v", err)
	}
}

func TestGroupHistoryRecoveryPagesRemainAtomic(t *testing.T) {
	a, sender, root, item := historyRecoveryFixture(t)
	const count = proofPage + 7
	for i := range count {
		env := craft(t, sender, a, envelope.Inner{ID: fmt.Sprintf("%032x", i+1), Conv: root.ID(), Root: json.RawMessage(mustJSON(root)), LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubHistory, Replica: true, Body: mustJSON(item)})
		if err := a.store.holdAs(env, reasonInvalid); err != nil {
			t.Fatal(err)
		}
	}
	conflict := historyRecoveryEnvelope(t, sender, a, root, item)
	if err := a.store.holdAs(conflict, reasonConflict); err != nil {
		t.Fatal(err)
	}
	// Refuse completion after more than one page has been staged. No page
	// may escape the transaction or consume the one-time scan on failure.
	if _, err := a.store.db.Exec(`CREATE TRIGGER fail_paged_history_scan BEFORE INSERT ON config WHEN NEW.k='group-history-invalid-recovery-v1' BEGIN SELECT RAISE(ABORT, 'fixture paged storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInvalidGroupHistory(); err == nil {
		t.Fatal("paged storage failure swallowed")
	}
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE reason=?`, reasonInvalid).Scan(&n); err != nil || n != count {
		t.Fatalf("partial page committed: %d %v", n, err)
	}
	if err := a.store.db.QueryRow(`SELECT count(*) FROM config WHERE k LIKE ?`, historyRecoveryCarrier+"%").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial page guards committed: %d %v", n, err)
	}
	if _, err := a.store.config(historyRecoveryScan); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("paged scan marked complete on failure: %v", err)
	}
	if _, err := a.store.db.Exec(`DROP TRIGGER fail_paged_history_scan`); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverInvalidGroupHistory(); err != nil {
		t.Fatal(err)
	}
	for i := range count {
		historyRecoveryReason(t, a, fmt.Sprintf("%032x", i+1), reasonProof)
	}
	historyRecoveryReason(t, a, conflict.ID, reasonConflict)
	if _, err := a.store.config(historyRecoveryCarrier + conflict.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("conflict marked across pages: %v", err)
	}
	if err := a.store.db.QueryRow(`SELECT count(*) FROM config WHERE k LIKE ?`, historyRecoveryCarrier+"%").Scan(&n); err != nil || n != count {
		t.Fatalf("later page guards missing: %d %v", n, err)
	}
	if _, err := a.store.config(historyRecoveryScan); err != nil {
		t.Fatalf("paged scan did not complete: %v", err)
	}
}
