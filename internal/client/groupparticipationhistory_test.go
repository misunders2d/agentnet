package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupParticipationHistoryLinkedRestartReorder(t *testing.T) {
	testGroupParticipationHistoryLinkedRestartReorder(t, false, false)
}

func TestGroupParticipationHistoryDismissedLinkedRestartReorder(t *testing.T) {
	testGroupParticipationHistoryLinkedRestartReorder(t, true, false)
}

func TestGroupParticipationHistoryInvalidUpgradeRecovery(t *testing.T) {
	testGroupParticipationHistoryLinkedRestartReorder(t, true, true)
}

func testGroupParticipationHistoryLinkedRestartReorder(t *testing.T, dismissed, recoverInvalid bool) {
	stub := installAgentStub(t)
	w, producer, packet, stops := groupTurnsFixture(t)
	host := proofReader(t, w, "history-visitor")
	runAgent(t, host)
	publishGroupFixtureCaps(t, host, true)
	fakeNotify(host)
	record, err := host.CreateLocalAgent("History reviewer", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "selected.txt")
	if err = os.WriteFile(path, []byte("PID_HISTORY_SELECTED_FILE"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := producer.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "selected original", Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, host.Address, record.ID, []string{original.LID}, nil, "selected history scope")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor invitation ready", func() bool { v, e := host.Participation(p.PID); return e == nil && v.State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "visitor accepted before link", func() bool { v, e := producer.Participation(p.PID); return e == nil && v.Claimable() })
	if err = host.Approve(producer.Address); err != nil {
		t.Fatal(err)
	}
	question, err := producer.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "before late link")
	if err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, producer, packet.State.Conv, question.ID)
	eventually(t, "native working status before link", func() bool {
		return inboxCount(t, producer, `conv=? AND sub=?`, packet.State.Conv, envelope.SubStatus) > 0
	})
	if stub.runs() != 1 || !strings.Contains(stub.last(), "PID_HISTORY_SELECTED_FILE") {
		t.Fatalf("original execution %d", stub.runs())
	}
	if dismissed {
		if _, err = w.alice.DismissParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
		eventually(t, "producer sees ended assistant", func() bool {
			return stateAt(t, producer, p.PID).State == PartDismissed
		})
		// The inviter also retains its exact selected excerpt after the end.
		ownPhone, ownAwait, _ := linkPhone(t, w.alice, "inviter-history")
		ownLink := pendingLink(t, w.alice)
		if err = w.alice.DecideLink(tctx(t), ownLink.ID, true); err != nil {
			t.Fatal(err)
		}
		if result := <-ownAwait; result.err != nil {
			t.Fatal(result.err)
		}
		if _, err = w.alice.historyPageFor(ownPhone.Self(), historyPos{}); err != nil {
			t.Fatalf("inviter history after end: %v", err)
		}

	}
	phone, await, _ := linkPhone(t, producer, "pid-phone")
	request := pendingLink(t, producer)
	stops[producer]()
	if err = producer.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	// Install only verified current own membership through existing encrypted carriers.
	copies, err := w.alice.groupDeliveryCopies(tctx(t), packet)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.addConvOutbox(copies, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range copies {
		if c.env.To == phone.Address {
			groupGovernanceDeliver(t, w.alice, phone, c.env)
		}
	}
	phone.retryProof(tctx(t))
	if _, err = phone.GroupContext(packet.State.Conv); err != nil {
		t.Fatal(err)
	}
	if _, err = producer.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	type history struct {
		env  envelope.Envelope
		item HistoryItem
	}
	rows, err := producer.store.db.Query(`SELECT envelope,body,required_cap FROM outbox WHERE recipient=? AND sub='history' ORDER BY rowid`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	var histories []history
	for rows.Next() {
		var raw, body []byte
		var cap string
		var h history
		if err = rows.Scan(&raw, &body, &cap); err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(raw, &h.env) != nil || json.Unmarshal(body, &h.item) != nil || cap != protocol.CapGroup {
			t.Fatal("group history lost grp1/metadata")
		}
		histories = append(histories, h)
	}
	rows.Close()
	for _, h := range histories {
		if h.item.Sub == envelope.SubStatus {
			var originalStamp string
			if err = producer.store.db.QueryRow(`SELECT group_admission FROM inbox WHERE id=?`, h.item.ID).Scan(&originalStamp); err != nil {
				t.Fatal(err)
			}
			if _, err = producer.store.db.Exec(`UPDATE inbox SET group_admission=NULL WHERE id=?`, h.item.ID); err != nil {
				t.Fatal(err)
			}
			_, e := producer.historyCopy(phone.Self(), packet.State.Conv, json.RawMessage(mustJSON(packet.Root)), h.item)
			if !errors.Is(e, errGroupParticipationHistoryEpoch) {
				t.Fatalf("unstamped old status gained authority: %v", e)
			}
			if _, err = producer.store.db.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, originalStamp, h.item.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(histories) < 6 {
		t.Fatalf("missing PID histories: %d", len(histories))
	}
	var output history
	for _, h := range histories {
		if h.item.ID == answer.ID {
			output = h
		}
	}
	if output.env.ID == "" {
		t.Fatal("visible output missing from snapshot")
	}
	if recoverInvalid {
		// Model v0.8.6 retaining these encrypted copies as invalid when
		// the accepted assistant ended. Keep that same store across the upgrade;
		// do not redeliver them through the fixed fresh-ingress path.
		for _, h := range histories {
			if h.item.Sub != envelope.SubEvent && groupParticipationHistoryItem(h.item) {
				if err = phone.store.holdAs(h.env, reasonInvalid); err != nil {
					t.Fatal(err)
				}
			} else if err = phone.accept(tctx(t), h.env); err != nil {
				t.Fatal(err)
			}
		}
		// Force the output to precede its held request, independently of the
		// random carrier IDs used to order equal receive timestamps.
		if _, err = phone.store.db.Exec(`UPDATE quarantine SET received_at=1 WHERE id=?`, output.env.ID); err != nil {
			t.Fatal(err)
		}
		if err = phone.store.deleteConfig(historyRecoveryScan); err != nil {
			t.Fatal(err)
		}
	} else {
		if err = phone.accept(tctx(t), output.env); err != nil {
			t.Fatal(err)
		}
		var reason string
		if err = phone.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, output.env.ID).Scan(&reason); err != nil || reason != reasonProof {
			t.Fatalf("output-before-proof %q %v", reason, err)
		}
	}
	home := phone.home
	phone.Close()
	phone, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	for _, h := range histories {
		if h.env.ID != output.env.ID && (!recoverInvalid || h.item.Sub == envelope.SubEvent || !groupParticipationHistoryItem(h.item)) {
			if err = phone.accept(tctx(t), h.env); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Replay the complete history batch before retrying held proof dependencies.
	phone.retryProof(tctx(t))
	if recoverInvalid {
		historyRecoveryReason(t, phone, output.env.ID, reasonProof)
		// The daemon follows convRetry scheduled by each newly admitted
		// dependency. Drain only that actual work, bounded by this batch.
		for pass := 0; pass < len(histories) && phone.convWork.take()&convRetry != 0; pass++ {
			phone.retryProof(tctx(t))
		}
	}
	view, err := phone.Participation(p.PID)
	if err != nil || !dismissed && !view.Claimable() || dismissed && view.State != PartDismissed || view.Host.Fingerprint != host.Self().Fingerprint() || view.AgentID != record.ID {
		t.Fatalf("linked PID view %+v %v", view, err)
	}
	var n int
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND pid=? AND kind='answer' AND claimed_fp=? AND agent_id=? AND reply_to=? AND id=? AND lid=?`, packet.State.Conv, p.PID, host.Self().Fingerprint(), record.ID, question.ID, output.item.ID, output.item.LID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("linked output attribution %d %v", n, err)
	}
	if recoverInvalid {
		if err = phone.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, output.env.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("recovered output carrier still held: %d %v", n, err)
		}
		if _, err = phone.store.config(historyRecoveryCarrier + output.env.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("recovered output guard not released: %v", err)
		}
	}
	if inboxCount(t, phone, `conv=? AND sub=?`, packet.State.Conv, envelope.SubStatus) == 0 {
		t.Fatal("linked native status missing")
	}
	for _, h := range histories {
		if h.item.Sub == envelope.SubExcerpt {
			var selected HistoryItem
			if json.Unmarshal([]byte(h.item.Body), &selected) != nil || selected.LID != original.LID || len(selected.Attachments) != 1 || selected.Attachments[0].Name != "selected.txt" {
				t.Fatal("selected excerpt provenance lost")
			}
		}
		if err = phone.accept(tctx(t), h.env); err != nil {
			t.Fatal(err)
		}
	}
	if inboxCount(t, phone, `conv=? AND state IN ('agent-waiting','running','done','task-waiting')`, packet.State.Conv) != 0 || stub.runs() != 1 {
		t.Fatal("linked histories executed/reran work")
	}
	// An own linked sender cannot invent the original host or its own admission.
	forged := output.item
	forged.ID, forged.LID = protocol.NewID(), protocol.NewID()
	forged.FromKey = producer.Self().Fingerprint()
	body, _ := json.Marshal(forged)
	env := craft(t, producer, phone, envelope.Inner{Conv: packet.State.Conv, Root: json.RawMessage(mustJSON(packet.Root)), LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubHistory, Replica: true, Body: string(body)})
	if err = phone.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, phone, `id=?`, forged.ID) != 0 {
		t.Fatal("forged original host accepted")
	}
	forged = output.item
	forged.ID, forged.LID = protocol.NewID(), protocol.NewID()
	forged.GroupAdmission = strings.Repeat("a", 64)
	body, _ = json.Marshal(forged)
	env = craft(t, producer, phone, envelope.Inner{Conv: packet.State.Conv, Root: json.RawMessage(mustJSON(packet.Root)), LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubHistory, Replica: true, Body: string(body)})
	if err = phone.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, phone, `id=?`, forged.ID) != 0 {
		t.Fatal("removed own epoch accepted")
	}
	// Existing own-live file recovery supplies exact returned bytes; the PID
	// excerpt retains its signed selection and manifest, never a new grant.
	var fileID string
	for _, h := range histories {
		if h.item.LID == original.LID && h.item.Sub == "" && h.item.PID == "" {
			fileID = h.item.ID
		}
	}
	if fileID == "" {
		t.Fatal("selected original history missing")
	}
	oldSession := producer.session
	runAgent(t, producer)
	runAgent(t, phone)
	eventually(t, "fresh producer session before fixture caps", func() bool {
		label, name, _ := protocol.SplitAddress(producer.Address)
		var profile protocol.Profile
		if producer.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile) != nil {
			return false
		}
		for _, session := range profile.Sessions {
			if session != oldSession && profile.Supports(producer.Address, producer.Self().SignKey, protocol.CapAgentIdentity) {
				return true
			}
		}
		return false
	})
	publishGroupFixtureCaps(t, producer, true)
	publishGroupFixtureCaps(t, phone, true)
	if err = phone.requireParticipationCaps(tctx(t), producer.Self(), protocol.CapGroup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			for _, a := range []*Agent{producer, phone} {
				rows, e := a.store.db.Query(`SELECT sub,state,coalesce(error,''),coalesce(required_cap,'') FROM outbox WHERE conv=? AND sub='file'`, packet.State.Conv)
				if e == nil {
					for rows.Next() {
						var sub, state, detail, cap string
						if rows.Scan(&sub, &state, &detail, &cap) == nil {
							t.Logf("file diagnostics %s %s %s %s %s", a.Address, sub, state, cap, detail)
						}
					}
					rows.Close()
				}
			}
		}
	})
	if err = phone.RequestFile(tctx(t), fileID, 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "linked selected file returned", func() bool {
		f, e := phone.store.attachments(fileID)
		return e == nil && len(f) == 1 && f[0].BlobID != "" && !strings.HasPrefix(f[0].BlobID, historyBlob)
	})
	paths, e := phone.Download(tctx(t), fileID, t.TempDir(), false)
	if e != nil || len(paths) != 1 {
		t.Fatalf("selected file download %v %v", paths, e)
	}
	data, e := os.ReadFile(paths[0])
	if e != nil || string(data) != "PID_HISTORY_SELECTED_FILE" {
		t.Fatalf("returned selected bytes %q %v", data, e)
	}
	self, _, _ := producer.store.selfPerson(producer.Address)
	removed, e := w.alice.RemoveGroupMember(tctx(t), packet.State.Conv, self.roster.Person)
	if e != nil {
		t.Fatal(e)
	}
	rejoined := groupInteractionRejoin(t, w.alice, producer, removed)
	groupGovernanceAwait(t, rejoined, producer, phone)
	if _, err = producer.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, stateQueued, output.env.ID); err != nil {
		t.Fatal(err)
	}
	if handled, allowed, e := producer.mayDeliverGroupTurn(output.env); e != nil || !handled || allowed {
		t.Fatalf("same-key rejoin regained queued PID history %v %v %v", handled, allowed, e)
	}
	stale := output.item
	stale.ID, stale.LID = protocol.NewID(), protocol.NewID()
	body, _ = json.Marshal(stale)
	env = craft(t, producer, phone, envelope.Inner{Conv: packet.State.Conv, Root: json.RawMessage(mustJSON(packet.Root)), LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubHistory, Replica: true, Body: string(body)})
	if err = phone.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, phone, `id=?`, stale.ID) != 0 {
		t.Fatal("raw previous-admission history acquired new epoch")
	}
	// A visitor's new own device cannot become a room-history recipient.
	visitorPhone, visitorAwait, _ := linkPhone(t, host, "visitor-phone")
	link := pendingLink(t, host)
	if err = host.DecideLink(tctx(t), link.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-visitorAwait; result.err != nil {
		t.Fatal(result.err)
	}
	if _, err = host.historyPageFor(visitorPhone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	var exports int
	if err = host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND conv=? AND sub='history'`, visitorPhone.Address, packet.State.Conv).Scan(&exports); err != nil || exports != 0 {
		t.Fatalf("visitor sibling exported %d %v", exports, err)
	}
}
