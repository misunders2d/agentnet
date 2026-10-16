package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	if os.Getenv("AGENTNET_LIVE_STEERING_DIRECT") == "1" {
		for _, mode := range []string{"default", "named"} {
			if !t.Run("direct/"+mode, func(t *testing.T) { actualOwnedDirectSteering(t, name, optIn, mode == "named") }) {
				return
			}
		}
		return
	}
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
	// Explicit, private diagnostics for an opt-in actual native failure. Never
	// put native failure text (which can contain local information) in logs.
	if private := os.Getenv("AGENTNET_LIVE_STEERING_FAILURE"); private != "" {
		if !filepath.IsAbs(private) {
			t.Fatal("absolute private failure artifact required")
		}
		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			var failure struct {
				Harness  string `json:"harness"`
				State    string `json:"state"`
				Detail   string `json:"detail"`
				Attempts int    `json:"attempts"`
			}
			failure.Harness = name
			if err := a.store.db.QueryRow(`SELECT state,coalesce(detail,''),attempts FROM inbox WHERE id=?`, root.LID).Scan(&failure.State, &failure.Detail, &failure.Attempts); err != nil {
				t.Log("private native failure state unavailable")
				return
			}
			if len(failure.Detail) > 4096 {
				failure.Detail = failure.Detail[:4096]
			}
			data, err := json.Marshal(failure)
			if err != nil {
				t.Log("private native failure serialization unavailable")
				return
			}
			file, err := os.OpenFile(private, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Log("private native failure artifact unavailable")
				return
			}
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				t.Log("private native failure artifact incomplete")
			}
		})
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

// Direct mode uses real sender/recipient daemons for verified progress and
// carrier admission. Only the original worker is invoked explicitly; no
// correction exists before native readiness.
func actualOwnedDirectSteering(t *testing.T, name, optIn string, named bool) {
	t.Helper()
	binary := os.Getenv(optIn)
	if binary == "" {
		t.Skip("opt-in " + optIn + "=/absolute/verified/" + name)
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("absolute verified native executable required")
	}
	original := Harnesses[name]
	selected := original
	selected.bin = binary
	Harnesses[name] = selected
	t.Cleanup(func() { Harnesses[name] = original })
	w := newWorld(t, "")
	a, sender := w.bob, w.alice
	if err := a.Approve(sender.Address); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready := filepath.Join(dir, "owned-ready")
	nonce := protocol.NewID()[:8]
	command := fmt.Sprintf("printf READY > '%s'; sleep 12", ready)
	if name == "omp" {
		command = fmt.Sprintf("touch '%s'; sleep 12", ready)
	}
	prompt := fmt.Sprintf("Harmless owned steering qualification. Use your existing bash tool exactly once to run this exact command: %s . It writes only this disposable readiness file and sleeps once. The local human preauthorizes only this exact command in this working directory. If native permissions require upfront approval, request that exact approval before execution. Never retry a refusal. Do not read other files, use network, delegate, change settings, or run other commands. After it finishes answer exactly ORIGINAL_%s . Process any explicit correction before answering.", command, nonce)
	responder := Responder{Harness: name, Dir: dir, Timeout: 130 * time.Second}
	var target *envelope.Target
	var stamp *ExecutorStamp
	if named {
		local, err := a.CreateLocalAgent("owned direct qualification", responder)
		if err != nil {
			t.Fatal(err)
		}
		target = &envelope.Target{Address: a.Address, Fingerprint: a.Self().Fingerprint(), AgentID: local.ID}
		stamp, err = a.ResolveExecutorIn(a.store.db, local.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Start communication before the manually claimed worker. HumanOnly keeps
	// this fixture's recipient daemon from independently scheduling a second job;
	// it does not alter the selected native child's settings or permissions.
	stopSender := runAgent(t, sender)
	defer stopSender()
	daemonCtx, stopRecipient := context.WithCancel(t.Context())
	recipientDone := make(chan struct{})
	go func() { defer close(recipientDone); _ = a.Run(daemonCtx, RunOptions{HumanOnly: true}) }()
	defer func() { stopRecipient(); <-recipientDone }()
	eventually(t, "recipient status daemon ready", func() bool { return a.statusLive.Load() })
	root := sealTo(t, sender, a, envelope.Inner{Kind: envelope.KindQuestion, Body: prompt, Target: target})
	inner, err := envelope.Open(root, a.id, a.Address, sender.Self())
	if err != nil {
		t.Fatal(err)
	}
	if err = sender.store.addOutbox(root, inner, "", nil, boundOutgoing{fingerprint: a.Self().Fingerprint()}); err != nil {
		t.Fatal(err)
	}
	if _, err = sender.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, protocol.StateCustody, root.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "real signed direct root delivery", func() bool {
		var n int
		return a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, root.ID).Scan(&n) == nil && n == 1
	})
	var exact bool
	if err = a.store.db.QueryRow(`SELECT conv IS NULL AND pid IS NULL AND receiver_route IS NULL AND verified_by=? AND replica=0 FROM inbox WHERE id=?`, sender.Self().Fingerprint(), root.ID).Scan(&exact); err != nil || !exact {
		t.Fatal("direct signed NULL conversation/PID admission missing", err)
	}
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,last_attempt_at=unixepoch() WHERE id=?`, stateRunning, root.ID); err != nil {
		t.Fatal(err)
	}
	a.noteStatus(root.ID) // same durable status trigger as the normal scheduler
	parent := job{ID: root.ID, From: sender.Address, Key: sender.Self().Fingerprint(), Kind: envelope.KindQuestion, Body: prompt, Target: target, Executor: stamp, nativeAttempt: 1}
	if target != nil {
		parent.AgentID = target.AgentID
	}
	// Only an explicit private owner-only diagnostic receives native failure text.
	if private := os.Getenv("AGENTNET_LIVE_STEERING_FAILURE"); private != "" {
		if !filepath.IsAbs(private) {
			t.Fatal("absolute private failure artifact required")
		}
		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			var state, detail string
			var attempts int
			if a.store.db.QueryRow(`SELECT state,coalesce(detail,''),attempts FROM inbox WHERE id=?`, root.ID).Scan(&state, &detail, &attempts) != nil {
				return
			}
			if len(detail) > 4096 {
				detail = detail[:4096]
			}
			data, _ := json.Marshal(map[string]any{"harness": name, "named": named, "state": state, "attempts": attempts, "detail": detail})
			f, e := os.OpenFile(private, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e == nil {
				_, _ = f.Write(data)
				_ = f.Close()
			}
		})
	}
	eventually(t, "recipient capability advertised", func() bool { return a.requireAgentIdentity(t.Context(), sender.Self()) == nil })
	senderRunning := func() bool {
		h, e := sender.followupOriginal(sender.store.db, "", envelope.Ref{ID: root.ID, Fingerprint: parent.Key})
		if e != nil {
			return false
		}
		v, e := sender.followupExecution(h.h, "")
		return e == nil && v != nil && v.State == stateRunning && v.Attempt == 1 && !v.Stale
	}
	evidence := newNativeActivityEvidence(name, named)
	evidenceDir := os.Getenv("AGENTNET_LIVE_NATIVE_ACTIVITY_EVIDENCE")
	if evidenceDir != "" {
		if err := nativeActivityEvidenceDirectory(evidenceDir); err != nil {
			t.Fatal("private native activity evidence directory unavailable")
		}
	}
	t.Cleanup(func() {
		t.Logf("native activity evidence thinking=%d fallback=%d input=%d unsafe=%d mismatch=%d samples=%d thinking_absent=%t attempt_match=%t request_match=%t target_match=%t stale_seen=%t", len(evidence.Thinking), len(evidence.Fallback), evidence.InputCount, evidence.Unsafe, evidence.Mismatch, evidence.SamplesTotal, evidence.ThinkingAbsent, evidence.AttemptMatch, evidence.RequestMatch, evidence.TargetMatch, evidence.StaleSeen)
		if evidenceDir != "" && writeNativeActivityEvidence(evidenceDir, evidence) != nil {
			t.Error("private native activity evidence artifact unavailable")
		}
	})
	observe := func() {
		ref := envelope.Ref{ID: root.ID, Fingerprint: parent.Key}
		h, err := sender.followupOriginal(sender.store.db, "", ref)
		var v *ExecView
		if err == nil {
			v, err = sender.followupExecution(h.h, "")
		}
		if err != nil {
			v = nil
		}
		evidence.observe(h, v, ref, a.Address, a.Self().Fingerprint(), target)
	}
	eventually(t, "sender verified current running attempt", senderRunning)
	observe()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); a.runJob(ctx, parent, &responder, nil) }()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	approved := ""
	checkApproval := func() {
		n := a.nativeRequestFor(parent.ID)
		if n == nil {
			return
		}
		if approved == n.Token {
			return
		}
		if name != "codex" || approved != "" || n.Request != parent.ID || n.Key != parent.Key || n.Attempt != 1 || n.Harness != name || n.AgentID != parent.AgentID || n.Thread == "" || n.Turn == "" || n.Item == "" || n.Unsupported != "" || n.CancelRun {
			t.Fatal("unexpected native precondition; no response or retry")
		}
		wrapped := "/usr/bin/bash -lc " + strconv.Quote(command) + "\nNative execution environment: local"
		if err := nativeFixtureApprovalError(n, command, wrapped, dir); err != nil {
			_ = writeNativeSteeringRefusalDiagnostic(os.Getenv("AGENTNET_LIVE_CODEX_STEERING_REFUSAL"), n, command, wrapped)
			t.Fatal(err)
		}
		if _, e := os.Stat(ready); !os.IsNotExist(e) {
			t.Fatal("readiness approval arrived after execution; no response")
		}
		if err := a.RespondNativeRequest(parent.ID, parent.Key, 1, nativeResponseText(n.Token, "allow"), protocol.NewID()); err != nil {
			t.Fatal("exact native readiness approval refused; no retry", err)
		}
		approved = n.Token
	}
	wait := func(what string, check func() bool) {
		t.Helper()
		for !check() {
			checkApproval()
			observe()
			select {
			case <-done:
				t.Fatalf("native job ended before %s; no retry", what)
			case <-ctx.Done():
				t.Fatalf("%s timed out; no retry", what)
			case <-ticker.C:
			}
		}
	}
	wait("owned readiness file", func() bool {
		if name == "omp" {
			info, e := os.Stat(ready)
			return e == nil && info.Mode().IsRegular()
		}
		b, e := os.ReadFile(ready)
		return e == nil && string(b) == "READY"
	})
	wait("sender verified current running attempt", senderRunning)
	var before, after int
	if err = a.store.db.QueryRow(`SELECT run_pgid FROM inbox WHERE id=?`, root.ID).Scan(&before); err != nil || before == 0 {
		t.Fatal("native process proof missing", err)
	}
	t.Logf("direct readiness harness=%s named=%t root=%s pgid_before=%d sender_current_attempt=1", name, named, root.ID, before)
	correction := protocol.NewID()
	body := "Correction to this same still-running request: replace the final answer with CORRECTED_ followed by the reverse of the original nonce. Do not run the command again; preserve original context."
	if _, err = sender.QueueRequestFollowup(ctx, RequestFollowup{Conv: "", Ref: envelope.Ref{ID: root.ID, Fingerprint: parent.Key}, Attempt: 1, ID: correction, Body: body}); err != nil {
		t.Fatal("real sender correction rejected", err)
	}
	var raw []byte
	if err = sender.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, correction).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var carrier envelope.Envelope
	if json.Unmarshal(raw, &carrier) != nil {
		t.Fatal("correction carrier malformed")
	}
	correctionInner, err := envelope.Open(carrier, a.id, a.Address, sender.Self())
	if err != nil || carrier.ID != correction || correctionInner.Conv != "" || correctionInner.Followup == nil || correctionInner.Followup.Attempt != 1 || correctionInner.Followup.Ref.ID != root.ID || correctionInner.Followup.Ref.Fingerprint != parent.Key || !sameFollowupTarget(correctionInner.Target, target) {
		t.Fatal("exact signed sender correction carrier changed", err)
	}
	wait("native consumption", func() bool { s, _ := a.store.jobState(correction); return s == stateSteered })
	if err = a.store.db.QueryRow(`SELECT run_pgid FROM inbox WHERE id=?`, root.ID).Scan(&after); err != nil || after != before {
		t.Fatal("original native process changed", err)
	}
	for waiting := true; waiting; {
		select {
		case <-done:
			observe()
			waiting = false
		case <-ctx.Done():
			t.Fatal("owned request did not finish")
		case <-ticker.C:
			observe()
		}
	}
	state, err := a.store.jobState(root.ID)
	if err != nil || state != stateAnswered {
		t.Fatal("original request did not answer", state, err)
	}
	chars := []byte(nonce)
	for i, j := 0, len(chars)-1; i < j; i, j = i+1, j-1 {
		chars[i], chars[j] = chars[j], chars[i]
	}
	expected := "CORRECTED_" + string(chars)
	var count, attempts int
	var answerID, result string
	if err = a.store.db.QueryRow(`SELECT count(DISTINCT coalesce(lid,id)),coalesce(max(id),''),coalesce(max(body),'') FROM outbox WHERE reply_to=? AND json_extract(envelope,'$.kind')=?`, root.ID, envelope.KindAnswer).Scan(&count, &answerID, &result); err != nil || count != 1 || strings.TrimSpace(result) != expected {
		t.Fatalf("expected exactly one exact corrected answer; count=%d matched=%t err=%v", count, strings.TrimSpace(result) == expected, err)
	}
	if err = a.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, root.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("original repeated", attempts, err)
	}
	// Real daemon delivery, signature verification and receiver admission. Never
	// manually receive the answer a second time to mask a missing verified row.
	for {
		var author, agentID, body string
		e := sender.store.db.QueryRow(`SELECT verified_by,coalesce(agent_id,''),body FROM inbox WHERE id=? AND kind=? AND state!='quarantined'`, answerID, envelope.KindAnswer).Scan(&author, &agentID, &body)
		if e == nil && author == a.Self().Fingerprint() && agentID == parent.AgentID && strings.TrimSpace(body) == expected {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("sender did not admit signed corrected answer")
		case <-ticker.C:
		}
	}
	if _, found, e := a.claimNativeFollowup(parent); e != nil || found {
		t.Fatal("finished correction replayed", found, e)
	}
	t.Logf("direct harness=%s named=%t root=%s correction=%s answer=%s pgid_before=%d pgid_after=%d attempts=1 signed_sender_admission=true exact_nonce_reversal=true no_replay=true executor=%s", name, named, root.ID, correction, answerID, before, after, parent.AgentID)
}

// Test-only public projection evidence. No raw native event, identifier or
// rejected detail is retained; the same helper is used by passive observation.
type nativeActivityEvidence struct {
	Harness        string   `json:"harness"`
	Named          bool     `json:"named"`
	Thinking       []string `json:"thinking"`
	Fallback       []string `json:"fallback"`
	ThinkingCount  int      `json:"thinking_count"`
	FallbackCount  int      `json:"fallback_count"`
	InputSeen      bool     `json:"input_seen"`
	InputCount     int      `json:"input_count"`
	Unsafe         int      `json:"unsafe"`
	Mismatch       int      `json:"mismatch"`
	AttemptMatch   bool     `json:"attempt_match"`
	RequestMatch   bool     `json:"request_match"`
	TargetMatch    bool     `json:"target_match"`
	StaleSeen      bool     `json:"stale_seen"`
	SamplesTotal   int      `json:"samples_total"`
	ThinkingAbsent bool     `json:"thinking_absent"`
}

func newNativeActivityEvidence(harness string, named bool) *nativeActivityEvidence {
	return &nativeActivityEvidence{Harness: harness, Named: named, Thinking: []string{}, Fallback: []string{}, ThinkingAbsent: true}
}

func (e *nativeActivityEvidence) observe(original followupOriginal, v *ExecView, ref envelope.Ref, recipient, key string, target *envelope.Target) {
	e.SamplesTotal++
	requestOK := ref.ID != "" && ref.Fingerprint != "" && original.h.ID == ref.ID && original.h.FromKey == ref.Fingerprint
	attemptOK := v != nil && v.Attempt == 1
	targetOK := recipient != "" && key != "" && original.to == recipient && original.key == key && v != nil && v.Host == recipient && sameFollowupTarget(original.h.Target, target)
	if original.h.Target != nil {
		targetOK = targetOK && original.h.Target.Address == recipient && original.h.Target.Fingerprint == key
	}
	e.RequestMatch = e.RequestMatch || requestOK
	e.AttemptMatch = e.AttemptMatch || attemptOK
	e.TargetMatch = e.TargetMatch || targetOK
	e.StaleSeen = e.StaleSeen || v != nil && v.Stale
	if !requestOK || !attemptOK || !targetOK || v.State != stateRunning || v.Stale {
		e.Mismatch++
		return
	}
	label, stamp, timed := strings.Cut(v.Detail, "\nReported at ")
	if timed {
		at, err := time.Parse(time.RFC3339, stamp)
		if err != nil || at.UTC().Format(time.RFC3339) != stamp {
			e.Unsafe++
			return
		}
	}
	if label == nativeInputDetail {
		e.InputSeen = true
		e.InputCount++
		return
	}
	if !publicActivityDetail(v.Detail) {
		e.Unsafe++
		return
	}
	labels := &e.Fallback
	if strings.HasPrefix(label, "Thinking: ") {
		e.ThinkingCount++
		e.ThinkingAbsent = false
		labels = &e.Thinking
	} else {
		e.FallbackCount++
	}
	if len(*labels) < 16 && (len(*labels) == 0 || (*labels)[len(*labels)-1] != label) {
		*labels = append(*labels, label)
	}
}

func nativeActivityEvidenceDirectory(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("absolute private evidence directory required")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0700 != 0700 {
		return errors.New("existing private evidence directory required")
	}
	return nil
}

func writeNativeActivityEvidence(dir string, evidence *nativeActivityEvidence) error {
	switch evidence.Harness {
	case "pi", "omp", "claude", "codex":
	default:
		return errors.New("unknown evidence harness")
	}
	if err := nativeActivityEvidenceDirectory(dir); err != nil {
		return err
	}
	mode := "default"
	if evidence.Named {
		mode = "named"
	}
	data, err := json.Marshal(evidence)
	if err != nil || len(data) > 16*1024 {
		return errors.New("bounded public evidence required")
	}
	f, err := os.OpenFile(filepath.Join(dir, evidence.Harness+"-"+mode+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
