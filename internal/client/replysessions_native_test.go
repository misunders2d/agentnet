package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// The real installed native binaries run only with a deterministic provider,
// private profiles and a loopback-only network namespace. Ordinary test runs
// do not invoke any installed harness or a model.
func TestReplySessionActualNativePiOMP(t *testing.T) {
	bin := os.Getenv("AGENTNET_LIVE_RECEIVER_BINARY")
	if bin == "" {
		t.Skip("opt-in isolated native fixture")
	}
	interfaces, e := net.Interfaces()
	if e != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("native fixture requires loopback-only namespace")
	}
	bin, e = filepath.Abs(bin)
	if e != nil {
		t.Fatal(e)
	}
	for _, harness := range []string{"pi", "omp"} {
		t.Run(harness, func(t *testing.T) { nativeReceiverJourney(t, bin, harness) })
	}
}

type nativeEvent struct {
	Event, Tag, File, Session string
	Success                   bool
	Inputs                    []map[string]string
	Details                   map[string]string
}

func nativeReceiverJourney(t *testing.T, bin, harness string, closed ...bool) {
	handoff := len(closed) > 0 && closed[0]
	root := filepath.Join(os.Getenv("AGENTNET_LIVE_RECEIVER_EVIDENCE"), harness)
	if root == harness {
		root = t.TempDir()
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	w := newWorld(t, "")
	stopAlice := runAgent(t, w.alice)
	runAgent(t, w.bob)
	for _, d := range []string{"profile", "agent", "sessions", "work"} {
		if e := os.MkdirAll(filepath.Join(root, d), 0700); e != nil {
			t.Fatal(e)
		}
	}
	eventsFile := filepath.Join(root, "events.jsonl")
	os.Remove(eventsFile)
	logEvents := func() []nativeEvent {
		f, e := os.Open(eventsFile)
		if e != nil {
			return nil
		}
		defer f.Close()
		var out []nativeEvent
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 4096), 2<<20)
		for s.Scan() {
			var v nativeEvent
			if json.Unmarshal(s.Bytes(), &v) == nil {
				out = append(out, v)
			}
		}
		return out
	}
	wait := func(label string, pred func() bool) {
		t.Helper()
		end := time.Now().Add(25 * time.Second)
		for time.Now().Before(end) {
			if pred() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}

		if handoff {
			rows, _ := w.alice.ReplyReceiverBindings()
			raw, _ := json.MarshalIndent(rows, "", "  ")
			os.WriteFile(filepath.Join(root, "first-failure-bindings.json"), raw, 0600)
		}
		t.Fatalf("native %s deadline; events %+v", label, logEvents())
	}
	quoted := func(s string) string { r, _ := json.Marshal(s); return string(r) }
	wrapper := filepath.Join(root, "hook-forward.py")
	// Drop ACK before the real command to reproduce a crash-window loss. Never
	// log local owner credentials; all other hooks use the real native CLI.
	script := "#!/usr/bin/python3\nimport os,sys,subprocess,json\na=sys.argv[1:]\ndata=None\nif 'hook' in a:\n data=sys.stdin.buffer.read()\n if os.path.exists(" + quoted(filepath.Join(root, "drop-ack")) + ") and json.loads(data).get('receiver_action')=='ack': sys.exit(0)\nr=subprocess.run([" + quoted(bin) + "]+a,input=data)\nsys.exit(r.returncode)\n"
	if e := os.WriteFile(wrapper, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}

	var closedAgent string
	var closedWork string
	if handoff {
		tools := filepath.Join(root, "tools")
		os.MkdirAll(tools, 0700)
		closedWork = filepath.Join(root, "managed")
		os.MkdirAll(closedWork, 0700)
		script := `#!/usr/bin/python3
import os,sys,json
from pathlib import Path
p=sys.stdin.read(); d=Path.cwd()
assert os.environ.get('AGENTNET_REPLY_BINDING')
assert 'WRITE_CLOSED_HANDOFF_REPORT' in p and 'original local request idle' in p
assert 'native reply data' in p
assert '--permission-mode' not in sys.argv
with (d/'runs').open('a') as f: f.write('selected managed run\n')
(d/'report').write_text('actual work after explicit native shutdown from original local delegation\n')
print('selected managed work complete')
`
		os.WriteFile(filepath.Join(tools, "claude"), []byte(script), 0700)
		t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
		a, e := w.alice.CreateLocalAgent("explicit closed receiver", Responder{Harness: "claude", Dir: closedWork, Timeout: time.Minute})
		if e != nil {
			t.Fatal(e)
		}
		closedAgent = a.ID
		defaults := filepath.Join(root, "default")
		os.MkdirAll(defaults, 0700)
		setResponder(t, w.alice, "claude", defaults, time.Minute)
	}

	cmd := exec.Command(bin, "--home", w.alice.home, "hooks", "show", harness)
	rendered, e := cmd.Output()
	if e != nil {
		t.Fatal(e)
	}
	rendered = bytes.Replace(rendered, []byte(quoted(bin)), []byte(quoted(wrapper)), 1)
	extension := filepath.Join(root, "receiver.ts")
	if e = os.WriteFile(extension, rendered, 0600); e != nil {
		t.Fatal(e)
	}
	probe, e := os.ReadFile("testdata/reply_session_native.ts")
	if e != nil {
		t.Fatal(e)
	}
	stream := "@oh-my-pi/pi-ai/utils/event-stream"
	api := "typesafe"
	parameters := "pi.zod.object({})"
	endEvent := "agent_end"
	nodeRoot := "/home/misunderstood/.local/share/mise/installs/node/26.10.0"
	if harness == "pi" {
		stream = "file://" + nodeRoot + "/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-ai/dist/utils/event-stream.js"
		api = "synthetic-live"
		parameters = "{type:'object',properties:{},additionalProperties:false}"
		endEvent = "agent_settled"
	}
	replacements := map[string]string{"__ROOT__": root, "__BIN__": bin, "__HOME__": w.alice.home, "__PEER__": w.bob.Address, "__STREAM__": stream, "__API__": api, "__PARAMETERS__": parameters, "__END__": endEvent}
	for from, to := range replacements {
		probe = bytes.ReplaceAll(probe, []byte(from), []byte(to))
	}
	if handoff {
		flags := []string{"--on-close-agent", closedAgent, "--continue", "WRITE_CLOSED_HANDOFF_REPORT", "--continue-mode", "task"}
		raw, _ := json.Marshal(flags)
		inject := strings.TrimSuffix(strings.TrimPrefix(string(raw), "["), "]")
		probe = bytes.Replace(probe, []byte("'ask','--wait','0',PEER"), []byte("'ask','--wait','0',"+inject+",PEER"), 1)
	}
	probeFile := filepath.Join(root, "probe.ts")
	if e = os.WriteFile(probeFile, probe, 0600); e != nil {
		t.Fatal(e)
	}
	env := []string{"HOME=" + filepath.Join(root, "profile"), "PATH=" + nodeRoot + "/bin:/usr/bin:/bin", "PI_CODING_AGENT_DIR=" + filepath.Join(root, "agent"), "XDG_CONFIG_HOME=" + filepath.Join(root, "profile/config"), "XDG_CACHE_HOME=" + filepath.Join(root, "profile/cache"), "XDG_DATA_HOME=" + filepath.Join(root, "profile/data"), "TERM=dumb"}
	if handoff {
		env[1] = "PATH=" + filepath.Join(root, "tools") + ":" + nodeRoot + "/bin:/usr/bin:/bin"
	}
	executable := nodeRoot + "/bin/node"
	base := []string{nodeRoot + "/lib/node_modules/@earendil-works/pi-coding-agent/dist/cli.js", "--mode", "rpc", "--provider", "synthetic-live-receiver", "--model", "fixture", "--no-tools", "--tools", "fixture_gate", "--no-extensions", "--no-skills", "--no-prompt-templates", "-e", extension, "-e", probeFile, "--session-dir", filepath.Join(root, "sessions")}
	if harness == "omp" {
		executable = "/home/misunderstood/.local/share/mise/installs/github-can1357-oh-my-pi/18.4.8/omp"
		config := filepath.Join(root, "agent/config.yml")
		os.WriteFile(config, []byte("disabledProviders: [local, web, ollama, llama.cpp, apple, lm-studio]\nenabledProviders: [synthetic-live-receiver]\n"), 0600)
		os.WriteFile(filepath.Join(root, "agent/models.yml"), []byte("providers:\n  synthetic-live-receiver:\n    api: typesafe\n    baseUrl: http://127.0.0.1:1/v1\n    apiKey: synthetic-only\n    models:\n      - id: fixture\n        name: Synthetic fixture\n        reasoning: false\n        input: [text]\n        cost: {input: 0, output: 0, cacheRead: 0, cacheWrite: 0}\n        contextWindow: 100000\n        maxTokens: 100\n"), 0600)
		env = append(env, "PI_CONFIG_FILES="+config)
		base = []string{"--mode", "rpc", "--provider", "synthetic-live-receiver", "--model", "fixture", "--trusted-extension", extension, "--trusted-extension", probeFile, "--no-skills", "--no-rules", "--no-tools", "--tools", "fixture_gate", "--no-lsp", "--no-pty", "--no-title", "--no-prewalk", "--session-dir", filepath.Join(root, "sessions")}
	}
	type child struct {
		cmd   *exec.Cmd
		stdin io.WriteCloser
		done  chan error
		log   *os.File
	}
	launch := func(file string) *child {
		t.Helper()
		args := append([]string(nil), base...)
		if file != "" {
			args = append(args, "--session", file)
		}
		c := &child{cmd: exec.Command(executable, args...), done: make(chan error, 1)}
		c.cmd.Env = env
		c.cmd.Dir = filepath.Join(root, "work")
		c.log, e = os.CreateTemp(root, "native-rpc-*.jsonl")
		if e != nil {
			t.Fatal(e)
		}
		c.cmd.Stdout = c.log
		c.cmd.Stderr = c.log
		c.stdin, e = c.cmd.StdinPipe()
		if e != nil {
			t.Fatal(e)
		}
		if e = c.cmd.Start(); e != nil {
			t.Fatal(e)
		}
		go func() { c.done <- c.cmd.Wait() }()
		t.Cleanup(func() {
			c.stdin.Close()
			select {
			case <-c.done:
			case <-time.After(3 * time.Second):
				c.cmd.Process.Kill()
				<-c.done
			}
			c.log.Close()
		})
		return c
	}
	send := func(c *child, text string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"type": "prompt", "id": fmt.Sprint(time.Now().UnixNano()), "message": text})
		if _, e = c.stdin.Write(append(raw, '\n')); e != nil {
			t.Fatal(e)
		}
	}
	stop := func(c *child) {
		t.Helper()
		c.stdin.Close()
		select {
		case e := <-c.done:
			if e != nil {
				t.Fatal(e)
			}
			c.done <- nil
		case <-time.After(8 * time.Second):
			c.cmd.Process.Kill()
			t.Fatal("native shutdown deadline")
		}
		c.log.Close()
	}
	first := launch("")
	wait("auto-register", func() bool { v, _ := w.alice.ReplySessions(); return len(v) == 1 && len(logEvents()) > 0 })
	initial := logEvents()[0]
	if initial.Session == "" || initial.File == "" {
		t.Fatalf("native identity %+v", initial)
	}
	send(first, "/receiver-probe idle")
	var idle ReplyReceiverBinding
	wait("inherited request", func() bool {
		v, _ := w.alice.ReplyReceiverBindings()
		for _, b := range v {
			var body string
			w.alice.store.db.QueryRow(`SELECT body FROM outbox WHERE reply_receiver=? LIMIT 1`, b.ID).Scan(&body)
			if body == "original local request idle" {
				idle = b
				return b.Receiver.Kind == "live_session"
			}
		}
		return false
	})
	remote := func(b ReplyReceiverBinding, file string) string {
		t.Helper()
		var files []string
		if file != "" {
			files = []string{file}
		}
		r, e := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, ReplyTo: b.RequestRef, Body: "native reply data", Files: files})
		if e != nil {
			t.Fatal(e)
		}
		return r.ID
	}
	state := func(binding, input string) string {
		var s string
		w.alice.store.db.QueryRow(`SELECT state FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, binding, input).Scan(&s)
		return s
	}
	if handoff {
		if idle.Receiver.OnClose == nil || idle.Receiver.OnClose.AgentID != closedAgent {
			t.Fatal("native CLI did not preauthorize exact closed receiver")
		}
		beforeClose, e := replySessionIn(w.alice.store.db, idle.Receiver.SessionHandle)
		if e != nil {
			t.Fatal(e)
		}
		stop(first)
		wait("durable native shutdown and handoff", func() bool {
			v, e := replyReceiverIn(w.alice.store.db, idle.ID)
			return e == nil && v.HandoffState == "handed_over" && v.Receiver.AgentID == closedAgent
		})
		afterClose, e := replySessionIn(w.alice.store.db, idle.Receiver.SessionHandle)
		if e != nil || afterClose.Harness != harness || afterClose.SessionID != beforeClose.SessionID || afterClose.File != beforeClose.File || afterClose.Active || afterClose.CloseReason != "shutdown" || afterClose.Generation <= beforeClose.Generation || afterClose.CloseGeneration != afterClose.Generation {
			t.Fatalf("native shutdown did not fence exact registered generation: before=%d after=%d closed=%d reason=%s err=%v", beforeClose.Generation, afterClose.Generation, afterClose.CloseGeneration, afterClose.CloseReason, e)
		}
		stopAlice()
		resumed, e := Open(w.alice.home)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { resumed.Close() })
		w.alice = resumed
		stopResumed := runAgent(t, resumed)
		input := remote(idle, "")
		wait("managed actual work after native close and daemon restart", func() bool {
			raw, e := os.ReadFile(filepath.Join(closedWork, "report"))
			return e == nil && string(raw) == "actual work after explicit native shutdown from original local delegation\n"
		})
		wait("managed completed input", func() bool { return state(idle.ID, input) == "completed" })
		stopResumed()
		again, e := Open(resumed.home)
		if e != nil {
			t.Fatal(e)
		}
		defer again.Close()
		if _, ok, e := again.claimReplyReceiverJob(); e != nil || ok {
			t.Fatalf("completed shutdown work replayed %v %v", ok, e)
		}
		runs, e := os.ReadFile(filepath.Join(closedWork, "runs"))
		if e != nil || strings.Count(string(runs), "selected managed run") != 1 {
			t.Fatalf("managed count %v %v", runs, e)
		}
		for _, v := range logEvents() {
			if v.Event == "stream" || v.Event == "NETWORK_PROHIBITED" {
				t.Fatal("closed native runtime consumed return or attempted network")
			}
		}
		if _, e := os.Stat(filepath.Join(root, "default", "runs")); !os.IsNotExist(e) {
			t.Fatal("default consumed selected shutdown input")
		}
		t.Log("actual installed native shutdown before first claim -> exact preauthorized managed work once after daemon restart; native0/default0/completed1; no raw native transcript copied")
		return
	}

	file := filepath.Join(root, "reply.txt")
	fileBytes := []byte("exact native selected attachment\n")
	os.WriteFile(file, fileBytes, 0600)
	input := remote(idle, file)
	wait("idle selected accepted", func() bool { return state(idle.ID, input) == "accepted" })
	wait("idle native completed", func() bool {
		for _, v := range logEvents() {
			if v.Event == "end" && len(v.Inputs) == 1 {
				return true
			}
		}
		return false
	})
	downloadDir := filepath.Join(root, "download")
	os.Mkdir(downloadDir, 0700)
	if out, e := exec.Command(bin, "--home", w.alice.home, "download", "--dir", downloadDir, input).CombinedOutput(); e != nil {
		t.Fatalf("file %v %s", e, out)
	}
	got, e := os.ReadFile(filepath.Join(downloadDir, "reply.txt"))
	if e != nil || !bytes.Equal(got, fileBytes) {
		t.Fatal("native input attachment bytes")
	}
	// A second real native session observes passive metadata, without a turn.
	unrelated := launch("")
	wait("second session", func() bool {
		n := 0
		for _, v := range logEvents() {
			if v.Event == "start" {
				n++
			}
		}
		return n >= 2
	})
	send(first, "/receiver-probe busy")
	var busy ReplyReceiverBinding
	wait("second binding", func() bool {
		v, _ := w.alice.ReplyReceiverBindings()
		for _, b := range v {
			if b.ID != idle.ID {
				busy = b
				return true
			}
		}
		return false
	})
	send(first, "fixture:busy-start")
	wait("real busy tool", func() bool {
		for _, v := range logEvents() {
			if v.Event == "tool-start" {
				return true
			}
		}
		return false
	})
	second := remote(busy, "")
	wait("busy claim queued", func() bool {
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE inbox_id=? AND live_claim IS NOT NULL`, second).Scan(&n)
		return n == 1
	})
	if state(busy.ID, second) != "pending" {
		t.Fatal("busy input ACK before tool boundary")
	}
	for _, v := range logEvents() {
		if v.Event == "input" && v.Details["input_id"] == second {
			t.Fatal("busy follow-up interrupted tool")
		}
	}
	os.WriteFile(filepath.Join(root, "release"), []byte("release"), 0600)
	wait("busy durable acceptance", func() bool { return state(busy.ID, second) == "accepted" })
	wait("busy native completed", func() bool {
		for _, v := range logEvents() {
			if v.Event == "end" && len(v.Inputs) == 2 {
				return true
			}
		}
		return false
	})
	for _, v := range logEvents() {
		if v.Event == "stream" && v.Session != initial.Session {
			t.Fatal("unrelated native session started model")
		}
		if v.Event == "NETWORK_PROHIBITED" {
			t.Fatal("native attempted outside request")
		}
	}
	stop(unrelated)
	// Persist native input, lose backend ACK, then reopen exact JSONL. No native
	// generation may send it again or treat acceptance as effect completion.
	os.WriteFile(filepath.Join(root, "drop-ack"), nil, 0600)
	send(first, "/receiver-probe restart")
	var lost ReplyReceiverBinding
	wait("restart binding", func() bool {
		v, _ := w.alice.ReplyReceiverBindings()
		for _, b := range v {
			if b.ID != idle.ID && b.ID != busy.ID {
				lost = b
				return true
			}
		}
		return false
	})
	third := remote(lost, "")
	wait("lost ACK native persisted", func() bool {
		for _, v := range logEvents() {
			if v.Event == "end" && len(v.Inputs) == 3 {
				return true
			}
		}
		return false
	})
	if state(lost.ID, third) != "pending" {
		t.Fatal("fault injection failed")
	}
	stop(first)
	before := len(logEvents())
	os.Remove(filepath.Join(root, "drop-ack"))
	resumed := launch(initial.File)
	wait("restart reconciliation", func() bool { return state(lost.ID, third) == "accepted" })
	time.Sleep(500 * time.Millisecond)
	for _, v := range logEvents()[before:] {
		if v.Event == "stream" || v.Event == "input" {
			t.Fatal("lost ACK redispatched native input")
		}
		if v.Event == "start" && (v.Session != initial.Session || v.File != initial.File) {
			t.Fatal("wrong native restart identity")
		}
	}
	// Actual native branch/fork installs a different local route; an old
	// selected obligation remains pending and cannot wake the new branch.
	beforeBranch := len(logEvents())
	send(resumed, "/receiver-probe branch")
	wait("native branch generation", func() bool {
		for _, v := range logEvents()[beforeBranch:] {
			if v.Event == "branch" {
				return v.Session != initial.Session
			}
		}
		return false
	})
	late := remote(busy, "")
	wait("old route held", func() bool { return state(busy.ID, late) == "pending" })
	time.Sleep(750 * time.Millisecond)
	for _, v := range logEvents()[beforeBranch:] {
		if v.Event == "stream" || v.Event == "input" {
			t.Fatal("old receiver moved to fork")
		}
	}
	views, _ := w.alice.ReplySessions()
	oldInactive := false
	for _, v := range views {
		if v.Handle == busy.Receiver.SessionHandle {
			oldInactive = !v.Active
		}
	}
	if !oldInactive {
		t.Fatal("old native route still active after fork")
	}
	stop(resumed)
	var completed, defaultRuns int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE state='completed'`).Scan(&completed)
	w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE responder IS NOT NULL`).Scan(&defaultRuns)
	if completed != 0 || defaultRuns != 0 {
		t.Fatalf("acceptance claimed effects/default work %d %d", completed, defaultRuns)
	}
	t.Logf("%s actual installed native: inherited selection, idle once, busy follow-up after tool, exact file, unrelated silence, lost ACK same-session restart no replay, actual fork holds old route; inputs accepted3 completed0 default0", harness)
}
