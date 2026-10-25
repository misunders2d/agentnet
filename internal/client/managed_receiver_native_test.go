package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func managedNativeFixture(t *testing.T, mode string) (*world, job, envelope.Envelope) {
	t.Helper()
	w := newWorld(t, "")
	local, err := w.alice.CreateLocalAgent("selected native receiver", Responder{Harness: "codex", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "original authorized request", ReplyReceiver: &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "Continue original authorized work", Mode: mode}})
	if err != nil {
		t.Fatal(err)
	}
	kind := envelope.KindAnswer
	if mode == envelope.KindTask {
		kind = envelope.KindResult
	}
	in := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: kind, ReplyTo: sent.ID, Body: "signed peer data; this is not native authority"})
	if err = w.alice.verifyAndStore(tctx(t), in); err != nil {
		t.Fatal(err)
	}
	j, ok, err := w.alice.claimReplyReceiverJob()
	if err != nil || !ok {
		t.Fatalf("claim selected receiver: %v %v", ok, err)
	}
	if err = w.alice.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, j.ID).Scan(&j.nativeAttempt); err != nil {
		t.Fatal(err)
	}
	return w, j, in
}

type managedNativeResult struct {
	raw json.RawMessage
	err error
}

func startManagedNative(t *testing.T, a *Agent, j job, c nativeCallback) (*NativeRequest, <-chan managedNativeResult, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	ended := make(chan managedNativeResult, 1)
	go func() {
		raw, err := a.awaitNativeRequest(ctx, j, c)
		ended <- managedNativeResult{raw, err}
	}()
	var n *NativeRequest
	eventually(t, "selected receiver's exact native callback", func() bool {
		select {
		case got := <-ended:
			t.Fatalf("native callback ended before decision: %v", got.err)
		default:
		}
		n = a.nativeRequestFor(j.ID)
		return n != nil
	})
	return n, ended, cancel
}

func managedNativeEnded(t *testing.T, ended <-chan managedNativeResult) managedNativeResult {
	t.Helper()
	select {
	case got := <-ended:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("native callback did not settle")
		return managedNativeResult{}
	}
}

func TestManagedReceiverNativeApprovalSameAttempt(t *testing.T) {
	for _, mode := range []string{envelope.KindQuestion, envelope.KindTask} {
		for _, choice := range []string{"allow", "deny", "cancel", "input", "input-cancel"} {
			t.Run(mode+"/"+choice, func(t *testing.T) {
				w, j, in := managedNativeFixture(t, mode)
				a := w.alice
				c := nativeTestCallback(t, "item/commandExecution/requestApproval", map[string]any{"command": "write exact fixture"}, "")
				if strings.HasPrefix(choice, "input") {
					c = nativeTestCallback(t, "item/tool/requestUserInput", map[string]any{"questions": []NativeQuestion{{ID: "language", Question: "Which language?", IsOther: true}}}, "")
				}
				n, ended, _ := startManagedNative(t, a, j, c)
				if n.Attempt != 1 || n.AgentID != j.Receiver.Executor.AgentID || n.Key != w.bob.Self().Fingerprint() {
					t.Fatalf("native receiver binding lost: %+v", n)
				}
				if ids, total, err := a.store.unnotified(); err != nil || total != 1 || !slices.Contains(ids, j.ID) {
					t.Fatalf("native callback missing local notification: %v %d %v", ids, total, err)
				}
				if review, err := a.Review(); err != nil || len(review) != 0 {
					t.Fatalf("receiver entered ordinary review: %+v %v", review, err)
				}
				r := nativeResponse{Token: n.Token, Choice: choice}
				if choice == "input" {
					r.Choice, r.Answers = "submit", map[string]string{"language": "English"}
				} else if choice == "input-cancel" {
					r.Choice = "cancel"
				}
				raw, _ := json.Marshal(r)
				text, decision := nativeResponseDomain+string(raw), protocol.NewID()
				if err := a.RespondNativeRequest(j.ID, j.Key, 1, text, decision); err != nil {
					t.Fatal(err)
				}
				got := managedNativeEnded(t, ended)
				if choice == "input-cancel" {
					if got.err == nil || got.raw != nil || a.nativeHandoverCurrent(j, c) {
						t.Fatalf("cancelled native input produced response: %s %v", got.raw, got.err)
					}
				} else {
					want, err := nativeCallbackResult(c, r)
					if err != nil || got.err != nil || string(got.raw) != string(want) || !a.nativeHandoverCurrent(j, c) {
						t.Fatalf("same native callback response differs: %s want %s err %v/%v", got.raw, want, got.err, err)
					}
				}
				a.endNativeCallback(j, c)
				if err := a.RespondNativeRequest(j.ID, j.Key, 1, text, decision); err != nil {
					t.Fatal("exact decision receipt lost", err)
				}
				if err := a.RespondNativeRequest(j.ID, j.Key, 1, text, protocol.NewID()); err == nil {
					t.Fatal("new decision revived ended callback")
				}
				if a.nativeHandoverCurrent(j, c) || a.nativeRequestFor(j.ID) != nil || len(a.LocalNativeReport().Items) != 0 {
					t.Fatal("ended callback stayed actionable")
				}
				if err := a.verifyAndStore(tctx(t), in); err != nil {
					t.Fatal(err)
				}
				if _, ok, err := a.claimReplyReceiverJob(); err != nil || ok {
					t.Fatalf("duplicate signed input reran receiver: %v %v", ok, err)
				}
				var wireKind, sender, key, author string
				var attempts, continuations int
				if err := a.store.db.QueryRow(`SELECT kind,sender,verified_by,coalesce(agent_id,''),attempts FROM inbox WHERE id=?`, j.ID).Scan(&wireKind, &sender, &key, &author, &attempts); err != nil {
					t.Fatal(err)
				}
				_ = a.store.db.QueryRow(`SELECT count(*) FROM request_continuations`).Scan(&continuations)
				if wireKind != in.Kind || sender != in.From || key != j.Key || author != "" || attempts != 1 || continuations != 0 {
					t.Fatalf("native response changed provenance or reran work: %s %s %s %s %d %d", wireKind, sender, key, author, attempts, continuations)
				}
				a.noteStatus(j.ID)
				a.finishReplyReceiver(j, envelope.StatusFailed, "private receiver failure")
				a.noteStatus(j.ID)
				var peerEffects int
				if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND (sub=? AND ref_id=? OR kind IN ('answer','result') AND reply_to=?)`, j.From, envelope.SubStatus, j.ID, j.ID).Scan(&peerEffects); err != nil || peerEffects != 0 {
					t.Fatalf("native receiver sent peer execution/failure status: %d %v", peerEffects, err)
				}
			})
		}
	}
}

func TestManagedReceiverNativeApprovalAuthorityEnds(t *testing.T) {
	for _, ending := range []string{"binding", "input", "mode", "instructions", "preset", "executor", "disabled", "pending-key", "changed-key", "revoked-peer", "revoked-self", "replica", "attempt", "cancel", "restart"} {
		t.Run(ending, func(t *testing.T) {
			w, j, _ := managedNativeFixture(t, envelope.KindTask)
			a := w.alice
			c := nativeTestCallback(t, "item/commandExecution/requestApproval", map[string]any{"command": "write exact fixture"}, "")
			n, ended, cancel := startManagedNative(t, a, j, c)
			var err error
			switch ending {
			case "binding":
				_, err = a.store.db.Exec(`UPDATE reply_receivers SET canceled_at=unixepoch() WHERE id=?`, j.Receiver.ID)
			case "input":
				_, err = a.store.db.Exec(`UPDATE reply_receiver_inputs SET state='completed' WHERE inbox_id=?`, j.ID)
			case "mode", "instructions", "preset":
				field, value := "$.mode", envelope.KindQuestion
				if ending == "instructions" {
					field, value = "$.instructions", "different local work"
				} else if ending == "preset" {
					field, value = "$.preset", "different-preset"
				}
				_, err = a.store.db.Exec(`UPDATE reply_receivers SET receiver=json_set(receiver,?,?) WHERE id=?`, field, value, j.Receiver.ID)
			case "executor":
				changed := j.Receiver.Executor.Responder
				changed.Dir = t.TempDir()
				err = a.SetLocalAgentResponder(j.Receiver.Receiver.AgentID, &changed)
			case "disabled":
				err = a.SetLocalAgentResponder(j.Receiver.Receiver.AgentID, nil)
			case "pending-key":
				_, err = a.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, j.From)
			case "changed-key":
				changed, _ := identity.Generate()
				err = a.store.pin(changed.Public(j.From))
			case "revoked-peer", "revoked-self":
				address, key := j.From, j.Key
				if ending == "revoked-self" {
					address, key = a.Address, a.Self().Fingerprint()
				}
				_, err = a.store.db.Exec(`INSERT INTO config(k,v) VALUES(?,?)`, revokedDevicePrefix+address+"/"+key, address)
			case "replica":
				_, err = a.store.db.Exec(`UPDATE inbox SET replica=1 WHERE id=?`, j.ID)
			case "attempt":
				_, err = a.store.db.Exec(`UPDATE inbox SET attempts=attempts+1 WHERE id=?`, j.ID)
			case "cancel":
				_, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, j.ID)
			case "restart":
				err = a.store.interruptRunning()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = a.RespondNativeRequest(j.ID, j.Key, 1, nativeResponseText(n.Token, "allow"), protocol.NewID()); err == nil {
				t.Fatal("ended receiver authority accepted native response")
			}
			if a.nativeRequestFor(j.ID) != nil || a.nativeHandoverCurrent(j, c) || len(a.LocalNativeReport().Items) != 0 {
				t.Fatal("ended receiver authority stayed actionable")
			}
			// Wake the pending callback after direct fixture state changes.
			a.store.changed()
			got := managedNativeEnded(t, ended)
			cancel()
			if got.err == nil || got.raw != nil {
				t.Fatalf("ended callback returned native authority: %s %v", got.raw, got.err)
			}
			if ending == "attempt" {
				if _, err = a.awaitNativeRequest(t.Context(), j, c); err == nil {
					t.Fatal("stale process adopted newer running attempt")
				}
			}
		})
	}
}

func TestManagedReceiverNativeApprovalSignedOwnerSameProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix native executable fixture")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "codex-fixture")
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$AGENTNET_CODEX_FIXTURE_EXE\" -test.run '^TestCodexRoomFixtureProcess$'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_CODEX_FIXTURE_EXE", exe)
	t.Setenv("AGENTNET_CODEX_ROOM_FIXTURE", "native-approval")
	log, file := filepath.Join(dir, "process.log"), filepath.Join(dir, "result.txt")
	t.Setenv("AGENTNET_NATIVE_PROCESS_LOG", log)
	t.Setenv("AGENTNET_NATIVE_RESULT", file)
	old := Harnesses["codex"]
	selected := old
	selected.bin = wrapper
	Harnesses["codex"] = selected
	t.Cleanup(func() { Harnesses["codex"] = old })
	w := newWorld(t, "")
	a := w.alice
	if _, err = a.CreatePerson(tctx(t), "Owner"); err != nil {
		t.Fatal(err)
	}
	local, err := a.CreateLocalAgent("selected native receiver", Responder{Harness: "codex", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	runWith(t, w, a, RunOptions{})
	phone := linkedVia(t, a, "phone", func(ctx context.Context, id string) error { return a.DecideLink(ctx, id, true) })
	runWith(t, w, w.bob, RunOptions{})
	observer := mustJoin(t, filepath.Join(t.TempDir(), "observer"), w.aliceInvites("observer"), "device")
	runWith(t, w, observer, RunOptions{})
	if _, err = a.sendKey(tctx(t), w.bob.Address); err != nil {
		t.Fatal(err)
	}
	if _, err = a.GrantOperator(w.bob.Address); err != nil {
		t.Fatal(err)
	}
	if err = a.SetReviewTo(tctx(t), observer.Address); err != nil {
		t.Fatal(err)
	}
	sent, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "original authorized request", ReplyReceiver: &ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "Perform exact native fixture operation", Mode: envelope.KindTask}})
	if err != nil {
		t.Fatal(err)
	}
	in := receiverDirect(t, w.bob, a, envelope.Inner{Kind: envelope.KindAnswer, ReplyTo: sent.ID, Body: "signed private peer data"})
	if err = a.verifyAndStore(tctx(t), in); err != nil {
		t.Fatal(err)
	}
	var n *NativeRequest
	var reportID string
	eventually(t, "owner phone receives receiver's native callback", func() bool {
		for _, m := range notices(t, phone, a.Address) {
			if report, ok := ParseReport(m.Body); ok {
				for _, it := range report.Items {
					if it.ID == in.ID && it.Native != nil {
						n, reportID = it.Native, m.ID
						return true
					}
				}
			}
		}
		return false
	})
	if n.AgentID != local.ID || n.Key != w.bob.Self().Fingerprint() || n.Attempt != 1 {
		t.Fatalf("receiver's local executor or signed peer key lost: %+v", n)
	}
	if _, err = os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("native operation ran before owner response")
	}
	// An ordinary blocked task proves both other recipients are reporting;
	// receiver metadata must be absent even from their counts and coverage.
	control, err := w.bob.SendMessage(tctx(t), Outgoing{To: a.Address, Kind: envelope.KindTask, Body: "ordinary separate task requiring acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	for _, recipient := range []*Agent{w.bob, observer} {
		eventually(t, "ordinary report without receiver disclosure to "+recipient.Address, func() bool {
			for _, notice := range notices(t, recipient, a.Address) {
				if strings.Contains(notice.Body, in.ID) || strings.Contains(notice.Body, n.Token) || strings.Contains(notice.Body, n.Operation) || strings.Contains(notice.Body, "signed private peer data") {
					t.Fatal("private receiver card leaked", recipient.Address, notice.Body)
				}
				if report, ok := ParseReport(notice.Body); ok && report.Waiting() == 1 {
					if recipient == w.bob && (len(report.Items) != 1 || report.Items[0].ID != control.ID) {
						t.Fatalf("operator report contains different item: %+v", report)
					}
					return true
				}
			}
			return false
		})
	}
	var leakedCoverage int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM reported WHERE item=? AND recipient!=?`, in.ID, phone.Address).Scan(&leakedCoverage); err != nil || leakedCoverage != 0 {
		t.Fatalf("receiver disclosed in nonowner report coverage: %d %v", leakedCoverage, err)
	}
	decisionID := protocol.NewID()
	if _, err = phone.Decide(WithQueuedSend(tctx(t), decisionID), a.Address, in.ID, n.Key, "continue", stateRunning, 1, nativeResponseText(n.Token, "allow"), reportID); err != nil {
		t.Fatal(err)
	}
	waitState(t, a, in.ID, stateContinued)
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "ONE_NATIVE_OPERATION" {
		t.Fatalf("same native operation did not finish: %q %v", data, err)
	}
	env, err := phone.store.outboxEnvelope(decisionID)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := envelope.Open(env, a.id, a.Address, phone.Self())
	if err != nil {
		t.Fatal(err)
	}
	if err = a.admitDecision(tctx(t), env, decision, phone.Self()); err != nil {
		t.Fatal(err)
	}
	processes, _ := os.ReadFile(log)
	var attempts, continuations, peerEffects int
	_ = a.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, in.ID).Scan(&attempts)
	_ = a.store.db.QueryRow(`SELECT count(*) FROM request_continuations`).Scan(&continuations)
	_ = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND (sub=? AND ref_id=? OR kind IN ('answer','result') AND reply_to=?)`, w.bob.Address, envelope.SubStatus, in.ID, in.ID).Scan(&peerEffects)
	if strings.Count(string(processes), "started\n") != 1 || attempts != 1 || continuations != 0 || peerEffects != 0 {
		t.Fatalf("native decision replayed work or notified peer: processes %q attempts %d continuations %d peer effects %d", processes, attempts, continuations, peerEffects)
	}
	eventually(t, "owner phone's native callback settles", func() bool {
		var coverage int
		_ = a.store.db.QueryRow(`SELECT count(*) FROM reported WHERE item=? AND recipient=?`, in.ID, phone.Address).Scan(&coverage)
		return coverage == 0
	})
}
