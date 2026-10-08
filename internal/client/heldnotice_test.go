package client

import (
	"fmt"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"path/filepath"
	"testing"
)

func TestHeldNoticeMigrationArchivePreservesInvalidDedup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.db")
	// Exercise the shipped schema before the appended step, not a reconstructed table.
	step := -1
	for i, sql := range schema {
		if sql == heldNoticeSchema {
			step = i
			break
		}
	}
	if step < 0 {
		t.Fatal("held notice schema step missing")
	}
	db, err := sqlitedb.Open(path, schema[:step])
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	const raw = "SYNTHETIC_INVALID_CIPHERTEXT"
	if _, err = db.Exec(`INSERT INTO quarantine(id,sender,reason,envelope,received_at,acked) VALUES(?,?,'invalid',?,1,1)`, id, "bob/laptop", raw); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{store: s}
	q, err := a.Quarantine()
	if err != nil || len(q) != 1 || q[0].DetailCode != "" {
		t.Fatalf("legacy cause invented: %+v %v", q, err)
	}
	for i := 0; i < 2; i++ {
		if err = a.ArchiveHeldNotice(id); err != nil {
			t.Fatal(err)
		}
	}
	q, err = a.Quarantine()
	if err != nil || len(q) != 0 {
		t.Fatalf("notice remains visible: %+v %v", q, err)
	}
	var envelope, reason string
	var acked int
	if err = s.db.QueryRow(`SELECT envelope,reason,acked FROM quarantine WHERE id=?`, id).Scan(&envelope, &reason, &acked); err != nil {
		t.Fatal(err)
	}
	if envelope != raw || reason != "invalid" || acked != 1 {
		t.Fatal("archive changed retained envelope or receipt")
	}
	// seen is the exact duplicate receive gate; archiving must not make the
	// invalid envelope eligible for admission or execution again.
	if seen, err := s.seen(id); err != nil || !seen {
		t.Fatalf("archive reopened duplicate: %v %v", seen, err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT (SELECT count(*) FROM inbox)+(SELECT count(*) FROM outbox)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("archive admitted or queued work: %d %v", n, err)
	}
}
func TestHeldNoticeDiagnosticPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	env := envelope.Envelope{ID: "fedcba9876543210fedcba9876543210", From: "bob/laptop"}
	if err = s.holdAsDiagnostic(env, reasonInvalid, "group: conflicting logical turn"); err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	q, err := (&Agent{store: s}).Quarantine()
	if err != nil || len(q) != 1 || q[0].DetailCode != "group_conflicting_copy" {
		t.Fatalf("lost safe cause: %+v %v", q, err)
	}
}
func TestHeldNoticeDiagnosticAllowlist(t *testing.T) {
	if heldDiagnosticCode("group: conflicting logical turn") != "group_conflicting_copy" {
		t.Fatal("known safe cause missing")
	}
	if heldDiagnosticCode("SYNTHETIC_PRIVATE_BODY password=secret") != "" {
		t.Fatal("arbitrary error content persisted")
	}
}

func TestHeldNoticeArchiveRejectsUnknownAndActionableReasons(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{store: s}
	if err = a.ArchiveHeldNotice("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != ErrNoMessage {
		t.Fatalf("unknown ID: %v", err)
	}
	for i, reason := range []string{reasonProof, reasonKeyChanged, reasonConflict, reasonDuplicate} {
		id := fmt.Sprintf("%032x", i+1)
		raw := "SYNTHETIC_CIPHERTEXT_" + reason
		if err = s.quarantine(id, "bob/laptop", reason, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		if err = a.ArchiveHeldNotice(id); err != ErrNoMessage {
			t.Fatalf("archived actionable %s: %v", reason, err)
		}
		var archived, acked int
		var gotRaw, gotReason string
		if err = s.db.QueryRow(`SELECT notice_archived,acked,envelope,reason FROM quarantine WHERE id=?`, id).Scan(&archived, &acked, &gotRaw, &gotReason); err != nil {
			t.Fatal(err)
		}
		if archived != 0 || acked != 0 || gotRaw != raw || gotReason != reason {
			t.Fatalf("refusal changed %s", reason)
		}
	}
	q, err := a.Quarantine()
	if err != nil || len(q) != 4 {
		t.Fatalf("actionable notices hidden: %+v %v", q, err)
	}
}
