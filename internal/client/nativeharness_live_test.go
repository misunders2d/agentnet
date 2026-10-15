package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Opt-in native installation check. It keeps the real owner environment and
// tools, uses disposable Hub/identities/workspace, and never resumes an open
// session or old request. The output file proves a native tool actually ran.
func TestLiveNativeHarnessGroupTools(t *testing.T) {
	name := os.Getenv("AGENTNET_LIVE_NATIVE")
	if name == "" {
		t.Skip("opt-in: AGENTNET_LIVE_NATIVE=pi|claude|omp|codex|agy")
	}
	if _, ok := Harnesses[name]; !ok {
		t.Fatal("unknown native harness")
	}
	if os.Getenv("AGENTNET_LIVE_NATIVE_ACTION") == "owner-journey-fixture" {
		actualNativeOwnerGroupJourney(t, true)
		return
	}
	launch, err := resolveNativeLaunch(Harnesses[name].bin)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native %s via %s (%s)", name, launch.Path, launch.Installation)
	if os.Getenv("AGENTNET_LIVE_NATIVE_ACTION") == "owner-journey" {
		if name != "codex" {
			t.Fatal("owner journey requires the installed Codex/Claude/Pi combination")
		}
		actualNativeOwnerGroupJourney(t, false)
		return
	}
	if os.Getenv("AGENTNET_LIVE_NATIVE_ACTION") == "delegate" {
		switch name {
		case "pi", "claude", "omp", "codex", "agy":
		default:
			t.Fatal("native task delegation is not qualified for this harness")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
		defer cancel()
		actualNativeRoomQuestionSmoke(t, ctx, name, envelope.KindTask)
		return
	}
	w, carol, packet, _ := groupTurnsFixture(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "read-fixture.txt")
	result := filepath.Join(dir, "written-fixture.txt")
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	expected := "native-tool-" + hex.EncodeToString(token)
	if err := os.WriteFile(source, []byte(expected), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.SetResponder(&Responder{Harness: name, Dir: dir, Timeout: 150 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.Approve(carol.Address); err != nil {
		t.Fatal(err)
	}
	participant := p6Member(t, w.alice, w.alice, packet.State.Conv)
	eventually(t, "requester sees native participant", func() bool { return stateAt(t, carol, participant.PID).Claimable() })
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	action := os.Getenv("AGENTNET_LIVE_NATIVE_ACTION")
	prompt := fmt.Sprintf("Isolated native tool verification. Use your normal filesystem tools to read %q and copy its exact contents to %q, then reply with those exact contents. These two files are the complete task scope. Do not alter any other file, contact anyone, change setup or permission settings, or use external services other than your normal model provider. If your native permissions require approval, report that honestly. Do not guess the file contents.", source, result)
	if action == "read" {
		prompt = fmt.Sprintf("Isolated native tool verification. Read %q using your normal filesystem tools, then reply with its exact contents. Do not modify files or contact anyone. Do not guess the contents.", source)
	}
	sent, err := carol.AskAgent(ctx, participant.PID, envelope.KindTask, prompt)
	if err != nil {
		t.Fatal(err)
	}
	// Cross-person fixture acceptance is explicit; own-device consent is covered
	// by its separate phone/second-person journeys. No live grant is modified.
	waitState(t, w.alice, sent.LID, stateAwaiting)
	if err := w.alice.Accept(sent.LID); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state := jobState(t, w.alice, sent.LID)
		if state == stateAnswered {
			if action == "approval" {
				t.Fatal("expected this native setup to require approval; no approval-path evidence")
			}
			break
		}
		if state == stateJobFailed || state == stateNeedHuman || state == stateInterrupt || state == stateDeclined {
			row := inboxRow(t, w.alice, sent.LID)
			if action == "approval" && state == stateNeedHuman && strings.Contains(row.Detail, "native approval") && strings.Contains(row.Detail, "read-only") {
				if _, err := os.Stat(result); !os.IsNotExist(err) {
					t.Fatalf("unapproved tool changed output: %v", err)
				}
				t.Log("Actual native read-only approval request preserved as needs-human; no approval/config override or filesystem write. Reason:", row.Detail)
				return
			}
			t.Fatalf("native task ended %s without verified completion: %s", state, row.Detail)
		}
		select {
		case <-ctx.Done():
			t.Fatal("native fixture budget exceeded; no retry")
		case <-ticker.C:
		}
	}
	data, err := os.ReadFile(result)
	if action != "read" && (err != nil || string(data) != expected) {
		t.Fatalf("native filesystem effect missing: %v", err)
	}
	eventually(t, "signed attributed result reaches requester", func() bool {
		messages, err := carol.ConversationMessages(packet.State.Conv)
		if err != nil {
			return false
		}
		for _, m := range messages {
			if m.ReplyTo == sent.LID && m.Kind == envelope.KindResult && m.PID == participant.PID && m.From == w.alice.Address && m.Key == w.alice.Self().Fingerprint() && strings.Contains(m.Body, expected) {
				return true
			}
		}
		return false
	})
	t.Logf("native filesystem %s and signed attributed group result verified; other tool families remain separate gates", map[bool]string{true: "read", false: "read/write"}[action == "read"])
}

// One disposable native-peer journey, selected only by the explicit action.
// Claude and Pi keep their normal one-shot setup; only Codex has a live native
// thread binding. No model, permission, writable-root or approval flag is added.
func actualNativeOwnerGroupJourney(t *testing.T, fixtureOnly bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Fatal("this opt-in uses Unix fixture shell commands; Windows is separate")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	w, carol, packet, _ := groupTurnsFixture(t)
	phone := humanLinked(t, w.alice)
	publishGroupFixtureCaps(t, phone, true)
	conv, main, dir := packet.State.Conv, "", t.TempDir()
	cli := filepath.Join(dir, "agentnet")
	if !fixtureOnly {
		build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", cli, "./cmd/agentnet")
		build.Dir = "../.."
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("fixture CLI: %v %s", err, out)
		}
	}
	old, err := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "UNSELECTED_BEFORE_NATIVE_INVITATION"})
	if err != nil {
		t.Fatal(err)
	}
	hosts, names := []*Agent{w.alice, w.bob, carol}, []string{"codex", "claude", "pi"}
	parts := make([]ParticipationInfo, 3)
	for i, host := range hosts {
		if !fixtureOnly {
			if _, err := resolveNativeLaunch(Harnesses[names[i]].bin); err != nil {
				t.Fatal(err)
			}
		}
		record, err := host.CreateLocalAgent("owned native "+names[i], Responder{Harness: names[i], Dir: dir, Timeout: 150 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if err = host.PublishAgentCatalog(ctx); err != nil {
			t.Fatal(err)
		}
		p, err := w.alice.InviteAgentInScope(ctx, conv, host.Address, record.ID, nil, nil, "owned native qualification", &main)
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "native invitation reaches exact host", func() bool {
			p, err := host.Participation(p.PID)
			return err == nil && (p.State == PartInvited || p.Claimable())
		})
		if stateAt(t, host, p.PID).State == PartInvited {
			if _, err = host.AcceptParticipation(ctx, p.PID); err != nil {
				t.Fatal(err)
			}
		}
		parts[i] = p
	}
	for _, reader := range append(slices.Clone(hosts), phone) {
		for _, p := range parts {
			eventually(t, "current native participant known", func() bool { return stateAt(t, reader, p.PID).Claimable() })
		}
	}
	// All consent is local to disposable fixtures, including the original
	// current human key and both possible intermediary keys in the task chain.
	for _, host := range hosts[1:] {
		for _, sender := range []*Agent{phone, w.alice, w.bob} {
			if sender == host {
				continue
			}
			if _, err = host.sendKey(ctx, sender.Address); err != nil {
				t.Fatal(err)
			}
			if err = host.Approve(sender.Address); err != nil {
				t.Fatal(err)
			}
			if _, err = host.GrantTasks(sender.Address); err != nil {
				t.Fatal(err)
			}
		}
	}
	if fixtureOnly {
		if own, err := w.alice.nativeOwnerHumanIn(w.alice.store.db, phone.Address, phone.Self().Fingerprint()); err != nil || !own {
			t.Fatal("fixture phone lacks exact current own-human authority", err)
		}
		turn, err := phone.SendConv(ctx, conv, ConvOutgoing{Body: "NATIVE_FIXTURE_CURRENT_TOPIC"})
		if err != nil {
			t.Fatal(err)
		}
		other, err := phone.SendConv(ctx, conv, ConvOutgoing{Body: "NATIVE_FIXTURE_OTHER_TOPIC", Topic: "new"})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "fixture signed scoped context", func() bool {
			c, err := w.alice.ParticipationContext(parts[0].PID, 0)
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(c.lines, "\n")
			if strings.Contains(text, old.LID) || strings.Contains(text, other.LID) {
				t.Fatal("fixture expanded selected history or topic")
			}
			return strings.Contains(text, turn.LID)
		})
		if _, err := w.alice.DismissParticipation(ctx, parts[0].PID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.alice.ParticipationContext(parts[0].PID, 0); err == nil {
			t.Fatal("fixture dismissed reader has context")
		}
		for _, host := range hosts {
			var attempted int
			if err := host.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE attempts>0`).Scan(&attempted); err != nil || attempted != 0 {
				t.Fatal("fixture-only guard attempted native work", err, attempted)
			}
		}
		t.Log("fixture-only scope/linked-human/consent/dismissal checks; zero model/native starts")
		return
	}
	// The bound CLI waits on the existing daemon socket. Refuse a long or
	// otherwise unusable fixture TMPDIR before spending any model invocation.
	for _, host := range hosts {
		conn, _, gone, err := host.subscribeLocalChanges()
		if err != nil {
			t.Fatal("native journey needs an existing local change socket; use a short canonical fixture TMPDIR before native launch", err)
		}
		conn.Close()
		<-gone
	}
	ready, source := filepath.Join(dir, "pi-ready"), filepath.Join(dir, "pi-late-nonce")
	readyToken := "NATIVE_PI_READY_" + protocol.NewID()
	piCommand := fmt.Sprintf("printf '%%s' %q > %q && sleep 15 && cat %q", readyToken, ready, source)
	piPrompt := fmt.Sprintf("Disposable native peer task. Use your normal shell once to execute exactly: %s . The file is supplied by the fixture after readiness. Return its exact contents, never guess. No other file/action/setup change or denied-operation retry is authorized. If native permission is unavailable, report it honestly.", piCommand)
	claudeCommand := fmt.Sprintf("%q --home \"$AGENTNET_HOME\" room ask --pid %q --kind task %q && sleep 30", cli, parts[2].PID, piPrompt)
	claudePrompt := fmt.Sprintf("Disposable native collaboration. Use your normal shell exactly once to execute: %s . Preserve every existing AGENTNET bound environment variable. This waits for one correlated signed Pi result, then holds this operation briefly. Return only CLAUDE_PEER_COMPLETE followed by the final eight characters of the child's nonce; omit the full nonce. Do not dispatch twice, change native setup/permissions or retry a refused effect. Report native refusal honestly.", claudeCommand)
	prompt := fmt.Sprintf("Disposable owner group journey. Use agentnet_room exactly once with action ask, kind task, pid %q and text %q. Wait for its exact result. Preserve this request and process later explicit owner corrections before your final answer; do not repeat a dispatched task. Without correction answer INITIAL_ONLY. Do not use unrelated tools or files, change setup/permissions or retry a refused action.", parts[1].PID, claudePrompt)
	root, err := phone.AskAgentInTopic(ctx, parts[0].PID, envelope.KindQuestion, prompt, main, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids, proofs := make([]string, 3), make([]nativeJourneyProof, 3)
	wait := nativeJourneyWait(t, ctx)
	wait("all native peers own actual work", func() bool {
		for i, host := range hosts {
			var query string
			var args []any
			if i == 0 {
				query, args = `SELECT id FROM inbox WHERE conv=? AND lid=? AND pid=? AND replica=0`, []any{conv, root.LID, parts[0].PID}
			} else {
				parent := root.LID
				if i > 1 && hosts[i-1].store.db.QueryRow(`SELECT lid FROM inbox WHERE id=?`, ids[i-1]).Scan(&parent) != nil {
					return false
				}
				query, args = `SELECT id FROM inbox WHERE conv=? AND pid=? AND reply_to=? AND kind='task' AND replica=0`, []any{conv, parts[i].PID, parent}
			}
			if host.store.db.QueryRow(query, args...).Scan(&ids[i]) != nil {
				return false
			}
			proofs[i] = readNativeJourneyProof(t, host, ids[i])
			p := proofs[i]
			if p.state != stateRunning || p.attempt != 1 || p.executor != parts[i].AgentID || p.pgid <= 1 {
				return false
			}
			host.workerLanes.Lock()
			lane, owned := host.workerLanes.jobs[ids[i]]
			host.workerLanes.Unlock()
			// Only the in-daemon Codex tool reports peerWaits. Claude's
			// separate CLI proves work through its signed child and owned
			// live process; its final nonce suffix proves actual reply input.
			if !owned || lane.executor != p.executor || (i == 0 && (lane.peerWaits == 0 || p.thread == "" || lane.native != p.thread)) {
				return false
			}
		}
		data, err := os.ReadFile(ready)
		return err == nil && string(data) == readyToken && proofs[0].pgid != proofs[1].pgid && proofs[0].pgid != proofs[2].pgid && proofs[1].pgid != proofs[2].pgid
	})
	var from, key, targetJSONText, topic, pid string
	var target envelope.Target
	if err = w.alice.store.db.QueryRow(`SELECT sender,verified_by,target,coalesce(topic,''),pid FROM inbox WHERE id=? AND kind='question' AND replica=0`, ids[0]).Scan(&from, &key, &targetJSONText, &topic, &pid); err != nil || json.Unmarshal([]byte(targetJSONText), &target) != nil || from != phone.Address || key != phone.Self().Fingerprint() || topic != main || pid != parts[0].PID || target.Address != w.alice.Address || target.Fingerprint != w.alice.Self().Fingerprint() || target.AgentID != parts[0].AgentID {
		t.Fatal("native root lost its exact human/key/PID/target binding", err)
	}
	if own, err := w.alice.nativeOwnerHumanIn(w.alice.store.db, from, key); err != nil || !own {
		t.Fatal("group request is not from the host's current verified own human", err)
	}
	peerNonce := "ACTUAL_PI_REPLY_" + protocol.NewID()
	if err = os.WriteFile(source, []byte(peerNonce), 0600); err != nil {
		t.Fatal(err)
	}
	var peerReply ConvMessage
	wait("actual Pi signed result admitted on Codex host", func() bool {
		messages, err := w.alice.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.Kind == envelope.KindResult && m.PID == parts[2].PID && m.VerifiedAgent && m.From == carol.Address && m.Key == carol.Self().Fingerprint() && m.Topic == main && !m.Replica && !m.History && strings.Contains(m.Body, peerNonce) {
				peerReply = m
				return true
			}
		}
		return false
	})
	var piRequestLID string
	if err = carol.store.db.QueryRow(`SELECT lid FROM inbox WHERE id=? AND attempts=1 AND state=?`, ids[2], stateAnswered).Scan(&piRequestLID); err != nil || peerReply.ReplyTo != piRequestLID {
		t.Fatal("Pi result lost its exact native child binding", err)
	}
	for i := 1; i < 3; i++ {
		var lid string
		if err = hosts[i].store.db.QueryRow(`SELECT lid FROM inbox WHERE id=?`, ids[i]).Scan(&lid); err != nil {
			t.Fatal(err)
		}
		messages, err := hosts[i].ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, m := range messages {
			if m.Kind == envelope.KindTask && m.LID == lid {
				if m.Human == nil || m.Human.AuthorPID != parts[i-1].PID || m.From != hosts[i-1].Address || m.Key != hosts[i-1].Self().Fingerprint() || m.Target == nil || m.Target.Address != hosts[i].Address || m.Target.Fingerprint != hosts[i].Self().Fingerprint() || m.Target.AgentID != parts[i].AgentID || m.Origin != envelope.OriginAgentPrefix+"room" || m.Replica || m.History {
					t.Fatal("native task chain attribution changed")
				}
				count++
			}
		}
		if count != 1 {
			t.Fatal("native child dispatched more than once or missing", count)
		}
	}
	fileBytes, evidence := "FRESH_NATIVE_FILE_"+protocol.NewID(), "CANONICAL_NATIVE_EVIDENCE_"+protocol.NewID()
	file := filepath.Join(dir, "canonical-evidence.txt")
	if err = os.WriteFile(file, []byte(fileBytes), 0600); err != nil {
		t.Fatal(err)
	}
	shared, err := phone.SendConv(ctx, conv, ConvOutgoing{Body: evidence, Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := phone.SendConv(ctx, conv, ConvOutgoing{Topic: "new", Body: "UNSHARED_OTHER_NATIVE_TOPIC", Files: []OutgoingFile{{Path: file, Name: "other-topic-private.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	ref := envelope.Ref{ID: root.LID, Fingerprint: phone.Self().Fingerprint()}
	markers := []string{"CORRECTION_" + protocol.NewID(), "GOAL_" + protocol.NewID()}
	corrections := []string{protocol.NewID(), protocol.NewID()}
	for i, body := range []string{
		"Ordinary owner correction: preserve the active peer wait and dispatched effects. Answer in English. Include " + markers[0] + ", the exact Pi peer-result nonce newly shared in this topic, and the canonical evidence marker and exact contents of its supplied readonly attachment. Read only that staged attachment; do not guess or repeat effects.",
		"Owner goal: keep the earlier correction and include " + markers[1] + " after its correction marker in the same final answer. Finish this original request using the supplied evidence. Do not launch another task or repeat a completed effect.",
	} {
		if _, err = phone.QueueRequestFollowup(ctx, RequestFollowup{Conv: conv, Ref: ref, ID: corrections[i], Body: body}); err != nil {
			t.Fatal(err)
		}
		var id string
		wait("ordinary owner correction reaches native host", func() bool {
			return w.alice.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND replica=0`, conv, corrections[i]).Scan(&id) == nil
		})
		wait("genuine native acknowledgment of owner correction", func() bool {
			p := readNativeJourneyProof(t, w.alice, ids[0])
			if p.state != stateRunning || p.attempt != 1 || p.thread != proofs[0].thread || p.executor != parts[0].AgentID {
				t.Fatal("correction queued/continued after original ended; no same-run acceptance proof")
			}
			state := jobState(t, w.alice, id)
			if state == stateNeedHuman || state == stateJobFailed || state == stateNotRun {
				t.Fatalf("native correction outcome %s; acceptance unproven, no replay", state)
			}
			return state == stateSteered
		})
		stored, err := storedRequestFollowup(w.alice.store.db, "in", id)
		if err != nil || stored == nil || stored.Ref != ref {
			t.Fatal("native followup original changed", err)
		}
		if i == 0 {
			requireNativeJourneyFile(t, w.alice, id, fileBytes)
		}
	}
	requireNativeJourneyRefs(t, w.alice, ids[0], 1, []protocol.GrantRef{{LID: peerReply.LID, Fingerprint: peerReply.Key}, {LID: shared.LID, Fingerprint: phone.Self().Fingerprint()}}, []protocol.GrantRef{{LID: old.LID, Fingerprint: w.alice.Self().Fingerprint()}, {LID: other.LID, Fingerprint: phone.Self().Fingerprint()}})
	var claudeRequestLID string
	if err = w.bob.store.db.QueryRow(`SELECT lid FROM inbox WHERE id=?`, ids[1]).Scan(&claudeRequestLID); err != nil {
		t.Fatal(err)
	}
	wait("Claude's exact native signed result completes the peer wait", func() bool {
		p := readNativeJourneyProof(t, w.bob, ids[1])
		messages, err := w.alice.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.Kind == envelope.KindResult && m.ReplyTo == claudeRequestLID && m.PID == parts[1].PID && m.VerifiedAgent && m.From == w.bob.Address && m.Key == w.bob.Self().Fingerprint() && !m.Replica && !m.History && strings.Contains(m.Body, "CLAUDE_PEER_COMPLETE") {
				if !strings.Contains(m.Body, peerNonce[len(peerNonce)-8:]) || strings.Contains(m.Body, peerNonce) || p.attempt != 1 {
					t.Fatal("peer nonce leaked through the direct wait or native child repeated")
				}
				return p.state == stateAnswered
			}
		}
		return false
	})
	wait("original native answer and signed owner delivery", func() bool {
		readNativeJourneyProof(t, w.alice, ids[0])
		messages, err := phone.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.Kind == envelope.KindAnswer && m.ReplyTo == root.LID && m.PID == parts[0].PID && m.VerifiedAgent && m.From == w.alice.Address && m.Key == w.alice.Self().Fingerprint() && !m.Replica && !m.History {
				for _, token := range []string{peerNonce, evidence, fileBytes, markers[0], markers[1]} {
					if !strings.Contains(m.Body, token) {
						t.Fatal("signed original answer lacks actually supplied peer/file/correction fact")
					}
				}
				if strings.Index(m.Body, markers[0]) >= strings.Index(m.Body, markers[1]) || strings.Contains(m.Body, "INITIAL_ONLY") || strings.Contains(m.Body, "UNSHARED_OTHER_NATIVE_TOPIC") || strings.Contains(m.Body, "UNSELECTED_BEFORE_NATIVE_INVITATION") {
					t.Fatal("native instruction order/scope changed")
				}
				return true
			}
		}
		return false
	})
	wait("original native job completes exactly once", func() bool {
		p := readNativeJourneyProof(t, w.alice, ids[0])
		if p.attempt != 1 || p.executor != parts[0].AgentID {
			t.Fatal("original native attempt/executor changed")
		}
		return p.state == stateAnswered
	})
	if _, err = w.alice.DismissParticipation(ctx, parts[0].PID); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.ParticipationContext(parts[0].PID, 0); err == nil {
		t.Fatal("dismissed native reader retains context access")
	}
	t.Logf("actual Codex/Claude/Pi overlap and signed peer chain; exact Codex thread %s attempt 1 accepted two ordered owner inputs and consumed captured Pi ref plus staged file; no native configuration change", proofs[0].thread)
}

// Small evidence assertions shared by these two opt-ins, not a runtime adapter.
type nativeJourneyProof struct {
	state, thread, executor string
	attempt, pgid           int
}

func readNativeJourneyProof(t *testing.T, a *Agent, id string) nativeJourneyProof {
	t.Helper()
	var p nativeJourneyProof
	if err := a.store.db.QueryRow(`SELECT state,attempts,coalesce(json_extract(session_ref,'$.id'),''),coalesce(json_extract(executor,'$.agent_id'),''),coalesce(run_pgid,0) FROM inbox WHERE id=? AND replica=0`, id).Scan(&p.state, &p.attempt, &p.thread, &p.executor, &p.pgid); err != nil {
		t.Fatal(err)
	}
	switch p.state {
	case stateJobFailed, stateNeedHuman, stateDeclined, stateInterrupt, stateNotRun, stateCancelled, stateNotDelivered:
		t.Fatalf("native work ended %s; permission/refusal/queue acceptance remains unproven; no retry", p.state)
	}
	return p
}

func nativeJourneyWait(t *testing.T, ctx context.Context) func(string, func() bool) {
	t.Helper()
	return func(what string, condition func() bool) {
		t.Helper()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for !condition() {
			select {
			case <-ctx.Done():
				t.Fatalf("%s: fixed 180-second budget exhausted; no replay", what)
			case <-ticker.C:
			}
		}
	}
}

func requireNativeJourneyRefs(t *testing.T, a *Agent, id string, attempt int, included, excluded []protocol.GrantRef) admittedAgentContext {
	t.Helper()
	var raw string
	var captured admittedAgentContext
	if err := a.store.db.QueryRow(`SELECT agent_context FROM inbox WHERE id=? AND attempts=?`, id, attempt).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &captured) != nil || captured.Attempt != attempt {
		t.Fatal("exact native context checkpoint unavailable", err)
	}
	for _, ref := range included {
		count := 0
		for _, got := range captured.Refs {
			if got == ref {
				count++
			}
		}
		if count != 1 {
			t.Fatal("native context missing exact ref or duplicated it", ref, count)
		}
	}
	for _, ref := range excluded {
		if slices.Contains(captured.Refs, ref) {
			t.Fatal("native context admitted unselected/refused ref", ref)
		}
	}
	return captured
}

func requireNativeJourneyFile(t *testing.T, a *Agent, correction, expected string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(a.home, "runs", correction, "in"))
	if err != nil || len(entries) != 1 {
		t.Fatal("native correction must stage exactly one authorized file", err, len(entries))
	}
	path := filepath.Join(a.home, "runs", correction, "in", entries[0].Name())
	data, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || info.Mode().Perm()&0222 != 0 || sha256.Sum256(data) != sha256.Sum256([]byte(expected)) {
		t.Fatal("native staged readonly file hash differs", err, statErr)
	}
}
