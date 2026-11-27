//go:build linux

package client

import (
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

// TestClaudeDesktopApprovalLive checks a real Claude Desktop Code chat's
// native tool approval through a connected Claude chat agent (MEL-829).
// AGENTNET_LIVE_CLAUDE_DESKTOP_DIR is a scratch folder and
// AGENTNET_LIVE_AGENTNET_BIN this build's agentnet. The test writes the
// folder's project hooks for bob's home, then waits for the person to open
// a new Code chat in that folder in Claude Desktop and send any message;
// for the third request they click Deny on the Write approval.
func TestClaudeDesktopApprovalLive(t *testing.T) {
	dir, bin := os.Getenv("AGENTNET_LIVE_CLAUDE_DESKTOP_DIR"), os.Getenv("AGENTNET_LIVE_AGENTNET_BIN")
	if dir == "" || bin == "" {
		t.Skip("opt-in: AGENTNET_LIVE_CLAUDE_DESKTOP_DIR and AGENTNET_LIVE_AGENTNET_BIN")
	}
	w := newWorld(t, "")
	runAgent(t, w.alice)
	label, name, _ := protocol.SplitAddress(w.alice.Address)
	var prof protocol.Profile
	eventually(t, "alice session published", func() bool {
		err := w.bob.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof)
		return err == nil && len(prof.Sessions) == 1
	})
	caps := protocol.CapsRecord{Address: w.alice.Address, Session: prof.Sessions[0], Caps: []string{protocol.CapAgentIdentity}, TS: time.Now().Unix() + 100}
	caps.Sign(w.alice.id.Sign)
	if err := w.alice.hub.do(tctx(t), "PUT", "/v1/caps", caps, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	chatOwnDevice(t, w)
	runAgent(t, w.bob)

	cmd := "'" + bin + "' --home '" + w.bob.home + "' hook claude"
	hooks := map[string]any{}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop", "SessionEnd"} {
		hooks[ev] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 10}}}}
	}
	raw, _ := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	fmt.Printf("LIVE-OPEN-CHAT %s\n", real)

	var sid string
	deadline := time.Now().Add(45 * time.Minute)
	for sid == "" {
		list, err := w.bob.ClaudeChatCandidates()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list {
			if c.Dir == real && c.Inbox {
				sid = c.SessionID
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no Claude Desktop chat registered in the folder")
		}
		time.Sleep(time.Second)
	}
	reg, _ := replySessionIn(w.bob.store.db, mustHandle(t, w.bob, sid))
	exe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", reg.Claude.PID))
	fmt.Printf("LIVE chat session=%s pid=%d engine=%s inbox=%s\n", sid, reg.Claude.PID, exe, reg.ClaudeInbox)
	agentID, err := w.bob.ConnectClaudeChat(sid, "Claude Desktop")
	if err != nil {
		t.Fatal(err)
	}

	ask := func(body string) (Message, string) {
		in := envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: body,
			Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: agentID}}
		if err := w.alice.store.addOutbox(envelope.Envelope{ID: in.ID, From: in.From, To: in.To}, in, "", nil); err != nil {
			t.Fatal(err)
		}
		if err := w.bob.store.addInbox(in, w.alice.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
		notifyDaemon(w.bob.home)
		start := time.Now()
		var row Message
		for end := time.Now().Add(15 * time.Minute); ; {
			row = inboxRow(t, w.bob, in.ID)
			if row.State != stateRunning && row.State != "" && row.State != statePending && row.State != stateAccepted {
				break
			}
			if time.Now().After(end) {
				t.Fatalf("no end: %+v", row)
			}
			time.Sleep(500 * time.Millisecond)
		}
		answer := ""
		var answerID string
		if w.bob.store.db.QueryRow(`SELECT coalesce(result_id,'') FROM inbox WHERE id=?`, in.ID).Scan(&answerID) == nil && answerID != "" && row.State == stateAnswered {
			if env, err := w.bob.store.outboxEnvelope(answerID); err == nil && w.alice.accept(tctx(t), env) == nil {
				answer = inboxRow(t, w.alice, answerID).Body
			}
		}
		led, _, _ := chatRowIn(w.bob.store.db, in.ID)
		fmt.Printf("LIVE request %s state=%s in %s ledger=%s detail=%q answer=%q\n", in.ID, row.State, time.Since(start).Round(time.Millisecond), led.State, row.Detail, answer)
		return row, answer
	}
	tok := "AN-CLAUDE-LIVE-" + protocol.NewID()[:8]
	if _, got := ask("Reply with exactly " + tok + " and nothing else. Do not use any tools."); !strings.Contains(got, tok) {
		t.Fatalf("first answer %q", got)
	}
	probe := filepath.Join(real, "agentnet-approval-probe.txt")
	fmt.Println("LIVE-DENY-NOW")
	row, got := ask("Use your Write tool to create the file " + probe + " containing the word hi. If the write is denied, say so plainly and do not retry.")
	if _, err := os.Stat(probe); err == nil {
		t.Fatalf("probe file was created: %+v %q", row, got)
	}
	low := strings.ToLower(got + " " + row.Detail)
	if !(strings.Contains(low, "denied") || strings.Contains(low, "declined") || strings.Contains(low, "rejected") || strings.Contains(low, "not created") || strings.Contains(low, "permission")) {
		t.Fatalf("approval outcome %+v %q", row, got)
	}
	fmt.Println("LIVE-DONE")
}

func mustHandle(t *testing.T, a *Agent, sid string) string {
	t.Helper()
	var h string
	if err := a.store.db.QueryRow(`SELECT handle FROM reply_sessions WHERE json_extract(record,'$.session_id')=?`, sid).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}
