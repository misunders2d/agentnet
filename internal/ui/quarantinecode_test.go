package ui

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

// TestQuarantineCode: a held-back message carries a code that says why
// (QuarantineItem.Code), the same in the daemon (holdCode over the
// client's quarantine reasons) and in the browser engine, so a skin words
// it itself with the sender's name instead of the address in Reason. Any
// reason not named is "unverified": its content is never shown.
func TestQuarantineCode(t *testing.T) {
	reasons := map[string]string{
		"key_changed":           HoldKeyChanged,
		"proof_pending":         HoldProof,
		"identity_conflict":     HoldConflict,
		"conflicting_duplicate": HoldDuplicate,
		"invalid":               HoldUnverified,
		"":                      HoldUnverified,
		"a_reason_added_later":  HoldUnverified,
	}
	for reason, want := range reasons {
		if got := holdCode(reason); got != want {
			t.Errorf("holdCode(%q) = %q, want %q", reason, got, want)
		}
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
	input, _ := json.Marshal(map[string]any{"reasons": reasons})
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
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil || got.Checks < len(reasons)+4 {
		t.Fatalf("checks %d, %v\n%s%s", got.Checks, err, stdout.Bytes(), stderr.Bytes())
	}
}
