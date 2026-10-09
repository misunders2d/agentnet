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
	for _, code := range []string{"participation_binding_mismatch", "envelope_malformed", "envelope_verification_failed", "recipient_mismatch", "sender_key_unavailable", "history_malformed", "context_unavailable", "admission_failed", "group_invitation_outdated", "group_consent_mismatch", "group_withdrawal_mismatch", "group_admission_unavailable", "group_authority_conflict", "group_conflicting_copy", "history_reader_not_member", "captured_consent_mismatch", "", "unknown_future_code"} {
		detail, recovery := heldNoticeText(code, "invalid")
		diagnostics[code] = map[string]string{"detail": detail, "recovery": recovery}
	}
	_, proofRecovery := heldNoticeText("", "proof_pending")
	input, _ := json.Marshal(map[string]any{"reasons": reasons, "diagnostics": diagnostics, "proofRecovery": proofRecovery})
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
