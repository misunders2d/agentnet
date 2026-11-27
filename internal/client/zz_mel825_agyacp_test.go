package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An `agy acp` stand-in, modelled on the live agy 1.5.18 exchange recorded
// in docs/revival/M4.md: a prompt sent while a turn runs makes agy answer the
// earlier prompt at once with a bare end_turn and finish on the newer one.
// The test executable is the native process; no vendor or model runs.
func TestAgyACPProcessHelper(t *testing.T) {
	log := os.Getenv("AGENTNET_TEST_AGYACP_LOG")
	if log == "" {
		t.Skip("native stand-in only")
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if slices.Equal(args, []string{"acp", "--help"}) {
		fmt.Println("Start the Agent Client Protocol (ACP) stdio server\n  --stdio  Use stdio JSON-RPC transport (default)")
		os.Exit(0)
	}
	if len(args) == 0 || args[0] != "acp" || slices.Contains(args, "--input-format") || slices.Contains(args, "--conversation") {
		os.Exit(9)
	}
	mode := os.Getenv("AGENTNET_TEST_AGYACP_MODE")
	var mu sync.Mutex
	record := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		json.NewEncoder(f).Encode(v)
		f.Close()
	}
	record(map[string]any{"process": args})
	in := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				os.Exit(3)
			}
			record(m)
			in <- m
		}
		close(in)
	}()
	enc := json.NewEncoder(os.Stdout)
	var session string
	reply := func(id any, result any) {
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	chunk := func(text string) {
		enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session,
			"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": text}}}})
	}
	toolCall := func() {
		enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session,
			"update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "web search", "kind": "fetch", "status": "pending"}}})
	}
	promptText := func(m map[string]any) string {
		blocks, _ := m["params"].(map[string]any)["prompt"].([]any)
		text, _ := blocks[0].(map[string]any)["text"].(string)
		lines := strings.Split(strings.TrimSpace(text), "\n")
		for _, line := range lines {
			if strings.Contains(line, "Moscow") {
				return line // the correction itself, not its added context
			}
		}
		return lines[len(lines)-1]
	}
	next := func() map[string]any {
		m, ok := <-in
		if !ok {
			os.Exit(0)
		}
		return m
	}
	for {
		m := next()
		switch m["method"] {
		case "initialize":
			reply(m["id"], map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{"resume": map[string]any{}}}})
		case "session/new":
			if mode == "auth" {
				enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m["id"], "error": map[string]any{"code": -32000, "message": "Authentication required"}})
				continue
			}
			session = newUUID()
			reply(m["id"], map[string]any{"sessionId": session})
		case "session/resume":
			if mode == "noresume" {
				enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m["id"], "error": map[string]any{"code": -32002, "message": "Session not found"}})
				continue
			}
			session = m["params"].(map[string]any)["sessionId"].(string)
			reply(m["id"], map[string]any{"sessionId": session})
		case "session/prompt":
			first := m["id"]
			switch mode {
			case "steer-early":
				// The real agy: a prompt that arrives during the turn takes it
				// over. The earlier prompt ends at once with a bare end_turn;
				// the answer and its end_turn belong to the later prompt.
				correction := next()
				if correction["method"] != "session/prompt" {
					os.Exit(4)
				}
				reply(first, map[string]any{"stopReason": "end_turn"})
				chunk("Answer for: " + promptText(correction))
				reply(correction["id"], map[string]any{"stopReason": "end_turn", "usage": map[string]int{"totalTokens": 3}})
			case "steer", "steer-race", "refuse", "crash":
				// Commentary and a tool call while the original works.
				chunk("Looking up the original city.")
				toolCall()
				correction := next()
				if correction["method"] != "session/prompt" {
					os.Exit(4)
				}
				switch mode {
				case "crash":
					os.Exit(3)
				case "refuse":
					enc.Encode(map[string]any{"jsonrpc": "2.0", "id": correction["id"], "error": map[string]any{"code": -32000,
						"message": "the running prompt is waiting for a permission or question answer; answer it or send session/cancel, then send this prompt again"}})
					chunk("Answer for: the original request")
					reply(first, map[string]any{"stopReason": "end_turn", "usage": map[string]int{"totalTokens": 3}})
				case "steer-race":
					// The original finished just as the correction was written:
					// agy runs the correction as the next turn.
					chunk("Answer for: the original request")
					reply(first, map[string]any{"stopReason": "end_turn", "usage": map[string]int{"totalTokens": 3}})
					chunk("Answer for: " + promptText(correction))
					reply(correction["id"], map[string]any{"stopReason": "end_turn", "usage": map[string]int{"totalTokens": 3}})
				default:
					reply(first, map[string]any{"stopReason": "end_turn"}) // the handover
					chunk("Answer for: " + promptText(correction))
					reply(correction["id"], map[string]any{"stopReason": "end_turn", "usage": map[string]int{"totalTokens": 3}})
				}
			case "permission":
				enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "session/request_permission", "params": map[string]any{"sessionId": session,
					"toolCall": map[string]any{"toolCallId": "t1", "title": "rm -rf build"}, "options": []map[string]string{
						{"optionId": "always", "name": "Always allow", "kind": "allow_always"}, {"optionId": "once", "name": "Allow", "kind": "allow_once"},
						{"optionId": "never", "name": "Always deny", "kind": "reject_always"}, {"optionId": "no", "name": "Deny", "kind": "reject_once"}}}})
				if answer := next(); answer["id"] != float64(0) {
					os.Exit(5)
				}
				chunk("an answer written after a denied action")
				reply(first, map[string]any{"stopReason": "end_turn"})
			default:
				chunk("Answer for: " + promptText(m))
				reply(first, map[string]any{"stopReason": "end_turn", "usage": map[string]int{"totalTokens": 3}})
			}
		}
	}
}

type agyACPFixtureLog struct{ path string }

func (l agyACPFixtureLog) messages(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines[:len(lines)-1] { // a partial trailing write is not a record yet
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func (l agyACPFixtureLog) count(t *testing.T, key, value string) (n int) {
	for _, m := range l.messages(t) {
		if v, ok := m[key]; ok && (value == "" || v == value) {
			n++
		}
	}
	return n
}

func (l agyACPFixtureLog) prompts(t *testing.T) (texts []string) {
	for _, m := range l.messages(t) {
		if m["method"] == "session/prompt" {
			blocks, _ := m["params"].(map[string]any)["prompt"].([]any)
			text, _ := blocks[0].(map[string]any)["text"].(string)
			texts = append(texts, text)
		}
	}
	return texts
}

// agyACPFixture makes the test executable the agy harness, with ACP.
func agyACPFixture(t *testing.T, mode string) (agyACPFixtureLog, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log, dir := filepath.Join(t.TempDir(), "acp"), t.TempDir()
	t.Setenv("AGENTNET_TEST_AGYACP_LOG", log)
	t.Setenv("AGENTNET_TEST_AGYACP_MODE", mode)
	old := Harnesses["agy"]
	h := old
	h.bin = exe
	h.question = append([]string{"-test.run=^TestAgyACPProcessHelper$", "--"}, h.question...)
	h.task = append([]string{"-test.run=^TestAgyACPProcessHelper$", "--"}, h.task...)
	Harnesses["agy"] = h
	t.Cleanup(func() { Harnesses["agy"] = old })
	return agyACPFixtureLog{log}, dir
}

// MEL-825: Antigravity over ACP, against the stand-in process. These
// top-level tests stay last in the package list, so the earlier race
// shards keep their tests, and each lands in the next shard: one heavy
// test would push a single shard past its 600-second bound.
func TestAgyACP(t *testing.T) {
	t.Run("launch selection", agyACPLaunchSelection)
	t.Run("bridge", agyACPBridgeCases)
}

func TestAgyACPEdit(t *testing.T) {
	t.Run("edit steers the running request", agyACPEditSteersRunningRequest)
}

func TestAgyACPSessions(t *testing.T) {
	t.Run("sessions", agyACPWorkerSessions)
	t.Run("refused resume", agyACPRefusedResume)
}

func TestAgyACPNeedsOwner(t *testing.T) {
	t.Run("needs the owner", agyACPNeedsOwner)
}

func TestAgyACPLive(t *testing.T) {
	t.Run("live agy", agyACPLive)
}

// Opt-in (AGENTNET_LIVE_NATIVE=agy): the installed agy, in an empty
// temporary directory, takes a correction into its running turn. Harmless
// no-tool prompts; no AgentNet Hub or chats; agy keeps the conversation.
func agyACPLive(t *testing.T) {
	if os.Getenv("AGENTNET_LIVE_NATIVE") != "agy" {
		t.Skip("opt-in: AGENTNET_LIVE_NATIVE=agy")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	launch, err := resolveNativeLaunch(Harnesses["agy"].bin)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, launch.Path, Harnesses["agy"].question...)
	cmd.Dir, cmd.Env = t.TempDir(), os.Environ()
	if !agyACPSupported(ctx, cmd) {
		t.Fatal("the installed agy has no ACP server")
	}
	prepAgyACPLaunch(cmd)
	b, err := newAgyACPBridge(ctx, cmd, "Reply with exactly the word OK and nothing else. Do not use any tools.", "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	<-b.followupReady()
	steered := make(chan string, 1)
	go func() {
		steered <- b.steer(ctx, "live-correction", "Correction to the request above: reply with exactly the word STEERED instead, and nothing else. Do not use any tools.")
	}()
	<-b.settledCh()
	runErr := cmd.Wait()
	b.finish()
	b.close()
	out, failure, auth := b.outcome()
	body, status, attention := out.result(runErr, &agyDiagnostics{auth: auth})
	got := <-steered
	t.Logf("steer=%s status=%s body=%q attention=%q failure=%q conversation=%s", got, status, body, attention, failure, out.conversation)
	if got != steerAccepted || status != envelope.StatusDone || !strings.Contains(body, "STEERED") {
		t.Fatal("the correction did not reach the running agy turn")
	}
}

// agyACPCommand is the harness's own launch, selected for ACP.
func agyACPCommand(t *testing.T, ctx context.Context, dir string, extra ...string) *exec.Cmd {
	t.Helper()
	h := Harnesses["agy"]
	cmd := exec.CommandContext(ctx, h.bin, append(slices.Clone(h.question), extra...)...)
	cmd.Dir, cmd.Env = dir, os.Environ()
	if !agyACPSupported(ctx, cmd) {
		t.Fatal("the ACP stand-in was not recognised")
	}
	prepAgyACPLaunch(cmd)
	return cmd
}

func agyACPLaunchSelection(t *testing.T) {
	_, dir := agyFixture(t, "answer")
	h := Harnesses["agy"]
	cmd := exec.Command(h.bin, append(slices.Clone(h.question), "--conversation", "c", "--add-dir", "/x")...)
	cmd.Dir, cmd.Env = dir, os.Environ()
	if agyACPSupported(t.Context(), cmd) {
		t.Fatal("an agy without the ACP server was taken for one")
	}
	prepAgyACPLaunch(cmd)
	if want := append(slices.Clone(h.question[:2]), "acp", "--add-dir", "/x"); !slices.Equal(cmd.Args[1:], want) {
		t.Fatalf("ACP launch %q, want %q", cmd.Args[1:], want)
	}
}

// The bridge against the stand-in process: a correction joins the running
// turn and the one answer is the corrected turn's; a refused correction stays
// queued; a lost process accepts nothing; nothing is ever allowed natively.
func agyACPBridgeCases(t *testing.T) {
	for _, tc := range []struct{ mode, steer, answer, attention string }{
		{mode: "steer", steer: steerAccepted, answer: "Answer for: Use Moscow instead"},
		{mode: "steer-race", steer: steerAccepted, answer: "Answer for: Use Moscow instead"},
		// The live failure: the correction follows the request at once and
		// the request's own write is slow; agy must still see them in order.
		{mode: "steer-early", steer: steerAccepted, answer: "Answer for: Use Moscow instead"},
		{mode: "refuse", steer: steerUnsupported, answer: "Answer for: the original request"},
		{mode: "crash", steer: steerUnknown},
		{mode: "permission", attention: "could not obtain a native tool approval"},
		{mode: "auth", attention: "requires sign-in"},
		{mode: "answer", answer: "Answer for: Original request"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			if tc.mode == "steer-early" {
				old := agyACPBeforeWrite
				agyACPBeforeWrite = func(v any) {
					if m, _ := v.(map[string]any); m["method"] == "session/prompt" && strings.Contains(fmt.Sprint(m["params"]), "Original request") {
						time.Sleep(200 * time.Millisecond)
					}
				}
				t.Cleanup(func() { agyACPBeforeWrite = old })
			}
			log, dir := agyACPFixture(t, tc.mode)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := agyACPCommand(t, ctx, dir)
			b, err := newAgyACPBridge(ctx, cmd, "Original request", "", false)
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var steered chan string
			if tc.steer != "" {
				select {
				case <-b.followupReady():
				case <-ctx.Done():
					t.Fatal("the request never started")
				}
				if tc.mode != "steer-early" {
					eventually(t, "native commentary read", func() bool {
						b.mu.Lock()
						defer b.mu.Unlock()
						return b.answer != "" || strings.Contains(b.run, "original city")
					})
				}
				steered = make(chan string, 1)
				go func() { steered <- b.steer(ctx, "edit-1", "Explicit correction edit-1\nUse Moscow instead") }()
			}
			waited := make(chan error, 1)
			go func() {
				err := cmd.Wait()
				b.finish() // as the worker does once the process has ended
				waited <- err
			}()
			if tc.steer != "" {
				eventually(t, "the request settled", func() bool { return isClosed(b.settledCh()) })
			}
			runErr := <-waited
			b.close()
			if steered != nil {
				if got := <-steered; got != tc.steer {
					t.Fatalf("steer %q, want %q", got, tc.steer)
				}
			}
			out, failure, auth := b.outcome()
			if failure != "" {
				t.Fatalf("failure %q", failure)
			}
			d := &agyDiagnostics{auth: auth}
			body, status, attention := out.result(runErr, d)
			switch {
			case tc.attention != "":
				if !strings.Contains(attention, tc.attention) || body != "" {
					t.Fatalf("attention %q body %q, want %q", attention, body, tc.attention)
				}
			case tc.mode == "crash":
				if status != envelope.StatusFailed || out.complete {
					t.Fatalf("a lost process gave %q %q", status, body)
				}
			default:
				if status != envelope.StatusDone || body != tc.answer || !validAgyConversation(out.conversation) {
					t.Fatalf("answer %q %q (conversation %q), want %q", status, body, out.conversation, tc.answer)
				}
			}
			if tc.mode == "permission" {
				var choice any
				for _, m := range log.messages(t) {
					if m["id"] == float64(0) && m["result"] != nil {
						choice = m["result"].(map[string]any)["outcome"].(map[string]any)["optionId"]
					}
				}
				if choice != "no" {
					t.Fatalf("permission answer %v, want only the one-time rejection", choice)
				}
			}
			if n := log.count(t, "process", ""); n != 1 {
				t.Fatalf("%d processes", n)
			}
		})
	}
}

// The owner's report: an edit of a request agy is still working on reaches
// the running turn. One process, one answer to the edited text, no follow-up.
func agyACPEditSteersRunningRequest(t *testing.T) {
	for _, mode := range []string{"steer", "crash"} {
		t.Run(mode, func(t *testing.T) {
			log, dir := agyACPFixture(t, mode)
			w, _, packet, _ := groupTurnsFixture(t)
			a := w.bob
			p := p6Member(t, w.alice, a, packet.State.Conv)
			if err := a.Approve(w.alice.Address); err != nil {
				t.Fatal(err)
			}
			id := protocol.NewID()
			if _, _, err := w.alice.AskAgentInTopicQueued(tctx(t), id, p.PID, envelope.KindQuestion, "Give me tomorrow's weather forecast in Kyiv", "", nil, protocol.NewID(), 0); err != nil {
				t.Fatal(err)
			}
			waitState(t, a, id, stateAgentWaiting)
			parent, found, _, _, err := a.store.claimAgentPage("agy", a.Address, a.Self().Fingerprint(), 0, agentPage)
			if err != nil || !found || parent.ID != id {
				t.Fatalf("claim original: %+v %t %v", parent, found, err)
			}
			parent.nativeAttempt = 1
			done := make(chan struct{})
			go func() {
				defer close(done)
				a.runJob(tctx(t), parent, &Responder{Harness: "agy", Dir: dir, Timeout: time.Minute}, nil)
			}()
			eventually(t, "agy working on the original", func() bool { return len(log.prompts(t)) == 1 })
			ref := ControlRef{Conv: packet.State.Conv, ID: id, Fingerprint: w.alice.Self().Fingerprint()}
			if _, err = w.alice.Revise(tctx(t), ref, "Give me tomorrow's weather forecast in Moscow"); err != nil {
				t.Fatal(err)
			}
			<-done
			prompts := log.prompts(t)
			if len(prompts) != 2 || strings.Count(prompts[1], "Moscow") != 1 || log.count(t, "process", "") != 1 {
				t.Fatalf("the edit did not reach the running process: %q", prompts)
			}
			var editID, editState string
			if err := a.store.db.QueryRow(`SELECT id,state FROM inbox WHERE sub=? AND reply_to=?`, requestEditJob, id).Scan(&editID, &editState); err != nil {
				t.Fatal(err)
			}
			if mode == "crash" {
				// Lost with the correction unconfirmed: neither runs again.
				if editState != stateNeedHuman {
					t.Fatalf("an unconfirmed correction is %q", editState)
				}
				if _, found, _, _, err := a.store.claimAgentPage("agy", a.Address, a.Self().Fingerprint(), 0, agentPage); err != nil || found {
					t.Fatalf("work became claimable again after the lost process: %t %v", found, err)
				}
				if state := jobState(t, a, id); state != stateJobFailed {
					t.Fatalf("a lost process left the request %q", state)
				}
				noReply(t, w.alice, packet.State.Conv, id)
				return
			}
			waitState(t, a, id, stateAnswered)
			var detail string
			if err := a.store.db.QueryRow(`SELECT state,coalesce(detail,'') FROM inbox WHERE id=?`, editID).Scan(&editState, &detail); err != nil {
				t.Fatal(err)
			}
			if editState != stateAnswered || !strings.HasPrefix(detail, "Accepted correction to request "+id) {
				t.Fatalf("the edit is %q (%q), want taken by the running request", editState, detail)
			}
			if m := replyAt(t, w.alice, packet.State.Conv, id); !strings.Contains(m.Body, "Answer for: Give me tomorrow's weather forecast in Moscow") || strings.Contains(m.Body, "original") {
				t.Fatalf("the answer is not the edited request's: %q", m.Body)
			}
			eventually(t, "requester sees the edit reached the live run", func() bool {
				controls, err := w.alice.controlsOf(ref)
				return err == nil && controls.EditExecution == "steered"
			})
			if _, found, _, _, err := a.store.claimAgentPage("agy", a.Address, a.Self().Fingerprint(), 0, agentPage); err != nil || found {
				t.Fatalf("a steered edit was queued again as a follow-up: %t %v", found, err)
			}
		})
	}
}

// Conversation continuity over ACP equals the stream transport's: a new
// conversation is recorded, the next message goes into the warm process as
// one more prompt, and after a restart session/resume continues the same id.
func agyACPWorkerSessions(t *testing.T) {
	log, dir := agyACPFixture(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "agy", dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	stop, _ := runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	pool := warmPoolOf(t, w.bob)
	q1, a1 := ask(t, w, "")
	ref := sessionOf(t, w.bob, q1)
	if ref == nil || ref.Harness != "agy" || !validAgyConversation(ref.ID) {
		t.Fatalf("session=%+v", ref)
	}
	if m, ok := findReply(w.alice, q1); !ok || !strings.Contains(m.Body, "Answer for:") {
		t.Fatalf("first answer %q", m.Body)
	}
	eventually(t, "agy kept warm", func() bool { return pool.idleCount() == 1 })
	q2, a2 := ask(t, w, a1)
	if r2 := sessionOf(t, w.bob, q2); r2 == nil || r2.ID != ref.ID {
		t.Fatalf("second turn's session %+v, want %s", r2, ref.ID)
	}
	if log.count(t, "process", "") != 1 || log.count(t, "method", "initialize") != 1 || len(log.prompts(t)) != 2 {
		t.Fatalf("the second message did not go into the running agy: %+v", log.messages(t))
	}
	stop()
	runWith(t, w, w.bob, RunOptions{})
	q3, _ := ask(t, w, a2)
	if r3 := sessionOf(t, w.bob, q3); r3 == nil || r3.ID != ref.ID {
		t.Fatalf("after a restart the session is %+v, want %s", r3, ref.ID)
	}
	var resumed any
	for _, m := range log.messages(t) {
		if m["method"] == "session/resume" {
			resumed = m["params"].(map[string]any)["sessionId"]
		}
	}
	if log.count(t, "process", "") != 2 || log.count(t, "method", "session/new") != 1 || resumed != ref.ID || len(log.prompts(t)) != 3 {
		t.Fatalf("a restart did not resume %s: %+v", ref.ID, log.messages(t))
	}
}

// A saved conversation agy no longer continues (an upgrade from the stream
// transport, a deleted conversation): that message fails and is not retried,
// and the next one starts a new conversation, as with every harness.
func agyACPRefusedResume(t *testing.T) {
	log, dir := agyACPFixture(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "agy", dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	stop, _ := runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q1, a1 := ask(t, w, "")
	ref := sessionOf(t, w.bob, q1)
	stop()
	t.Setenv("AGENTNET_TEST_AGYACP_MODE", "noresume")
	runWith(t, w, w.bob, RunOptions{})
	r2, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "synthetic question", ReplyTo: a1})
	if err != nil {
		t.Fatal(err)
	}
	var a2 Message
	eventually(t, "the refused resume is reported", func() bool {
		m, ok := findReply(w.alice, r2.ID)
		a2 = m
		return ok
	})
	if a2.Status != envelope.StatusFailed || !strings.Contains(a2.Body, "resuming the background session "+ref.ID+" failed; not retried automatically") || !strings.Contains(a2.Body, "Session not found") {
		t.Fatalf("refused resume answered %q %q", a2.Status, a2.Body)
	}
	q3, _ := ask(t, w, a2.ID)
	if r3 := sessionOf(t, w.bob, q3); r3 == nil || r3.ID == ref.ID || !validAgyConversation(r3.ID) {
		t.Fatalf("after a refused resume the session is %+v", r3)
	}
	if n := log.count(t, "method", "session/new"); n != 2 || len(log.prompts(t)) != 2 {
		t.Fatalf("session/new %d, prompts %q", n, log.prompts(t))
	}
}

// Through the worker: a permission request agy leaves to its client, or a
// missing sign-in, needs the owner; nothing is sent and nothing is retried.
func agyACPNeedsOwner(t *testing.T) {
	for _, mode := range []string{"permission", "auth"} {
		t.Run(mode, func(t *testing.T) {
			log, dir := agyACPFixture(t, mode)
			w := newWorld(t, "")
			setResponder(t, w.bob, "agy", dir, time.Minute)
			w.bob.Approve(w.alice.Address)
			runWith(t, w, w.bob, RunOptions{})
			runWith(t, w, w.alice, RunOptions{})
			q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "synthetic request"})
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, w.bob, q.ID, stateNeedHuman)
			var detail string
			if err := w.bob.store.db.QueryRow(`SELECT coalesce(detail,'') FROM inbox WHERE id=?`, q.ID).Scan(&detail); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"permission": "Antigravity could not obtain a native tool approval", "auth": "Antigravity requires sign-in"}[mode]
			if !strings.Contains(detail, want) {
				t.Fatalf("needs-you detail %q, want %q", detail, want)
			}
			time.Sleep(300 * time.Millisecond)
			if _, ok := findReply(w.alice, q.ID); ok {
				t.Fatal("a native blocker became an answer")
			}
			if log.count(t, "process", "") != 1 {
				t.Fatal("request retried")
			}
		})
	}
}
