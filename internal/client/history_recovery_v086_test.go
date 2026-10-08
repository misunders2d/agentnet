package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
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
			if _, err = a.historyPageFor(phone.Self(), historyPos{}); err != nil {
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
			if allReadersLeave && revisionCopies != 0 || !allReadersLeave && revisionCopies != 1 {
				t.Fatalf("original control recovery count %d", revisionCopies)
			}
			if !allReadersLeave {
				item := revision
				var originalRaw, recipient string
				if err = a.store.db.QueryRow(`SELECT envelope,recipient FROM outbox WHERE id=?`, item.ID).Scan(&originalRaw, &recipient); err != nil {
					t.Fatal(err)
				}
				var original envelope.Envelope
				if err = json.Unmarshal([]byte(originalRaw), &original); err != nil {
					t.Fatal(err)
				}
				if recipient != carol.Address || original.TS != item.TS || original.VerifySig(a.Self().SignKey) != nil {
					t.Fatal("fallback did not retain exact still-authorized original signed copy")
				}
				item.TS++
				current, err := a.GroupContext(packet.State.Conv)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = a.groupControlSourceAdmission(a.store.db, current, item, nil); err == nil || err.Error() != "group: historical control differs from original outbox" {
					t.Fatalf("tampered alternate source admitted: %v", err)
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
			for conv, expected := range map[string]string{packet.State.Conv: "out:revised", dm: "out:another chat"} {
				eventually(t, "linked history admitted after restart", func() bool { return strings.Join(convBodies(t, phone, conv), "|") == expected })
			}
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
