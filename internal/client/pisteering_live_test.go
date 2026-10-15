package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// One actual native request through the AgentNet worker, opt-in only. The
// executable is explicitly selected for this owned fixture; native resources,
// extensions and permission decisions remain inherited.
func TestPiActualOwnedSteering(t *testing.T) {
	actualOwnedSteering(t, "pi", "AGENTNET_LIVE_PI_STEERING")
}
func TestClaudeActualOwnedSteering(t *testing.T) {
	actualOwnedSteering(t, "claude", "AGENTNET_LIVE_CLAUDE_STEERING")
}
func TestOMPActualOwnedSteering(t *testing.T) {
	actualOwnedSteering(t, "omp", "AGENTNET_LIVE_OMP_STEERING")
}
func actualOwnedSteering(t *testing.T, name, optIn string) {
	t.Helper()
	binary := os.Getenv(optIn)
	if binary == "" {
		t.Skip("opt-in " + optIn + "=/absolute/verified/" + name)
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("absolute verified native executable required")
	}
	selected := Harnesses[name]
	original := selected
	selected.bin = binary
	Harnesses[name] = selected
	t.Cleanup(func() { Harnesses[name] = original })
	w, _, packet, _ := groupTurnsFixture(t)
	a := w.alice
	participant := p6Member(t, a, a, packet.State.Conv)
	dir := t.TempDir()
	ready := filepath.Join(dir, "owned-ready")
	nonce := protocol.NewID()[:8]
	command := fmt.Sprintf("printf READY > '%s'; sleep 12", ready)
	prompt := fmt.Sprintf("Harmless owned steering qualification. Use your existing bash tool exactly once to run this exact command: %s . It writes only this disposable readiness file. Do not read other files, use network, delegate, change settings, or run other commands. After it finishes answer exactly ORIGINAL_%s .", command, nonce)
	root, err := a.AskAgent(t.Context(), participant.PID, envelope.KindQuestion, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,last_attempt_at=unixepoch() WHERE id=?`, stateRunning, root.LID); err != nil {
		t.Fatal(err)
	}
	var target string
	if err = a.store.db.QueryRow(`SELECT target FROM inbox WHERE id=?`, root.LID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	parent := job{ID: root.LID, From: a.Address, Key: a.Self().Fingerprint(), Kind: envelope.KindQuestion, Body: prompt, Conv: packet.State.Conv, PID: participant.PID, Local: true}
	if err = json.Unmarshal([]byte(target), &parent.Target); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.runJob(ctx, parent, &Responder{Harness: name, Dir: dir, Timeout: 130 * time.Second}, nil)
	}()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	wait := func(what string, check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-done:
				t.Fatalf("native job ended before %s", what)
			case <-ctx.Done():
				t.Fatalf("%s timed out; no retry", what)
			case <-ticker.C:
			}
		}
	}
	wait("owned readiness file", func() bool { b, e := os.ReadFile(ready); return e == nil && string(b) == "READY" })
	var beforePID int
	if err = a.store.db.QueryRow(`SELECT run_pgid FROM inbox WHERE id=?`, root.LID).Scan(&beforePID); err != nil {
		t.Fatal(err)
	}
	correction := protocol.NewID()
	ref := envelope.Ref{ID: root.LID, Fingerprint: a.Self().Fingerprint()}
	body := "Correction to this same still-running request: replace the final answer with CORRECTED_ followed by the reverse of the original nonce. Do not run the command again; preserve original context."
	if _, err = a.QueueRequestFollowup(ctx, RequestFollowup{Conv: parent.Conv, Ref: ref, Attempt: 1, ID: correction, Body: body}); err != nil {
		t.Fatal(err)
	}
	wait("native consumption", func() bool { state, _ := a.store.jobState(correction); return state == stateSteered })
	var afterPID int
	if err = a.store.db.QueryRow(`SELECT run_pgid FROM inbox WHERE id=?`, root.LID).Scan(&afterPID); err != nil {
		t.Fatal(err)
	}
	if beforePID == 0 || beforePID != afterPID {
		t.Fatal("native process changed", beforePID, afterPID)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("owned job did not finish")
	}
	state, err := a.store.jobState(root.LID)
	if err != nil || state != stateAnswered {
		t.Fatal("original job did not answer", state, err)
	}
	chars := []byte(nonce)
	for i, j := 0, len(chars)-1; i < j; i, j = i+1, j-1 {
		chars[i], chars[j] = chars[j], chars[i]
	}
	expected := "CORRECTED_" + string(chars)
	var count int
	var result string
	if err = a.store.db.QueryRow(`SELECT count(DISTINCT coalesce(lid,id)),coalesce(max(body),'') FROM outbox WHERE reply_to=? AND kind=?`, root.LID, envelope.KindAnswer).Scan(&count, &result); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !strings.Contains(result, expected) {
		t.Fatalf("expected one correlated corrected result, count=%d matched=%t", count, strings.Contains(result, expected))
	}
	var attempts int
	if err = a.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, root.LID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("original reran", attempts, err)
	}
	t.Logf("actual %s consumed strict correction in same owned worker process, preserved original-only nonce, exactly one correlated answer; attempts=1", name)
}
