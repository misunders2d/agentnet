package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func groupHistoryLinkedFixture(t *testing.T) (*world, *Agent, *Agent, GroupContext) {
	t.Helper()
	w, _, packet, stops := groupTurnsFixture(t)
	phone, await, _ := linkPhone(t, w.alice, "history-late")
	request := pendingLink(t, w.alice)
	stops[w.alice]()
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	return w, w.alice, phone, packet
}

func groupHistoryCarrierCount(t *testing.T, a *Agent, to, conv string) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND conv=? AND sub IN ('group-proof','group-context')`, to, conv).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGroupHistoryLinkEmptyAndDoneRecovery(t *testing.T) {
	_, a, phone, packet := groupHistoryLinkedFixture(t)
	if n := groupHistoryCarrierCount(t, a, phone.Address, packet.State.Conv); n != 0 {
		t.Fatalf("unexpected pre-link delivery %d", n)
	}
	a.historyStep(tctx(t))
	var state, pos string
	if err := a.store.db.QueryRow(`SELECT state,pos FROM history_jobs WHERE device=?`, phone.Address).Scan(&state, &pos); err != nil || state != "done" {
		t.Fatalf("job %s %v", state, err)
	}
	n := groupHistoryCarrierCount(t, a, phone.Address, packet.State.Conv)
	if n != 2 {
		t.Fatalf("empty group's original proof+context absent: %d", n)
	}
	rows, err := a.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND conv=? AND sub IN ('group-proof','group-context') ORDER BY rowid`, phone.Address, packet.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	var envs []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	rows.Close()
	for _, env := range envs {
		groupGovernanceDeliver(t, a, phone, env)
		inner, err := envelope.Open(env, phone.id, phone.Address, a.Self())
		if err != nil {
			t.Fatal(err)
		}
		if inner.Sub == envelope.SubGroupProof {
			// Inspect original signed records after actual recipient-authorized blob fetch.
			raw, err := phone.groupCarrierBytes(tctx(t), inner.Attachments[0])
			if err != nil {
				t.Fatal(err)
			}
			var page protocol.GroupJournalPage
			if err = json.Unmarshal(raw, &page); err != nil {
				t.Fatal(err)
			}
			for _, record := range page.Records {
				var original []byte
				if err = a.store.db.QueryRow(`SELECT record FROM group_proof_records WHERE conv=? AND seq=?`, packet.State.Conv, record.Seq).Scan(&original); err != nil {
					t.Fatal(err)
				}
				exact, _ := json.Marshal(record)
				if !bytes.Equal(original, exact) {
					t.Fatal("original proof bytes changed")
				}
			}
		}
	}
	phone.retryProof(tctx(t))
	got, err := phone.GroupContext(packet.State.Conv)
	if err != nil || got.State.Hash() != packet.State.Hash() {
		t.Fatalf("linked empty context %v", err)
	}
	if len(groupTurns(t, phone, packet.State.Conv)) != 0 {
		t.Fatal("quiet carriers became ordinary messages")
	}
	reopened, err := Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.historyStep(tctx(t))
	if groupHistoryCarrierCount(t, reopened, phone.Address, packet.State.Conv) != n {
		t.Fatal("restart duplicated completed batch")
	}
	// Existing legacy done jobs which never enqueued any carrier reconcile without re-snapshotting.
	if _, err = reopened.store.db.Exec(`DELETE FROM outbox WHERE recipient=? AND conv=? AND sub IN ('group-proof','group-context')`, phone.Address, packet.State.Conv); err != nil {
		t.Fatal(err)
	}
	reopened.historyStep(tctx(t))
	if groupHistoryCarrierCount(t, reopened, phone.Address, packet.State.Conv) != n {
		t.Fatal("done snapshot failed to reconcile missing context")
	}
	var nextState, nextPos string
	reopened.store.db.QueryRow(`SELECT state,pos FROM history_jobs WHERE device=?`, phone.Address).Scan(&nextState, &nextPos)
	if nextState != state || nextPos != pos {
		t.Fatal("context reconciliation changed done history cursor")
	}
	// Terminal failure/expiration is not a completion marker; live batches are deduplicated.
	if _, err = reopened.store.db.Exec(`UPDATE outbox SET state='expired' WHERE recipient=? AND conv=? AND sub='group-context'`, phone.Address, packet.State.Conv); err != nil {
		t.Fatal(err)
	}
	reopened.historyStep(tctx(t))
	want := 2 * n
	if groupHistoryCarrierCount(t, reopened, phone.Address, packet.State.Conv) != want {
		t.Fatal("expired batch suppressed recovery")
	}
	reopened.historyStep(tctx(t))
	if groupHistoryCarrierCount(t, reopened, phone.Address, packet.State.Conv) != want {
		t.Fatal("recovery duplicated usable batch")
	}
	var history, jobs int
	reopened.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, phone.Address).Scan(&history)
	phone.store.db.QueryRow(`SELECT count(*) FROM jobs`).Scan(&jobs)
	if history != 0 || jobs != 0 {
		t.Fatalf("empty group leaked history/jobs %d/%d", history, jobs)
	}
}

func TestGroupHistoryLinkAtomicRollback(t *testing.T) {
	_, a, phone, p := groupHistoryLinkedFixture(t)
	var before string
	var uploadsBefore, attachmentsBefore int
	a.store.db.QueryRow(`SELECT count(*) FROM uploads`).Scan(&uploadsBefore)
	a.store.db.QueryRow(`SELECT count(*) FROM sent_attachments`).Scan(&attachmentsBefore)
	spoolBefore, err := os.ReadDir(filepath.Dir(a.spoolPath("")))
	if err != nil {
		t.Fatal(err)
	}
	a.store.db.QueryRow(`SELECT pos FROM history_jobs WHERE device=?`, phone.Address).Scan(&before)
	if _, err := a.store.db.Exec(`CREATE TRIGGER fail_link_context BEFORE INSERT ON outbox WHEN NEW.recipient='` + phone.Address + `' AND NEW.sub='group-context' BEGIN SELECT RAISE(ABORT,'context fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.historyPageFor(phone.Self(), historyPos{}); err == nil {
		t.Fatal("injected context commit failure missing")
	}
	if groupHistoryCarrierCount(t, a, phone.Address, p.State.Conv) != 0 {
		t.Fatal("partial proof survived failed checkpoint")
	}
	var after, state string
	a.store.db.QueryRow(`SELECT pos,state FROM history_jobs WHERE device=?`, phone.Address).Scan(&after, &state)
	if before != after || state != "running" {
		t.Fatal("failed batch advanced checkpoint")
	}
	var uploadsAfter, attachmentsAfter int
	a.store.db.QueryRow(`SELECT count(*) FROM uploads`).Scan(&uploadsAfter)
	a.store.db.QueryRow(`SELECT count(*) FROM sent_attachments`).Scan(&attachmentsAfter)
	spoolAfter, err := os.ReadDir(filepath.Dir(a.spoolPath("")))
	if err != nil {
		t.Fatal(err)
	}
	if uploadsBefore != uploadsAfter || attachmentsBefore != attachmentsAfter || len(spoolBefore) != len(spoolAfter) {
		t.Fatal("rollback leaked upload, attachment or spool rows")
	}

	if _, err := a.store.db.Exec(`DROP TRIGGER fail_link_context`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	if groupHistoryCarrierCount(t, a, phone.Address, p.State.Conv) != 2 {
		t.Fatal("retry did not commit one complete batch")
	}
}

func TestGroupHistoryLinkAuthorityChangesAndVisitor(t *testing.T) {
	w, a, phone, p := groupHistoryLinkedFixture(t)
	batches, err := a.prepareGroupHistoryCarriers(tctx(t), phone.Self())
	if err != nil || len(batches) != 1 {
		t.Fatalf("prepare %d %v", len(batches), err)
	}
	defer a.releaseGroupCopies(batches[0].copies)
	// A same-address unpinned key cannot receive membership context.
	wrong := phone.Self()
	wrong.SignKey = append([]byte(nil), a.Self().SignKey...)
	if _, err = a.prepareGroupHistoryCarriers(tctx(t), wrong); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("changed exact recipient key: %v", err)
	}
	if _, err = a.store.db.Exec(`INSERT OR REPLACE INTO group_known_heads(conv,bootstrap,seq,hash) VALUES(?,?,?,?)`, p.State.Conv, p.Root.Creator.Fingerprint, p.State.Seq+1, p.State.Hash()); err != nil {
		t.Fatal(err)
	}
	if err = a.checkGroupHistoryBatch(a.store.db, phone.Self(), batches[0]); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("new head installed stale batch: %v", err)
	}
	if _, err = a.store.db.Exec(`DELETE FROM group_known_heads WHERE conv=?`, p.State.Conv); err != nil {
		t.Fatal(err)
	}
	own, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	member, _ := p.State.Member(own.info.Person)
	if _, err = a.store.db.Exec(`INSERT INTO group_pending_withdrawals(conv,person,admission,record) VALUES(?,?,?,?)`, p.State.Conv, member.Person, member.Admission.Hash(), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err = a.checkGroupHistoryBatch(a.store.db, phone.Self(), batches[0]); err == nil {
		t.Fatal("pending own withdrawal installed batch")
	}
	if _, err = a.prepareGroupHistoryCarriers(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if groupHistoryCarrierCount(t, a, phone.Address, p.State.Conv) != 0 {
		t.Fatal("authority refusal enqueued carriers")
	}
	// A visitor may hold this context, but its newly linked sibling receives none.
	visitor := proofReader(t, w, "visitor-sibling")
	stopVisitor := runAgent(t, visitor)
	publishGroupFixtureCaps(t, visitor, true)
	stub := installAgentStub(t)
	named, err := visitor.CreateLocalAgent("Visitor", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = visitor.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	participation, err := w.bob.InviteNamedAgent(tctx(t), p.State.Conv, visitor.Address, named.ID, nil, nil, "no ambient room")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "verified visitor invitation", func() bool {
		v, e := visitor.Participation(participation.PID)
		return e == nil && v.State == PartInvited
	})
	sibling, await, _ := linkPhone(t, visitor, "visitor-phone")
	request := pendingLink(t, visitor)
	stopVisitor()
	if err = visitor.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	visitor.historyStep(tctx(t))
	if groupHistoryCarrierCount(t, visitor, sibling.Address, p.State.Conv) != 0 {
		t.Fatal("visitor sibling gained room authority")
	}
	var histories int
	visitor.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND conv=? AND sub='history'`, sibling.Address, p.State.Conv).Scan(&histories)
	if histories != 0 {
		t.Fatal("visitor sibling gained PID history")
	}

}
