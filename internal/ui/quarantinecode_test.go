package ui

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// TestQuarantineCode: a held-back message carries a code that says why
// (QuarantineItem.Code), the same in the daemon (holdCode over the
// client's quarantine reasons, on every overview item) and in the browser
// engine, so a skin words it itself with the sender's name instead of the
// address in Reason. Any reason not named is "unverified": its content is
// never shown.
func TestQuarantineCode(t *testing.T) {
	reasons := map[string]string{
		"key_changed":           HoldKeyChanged,
		"proof_pending":         HoldProof,
		"identity_conflict":     HoldConflict,
		"conflicting_duplicate": HoldDuplicate,
		"invalid":               HoldInvalid,
		"":                      HoldUnverified,
		"a_reason_added_later":  HoldUnverified,
	}
	for reason, want := range reasons {
		if got := holdCode(reason); got != want {
			t.Errorf("holdCode(%q) = %q, want %q", reason, got, want)
		}
	}
	if text := holdReason("invalid", "bob/desk"); strings.Contains(text, "did not verify") || !strings.Contains(text, "failed a check") || !strings.Contains(text, "contents stay hidden") {
		t.Fatalf("legacy invalid reasons must not claim failed signatures or reveal content: %q", text)
	}
	// The daemon's overview list carries each code (live.go quarantineItems):
	// a key_changed hold is what lets a page offer "Check and trust…".
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var held []client.Quarantined
	for reason := range reasons {
		held = append(held, client.Quarantined{ID: "held-" + reason, Sender: "alice/laptop", Reason: reason, ReceivedAt: at})
	}
	items := quarantineItems(held)
	if len(items) != len(held) {
		t.Fatalf("items %+v", items)
	}
	for _, q := range items {
		reason := strings.TrimPrefix(q.ID, "held-")
		if q.CanArchive != (reason == "invalid" || reason == "proof_pending") {
			t.Errorf("archive eligibility for %s: %v", reason, q.CanArchive)
		}
		if q.Code != reasons[reason] || q.Peer != "alice/laptop" || !q.At.Equal(at) || q.Reason != holdReason(reason, "alice/laptop") {
			t.Errorf("overview item for %q: %+v, want code %q", reason, q, reasons[reason])
		}
	}
	if empty, _ := json.Marshal(quarantineItems(nil)); string(empty) != "[]" {
		t.Errorf("no held messages: %s, want []", empty)
	}
	// The demo provider's held message says why too.
	o, err := NewFixture(time.Now).Overview()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range o.Quarantine {
		if q.Code == "" {
			t.Errorf("fixture quarantine item %s has no code", q.ID)
		}
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	diagnostics := map[string]map[string]string{}
	for _, code := range heldParityCodes {
		detail, recovery := heldNoticeText(code, "invalid")
		diagnostics[code] = map[string]string{"detail": detail, "recovery": recovery}
	}
	_, proofRecovery := heldNoticeText("", "proof_pending")
	// Every field of the daemon's held item, for each reason and cause: the
	// browser's (quarantineItem) must be the same, byte for byte.
	var parity []client.Quarantined
	for _, reason := range []string{"invalid", "proof_pending", "key_changed", "identity_conflict", "conflicting_duplicate", "a_reason_added_later"} {
		for i, code := range heldParityCodes {
			for _, logical := range []string{"", strings.Repeat("ab", 8)} {
				parity = append(parity, client.Quarantined{ID: "held", Sender: "admin/bezos", Reason: reason, DetailCode: code, ReceivedAt: at, Logical: logical, Size: 100 * (i % 3)})
			}
		}
	}
	input, _ := json.Marshal(map[string]any{"reasons": reasons, "diagnostics": diagnostics, "proofRecovery": proofRecovery, "parity": map[string]any{"held": parity, "items": quarantineItems(parity)}})
	cmd := exec.Command(node, "testdata/quarantine_code_check.mjs")
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	var got struct {
		Checks int `json:"checks"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil || got.Checks < 2*len(reasons)+4 {
		t.Fatalf("checks %d, %v\n%s%s", got.Checks, err, stdout.Bytes(), stderr.Bytes())
	}
}

// heldParityCodes: every cause either side words, plus legacy and unknown.
var heldParityCodes = []string{"participation_binding_mismatch", "envelope_malformed", "envelope_verification_failed", "recipient_mismatch", "sender_key_unavailable", "history_malformed", "context_unavailable", "admission_failed", "group_invitation_outdated", "group_consent_mismatch", "group_withdrawal_mismatch", "group_admission_unavailable", "group_authority_conflict", "group_conflicting_copy", "history_reader_not_member", "captured_consent_mismatch", "control_target_person_mismatch",
	"participation_invite_unresolved", "control_target_unknown_key", "group_context_unavailable", "conversation_unavailable", "control_not_author", "history_forwarder_not_member", "recipient_not_current_member", "participation_events_limit", "recipient_identity_changed", "", "unknown_future_code"}

// A held notice must say what is wrong and what can be done (t5-held): a
// copy that opened under the sender's own key names that device instead of
// "unverified sender"; each cause says who acts (update the sending device,
// or wait for an invitation or context); and the copies' logical record and
// size reach the page so it can count records, not envelopes.
func TestHeldNoticeActionable(t *testing.T) {
	at := time.Date(2026, 10, 10, 6, 40, 55, 0, time.UTC)
	item := func(reason, code, logical string, size int) QuarantineItem {
		return quarantineItems([]client.Quarantined{{ID: "0123456789abcdef0123456789abcdef", Sender: "admin/bezos", Reason: reason, DetailCode: code, ReceivedAt: at, Logical: logical, Size: size}})[0]
	}
	for _, c := range []struct {
		reason, code, action string
		verified             bool
	}{
		{"invalid", "participation_binding_mismatch", HeldUpdateSender, true}, // opened under bezos's key, then failed the binding check
		{"invalid", "history_forwarder_not_member", "", true},
		// The catch-all names no stage: v0.8.16 browsers stored the pre-open
		// "local recipient identity changed" under it (see below).
		{"invalid", "admission_failed", "", false},
		{"invalid", "envelope_verification_failed", "", false}, // never opened: only a claim
		{"invalid", "envelope_malformed", "", false},
		{"invalid", "recipient_mismatch", "", false},
		{"invalid", "sender_key_unavailable", "", false},
		{"invalid", "recipient_identity_changed", "", false},
		{"invalid", "", "", false}, // legacy: the stage is unknown
		{"invalid", "unknown_future_code", "", false},
		{"proof_pending", "participation_invite_unresolved", HeldWaitInvitation, true},
		{"proof_pending", "control_target_unknown_key", HeldWaitContext, true},
		{"proof_pending", "group_context_unavailable", HeldWaitContext, true},
		{"proof_pending", "conversation_unavailable", HeldWaitContext, true},
		{"proof_pending", "context_unavailable", HeldWaitContext, true}, // stored before precise codes
		{"proof_pending", "", HeldWaitContext, true},
		{"key_changed", "", "", false},
	} {
		q := item(c.reason, c.code, "", 0)
		if q.SenderVerified != c.verified || q.Action != c.action {
			t.Errorf("%s/%s: verified %v action %q, want %v %q", c.reason, c.code, q.SenderVerified, q.Action, c.verified, c.action)
		}
	}
	// Only a logical record, kept once a copy opened (holdOpened,
	// verifyAndStore; engine admitInner), proves the catch-all was a later
	// check; a cause recorded before opening outranks it.
	opened := strings.Repeat("ab", 16)
	if !item("invalid", "admission_failed", opened, 0).SenderVerified || item("invalid", "envelope_verification_failed", opened, 0).SenderVerified || item("invalid", "", opened, 0).SenderVerified {
		t.Errorf("a kept logical record decides only the catch-all")
	}
	for _, code := range []string{"participation_invite_unresolved", "control_target_unknown_key", "group_context_unavailable", "conversation_unavailable", "control_not_author", "control_target_person_mismatch", "history_forwarder_not_member", "recipient_not_current_member", "participation_events_limit", "recipient_identity_changed"} {
		detail, recovery := heldNoticeText(code, "proof_pending")
		generic, _ := heldNoticeText("", "invalid")
		if detail == "" || recovery == "" || detail == generic {
			t.Errorf("%s has no words of its own", code)
		}
	}
	if _, recovery := heldNoticeText("participation_binding_mismatch", "invalid"); !strings.Contains(recovery, "Update AgentNet on the sending device") {
		t.Errorf("binding mismatch names no action: %q", recovery)
	}
	q := item("invalid", "participation_binding_mismatch", strings.Repeat("ab", 16), 2840)
	if q.Logical != strings.Repeat("ab", 16) || q.Size != 2840 {
		t.Fatalf("record and size lost: %+v", q)
	}
	// Re-sent exact copies fold into one notice (client heldcopy.go): the
	// page gets their count and newest arrival with the record they repeat.
	last := at.Add(time.Hour)
	folded := quarantineItems([]client.Quarantined{{ID: "0123456789abcdef0123456789abcdef", Sender: "admin/bezos", Reason: "invalid", DetailCode: "participation_binding_mismatch", ReceivedAt: at, Logical: strings.Repeat("ab", 16), Size: 2840, Copies: 865, LastAt: &last}})[0]
	if folded.Copies != 865 || folded.LastAt == nil || !folded.LastAt.Equal(last) || folded.Logical == "" || !folded.SenderVerified || folded.Action != HeldUpdateSender {
		t.Fatalf("folded copies lost: %+v", folded)
	}
	if data, _ := json.Marshal(item("key_changed", "", "", 0)); strings.Contains(string(data), "sender_verified") || strings.Contains(string(data), "logical") || strings.Contains(string(data), "size") || strings.Contains(string(data), "action") || strings.Contains(string(data), "copies") || strings.Contains(string(data), "last_at") {
		t.Fatalf("empty fields not omitted: %s", data)
	}
}

// Classic and Zoom word a held message from its code with who() (never from
// Reason, which names the address), name every verified code, and their
// trust and approve dialogs send no one to a terminal.
func TestSkinsWordHeldBackFromCode(t *testing.T) {
	for _, skin := range []string{"classic", "zoom"} {
		src := readSources(t, "skins/"+skin+"/src", []string{".mjs"})
		for _, code := range []string{HoldKeyChanged, HoldProof, HoldConflict, HoldDuplicate} {
			if !regexp.MustCompile(`(?m)^  ` + code + `: \(`).MatchString(src) {
				t.Errorf("%s has no held-back words for %s", skin, code)
			}
		}
		if !strings.Contains(src, "heldWords[q.code](who(q.peer))") {
			t.Errorf("%s no longer words held-back rows from their code", skin)
		}
		for _, bad := range []string{"q.reason", "agentnet trust", "agentnet approve"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s still shows %q", skin, bad)
			}
		}
	}
}
