package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
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
func TestGroupHistoryWitnessGuestDerivedParent(t *testing.T) {
	groupParticipationHistoryAfterReadmission(t, true, false, true)
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
	derived := len(retiredTask) > 1 && retiredTask[1]
	executor := w.bob
	topic := ""
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
	} else if derived {
		topic = protocol.NewID()
		executor = proofReader(t, w, "historical-guest")
		runAgent(t, executor)
		for _, a := range []*Agent{approver, source, w.bob, executor} {
			roomReader(t, a)
		}
		inviteCtx, reportInvite := traceFixtureHTTP(t, tctx(t))
		parent, e := w.bob.InviteHumanInScope(inviteCtx, conv, executor.Address, nil, "", &topic)
		reportInvite(e)
		if e != nil {
			t.Fatal(e)
		}
		eventually(t, "historical human parent", func() bool { return stateAt(t, executor, parent.PID).State == PartInvited })
		if _, e = executor.AcceptParticipation(tctx(t), parent.PID); e != nil {
			t.Fatal(e)
		}
		part, err = executor.InviteAgentInScope(tctx(t), conv, executor.Address, "", nil, nil, "", &topic)
		if err != nil {
			t.Fatal(err)
		}
		if stateAt(t, executor, part.PID).State == PartInvited {
			if _, err = executor.AcceptParticipation(tctx(t), part.PID); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		part = p6Member(t, w.bob, w.bob, conv)
	}
	eventually(t, "own laptop sees original accepted member host", func() bool {
		return stateAt(t, source, part.PID).Claimable() && stateAt(t, approver, part.PID).Claimable()
	})
	ask, err := approver.AskAgentInTopic(tctx(t), part.PID, envelope.KindQuestion, "older accepted own replica", topic, nil)
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
	answer, err := executor.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindAnswer, PID: part.PID, AgentID: part.AgentID, ReplyTo: ask.LID, Body: "old exact answer", Topic: topic, Files: []OutgoingFile{{Path: filePath, Name: "history.txt"}}})
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
	status, err := envelope.Seal(envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), LID: protocol.NewID(), From: executor.Address, To: source.Address, TS: time.Now().Unix(), Conv: conv, Kind: envelope.KindMessage, Sub: envelope.SubStatus, Body: `{"state":"running","n":1,"at":1700000000,"detail":"fixture"}`, Ref: &envelope.Ref{ID: ask.LID, Fingerprint: approver.Self().Fingerprint()}}, executor.id.Sign, recipient)
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
	if original.Group == nil || !derived && original.Group.HostRole != "member" || derived && (original.Group.HostRole != "visitor" || original.Parent == "") {
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
	if derived {
		rows, e := source.historySourceRows(source.store.db, "id=?", "dir,id", 1, answerID)
		if e != nil || len(rows) != 1 {
			t.Fatalf("derived retained source: %v", e)
		}
		item, e := source.historySourceItem(source.store.db, rows[0])
		if e != nil {
			t.Fatal(e)
		}
		witness, e := source.makeGroupHistoryWitness(source.store.db, current, item)
		if e != nil {
			t.Fatalf("derived witness construction: %v", e)
		}
		item.GroupHistory = witness
		hm, e := groupHistoryMembers(source.store.db, current.Root, item)
		if e != nil {
			t.Fatal(e)
		}
		hi, e := participationIn(source.store.db, conv, part.PID, hm, source.Address)
		if e != nil || hi.Member || hi.Parent != part.Parent || hi.Invite == "" {
			t.Fatalf("derived witness resolution %+v %v", hi, e)
		}
		if e = humanTurnAuthorization(source.store.db, item.inner(conv), item.From, item.FromKey, source.Address, source.Self().Fingerprint(), true, hm); e != nil {
			t.Fatalf("derived witnessed output host=%s want=%s authorPID=%s state=%s held=%d: %v", hi.Host.Address, item.From, item.Human.AuthorPID, hi.State, hi.Held, e)
		}
		deps, e := source.historyDependencies(rows[0], item)
		if e != nil {
			t.Fatal(e)
		}
		requestFound := false
		for _, dep := range deps {
			if dep.in.PID == part.PID && dep.in.Sub == envelope.SubEvent {
				t.Fatal("self-contained witnessed output depends on standalone live participation history")
			}
			requestFound = requestFound || dep.in.LID == ask.LID
		}
		if !requestFound {
			t.Fatal("witnessed output lost its exact original request dependency")
		}
		for _, mode := range []string{"unwitnessed", "other-original", "other-participation"} {
			scoped := item
			switch mode {
			case "unwitnessed":
				scoped.GroupHistory = nil
			case "other-original":
				scoped.ID = protocol.NewID()
			case "other-participation":
				scoped.PID = protocol.NewID()
			}
			deps, e := source.historyDependencies(rows[0], scoped)
			if e != nil {
				t.Fatal(e)
			}
			participationFound := false
			for _, dep := range deps {
				participationFound = participationFound || dep.in.PID == part.PID && dep.in.Sub == envelope.SubEvent
			}
			if !participationFound {
				t.Fatalf("%s skipped required standalone participation evidence", mode)
			}
		}
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
	stopPhone := runAgent(t, phone)
	stopSource = runAgent(t, source)
	func() {
		accepted := false
		defer func() {
			if accepted {
				return
			}
			// One post-wait snapshot, before daemon cleanup; identifiers stay in query parameters/results only.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var ids, carriers string
			var selected, originalCarriers int
			e := source.store.db.QueryRowContext(ctx, `WITH direct AS (
 SELECT id,sub,state FROM outbox WHERE recipient=? AND recipient_fp=? AND conv=?
 AND ((sub='history' AND json_extract(body,'$.id')=?) OR sub IN ('group-context','group-proof'))
 ORDER BY sub='history' DESC,id LIMIT 32
), copies AS (
 SELECT * FROM direct UNION SELECT o.id,o.sub,o.state FROM direct d
 JOIN history_archive_entries a ON a.child=d.id JOIN outbox o ON o.id=a.manifest
 WHERE o.recipient=? AND o.recipient_fp=? AND o.sub='history-archive'
), classes AS (
 SELECT sub,CASE WHEN state IN ('queued','waiting','custody','delivered','quarantined','archive_staged','archive_accepted','archive_uploading','not_delivered','expired') THEN state ELSE 'other' END AS class FROM copies
)
SELECT (SELECT json_group_array(id) FROM copies),
 (SELECT json_group_array(json_object('sub',sub,'state',class,'count',n)) FROM (SELECT sub,class,count(*) n FROM classes GROUP BY sub,class)),
 (SELECT count(*) FROM direct), (SELECT count(*) FROM direct WHERE sub='history')`, phone.Address, phone.Self().Fingerprint(), conv, originalID, phone.Address, phone.Self().Fingerprint()).Scan(&ids, &carriers, &selected, &originalCarriers)
			if e != nil {
				t.Logf("history snapshot source_to_new_phone query_error=true budget_expired=%t target_unobserved=true", ctx.Err() != nil)
				return
			}
			t.Logf("history snapshot source_to_new_phone original_carriers=%d selected=%d possibly_capped=%t carriers=%s", originalCarriers, selected, selected == 32, carriers)
			var producer string
			e = source.store.db.QueryRowContext(ctx, `SELECT json_object(
 'jobs',(SELECT json_group_array(json_object('state',state,'updated_at',updated_at)) FROM history_jobs WHERE device=?),
 'export_errors',(SELECT json_group_array(substr(error,1,300)) FROM history_archive_export_errors WHERE recipient=?),
 'copies',(SELECT json_group_array(json_object('sub',sub,'state',state,'required',required_cap,'error',error,'count',n)) FROM
 (SELECT coalesce(sub,'') sub,state,coalesce(required_cap,'') required_cap,coalesce(error,'') error,count(*) n FROM outbox WHERE recipient=? GROUP BY sub,state,required_cap,error)))`, phone.Address, phone.Address, phone.Address).Scan(&producer)
			source.archivePosting.Lock()
			active, again, closed := source.archivePosting.done != nil, source.archivePosting.again, source.archivePosting.closed
			parentLive := source.archivePosting.archiveParent != nil && source.archivePosting.archiveParent.Err() == nil
			source.archivePosting.Unlock()
			t.Logf("history snapshot producer=%s error=%v archive_active=%t archive_again=%t archive_closed=%t archive_parent_live=%t archive_lock=%d upkeep_bits=%d reader_wait=%t", producer, e, active, again, closed, parentLive, len(source.archiveLock), source.convWork.bits.Load(), source.convWork.readerWait.Load())
			var rows, held, archives string
			var raw []byte
			var historicalProof, currentProof, receipts int
			e = phone.store.db.QueryRowContext(ctx, `WITH carriers AS (SELECT value id FROM json_each(?)), kinds(physical) AS (VALUES('original'),('equivalent')),
 wanted(id,conv,author,lid,hash) AS (VALUES(?,?,?,?,?)), matches AS (
 SELECT CASE WHEN i.id=w.id THEN 'original' ELSE 'equivalent' END physical,replica,state,group_history,
 (i.conv=w.conv AND coalesce(verified_by,claimed_fp)=w.author AND i.lid=w.lid AND content_hash=w.hash) exact_tuple
 FROM inbox i,wanted w WHERE i.id=w.id OR (i.conv=w.conv AND coalesce(verified_by,claimed_fp)=w.author AND i.lid=w.lid AND content_hash=w.hash)
)
SELECT (SELECT json_group_array(json_object('physical',physical,'count',coalesce(n,0),'exact_tuple',exact_tuple,'replica',replicas,'inert',inert,'witness',witness,'complete',complete))
 FROM kinds LEFT JOIN (SELECT physical,count(*) n,sum(exact_tuple) exact_tuple,sum(replica=1) replicas,sum(state='') inert,sum(group_history IS NOT NULL) witness,sum(replica=1 AND state='' AND group_history IS NOT NULL) complete FROM matches GROUP BY physical) USING(physical)),
 coalesce((SELECT payload FROM group_context WHERE conv=?),X''),
 EXISTS(SELECT 1 FROM group_proof_records WHERE conv=? AND bootstrap=? AND seq=?),
 EXISTS(SELECT 1 FROM group_proof_records WHERE conv=? AND bootstrap=? AND seq=?),
 (SELECT json_group_array(json_object('reason',class,'count',n)) FROM (SELECT CASE WHEN reason IN ('proof_pending','identity_conflict','conflicting_duplicate','key_changed','invalid') THEN reason ELSE 'other' END class,count(*) n FROM quarantine WHERE sender=? AND id IN (SELECT id FROM carriers) GROUP BY class)),
 (SELECT count(*) FROM history_receipts WHERE id IN (SELECT id FROM carriers)),
 (SELECT json_object('jobs',count(*),'retained',coalesce(sum(retained=1),0),'done',coalesce(sum(done=1),0),'error_present',coalesce(sum(error<>''),0),'position_total',coalesce(sum(pos),0)) FROM history_archive_jobs WHERE source_fp=? AND id IN (SELECT id FROM carriers))`, ids, originalID, conv, item.FromKey, item.LID, contentHash(item.inner(conv)), conv, conv, packet.Root.Creator.Fingerprint, item.GroupHistory.State.Seq, conv, packet.Root.Creator.Fingerprint, current.State.Seq, source.Address, source.Self().Fingerprint()).Scan(&rows, &raw, &historicalProof, &currentProof, &held, &receipts, &archives)
			if e != nil {
				t.Logf("history snapshot new_phone query_error=true budget_expired=%t target_unobserved=true", ctx.Err() != nil)
				return
			}
			var seen GroupContext
			present := len(raw) != 0
			decodeError := present && json.Unmarshal(raw, &seen) != nil
			matchDefined := present && !decodeError
			t.Logf("history snapshot new_phone rows=%s context_present=%t context_decode_error=%t context_match_defined=%t context_matches_current=%t historical_proof_present=%t current_proof_present=%t held=%s receipts=%d archives=%s", rows, present, decodeError, matchDefined, matchDefined && sameGroupRoot(seen.Root, current.Root) && seen.State.Hash() == current.State.Hash(), historicalProof != 0, currentProof != 0, held, receipts, archives)
			// This is the eligible queue head, not the stream's connection-local
			// syncInFlight. Separate Hub/phone reads cannot prove delivery or cause.
			hubPath := filepath.ToSlash(filepath.Join(w.hub.Dir, "hub.db"))
			if filepath.VolumeName(w.hub.Dir) != "" {
				hubPath = "/" + hubPath
			}
			hubURI := url.URL{Scheme: "file", Path: hubPath, RawQuery: "mode=ro"}
			hub, e := sql.Open("sqlite", hubURI.String())
			if e != nil {
				t.Logf("history snapshot hub_head open_error=true budget_expired=%t head_unobserved=true", ctx.Err() != nil)
				return
			}
			defer hub.Close()
			hub.SetMaxOpenConns(1)
			var related, headID, senderAlias string
			var sessionKnown, headRelated bool
			e = hub.QueryRowContext(ctx, `WITH reader AS (SELECT last_session FROM agents WHERE address=?),
 carriers AS (SELECT value id FROM json_each(?)), related AS (
 SELECT CASE WHEN m.id IS NULL THEN 'missing' WHEN m.state IN ('custody','delivered','quarantined','expired') THEN m.state ELSE 'other' END state,
 CASE WHEN m.id IS NULL THEN 'missing' WHEN m.lane IN ('live','sync') THEN m.lane ELSE 'other' END lane,
 CASE WHEN m.id IS NULL OR coalesce((SELECT last_session FROM reader),'')='' THEN 'unknown'
 WHEN m.session IS NULL OR m.session=(SELECT last_session FROM reader) OR m.fallback=1 THEN 'eligible' ELSE 'ineligible' END eligibility
 FROM carriers c LEFT JOIN messages m ON m.id=c.id AND m.recipient=?
), head AS (
 SELECT m.id,m.sender FROM messages m,reader r WHERE m.recipient=? AND m.state='custody' AND m.lane='sync'
 AND coalesce(r.last_session,'')<>'' AND (m.session IS NULL OR m.session=r.last_session OR m.fallback=1)
 ORDER BY m.seq LIMIT 1
)
SELECT (SELECT json_group_array(json_object('state',state,'lane',lane,'eligibility',eligibility,'count',n))
 FROM (SELECT state,lane,eligibility,count(*) n FROM related GROUP BY state,lane,eligibility)),
 coalesce((SELECT id FROM head),''),
 CASE WHEN NOT EXISTS(SELECT 1 FROM head) THEN 'none' WHEN (SELECT sender FROM head)=? THEN 'source'
 WHEN (SELECT sender FROM head)=? THEN 'approver' ELSE 'other' END,
 coalesce((SELECT last_session FROM reader),'')<>'', EXISTS(SELECT 1 FROM head WHERE id IN (SELECT id FROM carriers))`, phone.Address, ids, phone.Address, phone.Address, source.Address, approver.Address).Scan(&related, &headID, &senderAlias, &sessionKnown, &headRelated)
			if e != nil {
				t.Logf("history snapshot hub_head query_error=true budget_expired=%t head_unobserved=true", ctx.Err() != nil)
				return
			}
			t.Logf("history snapshot hub_head related=%s session_known=%t eligible_head_present=%t head_related=%t sender_alias=%s actual_in_flight_unobserved=true cross_store_atomic=false", related, sessionKnown, headID != "", headRelated, senderAlias)
			if headID == "" {
				return
			}
			var disposition string
			e = phone.store.db.QueryRowContext(ctx, `WITH wanted(id) AS (VALUES(?)), dispositions AS (
 SELECT id,acked FROM inbox WHERE id=(SELECT id FROM wanted)
 UNION ALL SELECT id,acked FROM quarantine WHERE id=(SELECT id FROM wanted)
 UNION ALL SELECT id,acked FROM history_receipts WHERE id=(SELECT id FROM wanted)
)
SELECT json_object('inbox',(SELECT count(*) FROM inbox WHERE id=(SELECT id FROM wanted)),
 'quarantine',(SELECT count(*) FROM quarantine WHERE id=(SELECT id FROM wanted)),
 'receipts',(SELECT count(*) FROM history_receipts WHERE id=(SELECT id FROM wanted)),
 'dedup_seen',(SELECT count(*) FROM dispositions),
 'ack_due',(SELECT count(*) FROM dispositions WHERE acked=0 AND id NOT IN (SELECT id FROM history_archive_children)),
 'archive_child',(SELECT count(*) FROM history_archive_children WHERE id=(SELECT id FROM wanted)),
 'jobs',count(*),'retained',coalesce(sum(retained=1),0),'done',coalesce(sum(done=1),0),
 'error_present',coalesce(sum(error<>''),0),'position_total',coalesce(sum(pos),0))
 FROM history_archive_jobs WHERE id=(SELECT id FROM wanted)`, headID).Scan(&disposition)
			if e != nil {
				t.Logf("history snapshot head_phone query_error=true budget_expired=%t disposition_unobserved=true", ctx.Err() != nil)
				return
			}
			t.Logf("history snapshot head_phone disposition=%s", disposition)
		}()
		eventually(t, "new phone accepts old inert history", func() bool {
			return inboxCount(t, phone, `id=? AND replica=1 AND state='' AND group_history IS NOT NULL`, originalID) == 1
		})
		accepted = true
	}()
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
	if derived {
		stopPhone()
		stopSource()
		t.Run("cold-author-admission", func(t *testing.T) {
			guest := part.Host.Person
			if _, member := current.State.Member(guest); member {
				t.Fatal("cold author must be a nonmember")
			}
			rootRaw, _ := json.Marshal(packet.Root)
			copy, err := source.historyCopy(phone.Self(), conv, rootRaw, item)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"person_devices", "person_chain", "persons"} {
				if _, err = phone.store.db.Exec(`DELETE FROM `+table+` WHERE person=?`, guest); err != nil {
					t.Fatal(err)
				}
			}
			phonePacket, err := groupTurnPacketIn(phone.store.db, conv)
			if err != nil || phonePacket.State.Hash() != current.State.Hash() {
				t.Fatalf("cold author changed current context: %v", err)
			}
			own, err := groupMemberAdmission(phone.store.db, phonePacket, phone.Address, phone.Self().Fingerprint())
			if err != nil || own.Hash() != item.GroupAdmission {
				t.Fatalf("cold author changed own admission: %v", err)
			}
			if err = phone.verifyAndStore(tctx(t), copy.env); err != nil {
				t.Fatal(err)
			}
			var held int
			if err = phone.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, copy.env.ID).Scan(&held); err != nil || held != 0 {
				t.Fatalf("cold author history carrier held: %d %v", held, err)
			}
			author, known, err := phone.store.personByID(guest)
			if err != nil || !known || !author.has(executor.Address, executor.Self().Fingerprint()) {
				t.Fatalf("normal history admission did not discover its exact author: %v", err)
			}
			var savedPublic string
			if err = phone.store.db.QueryRow(`SELECT public FROM peers WHERE address=?`, executor.Address).Scan(&savedPublic); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"pending", "changed-key", "frozen", "bad-signature", "bad-parent"} {
				t.Run(mode, func(t *testing.T) {
					t.Cleanup(func() {
						if _, err := phone.store.db.Exec(`UPDATE peers SET public=?,pending=NULL WHERE address=?`, savedPublic, executor.Address); err != nil {
							t.Error(err)
						}
						if _, err := phone.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, author.info.State, guest); err != nil {
							t.Error(err)
						}
					})
					var bad HistoryItem
					if err := json.Unmarshal([]byte(rawItem), &bad); err != nil {
						t.Fatal(err)
					}
					switch mode {
					case "pending":
						_, err = phone.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, executor.Address)
					case "changed-key":
						raw, _ := json.Marshal(w.bob.Self())
						_, err = phone.store.db.Exec(`UPDATE peers SET public=? WHERE address=?`, string(raw), executor.Address)
					case "frozen":
						_, err = phone.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, guest)
					case "bad-signature", "bad-parent":
						for i := range bad.GroupHistory.Memberships {
							ev := &bad.GroupHistory.Memberships[i]
							if ev.Parent == "" {
								continue
							}
							if mode == "bad-signature" {
								ev.Sig[0] ^= 1
							} else {
								ev.Parent = strings.Repeat("a", 64)
								ev.Sign(executor.id.Sign)
							}
							break
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					in := copy.in
					in.ID, in.LID = protocol.NewID(), protocol.NewID()
					raw, _ := json.Marshal(bad)
					in.Body = string(raw)
					recipient, _ := phone.Self().Recipient()
					env, e := envelope.Seal(in, source.id.Sign, recipient)
					if e != nil {
						t.Fatal(e)
					}
					if e = phone.verifyAndStore(tctx(t), env); e != nil {
						t.Fatal(e)
					}
					if e = phone.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, env.ID).Scan(&held); e != nil || held != 1 {
						t.Fatalf("unsafe history carrier admitted: %d %v", held, e)
					}
				})
			}
			if err = phone.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE conv=? AND pid=?`, conv, part.PID).Scan(&liveEvents); err != nil || liveEvents != 0 || stateAt(t, phone, part.PID).Claimable() {
				t.Fatalf("discovery imported live authority: events=%d err=%v", liveEvents, err)
			}
		})
		for _, mode := range []string{"pending-current-withdrawal", "conflicted-current-member", "withdrawn-own-reader"} {
			t.Run("duplicate-"+mode, func(t *testing.T) {
				rootRaw, _ := json.Marshal(packet.Root)
				copy, err := source.historyCopy(phone.Self(), conv, rootRaw, item)
				if err != nil {
					t.Fatal(err)
				}
				person := bob.info.Person
				wantErr, wantReason := ErrGroupContextPending, reasonProof
				if mode == "withdrawn-own-reader" {
					person = self.info.Person
					wantErr, wantReason = errGroupRecipientWithdrawn, reasonInvalid
				}
				member, ok := current.State.Member(person)
				if !ok {
					t.Fatal("current fixture member missing")
				}
				if mode == "conflicted-current-member" {
					p, found, e := phone.store.personByID(person)
					if e != nil || !found {
						t.Fatalf("current member person missing: %v", e)
					}
					_, err = phone.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, person)
					t.Cleanup(func() {
						if _, err := phone.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, p.info.State, person); err != nil {
							t.Error(err)
						}
					})
				} else {
					table := "group_pending_withdrawals"
					if mode == "withdrawn-own-reader" {
						table = "group_withdrawals"
					}
					_, err = phone.store.db.Exec(`INSERT INTO `+table+`(conv,person,admission,record) VALUES(?,?,?,'{}')`, conv, person, member.Admission.Hash())
					t.Cleanup(func() {
						if _, err := phone.store.db.Exec(`DELETE FROM `+table+` WHERE conv=? AND person=? AND admission=?`, conv, person, member.Admission.Hash()); err != nil {
							t.Error(err)
						}
					})
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err = phone.groupParticipationHistoryCheck(phone.store.db, packet.Root, source.Self(), item); !errors.Is(err, wantErr) {
					t.Fatalf("current group classification changed: want=%v got=%v", wantErr, err)
				}
				if mode == "conflicted-current-member" {
					// A pre-existing conflict stops the network refresh before
					// admission. Exercise the atomic duplicate guard directly.
					_, err = phone.store.addHistoryInbox(item.inner(conv), item.At, item.FromKey, source.Address, copy.env.ID, false, nil, func(tx *sql.Tx) error {
						_, err := phone.groupParticipationHistoryCheck(tx, packet.Root, source.Self(), item)
						return err
					})
					if !errors.Is(err, ErrGroupContextPending) {
						t.Fatalf("duplicate guard changed conflict classification: %v", err)
					}
				} else {
					if err = phone.verifyAndStore(tctx(t), copy.env); err != nil {
						t.Fatal(err)
					}
					var reason string
					if err = phone.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, copy.env.ID).Scan(&reason); err != nil || reason != wantReason {
						t.Fatalf("duplicate classification changed: want=%q reason=%q err=%v", wantReason, reason, err)
					}
				}
				var receipts int
				if err = phone.store.db.QueryRow(`SELECT count(*) FROM history_receipts WHERE id=?`, copy.env.ID).Scan(&receipts); err != nil || receipts != 0 {
					t.Fatalf("duplicate gained delivered disposition: receipts=%d err=%v", receipts, err)
				}
				if inboxCount(t, phone, `id=? AND replica=1 AND state='' AND group_history IS NOT NULL`, originalID) != 1 {
					t.Fatal("deferring a duplicate erased the admitted inert original")
				}
			})
		}
	}
	modes := []string{"missing-witness", "wrong-state", "forged-state", "other-pid", "unbound-roster", "wrong-task-epoch", "unknown-own-admission", "foreign-forwarder"}
	if derived {
		modes = append(modes, "missing-parent", "wrong-parent-consent")
	}
	for _, mode := range modes {
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
			case "missing-parent":
				var pid string
				for _, ev := range bad.GroupHistory.Memberships {
					if ev.Type == protocol.EventAccept && ev.Hash() == part.Parent {
						pid = ev.PID
					}
				}
				var kept []protocol.ParticipationEvent
				for _, ev := range bad.GroupHistory.Memberships {
					if ev.PID != pid {
						kept = append(kept, ev)
					}
				}
				bad.GroupHistory.Memberships = kept
			case "wrong-parent-consent":
				for i := range bad.GroupHistory.Memberships {
					ev := &bad.GroupHistory.Memberships[i]
					if ev.Parent != "" {
						ev.Parent = strings.Repeat("a", 64)
						ev.Sign(executor.id.Sign)
					}
				}
			}
			if _, err := phone.groupParticipationHistoryCheck(phone.store.db, packet.Root, forwarder, bad); err == nil {
				t.Fatal("invalid history witness accepted")
			}
		})
	}
	for _, mode := range []string{"pending-current-withdrawal", "conflicted-current-member"} {
		t.Run(mode, func(t *testing.T) {
			tx, err := phone.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			member, ok := current.State.Member(bob.info.Person)
			if !ok {
				t.Fatal("current host member missing")
			}
			if mode == "pending-current-withdrawal" {
				_, err = tx.Exec(`INSERT INTO group_pending_withdrawals(conv,person,admission,record) VALUES(?,?,?,'{}')`, conv, member.Person, member.Admission.Hash())
			} else {
				_, err = tx.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, member.Person)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = phone.groupParticipationHistoryCheck(tx, packet.Root, source.Self(), item); !errors.Is(err, ErrGroupContextPending) {
				t.Fatalf("unresolved current group admitted witnessed history: %v", err)
			}
		})
	}
	t.Run("pending-original-author", func(t *testing.T) {
		if _, err := phone.sendKey(tctx(t), w.bob.Address); err != nil {
			t.Fatal(err)
		}
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
