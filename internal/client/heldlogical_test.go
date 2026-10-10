package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

// The causes a flood of held copies actually had (t5-held): each waits on a
// different thing, so none may collapse into the context_unavailable
// catch-all; unmapped proof waits still do, and arbitrary text never persists.
func TestHeldNoticePreciseCauses(t *testing.T) {
	for why, want := range map[string]string{
		"named participation has no unambiguous invitation":                           "participation_invite_unresolved",
		"external output history has no unambiguous invitation":                       "participation_invite_unresolved",
		"human turn waits for its original invitation root":                           "participation_invite_unresolved",
		"the target's sender key is no member's (yet)":                                "control_target_unknown_key",
		ErrGroupContextPending.Error():                                                "group_context_unavailable",
		"the conversation is not here (yet)":                                          "conversation_unavailable",
		"only the person who sent a message edits or deletes it":                      "control_not_author",
		"group: only verified original members forward historical participation ends": "history_forwarder_not_member",
		errGroupRecipientNotCurrent.Error():                                           "recipient_not_current_member",
		errTooManyEvents.Error():                                                      "participation_events_limit",
	} {
		reason := reasonProof
		if strings.HasPrefix(want, "control_not") || strings.HasPrefix(want, "history_forwarder") || strings.HasPrefix(want, "recipient_not") || strings.HasPrefix(want, "participation_events") {
			reason = reasonInvalid
		}
		if got := heldFailureCode(reason, why); got != want {
			t.Errorf("%q held as %q, want %q", why, got, want)
		}
	}
	if got := heldFailureCode(reasonProof, "SYNTHETIC_PRIVATE_BODY password=secret"); got != "context_unavailable" {
		t.Fatalf("unmapped proof wait: %q", got)
	}
}

// A hidden proof wait that now waits on something more precise is not a new
// problem: before precise causes every wait was context_unavailable, and an
// archived one stayed archived. A refusal (invalid) or a safety hold still
// resurfaces.
func TestHeldNoticeArchivedProofWaitStaysArchivedAcrossCauses(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	env := envelope.Envelope{ID: protocol.NewID(), From: "admin/bezos"}
	archived := func() (n int) {
		t.Helper()
		if err := s.db.QueryRow(`SELECT notice_archived FROM quarantine WHERE id=?`, env.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := s.holdOpened(env, reasonProof, "SYNTHETIC unmapped wait", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.archiveHeldNotice(env.ID); err != nil {
		t.Fatal(err)
	}
	for _, why := range []string{"named participation has no unambiguous invitation", ErrGroupContextPending.Error(), "the conversation is not here (yet)"} {
		if err := s.holdOpened(env, reasonProof, why, "a"); err != nil {
			t.Fatal(err)
		}
		if archived() != 1 {
			t.Fatalf("archived proof wait resurfaced for %q", why)
		}
	}
	if err := s.holdOpened(env, reasonInvalid, "group: conflicting logical turn", "a"); err != nil || archived() != 0 {
		t.Fatalf("refusal stayed hidden: %v", err)
	}
}

// heldVector is one shared logical-key vector (internal/ui/testdata/
// held_logical.json): the browser engine must compute the same key.
type heldVector struct {
	Inner envelope.Inner `json:"inner"`
	FP    string         `json:"fp"`
	Key   string         `json:"key"`
}

func TestHeldLogicalKeyVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "ui", "testdata", "held_logical.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []heldVector
	if err := json.Unmarshal(data, &vectors); err != nil || len(vectors) < 4 {
		t.Fatalf("vectors: %d %v", len(vectors), err)
	}
	for i, v := range vectors {
		if got := heldLogicalKey(v.Inner, v.FP); got != v.Key {
			t.Errorf("vector %d: %s, want %s", i, got, v.Key)
		}
	}
}

// An older own producer re-sends the same carried record under new envelope
// and carrier IDs every time it wakes (t5-held: 4 records, 866 copies).
// Every copy stays retained and listed, and each names the logical record it
// carries, so a notice can say "4 records" instead of "866 problems".
func TestHeldNoticeNamesLogicalRecordOfEachCopy(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	stop()
	_, raw := rootOf(t, w.alice, conv)
	other := strings.Repeat("e", 64) // the root names another conversation: held after the envelope opened
	item := func(id, lid string) string {
		body, _ := json.Marshal(HistoryItem{V: 1, From: w.alice.Address, FromKey: w.alice.Self().Fingerprint(), ID: id, LID: lid, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "SECRET_HELD_BODY", At: time.Now().UnixMilli()})
		return string(body)
	}
	first, second := item(protocol.NewID(), protocol.NewID()), item(protocol.NewID(), protocol.NewID())
	turnLID := protocol.NewID()
	var ids []string
	for _, in := range []envelope.Inner{
		{Kind: envelope.KindMessage, Sub: envelope.SubHistory, Replica: true, Conv: other, Root: raw, LID: protocol.NewID(), Body: first},
		{Kind: envelope.KindMessage, Sub: envelope.SubHistory, Replica: true, Conv: other, Root: raw, LID: protocol.NewID(), Body: first}, // re-sent
		{Kind: envelope.KindMessage, Sub: envelope.SubHistory, Replica: true, Conv: other, Root: raw, LID: protocol.NewID(), Body: second},
		{Kind: envelope.KindMessage, Conv: other, Root: raw, LID: turnLID, Body: "SECRET_HELD_BODY"},
		{Kind: envelope.KindMessage, Conv: other, Root: raw, LID: turnLID, Body: "SECRET_HELD_BODY"}, // a per-device copy of the same turn
	} {
		env := craft(t, w.alice, w.bob, in)
		if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if got := heldReason(t, w.bob, env.ID); got != reasonInvalid {
			t.Fatalf("setup: held %q", got)
		}
		ids = append(ids, env.ID)
	}
	q, err := w.bob.Quarantine()
	if err != nil || len(q) != len(ids) {
		t.Fatalf("every copy stays listed: %+v %v", q, err)
	}
	key := map[string]string{}
	for _, h := range q {
		if len(h.Logical) != 32 || h.Size <= 0 || strings.Contains(h.Logical, "SECRET") {
			t.Fatalf("copy %s: logical %q size %d", h.ID, h.Logical, h.Size)
		}
		key[h.ID] = h.Logical
	}
	if key[ids[0]] != key[ids[1]] || key[ids[0]] == key[ids[2]] || key[ids[3]] != key[ids[4]] || key[ids[3]] == key[ids[0]] {
		t.Fatalf("copies not named by their logical record: %v", key)
	}
	// A later hold without an opened envelope keeps the record it named.
	var env envelope.Envelope
	var stored string
	if err := w.bob.store.db.QueryRow(`SELECT envelope FROM quarantine WHERE id=?`, ids[0]).Scan(&stored); err != nil || json.Unmarshal([]byte(stored), &env) != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.holdAs(env, reasonInvalid); err != nil {
		t.Fatal(err)
	}
	var logical string
	if err := w.bob.store.db.QueryRow(`SELECT logical FROM quarantine WHERE id=?`, ids[0]).Scan(&logical); err != nil || logical != key[ids[0]] {
		t.Fatalf("re-hold lost logical record: %q %v", logical, err)
	}
	var n int
	if err := w.bob.store.db.QueryRow(`SELECT (SELECT count(*) FROM inbox WHERE conv=?)+(SELECT count(*) FROM outbox WHERE conv=?)`, other, other).Scan(&n); err != nil || n != 0 {
		t.Fatalf("held copies admitted: %d %v", n, err)
	}
}

// Rows held before the logical step keep working: no invented record, and
// their size still lets a notice estimate how many records they carry.
func TestHeldLogicalMigrationKeepsLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.db")
	step := -1
	for i, sql := range schema {
		if sql == heldLogicalSchema {
			step = i
			break
		}
	}
	if step < 0 {
		t.Fatal("held logical schema step missing")
	}
	db, err := sqlitedb.Open(path, schema[:step])
	if err != nil {
		t.Fatal(err)
	}
	const raw = `{"v":2,"id":"0123456789abcdef0123456789abcdef","ct":"SYNTHETIC"}`
	if _, err = db.Exec(`INSERT INTO quarantine(id,sender,reason,envelope,received_at,acked,detail_code) VALUES(?,?,'invalid',?,1,1,'participation_binding_mismatch')`, "0123456789abcdef0123456789abcdef", "admin/bezos", raw); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	q, err := (&Agent{store: s}).Quarantine()
	if err != nil || len(q) != 1 || q[0].Logical != "" || q[0].Size != len(raw) || q[0].DetailCode != "participation_binding_mismatch" {
		t.Fatalf("legacy row: %+v %v", q, err)
	}
}
