package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Opt-in actual installed Codex, private default daemon, loopback-only provider.
// Normal tests never launch a harness, use an account or install a runtime.
func TestCodexActualSelectedReceiver(t *testing.T) {
	bin, root := os.Getenv("AGENTNET_CODEX_BINARY"), os.Getenv("AGENTNET_CODEX_RUNTIME")
	if bin == "" || root == "" {
		t.Skip("opt-in isolated native Codex journey")
	}
	interfaces, e := net.Interfaces()
	if e != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("native fixture requires loopback-only namespace")
	}
	w := newWorld(t, "")
	stopAlice := runAgent(t, w.alice)
	runAgent(t, w.bob)
	cmd := exec.Command("python3", "testdata/codex_receiver_native.py")
	cmd.Env = append(os.Environ(), "AGENTNET_CODEX_HOME="+w.alice.home)
	input, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	ended := false
	t.Cleanup(func() {
		if !ended {
			fmt.Fprintln(input, `{"finish":true}`)
			input.Close()
			cmd.Wait()
		}
		os.MkdirAll(filepath.Join(root, "evidence"), 0700)
		os.WriteFile(filepath.Join(root, "evidence", "driver.log"), log.Bytes(), 0600)
	})
	wait := func(label string, pred func() bool) {
		t.Helper()
		end := time.Now().Add(35 * time.Second)
		for time.Now().Before(end) {
			if pred() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		rows, _ := w.alice.ReplyReceiverBindings()
		raw, _ := json.MarshalIndent(rows, "", "  ")
		os.WriteFile(filepath.Join(root, "evidence", "first-failure-bindings.json"), raw, 0600)
		t.Fatalf("%s deadline; driver %s", label, log.String())
	}
	var selected ReplySessionView
	wait("genuine normal default-daemon registration", func() bool {
		rows, _ := w.alice.ReplySessions()
		for _, r := range rows {
			if r.Harness == "codex" && r.Active {
				selected = r
				return true
			}
		}
		return false
	})
	record, e := replySessionIn(w.alice.store.db, selected.Handle)
	if e != nil || record.Codex == nil {
		t.Fatalf("route %+v %v", record, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	e = record.Codex.verify(ctx, false)
	cancel()
	if e != nil {
		t.Fatal(e)
	}
	wait("initial native turn", func() bool {
		entries, e := codexNativeEntries(record.File, record.SessionID)
		return e == nil && len(entries) > 5
	})
	fmt.Fprintln(input, `{"spawn":true}`)
	var other ReplySessionView
	wait("second unselected native session", func() bool {
		rows, _ := w.alice.ReplySessions()
		for _, r := range rows {
			if r.Handle != selected.Handle && r.Harness == "codex" && r.Active {
				other = r
				return true
			}
		}
		return false
	})
	otherRecord, e := replySessionIn(w.alice.store.db, other.Handle)
	if e != nil {
		t.Fatal(e)
	}
	mainCalls := func(sid string) int {
		raw, e := os.ReadFile(filepath.Join(root, "evidence", "requests.json"))
		if e != nil {
			return -1
		}
		var calls []struct {
			Thread string `json:"thread"`
			Title  bool   `json:"title_request"`
		}
		if json.Unmarshal(raw, &calls) != nil {
			return -1
		}
		n := 0
		for _, v := range calls {
			if v.Thread == sid && !v.Title {
				n++
			}
		}
		return n
	}
	wait("other initial native main request", func() bool { return mainCalls(otherRecord.SessionID) == 1 })
	send := exec.Command(bin, "--home", w.alice.home, "ask", "--wait", "0", "--reply-receiver", "session:"+selected.Handle, w.bob.Address, "Original LOCAL Codex goal1003y")
	if out, e := send.CombinedOutput(); e != nil {
		t.Fatalf("originating CLI %v %s", e, out)
	}
	var binding ReplyReceiverBinding
	wait("local CLI binding", func() bool {
		rows, _ := w.alice.ReplyReceiverBindings()
		for _, r := range rows {
			if r.Receiver.SessionHandle == selected.Handle {
				binding = r
				return true
			}
		}
		return false
	})
	sent, e := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindAnswer, Body: "Verified remote clarification1003y; continue original local goal", ReplyTo: binding.RequestRef})
	if e != nil {
		t.Fatal(e)
	}
	wait("actual native durable receipt and AgentNet ACK", func() bool {
		var state string
		w.alice.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, binding.ID, sent.ID).Scan(&state)
		return state == "accepted"
	})
	var raw string
	if e = w.alice.store.db.QueryRow(`SELECT live_claim FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, binding.ID, sent.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var claim liveInputClaim
	if e = json.Unmarshal([]byte(raw), &claim); e != nil {
		t.Fatal(e)
	}
	text := codexInputText(&ReplyReceiverDelivery{BindingID: binding.ID, InputID: sent.ID, ClaimID: claim.ID, InputToken: claim.Token, RequestBody: "Original LOCAL Codex goal1003y", Message: Message{From: w.bob.Address, Kind: envelope.KindAnswer, Body: "Verified remote clarification1003y; continue original local goal"}})
	if ok, e := codexNativeReceipt(record.File, record.SessionID, text); e != nil || !ok {
		t.Fatalf("receipt %v %v", ok, e)
	}
	if ok, e := codexNativeReceipt(otherRecord.File, otherRecord.SessionID, text); e != nil || ok {
		t.Fatalf("unselected session received %v %v", ok, e)
	}
	wait("selected native continuation completed by fixture", func() bool {
		entries, e := codexNativeEntries(record.File, record.SessionID)
		if e != nil {
			return false
		}
		n := 0
		for _, v := range entries {
			var row struct {
				Type    string `json:"type"`
				Payload struct {
					Type string `json:"type"`
				} `json:"payload"`
			}
			if json.Unmarshal(v, &row) == nil && row.Type == "event_msg" && row.Payload.Type == "task_complete" {
				n++
			}
		}
		return n == 2
	})
	if n := mainCalls(otherRecord.SessionID); n != 1 {
		t.Fatalf("selected reply woke other native session: main requests %d, expected original1", n)
	}
	// Stop/start the enrolled daemon, preserving the accepted relation and token.
	stopAlice()
	again, e := Open(w.alice.home)
	if e != nil {
		t.Fatal(e)
	}
	defer again.Close()
	stopAgain := runAgent(t, again)
	time.Sleep(200 * time.Millisecond)
	call := ReplySessionCall{Handle: record.Handle, Generation: record.Generation, OwnerToken: record.OwnerToken, SessionID: record.SessionID, File: record.File}
	if d, e := again.TakeReplyReceiverInput(call); e != nil || d != nil {
		t.Fatalf("accepted input repeated after restart %+v %v", d, e)
	}
	bytesNative, e := os.ReadFile(record.File)
	if e != nil {
		t.Fatal(e)
	}
	var copies int
	for _, line := range strings.Split(string(bytesNative), "\n") {
		var v struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &v) == nil && v.Type == "event_msg" && v.Payload.Type == "item_completed" && v.Payload.Item.Type == "UserMessage" && strings.Contains(line, claim.Token) {
			copies++
		}
	}
	if copies != 1 {
		t.Fatal("native input token repeated")
	}
	remaining := map[string]any{}
	if os.Getenv("AGENTNET_CODEX_GATE") == "remaining" {
		busyRunning := func() bool {
			rows, _ := os.ReadDir("/proc")
			for _, row := range rows {
				pid, e := strconv.Atoi(row.Name())
				if e != nil {
					continue
				}
				cmd, _ := os.ReadFile(filepath.Join("/proc", row.Name(), "cmdline"))
				if string(cmd) != "/usr/bin/sleep\x008\x00" {
					continue
				}
				for steps := 0; pid > 1 && steps < 32; steps++ {
					if pid == record.Codex.PID {
						return true
					}
					parent, _, _, e := codexProcess(pid)
					if e != nil {
						break
					}
					pid = parent
				}
			}
			return false
		}
		completedTurns := func() int {
			entries, _ := codexNativeEntries(record.File, record.SessionID)
			n := 0
			for _, v := range entries {
				var row struct {
					Type    string `json:"type"`
					Payload struct {
						Type string `json:"type"`
					} `json:"payload"`
				}
				if json.Unmarshal(v, &row) == nil && row.Type == "event_msg" && row.Payload.Type == "task_complete" {
					n++
				}
			}
			return n
		}
		localGate := func(gate string) {
			t.Helper()
			fmt.Fprintf(input, "{\"gate\":%q,\"thread\":%q}\n", gate, record.SessionID)
			wait("fixture gate ready", func() bool {
				raw, _ := os.ReadFile(filepath.Join(root, "evidence", "gate-ready.json"))
				return bytes.Contains(raw, []byte(gate))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, e := codexCommand(ctx, *record.Codex, "queue", "--remote", record.Codex.Endpoint, "--thread", record.SessionID, "--message", "Synthetic locally authored "+gate+" tool gate"); e != nil {
				t.Fatal(e)
			}
		}
		localGate("busy")
		wait("native busy tool began", busyRunning)
		busyBinding, busyInput := nativeInput(t, again, w.bob, selected.Handle)
		wait("busy reply durably claimed", func() bool {
			var c string
			again.store.db.QueryRow(`SELECT live_claim FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, busyBinding, busyInput).Scan(&c)
			return c != ""
		})
		time.Sleep(300 * time.Millisecond)
		var busyState string
		again.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, busyBinding, busyInput).Scan(&busyState)
		if busyState != "pending" || !busyRunning() {
			t.Fatalf("busy reply interrupted tool: state %s busy%v", busyState, busyRunning())
		}
		wait("busy reply accepted after safe tool boundary", func() bool {
			again.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, busyBinding, busyInput).Scan(&busyState)
			return busyState == "accepted" && !busyRunning()
		})
		wait("busy continuation settled", func() bool { return completedTurns() >= 4 })
		remaining["busy_queued_until_tool_end"] = true
		stopAgain()
		lostBinding, lostInput := nativeInput(t, again, w.bob, selected.Handle)
		d, e := again.TakeReplyReceiverInput(call)
		if e != nil || d == nil || d.ReconcileOnly || d.BindingID != lostBinding || d.InputID != lostInput {
			t.Fatalf("pre-dispatch claim %+v %v", d, e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, e = codexCommand(ctx, *record.Codex, "queue", "--remote", record.Codex.Endpoint, "--thread", record.SessionID, "--message", codexInputText(d))
		cancel()
		if e != nil {
			t.Fatal(e)
		}
		wait("native receipt before simulated lost ACK", func() bool { ok, _ := codexNativeReceipt(record.File, record.SessionID, codexInputText(d)); return ok })
		again.Close()
		again, e = Open(w.alice.home)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { again.Close() })
		stopAgain = runAgent(t, again)
		wait("restart reconciles receipt without enqueue", func() bool {
			var s string
			again.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, lostBinding, lostInput).Scan(&s)
			return s == "accepted"
		})
		wait("lost input native settled", func() bool { return completedTurns() >= 5 })
		stopAgain()
		missingBinding, missingInput := nativeInput(t, again, w.bob, selected.Handle)
		d, e = again.TakeReplyReceiverInput(call)
		if e != nil || d == nil || d.ReconcileOnly || d.BindingID != missingBinding || d.InputID != missingInput {
			t.Fatalf("uncertain initial claim %+v %v", d, e)
		}
		before := mainCalls(record.SessionID)
		again.Close()
		again, e = Open(w.alice.home)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { again.Close() })
		stopAgain = runAgent(t, again)
		time.Sleep(time.Second)
		next, e := again.TakeReplyReceiverInput(call)
		if e != nil || next == nil || !next.ReconcileOnly || next.InputToken != d.InputToken {
			t.Fatalf("uncertain restarted claim %+v %v", next, e)
		}
		if ok, _ := codexNativeReceipt(record.File, record.SessionID, codexInputText(d)); ok || mainCalls(record.SessionID) != before {
			t.Fatal("uncertain claim resent")
		}
		remaining["lost_ack_reconciled"] = true
		remaining["missing_receipt_held_without_resend"] = true
		var missingState string
		again.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, missingBinding, missingInput).Scan(&missingState)
		if missingState != "pending" {
			t.Fatalf("uncertain marked %s", missingState)
		}
		if _, ok, e := again.store.claimJob("stub"); e != nil || ok {
			t.Fatalf("selected input escaped default fence: %v %v", ok, e)
		}
		remaining["uncertain_pending_default_claim_zero"] = true
		localGate("permission")
		wait("native approval required", func() bool {
			raw, _ := os.ReadFile(filepath.Join(root, "evidence", "screen-0.txt"))
			return bytes.Contains(raw, []byte("must-not-exist")) && (bytes.Contains(raw, []byte("Would you like")) || bytes.Contains(raw, []byte("Esc")))
		})
		if _, e := os.Stat(filepath.Join(root, "work", "must-not-exist")); !os.IsNotExist(e) {
			t.Fatal("native permission automatically approved")
		}
		fmt.Fprintln(input, `{"reject":0}`)
		time.Sleep(500 * time.Millisecond)
		if _, e := os.Stat(filepath.Join(root, "work", "must-not-exist")); !os.IsNotExist(e) {
			t.Fatal("denied tool ran")
		}
		remaining["native_permission_not_autoapproved"] = true
	}
	if os.Getenv("AGENTNET_CODEX_GATE") == "close" {
		stopAgain = codexActualCloseGate(t, again, w.bob, record, otherRecord, input, root, stopAgain)
	}
	result, _ := json.MarshalIndent(map[string]any{"selected": selected, "other": other, "binding": binding.ID, "input": sent.ID, "accepted_not_completed": true, "single_native_receipt": true, "other_main_requests": mainCalls(otherRecord.SessionID), "selected_main_requests": mainCalls(record.SessionID), "origin_cli_exited": true, "daemon_restart": true, "remaining": remaining}, "", "  ")
	os.WriteFile(filepath.Join(root, "evidence", "result.json"), result, 0600)
	stopAgain()
	fmt.Fprintln(input, `{"finish":true}`)
	input.Close()
	e = cmd.Wait()
	ended = true
	if e != nil {
		t.Fatalf("native driver %v %s", e, log.String())
	}
}
