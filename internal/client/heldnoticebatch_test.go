package client

import (
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestArchiveHeldNoticeSnapshot(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{store: s}
	add := func(reason string) string {
		t.Helper()
		id := protocol.NewID()
		if err := s.holdAs(envelope.Envelope{ID: id, From: "claimed/device"}, reason); err != nil {
			t.Fatal(err)
		}
		return id
	}
	invalid, proof, changed := add(reasonInvalid), add(reasonProof), add(reasonInvalid)
	refs := []HeldNoticeRef{{ID: invalid, Reason: reasonInvalid}, {ID: proof, Reason: reasonProof}, {ID: changed, Reason: reasonInvalid}}
	newer := add(reasonInvalid)
	if _, err = s.db.Exec(`UPDATE quarantine SET detail_code='admission_failed' WHERE id=?`, changed); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{2, 0} {
		n, e := a.ArchiveHeldNotices(refs)
		if e != nil || n != want {
			t.Fatalf("pass %d: archived %d want %d: %v", i, n, want, e)
		}
	}
	for _, id := range []string{changed, newer} {
		var archived bool
		if err := s.db.QueryRow(`SELECT notice_archived FROM quarantine WHERE id=?`, id).Scan(&archived); err != nil || archived {
			t.Fatalf("hid unchosen/changed notice: %s %v", id, err)
		}
	}
	var retained, jobs, receipts int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM quarantine),(SELECT count(*) FROM inbox)+(SELECT count(*) FROM outbox),(SELECT sum(acked) FROM quarantine)`).Scan(&retained, &jobs, &receipts); err != nil || retained != 4 || jobs != 0 || receipts != 0 {
		t.Fatal("archive changed admission or receipts", retained, jobs, receipts, err)
	}
	if _, err := a.ArchiveHeldNotices([]HeldNoticeRef{{ID: changed, Reason: reasonKeyChanged}}); err == nil {
		t.Fatal("security hold archived")
	}
	if _, err := a.ArchiveHeldNotices(make([]HeldNoticeRef, MaxHeldNoticeBatch+1)); err == nil {
		t.Fatal("unbounded archive")
	}
}
