package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupHistoryLinkOutgoingControlAfterReaderLeaves(t *testing.T) {
	for _, allReadersLeave := range []bool{false, true} {
		name := "current-alternative"
		if allReadersLeave {
			name = "all-original-readers-left"
		}
		t.Run(name, func(t *testing.T) {
			w, carol, packet, stops := groupTurnsFixture(t)
			a := w.alice
			dm := newDM(t, a, w.bob)
			sent, err := a.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "original"})
			if err != nil {
				t.Fatal(err)
			}
			ref, err := a.RefOf(packet.State.Conv, sent.ID, "out")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.Revise(tctx(t), ref, "revised"); err != nil {
				t.Fatal(err)
			}
			deleted, err := a.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "must remain deleted"})
			if err != nil {
				t.Fatal(err)
			}
			deletedRef, err := a.RefOf(packet.State.Conv, deleted.ID, "out")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.Retract(tctx(t), deletedRef, "removed"); err != nil {
				t.Fatal(err)
			}
			readers := []*Agent{w.bob}
			if allReadersLeave {
				readers = append(readers, carol)
			}
			for _, reader := range readers {
				withdrawal, err := reader.SignGroupWithdrawal(tctx(t), packet.State.Conv)
				if err != nil {
					t.Fatal(err)
				}
				if err = a.AcceptGroupWithdrawal(tctx(t), withdrawal); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = a.SendConv(tctx(t), dm, ConvOutgoing{Body: "another chat"}); err != nil {
				t.Fatal(err)
			}
			phone, await, _ := linkPhone(t, a, "after-reader-departure")
			request := pendingLink(t, a)
			stops[a]()
			if err = a.DecideLink(tctx(t), request.ID, true); err != nil {
				t.Fatal(err)
			}
			if result := <-await; result.err != nil {
				t.Fatal(result.err)
			}
			// Current own-human snapshots use the catch-up ledger. Starting one
			// through the legacy pager would intentionally trigger a one-time
			// accepted-row migration at the first upgraded restart.
			if _, err = a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
				t.Fatalf("departed original reader blocked linked history: %v", err)
			}
			var revisionCopies, deletedCopies int
			rows, err := a.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='history'`, phone.Address)
			if err != nil {
				t.Fatal(err)
			}
			var revision HistoryItem
			for rows.Next() {
				var raw string
				var carrier envelope.Envelope
				var item HistoryItem
				if err = rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal([]byte(raw), &carrier); err != nil {
					t.Fatal(err)
				}
				inner, err := envelope.Open(carrier, phone.id, phone.Address, a.Self())
				if err != nil {
					t.Fatal(err)
				}
				if inner.Conv != packet.State.Conv {
					continue
				}
				if err = json.Unmarshal([]byte(inner.Body), &item); err != nil {
					t.Fatal(err)
				}
				if item.Sub == "revision" {
					revisionCopies++
					revision = item
				}
				if item.Body == "must remain deleted" {
					deletedCopies++
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
			if deletedCopies != 0 {
				t.Fatal("deleted original exported")
			}
			if revisionCopies != 1 {
				t.Fatalf("original control recovery count %d", revisionCopies)
			}
			{
				item := revision
				var originalRaw, recipient string
				if err = a.store.db.QueryRow(`SELECT envelope,recipient FROM outbox WHERE id=?`, item.ID).Scan(&originalRaw, &recipient); err != nil {
					t.Fatal(err)
				}
				var original envelope.Envelope
				if err = json.Unmarshal([]byte(originalRaw), &original); err != nil {
					t.Fatal(err)
				}
				if !allReadersLeave && recipient != carol.Address || original.TS != item.TS || original.VerifySig(a.Self().SignKey) != nil {
					t.Fatal("fallback did not retain the preferred exact original signed copy")
				}
				item.TS++
				current, err := a.GroupContext(packet.State.Conv)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = a.groupControlSourceAdmission(a.store.db, current, item, nil); err == nil || err.Error() != "group: historical control differs from original outbox" {
					t.Fatalf("tampered alternate source admitted: %v", err)
				}
				for _, change := range []struct{ column, value string }{
					{"recipient_fp", a.Self().Fingerprint()},
					{"group_admission", strings.Repeat("0", 64)},
				} {
					tx, err := a.store.db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					result, err := tx.Exec(`UPDATE outbox SET `+change.column+`=? WHERE id=?`, change.value, revision.ID)
					if err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
					if n, err := result.RowsAffected(); err != nil || n != 1 {
						tx.Rollback()
						t.Fatalf("negative fixture changed %d rows: %v", n, err)
					}
					_, checkErr := a.groupControlSourceAdmission(tx, current, revision, nil)
					if err = tx.Rollback(); err != nil {
						t.Fatal(err)
					}
					if !errors.Is(checkErr, errGroupControlHistoryEpoch) {
						t.Fatalf("changed original %s admitted: %v", change.column, checkErr)
					}
				}
				foreign := revision
				foreign.From, foreign.FromKey = w.bob.Address, w.bob.Self().Fingerprint()
				if _, err = a.groupControlSourceAdmission(a.store.db, current, foreign, nil); err == nil {
					t.Fatal("departed foreign control gained own-original fallback")
				}
			}
			reopened, err := Open(a.home)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reopened.Close() }) // later runAgent cleanup stops the daemon before its store closes
			var before, after int
			if err = reopened.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, phone.Address).Scan(&before); err != nil {
				t.Fatal(err)
			}
			reopened.historyStep(tctx(t))
			if err = reopened.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, phone.Address).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("restart duplicated completed snapshot")
			}
			runAgent(t, phone)
			runAgent(t, reopened)
			eventually(t, "linked history retains original plus visible revision after restart", func() bool {
				rows := groupTurns(t, phone, packet.State.Conv)
				return len(rows) == 1 && rows[0].Dir == "out" && rows[0].Body == "original" && rows[0].Edited && rows[0].Shown(rows[0].Body) == "revised"
			})
			eventually(t, "other chat admitted after restart", func() bool { return strings.Join(convBodies(t, phone, dm), "|") == "out:another chat" })
			if err = groupTurnCheck(a.store.db, packet, w.bob.Address, w.bob.Self().Fingerprint()); err == nil {
				t.Fatal("history recovery restored departed reader admission")
			}
			var jobs int
			if err = phone.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE kind IN ('question','task') OR coalesce(executor,'')<>''`).Scan(&jobs); err != nil || jobs != 0 {
				t.Fatalf("history started work: %d %v", jobs, err)
			}
		})
	}
}

func TestGroupHistoryOwnControlAdmissionRejoinRefuses(t *testing.T) {
	w, carol, packet, stops := groupTurnsFixture(t)
	a := w.bob
	sent, err := a.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "own original"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := a.RefOf(packet.State.Conv, sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := a.Revise(tctx(t), ref, "own revision")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := a.historySourceRows(a.store.db, "id=?", "id", 1, revision.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("original control source: %d %v", len(rows), err)
	}
	item, err := a.historySourceItem(a.store.db, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.groupControlSourceAdmission(a.store.db, packet, item, nil); err != nil {
		t.Fatalf("original admitted control: %v", err)
	}
	own, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		t.Fatalf("own person: %v", err)
	}
	removed, err := w.alice.RemoveGroupMember(tctx(t), packet.State.Conv, own.info.Person)
	if err != nil {
		t.Fatal(err)
	}
	current := groupInteractionRejoin(t, w.alice, a, removed)
	groupGovernanceAwait(t, current, a, carol)
	stops[a]()
	if _, err = a.groupControlSourceAdmission(a.store.db, current, item, nil); !errors.Is(err, errGroupControlHistoryEpoch) {
		t.Fatalf("own same-key rejoin revived old control admission: %v", err)
	}
	// An unbound admission cannot be used even with a matching invented fence.
	admission, err := groupMemberAdmission(a.store.db, current, a.Address, a.Self().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	current.State.Title = "unsigned changed context"
	if err = groupControlHistoricalRecipient(a.store.db, current, admission, a.Address, a.Self().Fingerprint(), groupControlAdmissionFence(admission, protocol.GroupAdmission{})); !errors.Is(err, errGroupControlHistoryEpoch) {
		t.Fatalf("unsigned historical context accepted: %v", err)
	}
}
