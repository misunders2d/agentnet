package client

import (
	"context"
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

// One real ephemeral Codex question; all identities, Hub and target replies are
// synthetic. Opt in with an absolute installed binary, never an open session.
// The normal inherited Codex config/environment remains intact: no model,
// reasoning, plugin, skill or MCP settings are substituted by this test.
func TestCodexRoomActualQuestionSmoke(t *testing.T) {
	configured := os.Getenv("AGENTNET_LIVE_CODEX_ROOM")
	if configured == "" {
		t.Skip("opt-in: AGENTNET_LIVE_CODEX_ROOM=/absolute/path/to/codex")
	}
	if !filepath.IsAbs(configured) {
		t.Fatal("AGENTNET_LIVE_CODEX_ROOM must be an absolute installed Codex binary")
	}
	binary, err := filepath.EvalSymlinks(configured)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("installed Codex binary unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	versionCtx, stopVersion := context.WithTimeout(ctx, 5*time.Second)
	version, err := exec.CommandContext(versionCtx, binary, "--version").Output()
	stopVersion()
	if err != nil {
		t.Fatalf("installed Codex version: %v", err)
	}
	t.Logf("installed Codex %s; model/reasoning inherited from normal configuration (not overridden)", strings.TrimSpace(string(version)))
	original := Harnesses["codex"]
	selected := original
	selected.bin = binary
	Harnesses["codex"] = selected
	// Registered before fixture daemons: they stop before restoring this map.
	t.Cleanup(func() { Harnesses["codex"] = original })
	actualNativeRoomQuestionSmoke(t, ctx, "codex", envelope.KindQuestion)
}

// The existing retained-DM test selects this opt-in; its default mock branch
// stays intact. Peer answers are genuinely signed but the peer model is the
// existing synthetic agentstub. Only the reader uses installed native Codex.
func actualNativeRetainedDMContext(t *testing.T, configured string) {
	t.Helper()
	if !filepath.IsAbs(configured) || runtime.GOOS == "windows" {
		t.Fatal("retained-DM opt-in requires an absolute installed Codex and Unix fixture shell")
	}
	binary, err := filepath.EvalSymlinks(configured)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		t.Fatal("installed Codex unavailable", err)
	}
	original := Harnesses["codex"]
	selected := original
	selected.bin = binary
	Harnesses["codex"] = selected
	t.Cleanup(func() { Harnesses["codex"] = original })
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	stub := installAgentStub(t)
	w, conv, lids, _ := agentWorld(t)
	setResponder(t, w.alice, "agentstub", stub.dir, time.Minute)
	if err = w.alice.Approve(w.bob.Address); err != nil {
		t.Fatal(err)
	}
	peer, err := w.alice.InviteAgent(ctx, conv, w.alice.Address, nil, nil, "synthetic signed peer")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "synthetic peer known", func() bool { return stateAt(t, w.bob, peer.PID).Claimable() })
	askPeer := func(body string) ConvMessage {
		t.Helper()
		q, err := w.bob.AskAgent(ctx, peer.PID, envelope.KindQuestion, body)
		if err != nil {
			t.Fatal(err)
		}
		m := replyAt(t, w.bob, conv, q.ID)
		if !m.VerifiedAgent || m.Replica || m.History || m.From != w.alice.Address || m.Key != w.alice.Self().Fingerprint() || m.PID != peer.PID {
			t.Fatal("synthetic peer lacks exact verified reply binding")
		}
		return m
	}
	oldReply := askPeer("EARLIER_NATIVE_REQUEST")
	early, err := w.bob.SendConv(ctx, conv, ConvOutgoing{Body: "EARLIER_LOCAL_UNSHARED"})
	if err != nil {
		t.Fatal(err)
	}
	pid := participate(t, w, conv, lids[:1], nil)
	firstReply := askPeer("PROVEN_INITIAL_PEER_REQUEST")
	initialToken := "RECOVERED_INITIAL_" + protocol.NewID()
	initial, err := w.bob.SendConv(ctx, conv, ConvOutgoing{Body: initialToken})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the existing clock-order premise: a late physical copy cannot
	// move an older logical original past the retained signed acceptance.
	if _, err = w.bob.store.db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,kind,created_ms) SELECT ?,recipient,body,envelope,'not_delivered',created_at,conv,lid,kind,3000 FROM outbox WHERE id=?`, protocol.NewID(), early.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.store.db.Exec(`DELETE FROM room_context WHERE conv=? AND pid=?`, conv, pid); err != nil {
		t.Fatal(err)
	}
	var indexed int
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM room_context WHERE conv=? AND pid=?`, conv, pid).Scan(&indexed); err != nil || indexed != 0 {
		t.Fatal("retained reader was not unindexed", err)
	}
	dir, ready := t.TempDir(), "retained-native-ready"
	ready = filepath.Join(dir, ready)
	readyToken := "RETAINED_READY_" + protocol.NewID()
	command := fmt.Sprintf("printf '%%s' %q > %q; sleep 60", readyToken, ready)
	wrapped := "/usr/bin/bash -lc " + strconv.Quote(command) + "\nNative execution environment: local"
	prompt := fmt.Sprintf("Disposable retained-context native reader. Remember the exact verified peer reply message ID and RECOVERED_INITIAL marker in your initial supplied conversation. Use your normal shell exactly once to execute: %s . The human preauthorizes only that readiness command in the current directory. If native approval is required, ask upfront for that exact operation; never attempt a restricted effect first or retry a denial. Wait for this operation, then obey explicit corrections without repeating it. Without correction answer INITIAL_ONLY. Do not read unrelated files, change setup/permissions or guess missing evidence.", command)
	if err = w.bob.SetResponder(&Responder{Harness: "codex", Dir: dir, Timeout: 150 * time.Second}); err != nil {
		t.Fatal(err)
	}
	root, err := w.bob.AskAgent(ctx, pid, envelope.KindQuestion, prompt)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err = w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND local=1 AND replica=0 AND sender=? AND verified_by=? AND pid=? AND kind='question'`, conv, root.LID, w.bob.Address, w.bob.Self().Fingerprint(), pid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	wait := nativeJourneyWait(t, ctx)
	var proof nativeJourneyProof
	approved := ""
	wait("actual retained reader initial prompt and readiness", func() bool {
		proof = readNativeJourneyProof(t, w.bob, id)
		if pending := w.bob.nativeRequestFor(id); pending != nil {
			if approved != "" || proof.state != stateRunning || proof.attempt != 1 || proof.thread == "" || pending.Request != id || pending.Host != w.bob.Address || pending.Key != w.bob.Self().Fingerprint() || pending.AgentID != proof.executor || pending.Harness != "codex" || pending.Attempt != 1 || pending.Thread != proof.thread || pending.Turn == "" || pending.Item == "" || !protocol.ValidID(pending.Token) || pending.Unsupported != "" || pending.CancelRun || !slices.Contains(pending.Choices, "allow") {
				t.Fatal("retained readiness callback differs from exact current job/owner/thread/attempt; no response")
			}
			if _, err := os.Stat(ready); !os.IsNotExist(err) {
				t.Fatal("readiness approval did not precede its effect")
			}
			if own, err := w.bob.nativeOwnerHumanIn(w.bob.store.db, w.bob.Address, pending.Key); err != nil || !own {
				t.Fatal("exact native owner no longer current", err)
			}
			if err := nativeFixtureApprovalError(pending, command, wrapped, dir); err != nil {
				t.Fatal(err)
			}
			if err := w.bob.RespondNativeRequest(id, pending.Key, 1, nativeResponseText(pending.Token, "allow"), protocol.NewID()); err != nil {
				t.Fatal("exact native readiness approval refused; no retry", err)
			}
			approved = pending.Token
		}
		if proof.state == stateAnswered {
			t.Fatal("retained reader ended before fresh context handover")
		}
		data, err := os.ReadFile(ready)
		return err == nil && string(data) == readyToken && proof.state == stateRunning && proof.thread != "" && proof.attempt == 1 && proof.pgid > 1
	})
	thread := proof.thread
	included := []protocol.GrantRef{{LID: firstReply.LID, Fingerprint: firstReply.Key}, {LID: initial.LID, Fingerprint: w.bob.Self().Fingerprint()}}
	excluded := []protocol.GrantRef{{LID: oldReply.LID, Fingerprint: oldReply.Key}, {LID: early.LID, Fingerprint: w.bob.Self().Fingerprint()}, {LID: lids[1], Fingerprint: w.alice.Self().Fingerprint()}}
	requireNativeJourneyRefs(t, w.bob, id, 1, included, excluded)
	secondReply := askPeer("PROVEN_REFRESH_PEER_REQUEST")
	fileBytes, freshToken := "RECOVERED_FILE_"+protocol.NewID(), "RECOVERED_FRESH_"+protocol.NewID()
	file := filepath.Join(dir, "retained-canonical.txt")
	if err = os.WriteFile(file, []byte(fileBytes), 0600); err != nil {
		t.Fatal(err)
	}
	fresh, err := w.bob.SendConv(ctx, conv, ConvOutgoing{Body: freshToken, Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	// Exercise recovery again for the later peer/file, rather than let a new
	// ledger entry satisfy the missing retained-invitation boundary.
	if _, err = w.bob.store.db.Exec(`DELETE FROM room_context WHERE conv=? AND pid=?`, conv, pid); err != nil {
		t.Fatal(err)
	}
	marker := "RETAINED_CORRECTED_" + protocol.NewID()
	corrections := []string{protocol.NewID(), protocol.NewID()}
	ref := envelope.Ref{ID: root.LID, Fingerprint: w.bob.Self().Fingerprint()}
	for i, body := range []string{
		"Explicit owner correction: preserve the current readiness operation. In the final answer include " + marker + ", the exact initial verified peer reply message ID and initial RECOVERED_INITIAL marker, then the exact newly supplied verified peer reply message ID, RECOVERED_FRESH marker and supplied readonly file contents. Use supplied evidence; read only its staged file. Never guess, repeat effects or use unselected history.",
		"Owner goal: preserve the earlier correction and finish the same original request. Do not repeat the readiness operation, supplied messages or file effects; this input adds no evidence.",
	} {
		if _, err = w.bob.QueueRequestFollowup(ctx, RequestFollowup{Conv: conv, Ref: ref, ID: corrections[i], Body: body}); err != nil {
			t.Fatal(err)
		}
		var correctionID string
		if err = w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND local=1 AND replica=0`, conv, corrections[i]).Scan(&correctionID); err != nil {
			t.Fatal(err)
		}
		wait("actual retained native correction acknowledgment", func() bool {
			current := readNativeJourneyProof(t, w.bob, id)
			if current.state != stateRunning || current.attempt != 1 || current.thread != thread || current.executor != proof.executor {
				t.Fatal("retained correction queued/continued separately; original native binding changed")
			}
			state := jobState(t, w.bob, correctionID)
			if state == stateNeedHuman || state == stateJobFailed || state == stateNotRun {
				t.Fatalf("retained native correction %s; acceptance unproven, no replay", state)
			}
			return state == stateSteered
		})
		got, err := storedRequestFollowup(w.bob.store.db, "in", correctionID)
		if err != nil || got == nil || got.Ref != ref {
			t.Fatal("retained correction ref changed", err)
		}
		if i == 0 {
			requireNativeJourneyFile(t, w.bob, correctionID, fileBytes)
		} else if _, err := os.Stat(filepath.Join(w.bob.home, "runs", correctionID, "in")); !os.IsNotExist(err) {
			t.Fatal("unchanged evidence file was restaged", err)
		}
	}
	included = append(included, protocol.GrantRef{LID: secondReply.LID, Fingerprint: secondReply.Key}, protocol.GrantRef{LID: fresh.LID, Fingerprint: w.bob.Self().Fingerprint()})
	requireNativeJourneyRefs(t, w.bob, id, 1, included, excluded)
	wait("signed actual retained reader final consumption", func() bool {
		readNativeJourneyProof(t, w.bob, id)
		messages, err := w.alice.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.Kind == envelope.KindAnswer && m.ReplyTo == root.LID && m.PID == pid && m.VerifiedAgent && m.From == w.bob.Address && m.Key == w.bob.Self().Fingerprint() && !m.Replica && !m.History {
				for _, fact := range []string{marker, firstReply.LID, initialToken, secondReply.LID, freshToken, fileBytes} {
					if !strings.Contains(m.Body, fact) {
						t.Fatal("actual retained reader did not consume initial/fresh recovered peer/file evidence")
					}
				}
				for _, forbidden := range []string{oldReply.LID, "EARLIER_LOCAL_UNSHARED", "logs are in the ticket", "INITIAL_ONLY"} {
					if strings.Contains(m.Body, forbidden) {
						t.Fatal("retained reader disclosed unselected earlier history")
					}
				}
				return true
			}
		}
		return false
	})
	wait("retained native reader completes exactly once", func() bool {
		p := readNativeJourneyProof(t, w.bob, id)
		if p.attempt != 1 || p.executor != proof.executor {
			t.Fatal("retained reader original attempt/executor changed")
		}
		return p.state == stateAnswered
	})
	if stub.runs() != 3 {
		t.Fatal("synthetic peer reran", stub.runs())
	}
	t.Logf("actual installed Codex reader thread %s attempt 1 consumed unindexed retained initial/fresh refs and readonly file; two genuine steering ACKs/dedup; peer responses are signed synthetic fixtures; exact readiness approvals=%t", thread, approved != "")
}

// Reuse the same genuine question-root journey; the child is a synthetic responder.
func actualNativeRoomQuestionSmoke(t *testing.T, ctx context.Context, name, childKind string) {
	t.Helper()
	var err error
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	dir := t.TempDir()
	if err = w.alice.SetResponder(&Responder{Harness: name, Dir: dir, Timeout: 120 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir, Timeout: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.sendKey(ctx, carol.Address); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.Approve(carol.Address); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{w.alice.Address, carol.Address} {
		// Resolve the fixture's current authenticated directory key before
		// assigning its existing exact-key task consent.
		if _, err = w.bob.sendKey(ctx, address); err != nil {
			t.Fatal(err)
		}
		if err = w.bob.Approve(address); err != nil {
			t.Fatal(err)
		}
		if childKind == envelope.KindTask {
			// Recipient consents to both native host and original human keys,
			// as roomChain requires. These are disposable fixture homes only.
			if _, err = w.bob.GrantTasks(address); err != nil {
				t.Fatal(err)
			}
		}
	}
	source := p6Member(t, w.alice, w.alice, conv)
	target := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "target knows the current source agent", func() bool { return stateAt(t, w.bob, source.PID).Claimable() })
	eventually(t, "human knows both current agents", func() bool {
		return stateAt(t, carol, source.PID).Claimable() && stateAt(t, carol, target.PID).Claimable()
	})
	// ask waits for its correlated reply itself. No shell, network, other tool,
	// task, file access, continuation, model retry or permission change is asked.
	prompt := fmt.Sprintf("Compatibility smoke in this synthetic AgentNet group. Use only the dynamic agentnet_room tool exactly once with action ask, pid %q, and text %q. Await that tool's correlated reply, then give that reply as your final answer. Do not use shell commands, other tools, files or tasks, and do not invent an answer if the tool is unavailable.", target.PID, "Explain the synthetic deploy failure")
	if childKind == envelope.KindTask {
		prompt = fmt.Sprintf("Compatibility delegation in this synthetic AgentNet group, authorized by this original human question. Use the dynamic agentnet_room tool exactly once with action ask, kind task, pid %q, and text %q. The recipient has consented to every current key in this chain. Await exactly its correlated result and give that result as your final answer. Do not run other tools, contact unrelated recipients, change native setup/permissions or retry an unavailable/denied operation; report refusal honestly.", target.PID, "Explain the synthetic deploy failure")
		if name != "codex" {
			// Existing room_callback fixture build and ordinary bound CLI; no
			// adapter/tool schema or environment binding is substituted.
			cli := filepath.Join(dir, "agentnet")
			build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", cli, "./cmd/agentnet")
			build.Dir = "../.."
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("current fixture CLI build: %v %s", err, output)
			}
			prompt = fmt.Sprintf("Compatibility delegation in this synthetic AgentNet group, authorized by this original human question. Use your normal allowed shell tool exactly once to execute %q --home \"$AGENTNET_HOME\" room ask --pid %q --kind task %q . Preserve AGENTNET_ROOM_REQUEST and the other existing bound environment supplied by this run. The recipient has consented to every current key in this chain. This CLI waits for exactly its correlated signed result; give that result as your final answer. Do not dispatch again, contact unrelated recipients, change native setup/permissions or retry a denied/unavailable operation; report refusal honestly.", cli, target.PID, "Explain the synthetic deploy failure")
		}
	}
	root, err := carol.AskAgent(ctx, source.PID, envelope.KindQuestion, prompt)
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state := jobState(t, w.alice, root.LID)
		if state == stateAnswered {
			break
		}
		if state == stateJobFailed || state == stateNeedHuman || state == stateDeclined {
			row := inboxRow(t, w.alice, root.LID)
			t.Fatalf("real native %s question ended %s: %s; no model retry", name, state, row.Detail)
		}
		select {
		case <-ctx.Done():
			t.Fatal("real native room smoke exceeded the fixed 180-second budget; no retry")
		case <-ticker.C:
		}
	}
	var rootKind string
	var rootAttempts int
	if err = w.alice.store.db.QueryRow(`SELECT kind,attempts FROM inbox WHERE id=?`, root.LID).Scan(&rootKind, &rootAttempts); err != nil || rootKind != envelope.KindQuestion || rootAttempts != 1 {
		t.Fatal("genuine question root changed or reran", rootKind, rootAttempts, err)
	}
	// Check admitted, verified signed content at the target, not model claims.
	targetMessages, err := w.bob.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var child ConvMessage
	count := 0
	for _, m := range targetMessages {
		if (m.Kind == envelope.KindQuestion || m.Kind == envelope.KindTask) && m.PID == target.PID && m.ReplyTo == root.LID {
			count++
			child = m
		}
	}
	if count != 1 || child.Kind != childKind || child.Target == nil || child.Target.Address != w.bob.Address || child.Target.Fingerprint != w.bob.Self().Fingerprint() || child.Origin != envelope.OriginAgentPrefix+"room" || child.Key != w.alice.Self().Fingerprint() || child.From != w.alice.Address || child.Replica || child.Human == nil || !child.Human.AgentAuthor() || child.Human.AuthorPID != source.PID || child.Body != "Explain the synthetic deploy failure" {
		t.Fatalf("missing exact signed correlated child: count=%d child=%+v", count, child)
	}
	messages, err := w.alice.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	replyKind := envelope.KindAnswer
	if childKind == envelope.KindTask {
		replyKind = envelope.KindResult
	}
	var answer ConvMessage
	count = 0
	for _, m := range messages {
		if (m.Kind == envelope.KindAnswer || m.Kind == envelope.KindResult) && m.ReplyTo == child.LID && m.PID == target.PID {
			count++
			answer = m
		}
	}
	if count != 1 || answer.Kind != replyKind || !answer.VerifiedAgent || answer.Key != w.bob.Self().Fingerprint() || answer.From != w.bob.Address || answer.Replica || !strings.Contains(answer.Body, "the deploy failed at step 3") {
		t.Fatalf("missing verified correlated target reply: count=%d answer=%+v", count, answer)
	}
	if stub.runs() != 1 {
		t.Fatalf("target ran %d times, want exactly one child request", stub.runs())
	}
	if childKind == envelope.KindTask {
		for _, sender := range []*Agent{w.alice, carol} {
			if granted, err := taskGranted(w.bob.store.db, sender.Address, sender.Self().Fingerprint()); err != nil || !granted {
				t.Fatal("recipient's existing all-chain task consent missing", sender.Address, err)
			}
		}
		var kind, state string
		var attempts int
		if err = w.bob.store.db.QueryRow(`SELECT kind,state,attempts FROM inbox WHERE id=?`, child.ID).Scan(&kind, &state, &attempts); err != nil || kind != envelope.KindTask || state != stateAnswered || attempts != 1 {
			t.Fatal("consented child task did not execute exactly once", kind, state, attempts, err)
		}
		t.Logf("actual native %s question -> one signed authorized child task -> correlated signed result; child model/tools are synthetic", name)
	}
	// Final human-facing result must complete the original request as well.
	for {
		replies, _ := carol.ConversationMessages(conv)
		for _, m := range replies {
			if m.Kind == envelope.KindAnswer && m.ReplyTo == root.LID && m.PID == source.PID && m.VerifiedAgent && m.Key == w.alice.Self().Fingerprint() && strings.Contains(strings.ToLower(m.Body), "the deploy failed at step 3") {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("original human question has no verified completed answer within fixed budget")
		case <-ticker.C:
		}
	}
}
