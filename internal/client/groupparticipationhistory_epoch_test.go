package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An own laptop has already accepted this inert replica. Re-admitting the
// assistant's human host must not make that older turn block every later
// history page to a newly linked device, or revive live authority for its PID.
func TestGroupParticipationHistoryAfterHostReadmission(t *testing.T) {
	groupParticipationHistoryAfterReadmission(t, true)
}
func TestGroupHistoryWitnessMissingOriginalCiphertext(t *testing.T) {
	groupParticipationHistoryAfterReadmission(t, false)
}
func TestGroupHistoryWitnessRetiredTaskDevice(t *testing.T) {
	groupParticipationHistoryAfterReadmission(t, true, true)
}
func groupParticipationHistoryAfterReadmission(t *testing.T, sharedOriginal bool, retiredTask ...bool) {
	w, approver, source, packet := groupHistoryLinkedFixture(t)
	stopApprover := runAgent(t, approver)
	stopSource := runAgent(t, source)
	publishGroupFixtureCaps(t, source, true)
	groupGovernanceAwait(t, packet, source)
	conv := packet.State.Conv
	var err error
	if sharedOriginal {
		packet, err = approver.RenameGroup(tctx(t), conv, "Original state shared with source")
		if err != nil {
			t.Fatal(err)
		}
		groupGovernanceAwait(t, packet, source, w.bob)
	}
	var retired *Agent
	var part ParticipationInfo
	if len(retiredTask) > 0 && retiredTask[0] {
		var awaited chan linkOutcome
		retired, awaited, _ = linkPhone(t, w.bob, "retired-task-phone")
		request := pendingLink(t, w.bob)
		if err = w.bob.DecideLink(tctx(t), request.ID, true); err != nil {
			t.Fatal(err)
		}
		if result := <-awaited; result.err != nil {
			t.Fatal(result.err)
		}
		part, err = w.bob.InviteAgent(tctx(t), conv, w.bob.Address, nil, []string{retired.Self().Fingerprint()}, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "retired-device fixture invitation", func() bool { p := stateAt(t, w.bob, part.PID); return p.State == PartInvited || p.Claimable() })
		if stateAt(t, w.bob, part.PID).State == PartInvited {
			if _, err = w.bob.AcceptParticipation(tctx(t), part.PID); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		part = p6Member(t, w.bob, w.bob, conv)
	}
	eventually(t, "own laptop sees original accepted member host", func() bool {
		return stateAt(t, source, part.PID).Claimable() && stateAt(t, approver, part.PID).Claimable()
	})
	ask, err := approver.AskAgent(tctx(t), part.PID, envelope.KindQuestion, "older accepted own replica")
	if err != nil {
		t.Fatal(err)
	}
	var originalID string
	for _, copy := range ask.Copies {
		if copy.To == source.Address {
			originalID = copy.ID
		}
	}
	if originalID == "" {
		t.Fatal("own replica was not addressed to source")
	}
	eventually(t, "source stores exact original replica", func() bool {
		return inboxCount(t, source, `id=? AND replica=1`, originalID) == 1
	})
	filePath := filepath.Join(t.TempDir(), "history.txt")
	if err = os.WriteFile(filePath, []byte("EXACT_OLD_HISTORY_BYTES"), 0600); err != nil {
		t.Fatal(err)
	}
	answer, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindAnswer, PID: part.PID, AgentID: part.AgentID, ReplyTo: ask.LID, Body: "old exact answer", Files: []OutgoingFile{{Path: filePath, Name: "history.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	var answerID string
	for _, copy := range answer.Copies {
		if copy.To == source.Address {
			answerID = copy.ID
		}
	}
	if answerID == "" {
		t.Fatal("answer did not reach own source")
	}
	eventually(t, "source keeps exact original answer", func() bool { return inboxCount(t, source, `id=?`, answerID) == 1 })
	recipient, err := source.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	status, err := envelope.Seal(envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), LID: protocol.NewID(), From: w.bob.Address, To: source.Address, TS: time.Now().Unix(), Conv: conv, Kind: envelope.KindMessage, Sub: envelope.SubStatus, Body: `{"state":"running","n":1,"at":1700000000,"detail":"fixture"}`, Ref: &envelope.Ref{ID: ask.LID, Fingerprint: approver.Self().Fingerprint()}}, w.bob.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err = source.accept(tctx(t), status); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, source, `id=? AND group_admission IS NOT NULL`, status.ID) != 1 {
		t.Fatal("exact original status was not admitted")
	}
	if retired != nil {
		if err = w.bob.RemoveDevice(tctx(t), retired.Address); err != nil {
			t.Fatal(err)
		}
		person, _, e := w.bob.Person()
		if e != nil {
			t.Fatal(e)
		}
		if _, err = source.refreshPerson(tctx(t), person.Person, false); err != nil {
			t.Fatal(err)
		}
		if err = retired.hub.do(tctx(t), "GET", "/v1/agents", nil, nil); !errors.Is(err, ErrRevoked) {
			t.Fatalf("retired task key regained live access: %v", err)
		}
		live, e := source.dmMembers(conv)
		if e != nil {
			t.Fatal(e)
		}
		if live.memberKey(retired.Self().Fingerprint()) || live.device(retired.Address, retired.Self().Fingerprint()) {
			t.Fatal("retired task device remains a current member key")
		}
	}
	var original protocol.ParticipationEvent
	events, err := source.store.participationEvents(conv, part.PID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == protocol.EventInvite {
			original = ev
		}
	}
	if original.Group == nil || original.Group.HostRole != "member" {
		t.Fatal("original invite lacks exact member epoch")
	}
	bob, ok, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil || !ok {
		t.Fatalf("host person: %v", err)
	}
	removed, err := approver.RemoveGroupMember(tctx(t), conv, bob.info.Person)
	if err != nil {
		t.Fatal(err)
	}
	current := groupInteractionRejoin(t, approver, w.bob, removed)
	groupGovernanceAwait(t, current, source, w.bob)
	m, err := source.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	if m.keyEpoch(w.bob.Self().Fingerprint()) == original.Group.HostAdmission {
		t.Fatal("fixture did not change the host admission")
	}
	if valid, err := m.verifyInviteEpoch(source.store.db, original); err != nil || valid {
		t.Fatalf("old invite must remain unusable live: %v %v", valid, err)
	}
	if stateAt(t, source, part.PID).Claimable() {
		t.Fatal("host re-admission revived live participation")
	}
	if _, err := groupProofRecord(source.store.db, conv, packet.Root.Creator.Fingerprint, original.Group.Seq); err != nil {
		t.Fatalf("original public proof is missing: %v", err)
	}
	if _, err = approver.SendConv(tctx(t), conv, ConvOutgoing{Body: "newer ordinary history"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "source has later healthy turn", func() bool {
		return inboxCount(t, source, `conv=? AND body=?`, conv, "newer ordinary history") == 1
	})
	stopSource()
	phone, awaited, _ := linkPhone(t, approver, "after-host-readmission")
	request := pendingLink(t, approver)
	stopApprover()
	if err = approver.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	self, ok, err := source.store.selfPerson(source.Address)
	if err != nil || !ok {
		t.Fatalf("source person: %v", err)
	}
	if _, err = source.refreshPerson(tctx(t), self.info.Person, false); err != nil {
		t.Fatal(err)
	}
	if err = source.reconcileHistory(); err != nil {
		t.Fatal(err)
	}
	_, err = source.historyPageFor(phone.Self(), historyPos{})
	if !sharedOriginal {
		if !errors.Is(err, ErrGroupContextPending) || !errors.Is(err, errGroupCiphertextUnavailable) {
			t.Fatalf("missing original ciphertext must remain deferred: %v", err)
		}
		if inboxCount(t, source, `id=?`, originalID) != 1 {
			t.Fatal("unavailable source item was discarded")
		}
		return
	}
	if err != nil {
		t.Fatalf("old accepted own replica blocked new-device history after host re-admission: %v", err)
	}
	var old, healthy int
	if err = source.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history' AND json_extract(body,'$.id')=?`, phone.Address, originalID).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if err = source.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history' AND json_extract(body,'$.body')=?`, phone.Address, "newer ordinary history").Scan(&healthy); err != nil {
		t.Fatal(err)
	}
	if old != 1 || healthy != 1 {
		t.Fatalf("snapshot must retain old inert and newer healthy history: old=%d healthy=%d", old, healthy)
	}
	var rawItem string
	if err = source.store.db.QueryRow(`SELECT body FROM outbox WHERE recipient=? AND sub='history' AND json_extract(body,'$.id')=?`, phone.Address, originalID).Scan(&rawItem); err != nil {
		t.Fatal(err)
	}
	var item HistoryItem
	if err = json.Unmarshal([]byte(rawItem), &item); err != nil || item.GroupHistory == nil {
		t.Fatalf("missing signed historical witness: %v", err)
	}
	runAgent(t, phone)
	runAgent(t, source)
	eventually(t, "new phone accepts old inert history", func() bool {
		return inboxCount(t, phone, `id=? AND replica=1 AND state='' AND group_history IS NOT NULL`, originalID) == 1
	})
	eventually(t, "new phone accepts bound old output and status", func() bool {
		return inboxCount(t, phone, `id IN (?,?) AND state='' AND group_history IS NOT NULL`, answerID, status.ID) == 2
	})
	if err = phone.RequestFile(tctx(t), answerID, 0); err != nil {
		t.Fatalf("historical witness file request: %v", err)
	}
	eventually(t, "old exact attachment offered", func() bool {
		files, e := phone.store.attachments(answerID)
		return e == nil && len(files) == 1 && !strings.HasPrefix(files[0].BlobID, historyBlob)
	})
	downloaded, err := phone.Download(tctx(t), answerID, t.TempDir(), false)
	if err != nil || len(downloaded) != 1 {
		t.Fatalf("historical attachment download: %v %v", downloaded, err)
	}
	data, err := os.ReadFile(downloaded[0])
	if err != nil || string(data) != "EXACT_OLD_HISTORY_BYTES" {
		t.Fatalf("historical attachment bytes: %q %v", data, err)
	}
	var liveEvents int
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=? AND pid=?`, conv, part.PID).Scan(&liveEvents); err != nil || liveEvents != 0 {
		t.Fatalf("historical witness installed live ledger events: %d %v", liveEvents, err)
	}
	if stateAt(t, phone, part.PID).Claimable() {
		t.Fatal("historical witness revived live PID")
	}
	var active string
	if err = phone.store.db.QueryRow(`SELECT state FROM inbox WHERE id=?`, originalID).Scan(&active); err != nil || active != "" {
		t.Fatalf("history became executable: %q %v", active, err)
	}
	kept, err := storedGroupHistory(phone.store.db, originalID)
	if err != nil || groupHistoryJSON(kept) != groupHistoryJSON(item.GroupHistory) {
		t.Fatalf("received exact witness not retained: %v", err)
	}
	for _, mode := range []string{"missing-witness", "wrong-state", "forged-state", "other-pid", "unbound-roster", "wrong-task-epoch", "unknown-own-admission", "foreign-forwarder"} {
		t.Run(mode, func(t *testing.T) {
			var bad HistoryItem
			if err := json.Unmarshal([]byte(rawItem), &bad); err != nil {
				t.Fatal(err)
			}
			forwarder := source.Self()
			switch mode {
			case "missing-witness":
				bad.GroupHistory = nil
			case "wrong-state":
				bad.GroupHistory.State = current.State
			case "forged-state":
				bad.GroupHistory.State.Title += " forged"
			case "other-pid":
				bad.PID = protocol.NewID()
			case "unbound-roster":
				bad.GroupHistory.Memberships[0].Author.Roster = strings.Repeat("f", 64)
			case "wrong-task-epoch":
				for i := range bad.GroupHistory.Memberships {
					ev := &bad.GroupHistory.Memberships[i]
					if ev.Type == protocol.EventInvite {
						ev.TaskKeys = []string{w.bob.Self().Fingerprint()}
						ev.Group.TaskAdmissions = []string{strings.Repeat("e", 64)}
						ev.Sign(w.bob.id.Sign)
					}
				}
			case "unknown-own-admission":
				bad.GroupAdmission = strings.Repeat("e", 64)
			case "foreign-forwarder":
				forwarder = w.bob.Self()
			}
			if _, err := phone.groupParticipationHistoryCheck(phone.store.db, packet.Root, forwarder, bad); err == nil {
				t.Fatal("invalid history witness accepted")
			}
		})
	}
	t.Run("pending-original-author", func(t *testing.T) {
		tx, err := phone.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		res, err := tx.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.bob.Address)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("pending author pin: %d %v", n, err)
		}
		if _, err = phone.groupParticipationHistoryCheck(tx, packet.Root, source.Self(), item); err == nil {
			t.Fatal("pending original author accepted")
		}
	})
	t.Run("pending-own-forwarder", func(t *testing.T) {
		tx, err := phone.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		res, err := tx.Exec(`UPDATE peers SET pending=public WHERE address=?`, source.Address)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("pending source pin: %d %v", n, err)
		}
		if _, err = phone.groupParticipationHistoryCheck(tx, packet.Root, source.Self(), item); !errors.Is(err, errHistoryRecoveryAuthority) {
			t.Fatalf("pending own forwarder: %v", err)
		}
	})
	t.Run("pending-own-reader", func(t *testing.T) {
		tx, err := source.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		res, err := tx.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("pending reader pin: %d %v", n, err)
		}
		if err = source.groupParticipationHistoryOutboundCheck(tx, current, phone.Address, phone.Self().Fingerprint(), item); !errors.Is(err, errHistoryRecoveryAuthority) {
			t.Fatalf("pending own reader: %v", err)
		}
	})
	rootRaw, err := json.Marshal(packet.Root)
	if err != nil {
		t.Fatal(err)
	}
	again, err := phone.historyCopy(source.Self(), conv, rootRaw, item)
	if err != nil {
		t.Fatalf("received witness cannot forward: %v", err)
	}
	var repeated HistoryItem
	if err = json.Unmarshal([]byte(again.in.Body), &repeated); err != nil || groupHistoryJSON(repeated.GroupHistory) != groupHistoryJSON(item.GroupHistory) {
		t.Fatal("forwarded witness changed")
	}
	if err = approver.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, err = source.refreshPerson(tctx(t), self.info.Person, false); err != nil {
		t.Fatal(err)
	}
	if err = source.groupParticipationHistoryOutboundCheck(source.store.db, current, phone.Address, phone.Self().Fingerprint(), item); err == nil {
		t.Fatal("removed own reader accepted")
	}

}

func TestGroupHistoryWitnessCapabilityIsExplicit(t *testing.T) {
	old := protocol.CapsRecord{Caps: []string{protocol.CapRoom}}
	if !old.Reads(protocol.CapConvClear) || old.Reads(protocol.CapOwnSyncV2) {
		t.Fatal("released room implication changed or grants new witness capability")
	}
	advertised := append(append([]string{}, ownCaps...), protocol.CapAgent)
	if len(advertised) > protocol.MaxAdvertisedCaps {
		t.Fatal("own capability advertisement exceeds existing limit")
	}
	found := false
	for _, c := range advertised {
		if c == protocol.CapOwnSyncV2 {
			found = true
		}
	}
	if !found {
		t.Fatal("new witness capability is not advertised explicitly")
	}
}
