package client

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestGroupHistoryOwnOriginalFileAfterRevision(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	a := w.alice
	conv := packet.State.Conv
	path, data := writeFile(t, t.TempDir(), "original.bin", 4321)
	sent, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "original with topic", Topic: "new", Files: []OutgoingFile{{Path: path, Name: "original.bin"}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "original delivered before editing", func() bool { return inboxCount(t, w.bob, `conv=? AND lid=?`, conv, sent.LID) == 1 })
	ref, err := a.RefOf(conv, sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Revise(tctx(t), ref, "later visible revision"); err != nil {
		t.Fatal(err)
	}
	phone, await, _ := linkPhone(t, a, "original-file-phone")
	requestLink := pendingLink(t, a)
	stops[a]()
	if err = a.DecideLink(tctx(t), requestLink.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	sources, err := a.groupHistorySources(a.store.db, conv, sent.LID, a.Self().Fingerprint(), 0, 2, true)
	if err != nil || len(sources) != 1 {
		t.Fatalf("original source: %d %v", len(sources), err)
	}
	original := sources[0].item
	if original.Body != "original with topic" {
		t.Fatalf("fixture original body differs: %q", original.Body)
	}
	file := original.Attachments[0]
	index := 0
	m := fileMsg{V: 1, Type: "request", LID: original.LID, Author: original.FromKey, Hash: historyRef(conv, original).Hash, Index: &index, Name: file.Name, Size: &file.Size, SHA256: file.SHA256, GroupAdmission: sources[0].stamp}
	if err = a.groupFileAuthorized(a.store.db, packet, phone.Address, phone.Self().Fingerprint(), m); err != nil {
		t.Fatalf("own original file rejected after revision: %v", err)
	}
	if _, err = a.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatal(err)
	}
	groupHistoryDeliverCommittedFixture(t, a, phone, packet.State.Seq, false)
	root, _ := json.Marshal(packet.Root)
	copy, err := a.historyCopy(phone.Self(), conv, root, original)
	if err != nil {
		t.Fatal(err)
	}
	if err = phone.accept(tctx(t), copy.env); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, phone, `id=? AND replica=1`, original.ID) != 1 {
		t.Fatal("original history was not stored")
	}
	// The phone has the original only. Its forwarder already has a revision.
	if err = phone.RequestFile(tctx(t), original.ID, 0); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var request envelope.Envelope
	if err = phone.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub='file'`, a.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if err = a.accept(tctx(t), request); err != nil {
		t.Fatal(err)
	}
	var jobRaw []byte
	if err = a.store.db.QueryRow(`SELECT group_descriptor FROM file_serves WHERE id=?`, request.ID).Scan(&jobRaw); err != nil {
		t.Fatal(err)
	}
	var job groupFileServe
	if err = json.Unmarshal(jobRaw, &job); err != nil {
		t.Fatal(err)
	}
	if job.Message.Hash != m.Hash {
		t.Fatal("file request used projected rather than immutable original hash")
	}
	if err = a.serveGroupHistoryFile(tctx(t), request.ID, phone.Address, conv, job); err != nil {
		t.Fatal(err)
	}
	var offer envelope.Envelope
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub='file'`, phone.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &offer); err != nil {
		t.Fatal(err)
	}
	if err = a.uploadAll(tctx(t), offer); err != nil {
		t.Fatal(err)
	}
	if err = a.hub.do(tctx(t), "POST", "/v1/messages", offer, nil); err != nil {
		t.Fatal(err)
	}
	if err = phone.accept(tctx(t), offer); err != nil {
		t.Fatal(err)
	}
	paths, err := phone.Download(tctx(t), original.ID, t.TempDir(), false)
	if err != nil || len(paths) != 1 {
		t.Fatalf("download: %v %v", paths, err)
	}
	got, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("original exact file bytes differ")
	}
	for _, mode := range []string{"hash", "admission", "pending-reader"} {
		t.Run(mode, func(t *testing.T) {
			bad := m
			tx, err := a.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			switch mode {
			case "hash":
				bad.Hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "admission":
				bad.GroupAdmission = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "pending-reader":
				r, e := tx.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address)
				if e != nil {
					t.Fatal(e)
				}
				if n, e := r.RowsAffected(); e != nil || n != 1 {
					t.Fatalf("pending pin: %d %v", n, e)
				}
			}
			if e := a.groupFileAuthorized(tx, packet, phone.Address, phone.Self().Fingerprint(), bad); e == nil {
				t.Fatal("invalid own file accepted")
			}
		})
	}
}

func TestGroupHistoryForeignSelectedFileStillRevisionSensitive(t *testing.T) {
	w, carol, p, sent, _ := groupHistoryOfflineInvitation(t)
	deliverGroupLifecycleSubtype(t, carol, w.alice, envelope.SubGroupConsent)
	packet, err := w.alice.GroupContext(p.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := w.alice.groupHistorySources(w.alice.store.db, p.State.Conv, sent.LID, w.alice.Self().Fingerprint(), 0, 2)
	if err != nil || len(sources) != 1 {
		t.Fatalf("selected source %d %v", len(sources), err)
	}
	s := sources[0]
	f := s.item.Attachments[0]
	index := 0
	admission, err := groupMemberAdmission(w.alice.store.db, packet, carol.Address, carol.Self().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	m := fileMsg{V: 1, Type: "request", LID: s.item.LID, Author: s.item.FromKey, Hash: historyRef(p.State.Conv, s.item).Hash, Index: &index, Name: f.Name, Size: &f.Size, SHA256: f.SHA256, GroupAdmission: admission.Hash()}
	if err = w.alice.groupFileAuthorized(w.alice.store.db, packet, carol.Address, carol.Self().Fingerprint(), m); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	runAgent(t, carol)
	publishGroupFixtureCaps(t, w.bob, true)
	publishGroupFixtureCaps(t, carol, true)
	ref, err := w.alice.RefOf(p.State.Conv, sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.Revise(tctx(t), ref, "changed selected revision"); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.groupFileAuthorized(w.alice.store.db, packet, carol.Address, carol.Self().Fingerprint(), m); err == nil {
		t.Fatal("foreign selection used original fallback after revision")
	}
}
