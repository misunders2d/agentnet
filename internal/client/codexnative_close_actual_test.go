package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexActualCleanCloseOriginalBackup(t *testing.T) {
	t.Setenv("AGENTNET_CODEX_GATE", "close")
	TestCodexActualSelectedReceiver(t)
}

func TestCodexUnqualifiedEndCannotRecordShutdown(t *testing.T) {
	w := newWorld(t, "")
	if _, err := w.alice.CodexReplySessionHook("SessionEnd", "unqualified", filepath.Join(t.TempDir(), "rollout.jsonl")); err == nil {
		t.Fatal("unqualified End accepted")
	}
	var count int
	if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unqualified End changed registry: %d %v", count, err)
	}
}

// All native lifecycle inputs below come from the installed TUI/daemon. Claims
// use existing private owner APIs; no closure record or hook is fabricated.
func codexActualCloseGate(t *testing.T, a, b *Agent, selected, other replySessionRecord, input io.Writer, root string, stop func()) func() {
	t.Helper()
	evidence := filepath.Join(root, "evidence")
	waitWithin := func(label string, budget time.Duration, pred func() bool) {
		t.Helper()
		end := time.Now().Add(budget)
		for time.Now().Before(end) {
			if pred() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		witness := map[string]any{"stage": label, "budget_seconds": budget.Seconds(), "before_finally": true}
		for name, handle := range map[string]string{"selected": selected.Handle, "other": other.Handle} {
			r, err := replySessionIn(a.store.db, handle)
			witness[name] = map[string]any{"record_read_ok": err == nil, "active": r.Active, "generation": r.Generation, "close_generation": r.CloseGeneration, "close_reason": r.CloseReason, "codex_route_present": r.Codex != nil}
		}
		counts := map[string]int{}
		if data, err := os.ReadFile(filepath.Join(evidence, "hooks.jsonl")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				var event struct {
					Event   string `json:"hook_event_name"`
					Session string `json:"session_id"`
				}
				if json.Unmarshal([]byte(line), &event) == nil {
					counts[event.Event]++
					if event.Session == selected.SessionID {
						counts["selected/"+event.Event]++
					}
				}
			}
		}
		witness["hook_counts"] = counts
		data, _ := json.MarshalIndent(witness, "", "  ")
		os.WriteFile(filepath.Join(evidence, "close-first-failure.json"), data, 0600)
		t.Fatal(label + " deadline")
	}
	wait := func(label string, pred func() bool) { waitWithin(label, 35*time.Second, pred) }
	st := installAgentStub(t)
	backup, err := a.CreateLocalAgent("original authorized backup", Responder{Harness: "agentstub", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	unused, err := a.CreateLocalAgent("unselected backup", Responder{Harness: "agentstub", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	accepted, acceptedRef := closedRequest(t, a, b, selected.Handle, backup.ID)
	acceptedInput := closedInput(t, a, b, acceptedRef)
	wait("actual accepted opt-in native input", func() bool {
		var state string
		a.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, accepted, acceptedInput).Scan(&state)
		return state == "accepted"
	})
	stop() // AgentNet delivery daemon only; native Codex daemon/TUIs stay alive.
	uncertain, uncertainRef := closedRequest(t, a, b, selected.Handle, backup.ID)
	uncertainInput := closedInput(t, a, b, uncertainRef)
	call := ReplySessionCall{Handle: selected.Handle, Generation: selected.Generation, OwnerToken: selected.OwnerToken, SessionID: selected.SessionID, File: selected.File}
	claim, err := a.TakeReplyReceiverInput(call)
	if err != nil || claim == nil || claim.BindingID != uncertain || claim.InputID != uncertainInput || claim.ReconcileOnly {
		t.Fatalf("legitimate uncertain claim: %+v %v", claim, err)
	}
	eligible, eligibleRef := closedRequest(t, a, b, selected.Handle, backup.ID)
	eligibleInput := closedInput(t, a, b, eligibleRef)
	original, err := replyReceiverIn(a.store.db, eligible)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	raw, err := os.ReadFile(filepath.Join(evidence, "pids.json"))
	if err != nil || json.Unmarshal(raw, &pids) != nil || len(pids) != 2 {
		t.Fatal("missing exact two native PID witness")
	}
	terminalClose := func(index int) {
		t.Helper()
		fmt.Fprintf(input, "{\"close\":%d}\n", index)
		wait("native exit or genuine double-press prompt", func() bool {
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", pids[index])); os.IsNotExist(err) {
				return true
			}
			data, _ := os.ReadFile(filepath.Join(evidence, fmt.Sprintf("screen-%d.txt", index)))
			text := strings.ToLower(string(data))
			return strings.Contains(text, "again") && (strings.Contains(text, "ctrl+d") || strings.Contains(text, "ctrl-d") || strings.Contains(text, "ctrl d"))
		})
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pids[index])); err == nil {
			fmt.Fprintf(input, "{\"close\":%d}\n", index)
		}
		wait("genuine selected TUI exited before finish", func() bool { _, err := os.Stat(fmt.Sprintf("/proc/%d", pids[index])); return os.IsNotExist(err) })
	}
	terminalClose(0)
	// Native Ctrl-D unsubscribes; pinned default idle unload takes 60s.
	waitWithin("trusted current native terminal record", 75*time.Second, func() bool {
		r, err := replySessionIn(a.store.db, selected.Handle)
		return err == nil && !r.Active && r.CloseReason == "shutdown" && r.CloseGeneration == r.Generation && r.Generation == selected.Generation+1
	})
	// JSON fixture uses snake_case; inspect without reinterpreting native reason.
	raw, err = os.ReadFile(filepath.Join(evidence, "close-observation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var obs map[string]json.RawMessage
	json.Unmarshal(raw, &obs)
	var exits map[string]map[string]any
	json.Unmarshal(obs["exits"], &exits)
	x := exits["0"]
	if x == nil || x["other_alive"] != true || x["driver_loop_active"] != true || x["provider_alive"] != true || x["exit_code"] != float64(0) {
		t.Fatalf("closure boundary witness %s", raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = selected.Codex.verify(ctx, false)
	cancel()
	if err != nil {
		t.Fatal("live Codex daemon lost at closure: " + err.Error())
	}
	nextStop := runAgent(t, a)
	wait("one original managed backup consumed unclaimed input", func() bool {
		var state string
		a.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, eligible, eligibleInput).Scan(&state)
		return state == "completed"
	})
	after, err := replyReceiverIn(a.store.db, eligible)
	if err != nil || after.Receiver.AgentID != backup.ID || after.Receiver.Instructions != original.Receiver.OnClose.Instructions || after.Receiver.Mode != original.Receiver.OnClose.Mode || after.HandoffState != "handed_over" {
		t.Fatalf("original backup authority not preserved: %+v %v", after, err)
	}
	for _, id := range []string{accepted, uncertain} {
		r, err := replyReceiverIn(a.store.db, id)
		if err != nil || r.Receiver.Kind != "live_session" || r.HandoffState == "handed_over" {
			t.Fatalf("accepted/claimed-uncertain handed off: %+v %v", r, err)
		}
	}
	if st.runs() != 1 {
		t.Fatalf("backup runs %d, want1", st.runs())
	}
	nextStop()
	nextStop = runAgent(t, a)
	time.Sleep(400 * time.Millisecond)
	if st.runs() != 1 {
		t.Fatal("backup duplicated after delivery-daemon restart")
	}
	for _, r := range receiverBindings(t, a) {
		if r.Receiver.Kind == "managed_agent" && r.Receiver.AgentID == unused.ID {
			t.Fatal("unselected managed backup chosen")
		}
	}
	if _, ok, err := a.store.claimJob("agentstub"); err != nil || ok {
		t.Fatalf("default stole local selected data: %v %v", ok, err)
	}
	// Timeout remains vendor-owned. Delay only this second native End in the
	// existing private callback; no production timeout/permission is changed.
	fmt.Fprintf(input, "{\"delay_end\":{\"session_id\":%q,\"seconds\":15}}\n", other.SessionID)
	wait("owned delay control installed", func() bool {
		data, _ := os.ReadFile(filepath.Join(evidence, "end-delay.json"))
		return strings.Contains(string(data), other.SessionID)
	})
	terminalClose(1)
	waitWithin("genuine second idle unload reached delayed End hook", 75*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(evidence, "end-delay-started.json"))
		return err == nil
	})
	var delay struct {
		PID       int  `json:"pid"`
		PGID      int  `json:"pgid"`
		ChildPID  int  `json:"child_pid"`
		Forwarded bool `json:"forwarded"`
	}
	raw, err = os.ReadFile(filepath.Join(evidence, "end-delay-started.json"))
	if err != nil || json.Unmarshal(raw, &delay) != nil || delay.PID < 1 || delay.PGID < 1 || delay.ChildPID < 1 || delay.Forwarded {
		t.Fatal("owned hook timeout witness absent")
	}
	wait("native timeout killed exact callback group", func() bool {
		for _, pid := range []int{delay.PID, delay.ChildPID, delay.PGID} {
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !os.IsNotExist(err) {
				return false
			}
		}
		return true
	})
	r, err := replySessionIn(a.store.db, other.Handle)
	if err != nil || !r.Active || r.CloseReason == "shutdown" {
		t.Fatalf("timed-out hook wrote late closure: %+v %v", r, err)
	}
	if st.runs() != 1 {
		t.Fatal("timeout replayed backup")
	}
	result := map[string]any{"native_end_and_tui_exit": true, "second_native_alive_at_first_close": true, "codex_daemon_alive_at_first_close": true, "before_finish_signal_cleanup": true, "original_backup_runs": 1, "default_runs": 0, "other_backup_runs": 0, "accepted_held": true, "claimed_missing_receipt_uncertain_held": true, "restart_duplicate_zero": true, "hook_timeout_group_absent": true, "hook_timeout_no_late_shutdown": true, "native_version": codexNativeVersion, "selected_pid": pids[0], "other_pid": pids[1], "timeout_pid": delay.PID, "timeout_child_pid": delay.ChildPID, "timeout_pgid": delay.PGID, "source": "genuine native lifecycle; no received reason authority"}
	out, _ := json.MarshalIndent(result, "", "  ")
	os.WriteFile(filepath.Join(evidence, "close-result.json"), out, 0600)
	return nextStop
}
