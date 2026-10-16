package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func TestNativeThinkingSummaryReferenceVectors(t *testing.T) {
	t.Run("passive-native-evidence", nativeActivityEvidenceVectors)
	for _, v := range []struct{ input, want string }{
		{"Verifying service recovery status.", "Verifying service recovery status"},
		{"Reviewing the latest test results.", "Reviewing the latest test results"},
		{"raw analysis\nInspecting relevant code paths.", "Inspecting relevant code paths"},
		{"We need to inspect the implementation.", "We need to inspect the implementation"},
		{"Checking https://example.com/private", ""},
		{"Reviewing user@example.com", ""},
		{"Checking /root/.pi/agent/auth.json", ""},
		{"Reviewing internal/client/worker.go", ""},
		{"Checking @someone now", ""},
		{"Checking @channel now", ""},
		{"Checking bearer token abc", ""},
		{"Reviewing SSN 123-45-6789", ""},
		{"Analyzing the patient medical diagnosis", ""},
		{"Testing\u202e hidden direction text", "Testing hidden direction text"},
		{"Checking ｂｅａｒｅｒ ｔｏｋｅｎ abc", ""},
		{"Checking U123456789", ""},
		{"Using key AKIA1234567890ABCD12", ""},
		{"Checking a123456789012345", ""},
		{"Checking a12345678901234", "Checking a12345678901234"},
		{"Reviewing ﬁnal behavior", "Reviewing final behavior"},
		{"Checking ｓｔａｔｕｓ", "Checking status"},
		{"Reviewing 𝒇𝒊𝒏𝒂𝒍 behavior", "Reviewing final behavior"},
		{"Checking e\u0301tat", "Checking état"},
		{"Checking 12345678-1234-1234-1234-123456789abc", ""},
		{"Checking `private` code", ""},
		{"Reviewing " + strings.Repeat("huge payload ", 1000), ""},
		{strings.Repeat("private ", 10000) + "\nChecking safe behavior", "Checking safe behavior"},
	} {
		if got := safeThinkingSummary(v.input); got != v.want {
			t.Fatalf("input %q: got %q want %q", thinkingTail(v.input), got, v.want)
		}
		if v.want != "" && (!canonicalThinkingSummary(v.want) || !publicActivityDetail("Thinking: "+v.want+"\nReported at 2026-10-16T15:00:00Z")) {
			t.Fatal("safe output is not admitted as optional activity", v.want)
		}
	}
	long := safeThinkingSummary("Reviewing " + strings.Repeat("safe behavior ", 20))
	if len([]rune(long)) > 120 || !strings.HasSuffix(long, "…") || !canonicalThinkingSummary(long) {
		t.Fatal("clipped output is not bounded and canonical", long)
	}
	for _, raw := range []string{"", "Checking\nprivate", "Checking  private", "Checking\tprivate", "Checking\u202e private", "Checking ｓｔａｔｕｓ", "Checking " + strings.Repeat("x", 121)} {
		if publicActivityDetail("Thinking: " + raw) {
			t.Fatal("noncanonical summary became activity", raw)
		}
	}
}

func nativeActivityEvidenceVectors(t *testing.T) {
	ref := envelope.Ref{ID: "exact-request", Fingerprint: "original-author"}
	original := followupOriginal{h: HistoryItem{ID: ref.ID, FromKey: ref.Fingerprint}, to: "recipient/host", key: "pinned-recipient"}
	view := ExecView{State: stateRunning, Host: original.to, Attempt: 1}
	sample := func(e *nativeActivityEvidence, detail string) {
		v := view
		v.Detail = detail
		e.observe(original, &v, ref, original.to, original.key, nil)
	}
	e := newNativeActivityEvidence("pi", false)
	if publicActivityDetail(nativeInputDetail) {
		t.Fatal("input must be classified separately from activity")
	}
	for _, detail := range []string{"Thinking: Checking current behavior", "Thinking: Checking current behavior\nReported at 2026-10-16T15:00:00Z"} {
		sample(e, detail)
	}
	for k := activityWorking; k <= activityAnswer; k++ {
		sample(e, k.detail())
		sample(e, k.detail()+"\nReported at 2026-10-16T15:00:00Z")
	}
	sample(e, nativeInputDetail)
	sample(e, nativeInputDetail+"\nReported at 2026-10-16T15:00:00Z")
	if len(e.Thinking) != 1 || e.ThinkingCount != 2 || len(e.Fallback) != 7 || e.FallbackCount != 14 || !e.InputSeen || e.InputCount != 2 || e.ThinkingAbsent || !e.RequestMatch || !e.TargetMatch || !e.AttemptMatch || e.Mismatch != 0 {
		t.Fatal("safe default nil-target classification or consecutive dedupe changed")
	}
	unsafe := []string{
		"Thinking: Checking /private/collector-secret",
		"Thinking: Checking bearer token collector-secret",
		"Thinking: Checking https://example.com/collector-secret",
		"Thinking: Reviewing collector-secret@example.com",
		"Thinking: Checking a123456789012345 collector-secret",
		"Waiting for collector-secret",
		"Thinking: Checking current behavior\nReported at collector-secret",
		"Working\nReported at 2026-10-16T15:00:00+00:00",
		"Working\nReported at 2026-10-16T15:00:00Z\ncollector-secret",
		nativeInputDetail + "\nReported at collector-secret",
	}
	for _, detail := range unsafe {
		sample(e, detail)
	}
	if e.Unsafe != len(unsafe) || e.InputCount != 2 {
		t.Fatal("unsafe or malformed input detail was accepted")
	}
	for _, mutate := range []func(*followupOriginal, *ExecView){
		func(_ *followupOriginal, v *ExecView) { v.Stale = true },
		func(_ *followupOriginal, v *ExecView) { v.Attempt = 2 },
		func(_ *followupOriginal, v *ExecView) { v.Host = "other/host" },
		func(_ *followupOriginal, v *ExecView) { v.State = stateAnswered },
		func(h *followupOriginal, _ *ExecView) { h.h.ID = "wrong-request" },
		func(h *followupOriginal, _ *ExecView) { h.h.FromKey = "wrong-author" },
		func(h *followupOriginal, _ *ExecView) { h.to = "other/recipient" },
		func(h *followupOriginal, _ *ExecView) { h.key = "changed-pin" },
	} {
		h, v := original, view
		v.Detail = "Thinking: Checking mismatched behavior"
		mutate(&h, &v)
		before := len(e.Thinking)
		e.observe(h, &v, ref, original.to, original.key, nil)
		if len(e.Thinking) != before {
			t.Fatal("mismatched projection retained detail")
		}
	}
	e.observe(original, nil, ref, original.to, original.key, nil)
	if e.Mismatch != 9 || !e.StaleSeen {
		t.Fatal("mismatched or stale samples were not counted")
	}
	target := &envelope.Target{Address: original.to, Fingerprint: original.key, AgentID: "selected-executor"}
	h, v := original, view
	h.h.Target, v.Detail = target, "Thinking: Checking named behavior"
	named := newNativeActivityEvidence("codex", true)
	named.observe(h, &v, ref, original.to, original.key, target)
	wrong := *target
	wrong.AgentID = "different-executor"
	named.observe(h, &v, ref, original.to, original.key, &wrong)
	wrong = *target
	wrong.Fingerprint = "changed-pin"
	h.h.Target = &wrong
	named.observe(h, &v, ref, original.to, original.key, &wrong)
	if len(named.Thinking) != 1 || named.Mismatch != 2 {
		t.Fatal("named executor or pinned-target mismatch was accepted")
	}
	bounded := newNativeActivityEvidence("claude", false)
	for i := 0; i < 40; i++ {
		sample(bounded, "Thinking: Reviewing safe behavior "+string([]rune{rune('a' + i/26), rune('a' + i%26)}))
		sample(bounded, nativeActivity(int(activityWorking)+i%7).detail())
	}
	if len(bounded.Thinking) != 16 || len(bounded.Fallback) != 16 || bounded.ThinkingCount != 40 || bounded.FallbackCount != 40 {
		t.Fatal("label retention or continued counts exceeded the sixteen-label bound")
	}
	absent := newNativeActivityEvidence("omp", false)
	sample(absent, activityWorking.detail())
	if !absent.ThinkingAbsent || len(absent.Thinking) != 0 {
		t.Fatal("fallback was reported as native Thinking")
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > 16*1024 {
		t.Fatal("public evidence serialization failed its bound")
	}
	for _, forbidden := range append(unsafe, "collector-secret", ref.ID, ref.Fingerprint, original.to, original.key, "Reported at", "mismatched behavior") {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatal("evidence retained refused text, a stamp or an identifier")
		}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 16 {
		t.Fatal("public evidence schema changed")
	}
	for _, key := range []string{"harness", "named", "thinking", "fallback", "thinking_count", "fallback_count", "input_seen", "input_count", "unsafe", "mismatch", "attempt_match", "request_match", "target_match", "stale_seen", "samples_total", "thinking_absent"} {
		if _, ok := object[key]; !ok {
			t.Fatal("public evidence is missing an allowed field")
		}
	}
	dir := t.TempDir()
	if err := secfile.EnsureDir(dir); err != nil {
		t.Fatal("private evidence fixture setup failed")
	}
	for _, harness := range []string{"pi", "claude", "codex", "omp"} {
		for _, named := range []bool{false, true} {
			fixture := newNativeActivityEvidence(harness, named)
			if err := writeNativeActivityEvidence(dir, fixture); err != nil {
				t.Fatal("private public-label artifact write failed")
			}
			if writeNativeActivityEvidence(dir, fixture) == nil {
				t.Fatal("exclusive artifact was overwritten")
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 8 {
		t.Fatal("unexpected bounded artifact set")
	}
	for _, entry := range entries {
		if err := checkNativeEvidencePermissions(filepath.Join(dir, entry.Name()), false); err != nil {
			t.Fatal("artifact is not private")
		}
	}
	if writeNativeActivityEvidence(dir, newNativeActivityEvidence("../collector-secret", false)) == nil || nativeActivityEvidenceDirectory("relative") == nil || nativeActivityEvidenceDirectory(filepath.Join(dir, "missing")) == nil {
		t.Fatal("unknown harness or invalid directory accepted")
	}
	if err := shareNativeEvidenceDirectory(dir); err != nil {
		t.Fatal("nonprivate fixture setup failed")
	}
	if nativeActivityEvidenceDirectory(dir) == nil {
		t.Fatal("nonprivate directory accepted")
	}
	shared := t.TempDir()
	if err := shareNativeEvidenceDirectory(shared); err != nil {
		t.Fatal("shared fixture setup failed")
	}
	if writeNativeActivityEvidence(shared, newNativeActivityEvidence("pi", false)) == nil {
		t.Fatal("artifact created in shared directory")
	}
	if entries, err := os.ReadDir(shared); err != nil || len(entries) != 0 {
		t.Fatal("refused shared directory gained an artifact")
	}
	if nativeActivityEvidenceDirectory(shared) == nil {
		t.Fatal("supplied shared directory was tightened instead of refused")
	}
}

func TestNativeThinkingSnapshotDedupFallbackTimeAndConcurrentIntake(t *testing.T) {
	a := newRunActivity()
	a.reportThinking("Verifying service recovery status")
	first := a.native.Load()
	a.reportThinking("Verifying service recovery status")
	if first != a.native.Load() {
		t.Fatal("timestamp-only native repeat replaced snapshot")
	}
	a.reportThinking("Reviewing the latest test results")
	if got := a.native.Load(); got == first || got.summary != "Reviewing the latest test results" || got.at.Before(first.at) {
		t.Fatal("changing native wording lost", got)
	}
	emitNativeThinking(a.reportThinking, a.report, "Checking bearer token abc")
	if got := a.native.Load(); got.summary != "" || got.kind != activityWorking {
		t.Fatal("rejected detail retained old summary", got)
	}
	a.report(activityReading)
	latest := a.native.Load()
	a.reportProgress(nativeProgress{kind: activityEditing, at: latest.at.Add(-time.Second)})
	if a.native.Load() != latest {
		t.Fatal("late observation replaced newer activity")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 1000 {
				a.reportThinking("Checking current behavior")
				a.report(activityReading)
				_ = a.native.Load()
			}
		})
	}
	wg.Wait()
	if len(a.wake) != 1 || a.native.Load() == nil {
		t.Fatal("concurrent intake lost bounded latest snapshot")
	}
}

// Stream fragments through the real adapters: no prefix may become a public
// snapshot before a complete line or the adapter's explicit block end.
func TestNativeThinkingCompleteLinesAndTokenPrefixes(t *testing.T) {
	for _, adapter := range []string{"pi", "omp", "codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			activity := newRunActivity()
			store := securityStore(t)
			j := job{ID: "current", From: "sender/device", Key: "verified-key", nativeAttempt: 1}
			if _, err := store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,attempts) VALUES(?,?,1,'task','synthetic',1,'running',?,1)`, j.ID, j.From, j.Key); err != nil {
				t.Fatal(err)
			}
			publish := func(summary string) {
				activity.reportThinking(summary)
				store.markNativeProgress(t.Context(), j, *activity.native.Load())
			}
			stored := func() string {
				var detail string
				if err := store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, j.ID).Scan(&detail); err != nil {
					t.Fatal(err)
				}
				return detail
			}
			var start func()
			var delta func(string)
			var end func()
			switch adapter {
			case "pi", "omp":
				b, _, _ := piBridgeFixture(t)
				b.omp = adapter == "omp"
				b.onThinking, b.onActivity = publish, activity.report
				piEvent(b, map[string]any{"type": "message_start", "messageId": "current", "message": map[string]string{"role": "assistant"}})
				send := func(kind, text string) {
					piEvent(b, map[string]any{"type": "message_update", "messageId": "current", "assistantMessageEvent": map[string]any{"type": kind, "contentIndex": 0, "delta": text, "content": text}})
				}
				var full string
				start = func() { full = ""; send("thinking_start", "") }
				delta = func(text string) { full += text; send("thinking_delta", text) }
				end = func() { send("thinking_end", full) }
			case "codex":
				in, out := io.Pipe()
				defer in.Close()
				defer out.Close()
				b := &codexRoomBridge{ctx: t.Context(), input: out, output: io.Discard, thread: "owned", turn: "attempt", onThinking: publish, onActivity: activity.report}
				send := func(method string, params map[string]any) {
					raw, _ := json.Marshal(map[string]any{"method": method, "params": params})
					b.event(raw)
				}
				item := map[string]string{"id": "reasoning", "type": "reasoning"}
				start = func() { send("item/started", map[string]any{"threadId": "owned", "turnId": "attempt", "item": item}) }
				delta = func(text string) {
					send("item/reasoning/summaryTextDelta", map[string]any{"threadId": "owned", "turnId": "attempt", "itemId": "reasoning", "summaryIndex": 0, "delta": text})
				}
				end = func() { send("item/completed", map[string]any{"threadId": "owned", "turnId": "attempt", "item": item}) }
			case "claude":
				b, _, _ := claudeAdapterFixture(t)
				b.onThinking, b.onActivity = publish, activity.report
				send := func(value any) {
					claudeAdapterEvent(b, map[string]any{"type": "stream_event", "session_id": "owned", "event": value})
				}
				start = func() {
					send(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "thinking", "thinking": ""}})
				}
				delta = func(text string) {
					send(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "thinking_delta", "thinking": text}})
				}
				end = func() { send(map[string]any{"type": "content_block_stop", "index": 0}) }
			}
			summary := func() string {
				if p := activity.native.Load(); p != nil {
					return p.summary
				}
				return ""
			}
			for _, unsafe := range []string{"Using key AKIA1234567890ABCD12", "Checking 0123456789abcdef0123456789abcdef01234567"} {
				start()
				for _, r := range unsafe + "\n" {
					delta(string(r))
					if got := summary(); got != "" {
						t.Fatalf("streamed secret prefix published: %q", got)
					}
				}
				if strings.Contains(stored(), "Thinking: ") {
					t.Fatal("streamed prefix reached store", stored())
				}
			}
			start()
			delta("Checking current behavior")
			if summary() != "" {
				t.Fatal("unterminated line published", summary())
			}
			delta("\n")
			if summary() != "Checking current behavior" {
				t.Fatal("first complete line missing", summary())
			}
			delta("Reviewing latest results\u2028")
			if summary() != "Reviewing latest results" {
				t.Fatal("second complete line missing", summary())
			}
			delta("Verifying final behavior")
			if summary() != "Reviewing latest results" {
				t.Fatal("pending tail replaced complete line", summary())
			}
			end()
			if summary() != "Verifying final behavior" {
				t.Fatal("explicit block end lost final line", summary())
			}
			if !strings.HasPrefix(stored(), "Thinking: Verifying final behavior\nReported at ") {
				t.Fatal("completed block did not reach store", stored())
			}
			start()
			delta("Checking " + strings.Repeat("x", thinkingBufferMax+1) + " safe ending")
			end()
			if summary() != "" {
				t.Fatal("oversized final block fabricated a safe suffix", summary())
			}
			start()
			delta(strings.Repeat("x", thinkingBufferMax+1))
			delta("Checking fabricated safe suffix\n")
			if summary() != "" {
				t.Fatal("truncated line fabricated a safe suffix", summary())
			}
			delta("Checking safe recovery\u2029")
			if summary() != "Checking safe recovery" {
				t.Fatal("complete line after overflow missing", summary())
			}
			delta("Checking /private/data\n")
			if summary() != "" {
				t.Fatal("unsafe complete line retained prior detail", summary())
			}
		})
	}
}

func TestNativeThinkingPiOMPSourceEventsAndOptionalFailure(t *testing.T) {
	for _, omp := range []bool{false, true} {
		t.Run(map[bool]string{false: "pi", true: "omp"}[omp], func(t *testing.T) {
			b, _, out := piBridgeFixture(t)
			b.omp = omp
			activity := newRunActivity()
			b.onActivity, b.onThinking = activity.report, activity.reportThinking
			piEvent(b, map[string]any{"type": "message_start", "messageId": "current", "message": map[string]string{"role": "assistant"}})
			send := func(kind string, value any, message string, index int) {
				piEvent(b, map[string]any{"type": "message_update", "messageId": message, "assistantMessageEvent": map[string]any{"type": kind, "contentIndex": index, "delta": value, "content": value, "partial": "PRIVATE ignored snapshot"}})
			}
			send("thinking_start", "", "current", 0)
			send("thinking_delta", "Verifying service recovery status\n", "current", 0)
			if got := activity.native.Load().summary; got != "Verifying service recovery status" {
				t.Fatal("first native summary missing", got)
			}
			first := activity.native.Load()
			if omp {
				send("thinking_delta", "PRIVATE foreign message", "foreign", 0)
				if activity.native.Load() != first {
					t.Fatal("foreign OMP message changed progress")
				}
			}
			send("thinking_delta", "PRIVATE other block", "current", 1)
			if activity.native.Load() != first {
				t.Fatal("foreign block changed progress")
			}
			send("thinking_delta", "Reviewing the latest test results\n", "current", 0)
			if got := activity.native.Load().summary; got != "Reviewing the latest test results" {
				t.Fatal("changing native summary missing", got)
			}
			send("thinking_end", "Reviewing the latest test results", "current", 0)
			last := activity.native.Load()
			send("thinking_delta", "PRIVATE late block", "current", 0)
			if activity.native.Load() != last {
				t.Fatal("ended block accepted a delta")
			}
			send("thinking_start", "", "current", 1)
			send("thinking_delta", "Checking /private/data", "current", 1)
			if activity.native.Load().summary != "" {
				t.Fatal("unsafe detail retained a summary")
			}
			send("thinking_delta", 9, "current", 1)
			if b.failed {
				t.Fatal("optional malformed field failed job")
			}
			send("thinking_start", "", "current", 2)
			send("thinking_delta", "Reviewing "+strings.Repeat("huge payload ", 1000), "current", 2)
			if len([]rune(b.thinking.text)) > thinkingBufferMax || activity.native.Load().summary != "" {
				t.Fatal("oversized thinking retained/published raw detail")
			}
			b.onThinking = func(string) { panic("optional formatter/callback failure") }
			send("thinking_start", "", "current", 3)
			send("thinking_delta", "Checking safe behavior", "current", 3)
			if activity.native.Load().summary != "" || activity.native.Load().kind != activityWorking {
				t.Fatal("optional callback failure did not restore generic activity")
			}
			b.onActivity = func(nativeActivity) { panic("optional fallback callback failure") }
			send("thinking_delta", "\nReviewing safe behavior", "current", 3)
			piEvent(b, map[string]any{"type": "message_end", "messageId": "current", "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": []map[string]string{{"type": "text", "text": "original answer"}}}})
			piEvent(b, map[string]string{"type": "agent_end"})
			if omp {
				b.event([]byte(`{"type":"prompt_result","id":"agentnet-initial","status":"completed","sessionSettled":true}`))
			} else {
				b.event([]byte(`{"type":"agent_settled","aborted":false}`))
			}
			if b.failed || out.String() != "original answer\n" {
				t.Fatal("optional progress failure changed result", b.failed, out.String())
			}
			b.onThinking = func(string) { t.Fatal("late terminal thinking callback") }
			send("thinking_start", "", "current", 0)
		})
	}
}

func TestNativeThinkingCodexSummaryOnlyAndOptionalFailure(t *testing.T) {
	in, out := io.Pipe()
	defer in.Close()
	defer out.Close()
	var answer bytes.Buffer
	activity := newRunActivity()
	b := &codexRoomBridge{ctx: t.Context(), input: out, output: &answer, thread: "owned", turn: "attempt", onActivity: activity.report, onThinking: activity.reportThinking}
	send := func(method string, params map[string]any) {
		raw, _ := json.Marshal(map[string]any{"method": method, "params": params})
		b.event(raw)
	}
	start := func() {
		send("item/started", map[string]any{"threadId": "owned", "turnId": "attempt", "item": map[string]string{"id": "reasoning", "type": "reasoning"}})
	}
	delta := func(method, thread, turn, item string, index int, value any) {
		send(method, map[string]any{"threadId": thread, "turnId": turn, "itemId": item, "summaryIndex": index, "delta": value})
	}
	start()
	delta("item/reasoning/summaryTextDelta", "owned", "attempt", "reasoning", 0, "Verifying service recovery status")
	if activity.native.Load().summary != "" {
		t.Fatal("incomplete Codex section published")
	}
	delta("item/reasoning/summaryPartAdded", "owned", "attempt", "reasoning", 1, nil)
	first := activity.native.Load()
	if first.summary != "Verifying service recovery status" {
		t.Fatal(first)
	}
	for _, bad := range []struct{ method, thread, turn, item string }{
		{"item/reasoning/textDelta", "owned", "attempt", "reasoning"},
		{"item/reasoning/summaryTextDelta", "foreign", "attempt", "reasoning"},
		{"item/reasoning/summaryTextDelta", "owned", "old", "reasoning"},
		{"item/reasoning/summaryTextDelta", "owned", "attempt", "foreign-item"},
	} {
		delta(bad.method, bad.thread, bad.turn, bad.item, 1, "PRIVATE raw reasoning")
		if activity.native.Load() != first {
			t.Fatal("raw/foreign event changed summary", bad)
		}
	}
	delta("item/reasoning/summaryPartAdded", "owned", "attempt", "reasoning", 1, nil)
	delta("item/reasoning/summaryTextDelta", "owned", "attempt", "reasoning", 1, "Reviewing the latest test results\n")
	if activity.native.Load().summary != "Reviewing the latest test results" {
		t.Fatal("second native summary missing")
	}
	last := activity.native.Load()
	delta("item/reasoning/summaryTextDelta", "owned", "attempt", "reasoning", 0, "PRIVATE old summary")
	if activity.native.Load() != last {
		t.Fatal("older summary section revived")
	}
	delta("item/reasoning/summaryTextDelta", "owned", "attempt", "reasoning", 1, 9)
	if b.done || activity.native.Load().summary != "" {
		t.Fatal("malformed optional summary failed run or retained specific detail")
	}
	start()
	delta("item/reasoning/summaryTextDelta", "owned", "attempt", "reasoning", 0, "Checking safe behavior")
	b.onThinking = func(string) { panic("optional formatter/callback failure") }
	delta("item/reasoning/summaryTextDelta", "owned", "attempt", "reasoning", 0, "\nReviewing safe behavior")
	if activity.native.Load().summary != "" || activity.native.Load().kind != activityWorking {
		t.Fatal("optional callback failure did not restore generic activity")
	}
	b.onActivity = func(nativeActivity) { panic("optional fallback failure") }
	delta("item/reasoning/summaryTextDelta", "owned", "attempt", "reasoning", 0, "\nVerifying safe behavior")
	b.answer = "original answer"
	send("turn/completed", map[string]any{"threadId": "owned", "turn": map[string]string{"id": "attempt", "status": "completed"}})
	if !b.done || answer.String() != "original answer" {
		t.Fatal("optional failure changed result", answer.String())
	}
	b.onThinking = func(string) { t.Fatal("late terminal callback") }
	start()
}

func TestNativeThinkingClaudeNestedSummaryAndOptionalFailure(t *testing.T) {
	b, _, out := claudeAdapterFixture(t)
	activity := newRunActivity()
	b.onActivity, b.onThinking = activity.report, activity.reportThinking
	event := func(session string, parent any, replay bool, value any) {
		claudeAdapterEvent(b, map[string]any{"type": "stream_event", "session_id": session, "parent_tool_use_id": parent, "isReplay": replay, "event": value})
	}
	start := func() {
		event("owned", nil, false, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "thinking", "thinking": ""}})
	}
	delta := func(session string, parent any, replay bool, kind string, value any) {
		event(session, parent, replay, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": kind, "thinking": value, "signature": "PRIVATE signature", "partial_json": "PRIVATE args", "text": "PRIVATE answer"}})
	}
	start()
	delta("owned", nil, false, "thinking_delta", "Verifying service recovery status\n")
	first := activity.native.Load()
	if first.summary != "Verifying service recovery status" {
		t.Fatal(first)
	}
	delta("foreign", nil, false, "thinking_delta", "PRIVATE foreign session")
	delta("owned", "child-tool", false, "thinking_delta", "PRIVATE child session")
	delta("owned", nil, true, "thinking_delta", "PRIVATE replay")
	for _, kind := range []string{"signature_delta", "input_json_delta", "text_delta"} {
		delta("owned", nil, false, kind, "PRIVATE forbidden field")
	}
	if b.failed || activity.native.Load() != first {
		t.Fatal("foreign/raw event affected run")
	}
	delta("owned", nil, false, "thinking_delta", "Reviewing the latest test results\n")
	if activity.native.Load().summary != "Reviewing the latest test results" {
		t.Fatal("second native summary missing")
	}
	delta("owned", nil, false, "thinking_delta", 9)
	if b.failed || activity.native.Load().summary != "" {
		t.Fatal("malformed optional field failed run or retained summary")
	}
	claudeAdapterEvent(b, map[string]any{"type": "assistant", "session_id": "owned", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "thinking", "thinking": "Checking completed summary"}, {"type": "redacted_thinking", "data": "PRIVATE"}}}})
	if activity.native.Load().summary != "Checking completed summary" {
		t.Fatal("bounded complete-block fallback missing")
	}
	start()
	delta("owned", nil, false, "thinking_delta", "Checking safe behavior")
	b.onThinking = func(string) { panic("optional formatter/callback failure") }
	delta("owned", nil, false, "thinking_delta", "\nReviewing safe behavior")
	if activity.native.Load().summary != "" || activity.native.Load().kind != activityWorking {
		t.Fatal("optional callback failure did not restore generic activity")
	}
	b.onActivity = func(nativeActivity) { panic("optional fallback failure") }
	delta("owned", nil, false, "thinking_delta", "\nVerifying safe behavior")
	claudeAdapterEvent(b, map[string]any{"type": "result", "session_id": "owned", "result": "original answer", "is_error": false})
	if b.failed || out.String() != "original answer\n" {
		t.Fatal("optional progress failure changed result", b.failed, out.String())
	}
	b.onThinking = func(string) { t.Fatal("late terminal callback") }
	start()
}

func TestNativeActivityCodexExactPublicEvents(t *testing.T) {
	in, out := io.Pipe()
	defer in.Close()
	defer out.Close()
	var activity []nativeActivity
	var answer bytes.Buffer
	b := &codexRoomBridge{ctx: t.Context(), input: out, output: &answer, thread: "owned", turn: "attempt", onActivity: func(k nativeActivity) { activity = append(activity, k) }}
	event := func(method, thread, turn, kind string) {
		raw, _ := json.Marshal(map[string]any{"method": method, "params": map[string]any{"threadId": thread, "turnId": turn, "item": map[string]string{"id": "native-item", "type": kind, "command": "PRIVATE command and secret", "text": "PRIVATE content"}}})
		b.event(raw)
	}
	event("item/started", "other-thread", "attempt", "commandExecution")
	event("item/started", "owned", "previous-attempt", "fileChange")
	event("item/started", "owned", "attempt", "reasoning")
	if len(activity) != 0 {
		t.Fatal("unrelated or hidden event produced activity", activity)
	}
	event("item/started", "owned", "attempt", "commandExecution")
	event("item/completed", "owned", "attempt", "commandExecution")
	event("item/started", "owned", "attempt", "fileChange")
	event("item/started", "owned", "attempt", "webSearch")
	event("item/started", "owned", "attempt", "mcpToolCall")
	event("item/started", "owned", "attempt", "agentMessage")
	want := []nativeActivity{activityCommand, activityWorking, activityEditing, activitySearch, activityTool, activityAnswer}
	if !reflect.DeepEqual(activity, want) || answer.Len() != 0 {
		t.Fatalf("public activity %v, answer %q", activity, answer.String())
	}
	b.answer = "completed answer"
	b.event([]byte(`{"method":"turn/completed","params":{"threadId":"owned","turn":{"id":"attempt","status":"completed"}}}`))
	event("item/started", "owned", "attempt", "commandExecution")
	if !reflect.DeepEqual(activity, want) || answer.String() != "completed answer" {
		t.Fatal("late telemetry changed a completed answer", activity, answer.String())
	}
}

func TestNativeActivityClaudePublicBlocksAndCompletion(t *testing.T) {
	b, _, out := claudeAdapterFixture(t)
	var activity []nativeActivity
	b.onActivity = func(k nativeActivity) { activity = append(activity, k) }
	assistant := func(session, kind, name string, replay bool) {
		claudeAdapterEvent(b, map[string]any{"type": "assistant", "session_id": session, "isReplay": replay, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": kind, "name": name, "input": map[string]string{"path": "PRIVATE file", "command": "PRIVATE secret"}, "thinking": "PRIVATE reasoning", "text": "PRIVATE content"}}}})
	}
	assistant("owned", "thinking", "Read", false)
	assistant("owned", "tool_use", "Read", true)
	if len(activity) != 0 {
		t.Fatal("reasoning/replayed content became activity", activity)
	}
	assistant("owned", "tool_use", "Read", false)
	assistant("owned", "tool_use", "Bash", false)
	assistant("owned", "tool_use", "PRIVATE-tool-name", false)
	assistant("owned", "text", "", false)
	// Malformed optional block names are ignored, never a native failure.
	claudeAdapterEvent(b, map[string]any{"type": "assistant", "session_id": "owned", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": 9}}}})
	want := []nativeActivity{activityReading, activityCommand, activityTool, activityAnswer}
	if !reflect.DeepEqual(activity, want) || out.Len() != 0 || b.failed {
		t.Fatal(activity, out.String(), b.failed)
	}
	claudeAdapterEvent(b, map[string]any{"type": "result", "session_id": "owned", "result": "complete", "is_error": false})
	assistant("owned", "tool_use", "Write", false)
	if !reflect.DeepEqual(activity, want) || out.String() != "complete\n" {
		t.Fatal("late telemetry changed completion", activity, out.String())
	}
	t.Run("wrong-session", func(t *testing.T) {
		other, _, _ := claudeAdapterFixture(t)
		other.onActivity = func(nativeActivity) { t.Fatal("wrong-session activity") }
		claudeAdapterEvent(other, map[string]any{"type": "assistant", "session_id": "foreign", "message": map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "tool_use", "name": "Read"}}}})
	})
}

func TestNativeActivityPiOMPEventsAndMalformedOptionalFields(t *testing.T) {
	for _, omp := range []bool{false, true} {
		t.Run(map[bool]string{false: "pi", true: "omp"}[omp], func(t *testing.T) {
			b, _, out := piBridgeFixture(t)
			b.omp = omp
			var activity []nativeActivity
			b.onActivity = func(k nativeActivity) { activity = append(activity, k) }
			for _, raw := range []string{
				`{"type":"tool_execution_start","toolName":"read","args":{"path":"PRIVATE"}}`,
				`{"type":"tool_execution_end","toolName":"read","result":{"content":"PRIVATE"}}`,
				`{"type":"tool_execution_start","toolName":"bash","args":{"command":"PRIVATE"}}`,
				`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"PRIVATE"}}`,
				`{"type":"message_update","assistantMessageEvent":{"type":"text_start","partial":"PRIVATE"}}`,
				`{"type":"tool_execution_start","toolName":9}`,
				`{"type":"message_update","assistantMessageEvent":{"type":9}}`,
				`{"type":"message_update","assistantMessageEvent":"unsupported"}`,
			} {
				b.event([]byte(raw))
			}
			// A large unknown tool name is never emitted or retained as detail.
			piEvent(b, map[string]any{"type": "tool_execution_start", "toolName": strings.Repeat("PRIVATE", 10000)})
			want := []nativeActivity{activityReading, activityWorking, activityCommand, activityAnswer, activityTool}
			if !reflect.DeepEqual(activity, want) || b.failed || out.Len() != 0 {
				t.Fatal(activity, b.failed, out.String())
			}
			piEvent(b, map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "text", "text": "complete"}}, "stopReason": "stop"}})
			b.event([]byte(`{"type":"agent_end"}`))
			b.event([]byte(`{"type":"tool_execution_start","toolName":"write"}`))
			if omp {
				b.event([]byte(`{"type":"prompt_result","id":"agentnet-initial","status":"completed","sessionSettled":true}`))
			} else {
				b.event([]byte(`{"type":"agent_settled","aborted":false}`))
			}
			b.event([]byte(`{"type":"tool_execution_start","toolName":"write"}`))
			if !reflect.DeepEqual(activity, want) || out.String() != "complete\n" {
				t.Fatal("ended telemetry changed completion", activity, out.String())
			}
		})
	}
}

func TestNativeActivityAgyKnownPublicStepOnly(t *testing.T) {
	var activity []nativeActivity
	s := &agyStream{expected: agyTestConversation, onActivity: func(k nativeActivity) { activity = append(activity, k) }}
	write := func(raw string) { s.Write([]byte(raw + "\n")) }
	write(`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"before-init"}}`)
	write(`{"event":"init","conversation_id":"` + agyTestConversation + `"}`)
	write(`{"event":"step_update","step_update":{"step_type":"thinking","text_delta":"PRIVATE"}}`)
	write(`{"event":"step_update","step_update":{"step_type":9}}`)
	write(`{"event":"step_update","step_update":"unsupported"}`)
	write(`{"event":"step_update","conversation_id":"foreign","step_update":{"step_type":"agent_response"}}`)
	write(`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"PRIVATE"}}`)
	write(agyTestResult(agyTestConversation, "SUCCESS", "complete"))
	write(`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"late"}}`)
	body, status, attention := s.result(nil, &agyDiagnostics{})
	if !reflect.DeepEqual(activity, []nativeActivity{activityAnswer}) || body != "complete" || status != envelope.StatusDone || attention != "" {
		t.Fatal(activity, body, status, attention)
	}
}

func TestNativeActivityExactAttemptAndWaitPrecedence(t *testing.T) {
	s := securityStore(t)
	j := job{ID: "current", From: "sender/device", Key: "verified-key", nativeAttempt: 2}
	if _, err := s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,attempts) VALUES(?, ?,1,'task','PRIVATE task',1,'running',?,?)`, j.ID, j.From, j.Key, j.nativeAttempt); err != nil {
		t.Fatal(err)
	}
	type activityRow struct {
		ID, Sender, Key, State string
		Conv, PID, Detail      sql.NullString
		Attempt, Replica, Due  int
	}
	read := func() activityRow {
		t.Helper()
		var row activityRow
		if err := s.db.QueryRow(`SELECT id,sender,verified_by,state,conv,pid,detail,attempts,replica,status_due FROM inbox WHERE id=?`, j.ID).Scan(&row.ID, &row.Sender, &row.Key, &row.State, &row.Conv, &row.PID, &row.Detail, &row.Attempt, &row.Replica, &row.Due); err != nil {
			t.Fatal(err)
		}
		return row
	}
	unchanged := func(label string, before activityRow) {
		t.Helper()
		if after := read(); after != before {
			t.Fatalf("%s changed exact activity row: before=%+v after=%+v", label, before, after)
		}
	}
	initial := read()
	for name, change := range map[string]func(*job){
		"wrong-request": func(j *job) { j.ID = "other" },
		"wrong-sender":  func(j *job) { j.From = "other/device" },
		"wrong-key":     func(j *job) { j.Key = "other-key" },
		"old-attempt":   func(j *job) { j.nativeAttempt-- },
	} {
		other := j
		change(&other)
		if changed, err := s.markNativeProgressOutcome(t.Context(), other, nativeProgress{kind: activityReading, at: time.Now()}); changed || err != nil {
			t.Fatal(name, "changed activity")
		}
		unchanged(name, initial)
	}
	// The exact valid attempt may omit optional telemetry when the sole store
	// connection is unavailable. This known fixture omits the step, but the
	// returned error still means an unknown outcome to the caller.
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	start := time.Now()
	deferred, deferredErr := s.markNativeProgressOutcome(t.Context(), j, nativeProgress{kind: activityReading, at: time.Now()})
	elapsed := time.Since(start)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	unchanged("deliberately deferred valid update", initial)
	t.Logf("held-connection accepted=%t unknown=%t elapsed=%s", deferred, deferredErr != nil, elapsed)
	if deferred || deferredErr == nil {
		t.Fatal("occupied store did not return an unknown optional outcome")
	}
	// Acceptance and persistence are separate: an unknown write may have applied.
	p := nativeProgress{kind: activityReading, at: time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)}
	nativeProgressPersisted(t, s, j, p)
	persisted := read()
	duplicate, duplicateErr := s.markNativeProgressOutcome(t.Context(), j, p)
	unchanged("duplicate", persisted)
	if duplicate || duplicateErr != nil {
		t.Fatal("duplicate was not known unchanged")
	}
	var detail string
	var due, attempts int
	if err := s.db.QueryRow(`SELECT detail,status_due,attempts FROM inbox WHERE id=?`, j.ID).Scan(&detail, &due, &attempts); err != nil || !strings.HasPrefix(detail, "Reading files\nReported at ") || !publicActivityDetail(detail) || due != 1 || attempts != 2 {
		t.Fatal(detail, due, attempts, err)
	}
	if _, err := s.db.Exec(`UPDATE inbox SET detail=? WHERE id=?`, roomWaitDetail, j.ID); err != nil {
		t.Fatal(err)
	}
	waiting := read()
	if changed, err := s.markNativeProgressOutcome(t.Context(), j, nativeProgress{kind: activityCommand, at: time.Now()}); changed || err != nil {
		t.Fatal("dependency wait was not known unchanged")
	}
	unchanged("dependency wait", waiting)
	for _, state := range []string{stateNeedHuman, stateAnswered, stateResolved, stateCancelReq, stateCancelled, stateInterrupt, stateJobFailed} {
		if _, err := s.db.Exec(`UPDATE inbox SET state=?,detail='terminal' WHERE id=?`, state, j.ID); err != nil {
			t.Fatal(err)
		}
		terminal := read()
		if changed, err := s.markNativeProgressOutcome(t.Context(), j, nativeProgress{kind: activityCommand, at: time.Now()}); changed || err != nil {
			t.Fatal("terminal activity was not known unchanged", state)
		}
		unchanged(state, terminal)
	}
}

func TestNativeActivityBoundedStoreFailureAndRawOutput(t *testing.T) {
	s := securityStore(t)
	j := job{ID: "run", From: "sender/device", Key: "key", nativeAttempt: 1}
	conn, err := s.db.Conn(t.Context()) // occupy the only connection
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if s.markNativeActivity(t.Context(), j, activityReading) {
		t.Fatal("blocked store reported a write")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatal("optional telemetry waited on the store", elapsed)
	}
	conn.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.markNativeActivity(ctx, j, activityReading) {
		t.Fatal("cancelled telemetry wrote")
	}
	a := newRunActivity()
	w := activityWriter{Writer: io.Discard, activity: a}
	for range 10000 {
		w.Write([]byte("PRIVATE stdout is not progress"))
	}
	if a.native.Load() != nil {
		t.Fatal("raw output became activity")
	}
	for range 10000 {
		a.report(activityReading)
		a.report(activityEditing)
	}
	if a.native.Load().kind != activityEditing || len(a.wake) != 1 {
		t.Fatal("activity queue grew or latest category lost")
	}
}

func TestNativeActivityRequestClassesAndNativeWait(t *testing.T) {
	a := enrolledAt(t, "https://isolated.invalid", "")
	t.Cleanup(func() { a.Close() })
	for _, class := range []string{"device", "owner-dm", "remote-dm", "owner-group", "remote-group", "selected-local-continuation"} {
		t.Run(class, func(t *testing.T) {
			j := job{ID: class, From: "sender/device", Key: "key", nativeAttempt: 1}
			local := strings.HasPrefix(class, "owner-")
			if strings.Contains(class, "dm") || strings.Contains(class, "group") {
				j.Conv, j.PID = "permitted-conversation", "exact-agent"
			}
			if class == "selected-local-continuation" {
				j.Receiver = &ReplyReceiverBinding{ID: "local-binding"}
			}
			if _, err := a.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,attempts,conv,pid,local)
			 VALUES(?,?,1,'task','synthetic request',1,'running',?,1,nullif(?,''),nullif(?,''),?)`, j.ID, j.From, j.Key, j.Conv, j.PID, local); err != nil {
				t.Fatal(err)
			}
			if !a.store.markNativeActivity(t.Context(), j, activityReading) {
				t.Fatal("activity excluded request class")
			}
			var due int
			if err := a.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, j.ID).Scan(&due); err != nil || due != map[bool]int{true: 0, false: 1}[j.Receiver != nil] {
				t.Fatal("wrong status audience for request class", due, err)
			}
			if j.Receiver != nil {
				return // this continuation has no remote native-callback authority
			}
			if _, err := a.store.db.Exec(`INSERT INTO native_requests(token,request,attempt,deadline,rpc,method,params,job,view,state)
			 VALUES(?,?,1,0,'native-call','approval','PRIVATE arguments','{}','PRIVATE approval','pending')`, class+"-token", j.ID); err != nil {
				t.Fatal(err)
			}
			a.noteNativeWait(j.ID)
			if a.store.markNativeActivity(t.Context(), j, activityEditing) {
				t.Fatal("activity overwrote pending native input")
			}
			var detail string
			if err := a.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, j.ID).Scan(&detail); err != nil || detail != nativeInputDetail {
				t.Fatal("native wait missing or private data disclosed", detail, err)
			}
			if _, err := a.store.db.Exec(`UPDATE native_requests SET state='ended' WHERE request=?`, j.ID); err != nil {
				t.Fatal(err)
			}
			a.noteNativeWait(j.ID)
			if err := a.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, j.ID).Scan(&detail); err != nil || detail != "" {
				t.Fatal("native input wait stayed stale", detail, err)
			}
		})
	}
}

func TestNativeActivityPublicDetailRejectsArbitrarySuffix(t *testing.T) {
	for _, detail := range []string{"Reading files", "Reading files\nReported at 2026-10-15T22:00:00Z"} {
		if !publicActivityDetail(detail) {
			t.Fatal("public detail rejected", detail)
		}
	}
	for _, detail := range []string{"PRIVATE output", "Reading files PRIVATE", "Reading files\nReported at PRIVATE", "Reading files\nReported at 2026-10-15T22:00:00Z\nPRIVATE", "Reading files\nReported at 2026-10-15T22:00:00+00:00"} {
		if publicActivityDetail(detail) {
			t.Fatal("arbitrary detail exported as activity")
		}
	}
}

func TestNativeActivityStallKeepsNativeWaitAndForgetsOldTool(t *testing.T) {
	a := newRunActivity()
	a.report(activityReading)
	if _, due := a.stallDue(time.Unix(0, a.last.Load()).Add(runStallNotice)); !due {
		t.Fatal("stall boundary missing")
	}
	w := activityWriter{Writer: io.Discard, activity: a}
	w.Write([]byte("PRIVATE stderr after silence"))
	if a.native.Load() != nil {
		t.Fatal("raw output republished an old native tool event")
	}
	a.report(activityReading)
	if a.native.Load().kind != activityReading {
		t.Fatal("fresh same-kind native event was lost after silence")
	}
	s := securityStore(t)
	if _, err := s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,detail,last_attempt_at) VALUES('native-wait','peer/device',1,'task','synthetic',1,'running',?,1)`, nativeInputDetail); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.markRunVisibility("native-wait", true); err != nil || changed {
		t.Fatal("stall notice overwrote a known native input wait", changed, err)
	}
}
