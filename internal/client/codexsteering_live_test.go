package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Real installed Codex, one ordinary synthetic daemon, one selected executor,
// two fresh conversations and an actual turn/steer acknowledgment. Opt-in only:
// config/model/reasoning/tools/permissions stay inherited. Only each exact
// upfront readiness command may receive native approval; never retry a denial.
// Nonce files prove native tools ran; stored refs, reservations,
// attempts, staged bytes and the recipient's verified answer prove the journey.
func TestCodexActualIndependentSteering(t *testing.T) {
	if os.Getenv("AGENTNET_LIVE_STEERING_DIRECT") == "1" {
		actualOwnedSteering(t, "codex", "AGENTNET_LIVE_CODEX_STEERING")
		return
	}
	configured := os.Getenv("AGENTNET_LIVE_CODEX_STEERING")
	if configured == "" {
		t.Skip("opt-in: AGENTNET_LIVE_CODEX_STEERING=/absolute/path/to/codex")
	}
	if !filepath.IsAbs(configured) {
		t.Fatal("use an absolute installed Codex binary")
	}
	if runtime.GOOS == "windows" {
		t.Fatal("this native gate uses Unix shell sleep; Windows qualification is separate")
	}
	binary, err := filepath.EvalSymlinks(configured)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		t.Fatal("installed Codex binary unavailable", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	versionCtx, stopVersion := context.WithTimeout(ctx, 5*time.Second)
	version, err := exec.CommandContext(versionCtx, binary, "--version").Output()
	stopVersion()
	if err != nil {
		t.Fatal("installed Codex version unavailable", err)
	}
	t.Logf("installed %s; normal native config/model/reasoning/permissions inherited", strings.TrimSpace(string(version)))
	original := Harnesses["codex"]
	selected := original
	selected.bin = binary
	Harnesses["codex"] = selected
	// Fixture daemons/processes stop before the native adapter is restored.
	t.Cleanup(func() { Harnesses["codex"] = original })
	w, observer, packet, _ := groupTurnsFixture(t)
	dir := t.TempDir()
	agent, err := w.alice.CreateLocalAgent("native steering qualification", Responder{Harness: "codex", Dir: dir, Timeout: 150 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.PublishAgentCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	dm := newDM(t, w.alice, w.bob)
	convs := []string{dm, packet.State.Conv}
	ids, lids, pids, ready, tokens := make([]string, 2), make([]string, 2), make([]string, 2), make([]string, 2), make([]string, 2)
	commands, wrapped := make([]string, 2), make([]string, 2)
	var callbacks [2]int
	var approvals [2]string
	seenCallbacks := map[string]bool{}
	defer func() {
		t.Logf("genuine native readiness callbacks observed %d/%d; exact approvals submitted %t/%t", callbacks[0], callbacks[1], approvals[0] != "", approvals[1] != "")
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	wait := func(what string, condition func() bool) {
		t.Helper()
		for !condition() {
			select {
			case <-ctx.Done():
				t.Fatalf("%s: fixed 180-second native budget exhausted; no retry", what)
			case <-ticker.C:
			}
		}
	}
	type runProof struct {
		state, thread, executor, detail string
		attempt                         int
	}
	read := func(id string) runProof {
		t.Helper()
		var proof runProof
		if err := w.alice.store.db.QueryRowContext(ctx, `SELECT state,attempts,coalesce(json_extract(session_ref,'$.id'),''),coalesce(json_extract(executor,'$.agent_id'),''),coalesce(detail,'') FROM inbox WHERE id=?`, id).Scan(&proof.state, &proof.attempt, &proof.thread, &proof.executor, &proof.detail); err != nil {
			t.Fatal("native run proof unavailable", err)
		}
		if pending := w.alice.nativeRequestFor(id); pending != nil {
			i := slices.Index(ids, id)
			if i < 0 {
				t.Fatal("native readiness callback has no exact fixture job; no response")
			}
			if !seenCallbacks[pending.Token] {
				seenCallbacks[pending.Token] = true
				callbacks[i]++
			}
			w.alice.workerLanes.Lock()
			lane, owned := w.alice.workerLanes.jobs[id]
			w.alice.workerLanes.Unlock()
			if approvals[i] != "" || proof.state != stateRunning || proof.attempt != 1 || proof.executor != agent.ID || proof.thread == "" ||
				!owned || lane.executor != agent.ID || lane.native != proof.thread ||
				pending.Request != id || pending.Host != w.alice.Address || pending.Key != w.alice.Self().Fingerprint() || pending.AgentID != agent.ID || pending.Harness != "codex" ||
				pending.Attempt != 1 || pending.Thread != proof.thread || pending.Turn == "" || pending.Item == "" || !protocol.ValidID(pending.Token) ||
				pending.Unsupported != "" || pending.CancelRun || !slices.Contains(pending.Choices, "allow") {
				t.Fatal("native readiness approval is duplicate, unsupported or differs from current job/executor/thread/attempt; no response")
			}
			if _, err := os.Stat(ready[i]); !os.IsNotExist(err) {
				t.Fatal("native readiness approval is not before the nonce file's creation; no response")
			}
			if own, err := w.alice.nativeOwnerHumanIn(w.alice.store.db, w.alice.Address, pending.Key); err != nil || !own {
				t.Fatal("native readiness approval no longer has exact own-human authority; no response")
			}
			if err := nativeFixtureApprovalError(pending, commands[i], wrapped[i], dir); err != nil {
				// Optional private evidence cannot change this exact refusal.
				_ = writeNativeSteeringRefusalDiagnostic(os.Getenv("AGENTNET_LIVE_CODEX_STEERING_REFUSAL"), pending, commands[i], wrapped[i])
				t.Fatal(err)
			}
			// Answer the genuine pending token through the existing current-job
			// validator; the bridge still checks its exact native turn at handover.
			if err := w.alice.RespondNativeRequest(id, pending.Key, 1, nativeResponseText(pending.Token, "allow"), protocol.NewID()); err != nil {
				t.Fatal("exact upfront native readiness approval refused; no retry", err)
			}
			approvals[i] = pending.Token
		}
		if proof.state != stateRunning && proof.state != stateAgentWaiting && proof.state != stateAnswered && proof.state != stateCancelReq && proof.state != stateCancelled {
			t.Fatalf("native request %s ended or refused as %s: %s; no retry", id, proof.state, proof.detail)
		}
		return proof
	}
	for i, conv := range convs {
		part, err := w.alice.InviteNamedAgent(ctx, conv, w.alice.Address, agent.ID, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		pids[i] = part.PID
		wait("own named agent admission", func() bool { return stateAt(t, w.alice, part.PID).Claimable() })
		ready[i], tokens[i] = filepath.Join(dir, fmt.Sprintf("native-ready-%d", i)), "NATIVE_READY_"+protocol.NewID()
		commands[i] = fmt.Sprintf("printf '%%s' %q > %q; sleep 60", tokens[i], ready[i])
		wrapped[i] = "/usr/bin/bash -lc " + strconv.Quote(commands[i]) + "\nNative execution environment: local"
		prompt := fmt.Sprintf("Isolated native compatibility journey authorized by the local human. Use your normal shell tool exactly once to run this harmless command in this owned temporary directory: %s . It writes only the readiness nonce file and sleeps for 60 seconds. The human preauthorizes only this exact command in this working directory. If current native permissions require approval, use your native tool's upfront approval option before execution and wait for that exact approval. Do not first attempt a restricted write or retry any denied operation. Wait for the command to return. Process any explicit corrections to this request before answering; without a correction, give only INITIAL_ONLY_%s. Do not change setup or permissions, contact anyone, use unrelated files/tools/services or automatically retry a denied operation. If native permission requires approval or the operation is unavailable, report it honestly.", commands[i], tokens[i])
		request, err := w.alice.AskAgent(ctx, part.PID, envelope.KindQuestion, prompt)
		if err != nil {
			t.Fatal(err)
		}
		lids[i] = request.LID
		// Native work owns its physical local row; portable request refs use
		// the logical turn. DM copies intentionally have different IDs.
		if err = w.alice.store.db.QueryRowContext(ctx, `SELECT id FROM inbox WHERE conv=? AND lid=? AND pid=? AND local=1 AND replica=0`, conv, request.LID, part.PID).Scan(&ids[i]); err != nil {
			t.Fatal("exact local native request row unavailable", err)
		}
	}
	var proofs [2]runProof
	wait("simultaneous actual native ready ownership", func() bool {
		for i, id := range ids {
			proofs[i] = read(id)
			if proofs[i].state == stateAnswered {
				t.Fatal("native turn completed before simultaneous ready ownership")
			}
			data, err := os.ReadFile(ready[i])
			if err != nil || string(data) != tokens[i] || proofs[i].state != stateRunning || proofs[i].thread == "" {
				return false
			}
		}
		w.alice.workerLanes.Lock()
		first, firstOwned := w.alice.workerLanes.jobs[ids[0]]
		second, secondOwned := w.alice.workerLanes.jobs[ids[1]]
		w.alice.workerLanes.Unlock()
		return firstOwned && secondOwned && first.native == proofs[0].thread && second.native == proofs[1].thread
	})
	if proofs[0].thread == proofs[1].thread || proofs[0].attempt != 1 || proofs[1].attempt != 1 || proofs[0].executor != agent.ID || proofs[1].executor != agent.ID {
		t.Fatalf("actual independent thread/attempt/executor proof missing: %+v", proofs)
	}
	t.Logf("overlap: same executor %s; requests %s/%s; actual threads %s/%s; attempts 1/1; native tool nonce files verified", agent.ID, ids[0], ids[1], proofs[0].thread, proofs[1].thread)
	if err = w.alice.Cancel(ids[0]); err != nil {
		t.Fatal(err)
	}
	wait("first cancellation and owned cleanup", func() bool {
		first, second := read(ids[0]), read(ids[1])
		if second.state != stateRunning || second.thread != proofs[1].thread || second.attempt != 1 {
			t.Fatal("canceling one conversation affected the surviving native run")
		}
		w.alice.workerLanes.Lock()
		_, owned := w.alice.workerLanes.jobs[ids[0]]
		w.alice.workerLanes.Unlock()
		return first.state == stateCancelled && !owned
	})
	marker, evidence, fileBytes := "STEERED_"+protocol.NewID(), "FRESH_EVIDENCE_"+protocol.NewID(), "FRESH_FILE_"+protocol.NewID()
	file := filepath.Join(dir, "fresh-steering-evidence.txt")
	if err = os.WriteFile(file, []byte(fileBytes), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.SendConv(ctx, convs[1], ConvOutgoing{Body: evidence, Files: []OutgoingFile{{Path: file}}}); err != nil {
		t.Fatal(err)
	}
	ref := envelope.Ref{ID: lids[1], Fingerprint: w.alice.Self().Fingerprint()}
	correction := protocol.NewID()
	// This is the ordinary exact followup API, never a direct bridge call/ACK.
	if _, err = w.alice.QueueRequestFollowup(ctx, RequestFollowup{Conv: convs[1], Ref: ref, ID: correction, Body: fmt.Sprintf("Explicit correction: preserve this request and its current tool operation. Replace the INITIAL_ONLY answer with three lines: %s, the exact newly supplied FRESH_EVIDENCE marker, and the exact newly supplied FRESH_FILE contents. Use the authorized evidence/file actually supplied with this correction; do not guess or repeat effects.", marker)}); err != nil {
		t.Fatal(err)
	}
	var correctionID string
	if err = w.alice.store.db.QueryRowContext(ctx, `SELECT id FROM inbox WHERE conv=? AND lid=? AND local=1 AND replica=0`, convs[1], correction).Scan(&correctionID); err != nil {
		t.Fatal("exact local correction row unavailable", err)
	}
	wait("exact correction native acknowledgment", func() bool {
		state := jobState(t, w.alice, correctionID)
		if state == stateNeedHuman || state == stateJobFailed || state == stateNotRun {
			t.Fatalf("real native steering refused or acceptance unknown: %s; no retry", state)
		}
		return state == stateSteered
	})
	stored, err := storedRequestFollowup(w.alice.store.db, "in", correctionID)
	if err != nil || stored == nil || stored.Ref != ref {
		t.Fatal("accepted correction differs from exact original ref", err)
	}
	if proof := read(ids[1]); proof.state != stateRunning || proof.thread != proofs[1].thread || proof.attempt != 1 {
		t.Fatal("accepted correction replaced or restarted the native parent")
	}
	entries, err := os.ReadDir(filepath.Join(w.alice.home, "runs", correctionID, "in"))
	if err != nil || len(entries) != 1 {
		t.Fatal("accepted correction has no exact staged evidence file", err)
	}
	staged := filepath.Join(w.alice.home, "runs", correctionID, "in", entries[0].Name())
	data, err := os.ReadFile(staged)
	info, statErr := os.Stat(staged)
	if err != nil || string(data) != fileBytes || statErr != nil || info.Mode().Perm()&0222 != 0 {
		t.Fatal("actual staged evidence bytes/readonly mode missing", err, statErr)
	}
	wait("surviving original completion", func() bool { return read(ids[1]).state == stateAnswered })
	var answer ConvMessage
	wait("recipient verified signed corrected result", func() bool {
		messages, err := observer.ConversationMessages(convs[1])
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range messages {
			if (msg.ReplyTo == lids[1] || msg.ReplyTo == ids[1]) && msg.PID == pids[1] && msg.Kind == envelope.KindAnswer && msg.VerifiedAgent && !msg.Replica && !msg.History && msg.From == w.alice.Address && msg.Key == w.alice.Self().Fingerprint() {
				answer = msg
				return true
			}
		}
		return false
	})
	if !strings.Contains(answer.Body, marker) || !strings.Contains(answer.Body, evidence) || !strings.Contains(answer.Body, fileBytes) || strings.Contains(answer.Body, "INITIAL_ONLY_") {
		t.Fatal("signed original answer does not contain the actually supplied correction/evidence/file facts")
	}
	if proof := read(ids[1]); proof.attempt != 1 || proof.executor != agent.ID {
		t.Fatal("original native attempt/executor changed")
	}
	var admitted admittedAgentContext
	var raw string
	if err = w.alice.store.db.QueryRowContext(ctx, `SELECT agent_context FROM inbox WHERE id=?`, ids[1]).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &admitted) != nil || admitted.Attempt != 1 {
		t.Fatal("exact native attempt context checkpoint missing", err)
	}
	t.Logf("native steering accepted for %s into original %s/thread %s/attempt 1; staged bytes and recipient verified signed corrected answer %s proven", correction, ids[1], proofs[1].thread, answer.LID)
}

// Only the failing display and its two expected forms leave the temporary DB,
// at an explicit private destination. Never serialize the NativeRequest itself.
func writeNativeSteeringRefusalDiagnostic(path string, n *NativeRequest, command, wrapped string) bool {
	const limit = 8 << 10
	if path == "" || !filepath.IsAbs(path) || n == nil || len(n.Method)+len(n.CWD)+len(n.Operation)+len(command)+len(wrapped) > limit {
		return false
	}
	record := struct {
		Method    string    `json:"method"`
		CWD       string    `json:"cwd"`
		Operation string    `json:"operation"`
		Expected  [2]string `json:"expected"`
	}{n.Method, n.CWD, n.Operation, [2]string{command, wrapped}}
	data, err := json.Marshal(record)
	if err != nil || len(data) > limit {
		return false
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return false
	}
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return false
	}
	return true
}

func TestNativeSteeringRefusalDiagnostic(t *testing.T) {
	command, wrapped := "FIXTURE_LITERAL", "FIXTURE_WRAPPED"
	n := NativeRequest{Method: "item/commandExecution/requestApproval", CWD: "/owned/private-cwd", Operation: "PRIVATE_REJECTED_DISPLAY", Token: "OMITTED_CALLBACK_TOKEN", Key: "OMITTED_KEY", Reason: "OMITTED_PRIVATE_REASON", Thread: "OMITTED_THREAD", Questions: []NativeQuestion{{Question: "OMITTED_PRIVATE_PROMPT"}}}
	refusal := nativeFixtureApprovalError(&n, command, wrapped, n.CWD)
	if refusal == nil {
		t.Fatal("unknown display was approved")
	}
	t.Run("private-allowlisted-record", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "refusal.json")
		if !writeNativeSteeringRefusalDiagnostic(path, &n, command, wrapped) {
			t.Fatal("private capture unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 8<<10 {
			t.Fatal("private bounded record unavailable")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("capture is not an owner-only regular file")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || len(fields) != 4 {
			t.Fatal("capture contains unexpected fields")
		}
		for field, want := range map[string]string{"method": n.Method, "cwd": n.CWD, "operation": n.Operation} {
			var got string
			if json.Unmarshal(fields[field], &got) != nil || got != want {
				t.Fatal("capture changed an allowed field")
			}
		}
		var expected [2]string
		if json.Unmarshal(fields["expected"], &expected) != nil || expected != [2]string{command, wrapped} || strings.Contains(string(data), "OMITTED_") {
			t.Fatal("capture omitted expected forms or exposed private callback fields")
		}
	})
	for _, mode := range []string{"unset", "relative", "missing-parent", "oversize", "escaped-oversize"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "refusal.json")
			destination, current := path, n
			switch mode {
			case "unset":
				destination = ""
			case "relative":
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				destination, err = filepath.Rel(cwd, path)
				if err != nil {
					t.Skip("temporary destination is on another volume")
				}
			case "missing-parent":
				destination = filepath.Join(path, "refusal.json")
			case "oversize":
				current.Operation = strings.Repeat("x", 8<<10)
			case "escaped-oversize":
				current.Operation = strings.Repeat("\x00", 2000)
			}
			if writeNativeSteeringRefusalDiagnostic(destination, &current, command, wrapped) {
				t.Fatal("invalid capture destination or oversized record accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("failed capture left a file or created a directory")
			}
		})
	}
	for _, link := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing-symlink-%t", link), func(t *testing.T) {
			dir := t.TempDir()
			path, kept := filepath.Join(dir, "refusal.json"), filepath.Join(dir, "kept")
			if err := os.WriteFile(kept, []byte("UNCHANGED"), 0644); err != nil {
				t.Fatal(err)
			}
			if link {
				if err := os.Symlink(kept, path); err != nil {
					t.Skip("symlink creation unavailable")
				}
			} else {
				path = kept
			}
			if writeNativeSteeringRefusalDiagnostic(path, &n, command, wrapped) {
				t.Fatal("existing destination was reused")
			}
			data, err := os.ReadFile(kept)
			if err != nil || string(data) != "UNCHANGED" {
				t.Fatal("existing destination or symlink target changed")
			}
		})
	}
	if after := nativeFixtureApprovalError(&n, command, wrapped, n.CWD); after == nil || after.Error() != refusal.Error() || strings.Contains(after.Error(), n.Operation) || strings.Contains(after.Error(), n.CWD) {
		t.Fatal("capture changed refusal or exposed a private display/path in its console error")
	}
}
