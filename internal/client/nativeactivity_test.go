package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

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
		if s.markNativeActivity(t.Context(), other, activityReading) {
			t.Fatal(name, "changed activity")
		}
		unchanged(name, initial)
	}
	// The exact valid attempt may omit optional telemetry when the sole store
	// connection is unavailable. False must leave every stored field unchanged.
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	start := time.Now()
	deferred := s.markNativeActivity(t.Context(), j, activityReading)
	elapsed := time.Since(start)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	unchanged("deliberately deferred valid update", initial)
	t.Logf("held-connection valid update=%t elapsed=%s row=%+v", deferred, elapsed, read())
	if deferred {
		t.Fatal("optional update persisted through the occupied store")
	}
	// A fresh public report may follow an omitted optional write. Allow one
	// such re-emission only after proving no detail/count/authority field moved;
	// an accepted write and the subsequent duplicate checks remain mandatory.
	first := false
	for emission := 0; emission < 2; emission++ {
		first = s.markNativeActivity(t.Context(), j, activityReading)
		t.Logf("available-store emission=%d accepted=%t row=%+v", emission+1, first, read())
		if first {
			break
		}
		unchanged("omitted optional update", initial)
	}
	if !first {
		t.Fatal("no valid update accepted after bounded optional re-emission")
	}
	accepted := read()
	duplicate := s.markNativeActivity(t.Context(), j, activityReading)
	unchanged("duplicate", accepted)
	t.Logf("accepted=%t duplicate=%t row=%+v", first, duplicate, read())
	if duplicate {
		t.Fatal("duplicate persisted")
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
	if s.markNativeActivity(t.Context(), j, activityCommand) {
		t.Fatal("activity overwrote dependency wait")
	}
	unchanged("dependency wait", waiting)
	for _, state := range []string{stateNeedHuman, stateAnswered, stateResolved, stateCancelReq, stateCancelled, stateInterrupt, stateJobFailed} {
		if _, err := s.db.Exec(`UPDATE inbox SET state=?,detail='terminal' WHERE id=?`, state, j.ID); err != nil {
			t.Fatal(err)
		}
		terminal := read()
		if s.markNativeActivity(t.Context(), j, activityCommand) {
			t.Fatal("late activity changed", state)
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
	if a.native.Load() != 0 {
		t.Fatal("raw output became activity")
	}
	for range 10000 {
		a.report(activityReading)
		a.report(activityEditing)
	}
	if nativeActivity(a.native.Load()) != activityEditing || len(a.wake) != 1 {
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
	if a.native.Load() != 0 {
		t.Fatal("raw output republished an old native tool event")
	}
	a.report(activityReading)
	if nativeActivity(a.native.Load()) != activityReading {
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
