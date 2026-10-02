package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"os"
	"slices"
	"strings"
	"testing"
)

func groupHistoryOfflineInvitation(t *testing.T) (*world, *Agent, GroupContext, ConvSent, GroupInvitationInfo) {
	t.Helper()
	w, p := groupOrdinaryPublication(t)
	carol := proofReader(t, w, "history-offline")
	path, _ := writeFile(t, t.TempDir(), "earlier-selected.bin", 1234)
	sent, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "exact earlier history", Files: []OutgoingFile{{Path: path, Name: "earlier-selected.bin"}}})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := w.alice.SelectGroupHistory(tctx(t), p.State.Conv, GroupHistorySelection{Last: 1})
	if err != nil || len(refs) != 1 {
		t.Fatalf("source selection %v %v", refs, err)
	}
	person, _, err := carol.store.selfPerson(carol.Address)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := w.alice.InviteGroup(tctx(t), p.State.Conv, person.roster.Person, refs)
	if err != nil {
		t.Fatal(err)
	}
	deliverGroupLifecycleSubtype(t, w.alice, carol, envelope.SubGroupProof)
	deliverGroupLifecycleSubtype(t, w.alice, carol, envelope.SubGroupInvite)
	if err = carol.DecideGroupInvitation(tctx(t), inv.ID, true); err != nil {
		t.Fatal(err)
	}
	return w, carol, p, sent, inv
}

func groupHistoryDeliverCommittedFixture(t *testing.T, sender, receiver *Agent, seq int64, includeHistory bool) {
	t.Helper()
	rows, err := sender.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND (sub='group-proof' OR (sub='group-context' AND json_extract(body,'$.seq')=?) OR (? AND sub='history')) ORDER BY CASE sub WHEN 'group-proof' THEN 0 WHEN 'group-context' THEN 1 ELSE 2 END,rowid`, receiver.Address, seq, includeHistory)
	if err != nil {
		t.Fatal(err)
	}
	var envs []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &env); err != nil {
			break
		}
		envs = append(envs, env)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range envs {
		if err = sender.uploadAll(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if err = sender.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
			t.Fatal(err)
		}
		if err = receiver.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
	}
}

// Controls are not yet admitted in groups. This fixture supplies an already
// resolved exact ordinary retraction to exercise the existing redaction path;
// it adds no group control/public admission route.
func groupHistoryResolvedRetraction(t *testing.T, a *Agent, conv, lid, fp string) {
	t.Helper()
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), LID: protocol.NewID(), From: a.Address, TS: 1, Kind: envelope.KindMessage, Conv: conv, Sub: envelope.SubRetraction, Ref: &envelope.Ref{ID: lid, Fingerprint: fp}, Body: `{"reason":"synthetic resolved retraction"}`}
	if _, err := a.store.addConvInbox(in, fp, "", false, nil); err != nil {
		t.Fatal(err)
	}
	a.applyRetraction(in)
}

func TestGroupHistoryChangedBeforeCASRefuses(t *testing.T) {
	w, carol, p, sent, inv := groupHistoryOfflineInvitation(t)
	if _, err := w.alice.store.db.Exec(`UPDATE outbox SET body='changed visible content' WHERE conv=? AND lid=? AND sub IS NULL`, p.State.Conv, sent.LID); err != nil {
		t.Fatal(err)
	}
	deliverGroupLifecycleSubtype(t, carol, w.alice, envelope.SubGroupConsent)
	r, err := groupInvitationIn(w.alice.store.db, inv.ID, "out")
	if err != nil || r.State != "history-unavailable" {
		t.Fatalf("changed source outcome %s %v", r.State, err)
	}
	var n int
	if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM group_publications WHERE conv=? AND seq=?`, p.State.Conv, p.State.Seq+1).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unavailable source created CAS attempt %d %v", n, err)
	}
	assertNoGroupJoin(t, carol, p.State.Conv)
}

func TestGroupHistoryCommittedMutationRestartContextOnly(t *testing.T) {
	w, carol, p, sent, inv := groupHistoryOfflineInvitation(t)
	injectFaults(w.alice).add("POST", "/v1/groups/", 1, true)
	deliverGroupLifecycleSubtype(t, carol, w.alice, envelope.SubGroupConsent)
	var original []byte
	if err := w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=? AND published=0`, p.State.Conv, p.State.Seq+1).Scan(&original); err != nil {
		t.Fatal(err)
	}
	groupHistoryResolvedRetraction(t, w.alice, p.State.Conv, sent.LID, w.alice.Self().Fingerprint())
	home := w.alice.home
	w.alice.Close()
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	w.alice = a
	if err = a.RecoverGroupPublications(tctx(t)); !errors.Is(err, ErrGroupHistoryUnavailable) {
		t.Fatalf("committed unavailable recovery %v", err)
	}
	r, err := groupInvitationIn(a.store.db, inv.ID, "out")
	if err != nil || r.State != "published-history-unavailable" {
		t.Fatalf("honest committed outcome %s %v", r.State, err)
	}
	current, err := a.GroupContext(p.State.Conv)
	if err != nil || current.State.Seq != p.State.Seq+1 {
		t.Fatalf("committed membership lost %v", err)
	}
	var history, contexts int
	a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, carol.Address).Scan(&history)
	a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='group-context'`, carol.Address).Scan(&contexts)
	if history != 0 || contexts != 1 {
		t.Fatalf("context-only enqueue history%d context%d", history, contexts)
	}
	var recovered []byte
	a.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=?`, p.State.Conv, p.State.Seq+1).Scan(&recovered)
	if !bytes.Equal(original, recovered) {
		t.Fatal("already committed ciphertext changed")
	}
	if err = a.PublishGroupInvitation(tctx(t), inv.ID); !errors.Is(err, ErrGroupHistoryUnavailable) {
		t.Fatalf("retry hid history unavailability %v", err)
	}
	if err = a.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestGroupHistoryStoppedRedactionExactScope(t *testing.T) {
	w, carol, p, sent, inv := groupHistoryOfflineInvitation(t)
	deliverGroupLifecycleSubtype(t, carol, w.alice, envelope.SubGroupConsent)
	r, err := groupInvitationIn(w.alice.store.db, inv.ID, "out")
	if err != nil || r.State != "published" {
		t.Fatal("history publication did not enqueue")
	}
	var raw []byte
	var env envelope.Envelope
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub='history'`, carol.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &env) != nil {
		t.Fatal("history envelope")
	}
	// A separate signed turn with the same text survives exact logical scope.
	survivor, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "exact earlier history"})
	if err != nil {
		t.Fatal(err)
	}
	groupHistoryResolvedRetraction(t, w.alice, p.State.Conv, sent.LID, w.alice.Self().Fingerprint())
	var body, state string
	if err = w.alice.store.db.QueryRow(`SELECT body,state FROM outbox WHERE id=?`, env.ID).Scan(&body, &state); err != nil || body != "" || state != stateNotDelivered {
		t.Fatalf("stopped-daemon history archive %q %s %v", body, state, err)
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND lid=? AND body='exact earlier history'`, p.State.Conv, survivor.LID).Scan(&n)
	if n == 0 {
		t.Fatal("surviving logical turn redacted")
	}
	if _, allowed, err := w.alice.mayDeliverGroupTurn(env); allowed || err != nil {
		t.Fatalf("deleted carrier retried %v %v", allowed, err)
	}
	if _, err = w.alice.groupHistorySourceIn(w.alice.store.db, p.State.Conv, inv.Proposal.History[0]); !errors.Is(err, ErrGroupHistoryUnavailable) {
		t.Fatal("deleted source could be resealed")
	}
}

func TestGroupHistoryQueuedGrantSameKeyRejoinRefuses(t *testing.T) {
	w, carol, p, _, inv := groupHistoryOfflineInvitation(t)
	deliverGroupLifecycleSubtype(t, carol, w.alice, envelope.SubGroupConsent)
	current, err := w.alice.GroupContext(p.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var oldHistory envelope.Envelope
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub='history'`, carol.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &oldHistory) != nil {
		t.Fatal("queued history")
	}
	person, _, err := carol.store.selfPerson(carol.Address)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := current.State.Member(person.roster.Person)
	groupHistoryDeliverCommittedFixture(t, w.alice, carol, current.State.Seq, true)
	rows, err := carol.ConversationMessages(p.State.Conv)
	if err != nil || len(rows) != 1 || len(rows[0].Attachments) != 1 {
		t.Fatalf("selected imported fixture %v", err)
	}
	if err = carol.RequestFile(tctx(t), rows[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	var oldFile envelope.Envelope
	if err = carol.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub='file'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &oldFile) != nil {
		t.Fatal("queued exact selected file request")
	}
	removed := current
	removed.Proof = nil
	removed.State.Members = slices.Clone(current.State.Members)
	removed.State.Members = slices.DeleteFunc(removed.State.Members, func(m protocol.GroupMember) bool { return m.Person == person.roster.Person })
	removed.State.Seq++
	removed.State.Prev = current.State.Hash()
	removed, err = w.alice.SignGroupState(tctx(t), removed)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), removed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, removed); err != nil {
		t.Fatal(err)
	}
	// No retry in the removed interval: same address/key plus a NEW explicit
	// admission, even with identical refs, must not reactivate the old copy.
	admission, err := carol.SignGroupAdmission(p.Root, removed.State.Seq+1, removed.State.Hash(), inv.Proposal.History)
	if err != nil {
		t.Fatal(err)
	}
	rejoined := removed
	rejoined.State.Members = slices.Clone(removed.State.Members)
	rejoined.State.Seq++
	rejoined.State.Prev = removed.State.Hash()
	rejoined.State.Members = append(rejoined.State.Members, protocol.GroupMember{ConvMember: protocol.ConvMember{Person: person.roster.Person, Roster: person.roster.Hash()}, Admission: admission})
	slices.SortFunc(rejoined.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	rejoined, err = w.alice.SignGroupState(tctx(t), rejoined)
	if err != nil {
		t.Fatal(err)
	}
	commit, err = w.alice.BuildGroupCommit(tctx(t), rejoined)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, rejoined); err != nil {
		t.Fatal(err)
	}
	if handled, allowed, e := w.alice.mayDeliverGroupTurn(oldHistory); !handled || allowed || e != nil {
		t.Fatalf("old selected copy reactivated %v %v %v", handled, allowed, e)
	}
	groupHistoryDeliverCommittedFixture(t, w.alice, carol, rejoined.State.Seq, false)
	if handled, allowed, e := carol.mayDeliverGroupTurn(oldFile); !handled || allowed || e != nil {
		t.Fatalf("old queued file request acquired rejoin authority %v %v %v", handled, allowed, e)
	}
	index := 0
	size := int64(1)
	m := fileMsg{V: 1, Type: "request", LID: inv.Proposal.History[0].LID, Author: inv.Proposal.History[0].Author, Hash: inv.Proposal.History[0].Hash, SHA256: strings.Repeat("e", 64), Index: &index, Name: "old-selected.bin", Size: &size, GroupAdmission: old.Admission.Hash()}
	if e := w.alice.groupFileAuthorized(w.alice.store.db, rejoined, carol.Address, carol.Self().Fingerprint(), m); e == nil || !strings.Contains(e.Error(), "admission changed") {
		t.Fatalf("old file request adopted new admission %v", e)
	}
}

func TestGroupHistorySelectedTextAdmission(t *testing.T) {
	w, p := groupOrdinaryPublication(t)
	carol := proofReader(t, w, "history-carol")
	defer func() {
		if t.Failed() {
			for _, a := range []*Agent{w.alice, w.bob, carol} {
				for _, query := range []string{`SELECT 'outbox',coalesce(sub,''),state,coalesce(error,'') FROM outbox WHERE sub='file'`, `SELECT 'serve',device,state,coalesce(json_extract(group_descriptor,'$.message.index'),'') FROM file_serves`, `SELECT 'request',message_id,state,coalesce(detail,'') FROM file_requests`, `SELECT 'quarantine',sender,reason,'' FROM quarantine`} {
					rows, e := a.store.db.Query(query)
					if e != nil {
						t.Log(a.Address, e)
						continue
					}
					for rows.Next() {
						var tag, x, y, z string
						if e = rows.Scan(&tag, &x, &y, &z); e == nil {
							t.Log(a.Address, tag, x, y, z)
						}
					}
					rows.Close()
				}
			}
		}
	}()
	var stopAlice, stopCarol func()
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		stop := runAgent(t, a)
		if a == w.alice {
			stopAlice = stop
		}
		if a == carol {
			stopCarol = stop
		}
		publishGroupFixtureCaps(t, a, true)
	}
	eventually(t, "Bob current group", func() bool {
		got, e := w.bob.GroupContext(p.State.Conv)
		return e == nil && got.State.Hash() == p.State.Hash()
	})
	path, data := writeFile(t, t.TempDir(), "selected.bin", 40001)
	parent, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "unselected parent", Files: []OutgoingFile{{Path: path, Name: "unselected-same-sha.bin"}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "Bob sees parent", func() bool { return slices.Contains(convBodies(t, w.bob, p.State.Conv), "in:unselected parent") })
	selected, err := w.bob.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "selected Bob content", ReplyTo: parent.LID, Files: []OutgoingFile{{Path: path, Name: "selected.bin"}, {Path: path, Name: "selected-copy.bin"}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "selected source Alice", func() bool { return slices.Contains(convBodies(t, w.alice, p.State.Conv), "in:selected Bob content") })
	refs, err := w.alice.SelectGroupHistory(tctx(t), p.State.Conv, GroupHistorySelection{Last: 1})
	if err != nil || len(refs) != 1 || refs[0].LID != selected.LID {
		t.Fatalf("selection %v %v", refs, err)
	}
	// Historical attribution survives an author's later removal. The current
	// forwarder/receiver still require effective exact-key membership.
	bobPerson, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	removed := p
	removed.Proof = nil
	removed.State.Members = slices.Clone(p.State.Members)
	removed.State.Members = slices.DeleteFunc(removed.State.Members, func(m protocol.GroupMember) bool { return m.Person == bobPerson.roster.Person })
	removed.State.Seq++
	removed.State.Prev = p.State.Hash()
	removed, err = w.alice.SignGroupState(tctx(t), removed)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), removed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, removed); err != nil {
		t.Fatal(err)
	}
	p = removed
	person, _, err := carol.store.selfPerson(carol.Address)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := w.alice.InviteGroup(tctx(t), p.State.Conv, person.roster.Person, refs)
	if err != nil {
		t.Fatal(err)
	}
	awaitGroupInvitation(t, carol, invite.ID, "pending")
	assertNoGroupJoin(t, carol, p.State.Conv)
	if err = carol.DecideGroupInvitation(tctx(t), invite.ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "selected content installed", func() bool { return slices.Contains(convBodies(t, carol, p.State.Conv), "in:selected Bob content") })
	rows := groupTurns(t, carol, p.State.Conv)
	if len(rows) != 1 || !rows[0].History || rows[0].Claimed != w.bob.Self().Fingerprint() || rows[0].Key != "" || rows[0].ReplyTo != parent.LID {
		t.Fatalf("history authority %+v", rows)
	}
	if _, err = carol.groupHistorySourceIn(carol.store.db, p.State.Conv, protocol.GroupHistoryRef{LID: parent.LID, Author: w.alice.Self().Fingerprint(), Hash: refs[0].Hash}); err == nil {
		t.Fatal("unselected parent present")
	}
	var stamp string
	carol.store.db.QueryRow(`SELECT coalesce(group_admission,'') FROM inbox WHERE id=?`, rows[0].ID).Scan(&stamp)
	if stamp != "" {
		t.Fatal("selected import acquired direct stamp")
	}
	if len(rows[0].Attachments) != 2 || rows[0].Attachments[0].Name != "selected.bin" || !strings.HasPrefix(rows[0].Attachments[0].BlobID, historyBlob) {
		t.Fatalf("selected manifest %+v", rows[0].Attachments)
	}
	label, device, _ := protocol.SplitAddress(carol.Address)
	var oldProfile protocol.Profile
	if err = carol.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &oldProfile); err != nil {
		t.Fatal(err)
	}
	stopCarol()
	// Pause the forwarder until the restarted recipient's current signed
	// capability record explicitly models an older peer. Production now reads grp1.
	stopAlice()
	if err = carol.RequestFile(tctx(t), rows[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	if err = carol.RequestFile(tctx(t), rows[0].ID, 1); err == nil {
		t.Fatal("same SHA silently retargeted pending index")
	}
	runAgent(t, carol)
	eventually(t, "new exact native session before fixture capability", func() bool {
		var profile protocol.Profile
		if carol.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile) != nil {
			return false
		}
		for _, session := range profile.Sessions {
			if !slices.Contains(oldProfile.Sessions, session) && profile.Supports(carol.Address, carol.Self().SignKey, protocol.CapAgentIdentity) {
				return true
			}
		}
		return false
	})
	publishGroupFixtureCaps(t, carol, false)
	var beforeCaps protocol.Profile
	if err = carol.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &beforeCaps); err != nil {
		t.Fatal(err)
	}
	if beforeCaps.Supports(carol.Address, carol.Self().SignKey, protocol.CapGroup) {
		t.Fatal("synthetic older peer still advertises group capability")
	}
	runAgent(t, w.alice)
	eventually(t, "old-only capability refuses queued offer", func() bool {
		var n int
		if w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='file' AND state='waiting' AND required_cap=?`, carol.Address, protocol.CapGroup).Scan(&n) != nil {
			return false
		}
		return n == 1
	})
	t.Logf("synthetic restart old sessions %v; current sessions %v; old-only grp1 refuses", oldProfile.Sessions, beforeCaps.Sessions)
	publishGroupFixtureCaps(t, carol, true)
	eventually(t, "selected re-encrypted file offer", func() bool {
		files, e := carol.store.attachments(rows[0].ID)
		return e == nil && len(files) == 2 && !strings.HasPrefix(files[0].BlobID, historyBlob) && strings.HasPrefix(files[1].BlobID, historyBlob)
	})
	if err = carol.RequestFile(tctx(t), rows[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second exact same-SHA index", func() bool {
		files, e := carol.store.attachments(rows[0].ID)
		return e == nil && len(files) == 2 && !strings.HasPrefix(files[1].BlobID, historyBlob)
	})
	paths, err := carol.Download(tctx(t), rows[0].ID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatal("selected files incomplete")
	}
	for _, path := range paths {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("selected file bytes %v", err)
		}
	}
	parents, e := w.alice.groupHistorySources(w.alice.store.db, p.State.Conv, parent.LID, w.alice.Self().Fingerprint(), 0, 2)
	if e != nil || len(parents) != 1 {
		t.Fatal(e)
	}
	ref := historyRef(p.State.Conv, parents[0].item)
	index := 0
	size := int64(len(data))
	m := fileMsg{V: 1, Type: "request", LID: ref.LID, Author: ref.Author, Hash: ref.Hash, SHA256: parents[0].item.Attachments[0].SHA256, Index: &index, Name: "unselected-same-sha.bin", Size: &size}
	current, e := w.alice.GroupContext(p.State.Conv)
	if e != nil {
		t.Fatal(e)
	}
	admission, e := groupMemberAdmission(w.alice.store.db, current, carol.Address, carol.Self().Fingerprint())
	if e != nil {
		t.Fatal(e)
	}
	m.GroupAdmission = admission.Hash()
	if e = w.alice.groupFileAuthorized(w.alice.store.db, current, carol.Address, carol.Self().Fingerprint(), m); e == nil {
		t.Fatal("same SHA unselected turn granted file")
	}
	// Actual sealed signed history admission, not just a sender-side helper check.
	original, e := w.alice.groupHistorySourceIn(w.alice.store.db, p.State.Conv, refs[0])
	if e != nil {
		t.Fatal(e)
	}
	for _, variant := range []string{"body", "author", "manifest", "task"} {
		item := original.item
		item.Attachments = slices.Clone(item.Attachments)
		switch variant {
		case "body":
			item.Body += " tampered"
		case "author":
			item.FromKey = w.alice.Self().Fingerprint()
		case "manifest":
			item.Attachments[0].Name = "not-selected.bin"
		case "task":
			item.Kind = envelope.KindTask
		}
		copy, err := w.alice.groupHistoryCarrier(carol.Self(), current, item)
		if err != nil {
			t.Fatal(err)
		}
		if err = carol.accept(tctx(t), copy.env); err != nil {
			t.Fatal(err)
		}
		var reason string
		if err = carol.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, copy.env.ID).Scan(&reason); err != nil || reason != reasonInvalid {
			t.Fatalf("%s mutated carrier: %q %v", variant, reason, err)
		}
	}
	copy, e := w.alice.groupHistoryCarrier(carol.Self(), current, original.item)
	if e != nil {
		t.Fatal(e)
	}
	if e = carol.accept(tctx(t), copy.env); e != nil {
		t.Fatal(e)
	}
	if got, e := carol.ConversationMessages(p.State.Conv); e != nil || len(got) != 1 {
		t.Fatalf("duplicate expanded selected history: %d %v", len(got), e)
	}
	var jobs int
	if e = carol.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE kind IN ('question','task') OR coalesce(executor,'')<>''`).Scan(&jobs); e != nil || jobs != 0 {
		t.Fatalf("history granted execution: %d %v", jobs, e)
	}

}

func TestGroupHistorySelectedFileUnavailableHonest(t *testing.T) {
	w, carol, p, _, _ := groupHistoryOfflineInvitation(t)
	deliverGroupLifecycleSubtype(t, carol, w.alice, envelope.SubGroupConsent)
	current, err := w.alice.GroupContext(p.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	groupHistoryDeliverCommittedFixture(t, w.alice, carol, current.State.Seq, true)
	rows, err := carol.ConversationMessages(p.State.Conv)
	if err != nil || len(rows) != 1 || len(rows[0].Attachments) != 1 {
		t.Fatalf("selected source: %v", err)
	}
	if err = os.Remove(w.alice.keptPath(rows[0].Attachments[0].SHA256)); err != nil {
		t.Fatal(err)
	}
	if err = carol.RequestFile(tctx(t), rows[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var request envelope.Envelope
	if err = carol.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub='file' AND recipient=?`, w.alice.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.accept(tctx(t), request); err != nil {
		t.Fatal(err)
	}
	w.alice.serveFiles(tctx(t))
	var offer envelope.Envelope
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub='file' AND recipient=?`, carol.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &offer); err != nil {
		t.Fatal(err)
	}
	if len(offer.Blobs) != 0 {
		t.Fatal("unavailable offer contains bytes")
	}
	if err = carol.accept(tctx(t), offer); err != nil {
		t.Fatal(err)
	}
	var state, detail string
	if err = carol.store.db.QueryRow(`SELECT state,detail FROM file_requests WHERE message_id=?`, rows[0].ID).Scan(&state, &detail); err != nil || state != "unavailable" || detail == "" {
		t.Fatalf("honest unavailable %q %q %v", state, detail, err)
	}
	files, err := carol.store.attachments(rows[0].ID)
	if err != nil || len(files) != 1 || !strings.HasPrefix(files[0].BlobID, historyBlob) {
		t.Fatal("unavailable became openable bytes")
	}
}
