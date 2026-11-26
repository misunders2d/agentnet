package client

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// A Codex Desktop chat the person opted in, by hand, as a named local agent
// (MEL-830). A request to that agent is added once to the chat's own native
// queue (the app server's thread/queue/add, with a client message ID
// AgentNet keeps), and Codex Desktop runs it as the chat's next turn, with
// the chat's own model, settings and approvals. Its answer is that exact
// turn's completion in the chat's rollout.
//
// Codex Desktop runs its engine over private stdio, so AgentNet never joins
// its session: a short-lived app server of Desktop's own bundled engine,
// on the same Codex home, adds to the queue, lists it and deletes from it.
// Desktop starts a queued entry only for a chat it has opened since it
// started (switching to another chat afterwards does not stop it); an
// entry it has not started by the deadline, or when Desktop closes, is
// deleted from the queue, so it never runs later unseen.
//
// The ledger row, with the client ID, is committed before the add; a run
// that finds a row never adds again. Only the turn whose user message
// carries that client ID counts, closed by its own task_complete. A turn
// replaced or aborted first, a closed Desktop or the deadline end as "not
// captured", never as someone's answer.

// CodexChatHarness is the executor key of an opted-in Codex Desktop chat.
// It is not in Harnesses: nothing launches it, lists it as a program or
// picks it in a setup screen.
const CodexChatHarness = "codex-chat"

// codexChatDeadline bounds a wait without the person's own limit.
var codexChatDeadline = 2 * time.Hour

// codexChatMax keeps a queued message to a sane size.
const codexChatMax = 900_000

// codexDesktopOriginator is how Codex Desktop names itself in a rollout.
const codexDesktopOriginator = "Codex Desktop"

// CodexChat names the exact Codex Desktop thread and its native home.
type CodexChat struct {
	Thread string `json:"thread"`
	File   string `json:"file"`
	Home   string `json:"home"`
}

const codexChatSchema = `
CREATE TABLE codex_chat_deliveries(
 request TEXT PRIMARY KEY REFERENCES inbox(id) ON DELETE CASCADE,
 thread TEXT NOT NULL,
 file TEXT NOT NULL,
 home TEXT NOT NULL,
 client_id TEXT NOT NULL,
 queue_id TEXT NOT NULL DEFAULT '',
 engine_pid INTEGER NOT NULL,
 engine_ticks INTEGER NOT NULL,
 engine_exe TEXT NOT NULL,
 offset INTEGER NOT NULL,
 content_sha TEXT NOT NULL,
 env TEXT NOT NULL DEFAULT '',
 turn TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL,
 answer TEXT NOT NULL DEFAULT '',
 detail TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL DEFAULT (unixepoch()));
CREATE TRIGGER erase_codex_chat AFTER UPDATE OF body ON inbox
 WHEN NEW.body='' BEGIN UPDATE codex_chat_deliveries SET answer='' WHERE request=NEW.id; END;
`

// Codex ledger states beyond the shared ones (chatSending, chatStarted,
// chatAnswered, chatSuperseded, chatUnsent).
const (
	codexQueued  = "queued"  // the add returned; Desktop has not started it
	codexRemoved = "removed" // deleted from the queue before it started
)

type codexChatRow struct {
	Thread, File, Home, ClientID, QueueID, EngineExe, Turn, State, Answer, Detail string
	ContentSHA, Env                                                               string
	EnginePID                                                                     int
	EngineTicks                                                                   uint64
	Offset                                                                        int64
}

func codexChatRowIn(q dbq, request string) (codexChatRow, bool, error) {
	var r codexChatRow
	err := q.QueryRow(`SELECT thread,file,home,client_id,queue_id,engine_pid,engine_ticks,engine_exe,offset,content_sha,env,turn,state,answer,detail FROM codex_chat_deliveries WHERE request=?`, request).
		Scan(&r.Thread, &r.File, &r.Home, &r.ClientID, &r.QueueID, &r.EnginePID, &r.EngineTicks, &r.EngineExe, &r.Offset, &r.ContentSHA, &r.Env, &r.Turn, &r.State, &r.Answer, &r.Detail)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// server is the app server the row's request was queued through.
func (r codexChatRow) server() codexServer {
	s := codexServer{Exe: r.EngineExe, Home: r.Home}
	_ = json.Unmarshal([]byte(r.Env), &s.Env)
	return s
}

// codexContentSHA identifies the text of a queued request.
func codexContentSHA(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

// codexEngine is a running Codex Desktop engine process.
type codexEngine struct {
	PID   int
	Ticks uint64
	Exe   string
	Env   []string // its CODEX_* settings other than CODEX_HOME
}

// codexServer is how to start an app server on Desktop's own state: its
// engine exe, Codex home and CODEX_* settings.
type codexServer struct {
	Exe, Home string
	Env       []string
}

func (e codexEngine) server(home string) codexServer {
	return codexServer{Exe: e.Exe, Home: home, Env: e.Env}
}

// codexDesktopEngine finds the running Codex Desktop engine whose Codex
// home is home (codexchat_linux.go); tests replace it.
var codexDesktopEngine = findCodexDesktopEngine

// codexRolloutHeader is the first record of a rollout.
type codexRolloutHeader struct {
	ID, CWD, Originator, Source, Version string
}

func readCodexRolloutHeader(file string) (codexRolloutHeader, error) {
	var h codexRolloutHeader
	f, err := os.Open(file)
	if err != nil {
		return h, err
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(io.LimitReader(f, nativeRecordMax), 64<<10).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return h, errors.New("the Codex chat's history is empty")
	}
	var row struct {
		Type    string `json:"type"`
		Payload struct {
			ID         string `json:"id"`
			CWD        string `json:"cwd"`
			Originator string `json:"originator"`
			Source     string `json:"source"`
			Version    string `json:"cli_version"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &row) != nil || row.Type != "session_meta" {
		return h, errors.New("the Codex chat's history has no header")
	}
	p := row.Payload
	return codexRolloutHeader{ID: p.ID, CWD: p.CWD, Originator: p.Originator, Source: p.Source, Version: p.Version}, nil
}

// findCodexRollout is the one rollout of thread under home's sessions.
func findCodexRollout(home, thread string) (string, error) {
	if !validCodexThread(thread) {
		return "", errors.New("not a Codex thread ID")
	}
	var found []string
	err := filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), "-"+thread+".jsonl") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", errors.New("no Codex chat with that ID was found in " + home)
	case 1:
		return canonicalNativeFile(found[0])
	}
	return "", errors.New("more than one Codex history has that ID")
}

func validCodexThread(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// codexDesktopThread checks that file is thread's own Codex Desktop rollout.
func codexDesktopThread(file, thread string) (codexRolloutHeader, error) {
	h, err := readCodexRolloutHeader(file)
	if err != nil {
		return h, err
	}
	if h.ID != thread || h.Source != "vscode" || !nativeVersion.MatchString(h.Version) {
		return h, errors.New("the Codex chat's history does not belong to that thread")
	}
	if h.Originator != codexDesktopOriginator {
		return h, errors.New("that Codex chat was not started in Codex Desktop")
	}
	return h, nil
}

// codexChatTarget is the running Desktop engine for c, or why c cannot
// take a request now.
func codexChatTarget(c CodexChat) (codexEngine, error) {
	if _, err := codexDesktopThread(c.File, c.Thread); err != nil {
		return codexEngine{}, err
	}
	e, err := codexDesktopEngine(c.Home)
	if err != nil {
		return e, err
	}
	return e, nil
}

// CodexChatCandidate is a Codex Desktop chat that can be opted in.
type CodexChatCandidate struct {
	Thread  string    `json:"thread"`
	Name    string    `json:"name"`
	Dir     string    `json:"dir"`
	Updated time.Time `json:"updated"`
}

// CodexChatCandidates lists the most recent Codex Desktop chats in the
// caller's Codex home, newest first.
func CodexChatCandidates(limit int) ([]CodexChatCandidate, error) {
	home, err := codexHomeDir()
	if err != nil {
		return nil, err
	}
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl") {
			if info, err := d.Info(); err == nil {
				files = append(files, file{path, info.ModTime()})
			}
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	names := codexThreadNames(home)
	var out []CodexChatCandidate
	for _, f := range files {
		if len(out) >= limit {
			break
		}
		h, err := readCodexRolloutHeader(f.path)
		if err != nil || h.Originator != codexDesktopOriginator || !validCodexThread(h.ID) {
			continue
		}
		out = append(out, CodexChatCandidate{Thread: h.ID, Name: names[h.ID], Dir: h.CWD, Updated: f.mod})
	}
	return out, nil
}

// codexThreadNames reads Codex's own thread names (session_index.jsonl).
func codexThreadNames(home string) map[string]string {
	names := map[string]string{}
	f, err := os.Open(filepath.Join(home, "session_index.jsonl"))
	if err != nil {
		return names
	}
	defer f.Close()
	s := bufio.NewScanner(io.LimitReader(f, 32<<20))
	s.Buffer(make([]byte, 64<<10), 1<<20)
	for s.Scan() {
		var row struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(s.Bytes(), &row) == nil && row.ID != "" {
			names[row.ID] = row.Name
		}
	}
	return names
}

// ConnectCodexChat makes Codex Desktop chat thread a named local agent.
// Only the person runs it, by hand (cmd/agentnet refuses a session or run).
func (a *Agent) ConnectCodexChat(thread, label string) (string, error) {
	home, err := codexHomeDir()
	if err != nil {
		return "", err
	}
	file, err := findCodexRollout(home, thread)
	if err != nil {
		return "", err
	}
	h, err := codexDesktopThread(file, thread)
	if err != nil {
		return "", err
	}
	if _, err = codexNativeScan(file, thread, 0, nil); err != nil {
		return "", err
	}
	if _, err = codexDesktopEngine(home); err != nil {
		return "", err
	}
	c := CodexChat{Thread: thread, File: file, Home: home}
	dir := h.CWD
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = home
	}
	entries, err := a.localAgentsIn(a.store.db)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Responder != nil && e.Responder.CodexChat != nil && e.Responder.CodexChat.Thread == thread {
			if *e.Responder.CodexChat == c {
				return e.Record.ID, nil
			}
			next := copyResponder(*e.Responder)
			next.CodexChat, next.Dir = &c, dir
			return e.Record.ID, a.SetLocalAgentResponder(e.Record.ID, &next)
		}
	}
	record, err := a.CreateLocalAgent(label, Responder{Harness: CodexChatHarness, Dir: dir, CodexChat: &c})
	return record.ID, err
}

// codexAppServer runs a short-lived app server of the Desktop engine exe
// on home and gives fn its calls. It never opens or resumes a thread.
func codexAppServer(ctx context.Context, srv codexServer, fn func(call func(method string, params, out any) error) error) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, srv.Exe, "app-server")
	// Desktop's own CODEX_* settings (state and SQLite locations), not ours.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CODEX_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(append(cmd.Env, srv.Env...), "CODEX_HOME="+srv.Home)
	cmd.Dir = srv.Home
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	ownProcessGroup(cmd)
	if err = cmd.Start(); err != nil {
		return err
	}
	read := make(chan struct{})
	defer func() {
		cancel()
		stdin.Close()
		stopGroup(cmd)
		<-read // every read of stdout ends before Wait closes it
		cmd.Wait()
	}()
	type reply struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	replies := make(chan reply, 8)
	go func() {
		defer close(read)
		defer close(replies)
		s := bufio.NewScanner(stdout)
		s.Buffer(make([]byte, 64<<10), 16<<20)
		for s.Scan() {
			var r reply
			// A frame with a method is the server's own request or notice.
			if json.Unmarshal(s.Bytes(), &r) == nil && r.ID != 0 && r.Method == "" {
				select {
				case replies <- r:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	var serial int64
	send := func(v any) error {
		raw, _ := json.Marshal(v)
		_, err := stdin.Write(append(raw, '\n'))
		return err
	}
	call := func(method string, params, out any) error {
		serial++
		if err := send(map[string]any{"id": serial, "method": method, "params": params}); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return fmt.Errorf("Codex %s did not answer", method)
			case r, ok := <-replies:
				if !ok {
					return fmt.Errorf("Codex app server ended during %s", method)
				}
				if r.ID != serial {
					continue
				}
				if r.Error != nil {
					return &codexQueueRefusal{Method: method, Code: r.Error.Code}
				}
				if out != nil {
					return json.Unmarshal(r.Result, out)
				}
				return nil
			}
		}
	}
	init := map[string]any{"clientInfo": map[string]string{"name": "agentnet", "title": "AgentNet", "version": nativeClientVersion()},
		"capabilities": map[string]any{"experimentalApi": true}}
	if err = call("initialize", init, nil); err != nil {
		return err
	}
	if err = send(map[string]any{"method": "initialized"}); err != nil {
		return err
	}
	return fn(call)
}

// codexQueueRefusal is the app server's own refusal: nothing was done.
type codexQueueRefusal struct {
	Method string
	Code   int
}

func (e *codexQueueRefusal) Error() string {
	return fmt.Sprintf("Codex refused %s (code %d)", e.Method, e.Code)
}

func nativeClientVersion() string {
	if protocol.Version != "" {
		return protocol.Version
	}
	return "dev"
}

type codexQueueEntry struct {
	ID       string `json:"id"`
	ClientID string `json:"clientUserMessageId"`
}

func codexQueueAdd(ctx context.Context, srv codexServer, thread, clientID, text string) (string, error) {
	var out struct {
		Queued codexQueueEntry `json:"queuedSubmission"`
	}
	err := codexAppServer(ctx, srv, func(call func(string, any, any) error) error {
		return call("thread/queue/add", map[string]any{"threadId": thread, "clientUserMessageId": clientID,
			"input": []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}}, &out)
	})
	if err == nil && (out.Queued.ID == "" || out.Queued.ClientID != clientID) {
		err = errors.New("Codex queued an entry AgentNet cannot identify")
	}
	return out.Queued.ID, err
}

// codexQueueFind is the queue entry of clientID, or "" when none is queued.
func codexQueueFind(ctx context.Context, srv codexServer, thread, clientID string) (string, error) {
	id := ""
	err := codexAppServer(ctx, srv, func(call func(string, any, any) error) error {
		cursor := ""
		for range 100 {
			params := map[string]any{"threadId": thread, "limit": 100}
			if cursor != "" {
				params["cursor"] = cursor
			}
			var page struct {
				Data []codexQueueEntry `json:"data"`
				Next *string           `json:"nextCursor"`
			}
			if err := call("thread/queue/list", params, &page); err != nil {
				return err
			}
			for _, q := range page.Data {
				if q.ClientID == clientID {
					id = q.ID
					return nil
				}
			}
			if page.Next == nil || *page.Next == "" {
				return nil
			}
			cursor = *page.Next
		}
		return errors.New("Codex queue listing did not end")
	})
	return id, err
}

func codexQueueDelete(ctx context.Context, srv codexServer, thread, queueID string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	err := codexAppServer(ctx, srv, func(call func(string, any, any) error) error {
		return call("thread/queue/delete", map[string]any{"threadId": thread, "queuedSubmissionId": queueID}, &out)
	})
	return out.Deleted, err
}

// codexChatProgress is what the rollout, from a delivery's offset, says
// about its exact turn.
type codexChatProgress struct {
	turn       string
	started    bool // the user message with the client ID is recorded
	done       bool // that turn's task_complete
	answer     string
	superseded bool   // another turn started, or this one aborted, before it completed
	why        string // set when the turn is not this request's own answer
}

// codexChatScan follows row's request in the rollout. The turn counts only
// when its user message is the exact text queued (sha) and no other input
// joins that turn before it completes.
func codexChatScan(row codexChatRow) (codexChatProgress, error) {
	var p codexChatProgress
	var item string
	fresh := map[string]bool{} // turns started after the offset, with no input yet
	_, err := codexNativeScan(row.File, row.Thread, row.Offset, func(raw json.RawMessage) bool {
		var v struct {
			Type    string `json:"type"`
			Payload struct {
				Type   string  `json:"type"`
				Thread string  `json:"thread_id"`
				Turn   string  `json:"turn_id"`
				Last   *string `json:"last_agent_message"`
				Item   struct {
					Type     string `json:"type"`
					ID       string `json:"id"`
					ClientID string `json:"client_id"`
					Content  []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &v) != nil || v.Type != "event_msg" || p.done || p.superseded {
			return false
		}
		e := v.Payload
		user := e.Type == "item_completed" && e.Item.Type == "UserMessage"
		switch {
		case !p.started:
			if e.Type == "task_started" && e.Turn != "" {
				fresh[e.Turn] = true
			} else if user && e.Item.ClientID != row.ClientID {
				delete(fresh, e.Turn) // that turn already has someone else's input
			}
			if user && e.Item.ClientID == row.ClientID && e.Thread == row.Thread && e.Turn != "" {
				p.started, p.turn, item = true, e.Turn, e.Item.ID
				if !fresh[e.Turn] {
					// Steered into a turn already running: its answer is
					// that turn's, not this request's.
					p.superseded, p.why = true, "Codex Desktop added this to a turn that was already running, so that turn's answer is not this request's alone. It was not sent again."
					return false
				}
				var text strings.Builder
				for _, c := range e.Item.Content {
					if c.Type == "text" {
						text.WriteString(c.Text)
					}
				}
				if codexContentSHA(text.String()) != row.ContentSHA {
					p.superseded, p.why = true, "The request was changed in Codex Desktop before it ran, so its answer is not this request's. It was not sent again."
				}
			}
		case user && e.Turn == p.turn && e.Item.ID != item:
			p.superseded, p.why = true, "More input was added to the Codex chat's turn for this before it ended, so its answer is not this request's alone. It was not sent again."
		case e.Type == "task_complete" && e.Turn == p.turn:
			p.done = true
			if e.Last != nil {
				p.answer = *e.Last
			}
		case e.Type == "turn_aborted" && (e.Turn == p.turn || e.Turn == ""),
			e.Type == "task_started" && e.Turn != "" && e.Turn != p.turn:
			p.superseded = true
		}
		return false
	})
	if p.superseded && p.why == "" {
		p.why = "The Codex chat's turn for this ended without a recorded answer (stopped, or replaced by another turn). It was not sent again."
	}
	return p, err
}

// runCodexChat queues prompt once and waits for its exact answer.
func (a *Agent) runCodexChat(runCtx context.Context, j job, r *Responder, prompt string, activity *runActivity) chatOutcome {
	c := r.CodexChat
	if !a.claudeChatAsker(j) {
		return chatEnd(stateNotRun, "A Codex Desktop chat agent takes direct requests only from your own devices. Nothing was sent.")
	}
	// Requests of earlier runs still in a queue leave it first.
	a.withdrawCodexChats(runCtx)
	row, exists, err := codexChatRowIn(a.store.db, j.ID)
	if err != nil {
		return chatEnd(stateJobFailed, err.Error())
	}
	if exists && (row.State == chatUnsent || row.State == codexRemoved) {
		// A delete can cross Desktop starting it: only a request the
		// history never shows may be queued again on a rerun.
		p, err := codexChatScan(row)
		if err != nil {
			return chatEnd(stateNeedHuman, "The Codex chat's history could not be read ("+err.Error()+"). It is not sent again.")
		}
		if p.started {
			a.setCodexChat(j.ID, row.State, chatStarted, "", "turn", p.turn)
			row.State, row.Turn = chatStarted, p.turn
		} else {
			if _, err = a.store.db.Exec(`DELETE FROM codex_chat_deliveries WHERE request=? AND state IN (?,?)`, j.ID, chatUnsent, codexRemoved); err != nil {
				return chatEnd(stateJobFailed, err.Error())
			}
			exists = false
		}
	}
	if !exists {
		if len(prompt) > codexChatMax {
			return chatEnd(stateJobFailed, "the request is too long for a Codex chat message")
		}
		var out *chatOutcome
		if row, out = a.deliverCodexChat(runCtx, j, *c, prompt); out != nil {
			return *out
		}
	} else if row.Thread != c.Thread || row.File != c.File || row.Home != c.Home {
		return chatEnd(stateNeedHuman, "This was queued in a different Codex chat; it is not sent again.")
	} else if row.State == chatSending {
		// A run stopped between committing the row and recording the add:
		// the rollout and the queue say whether it arrived. Never added again.
		if out := a.reconcileCodexChat(j, &row); out != nil {
			return *out
		}
	}
	return a.waitCodexChat(runCtx, j, row, r.Timeout, activity)
}

// deliverCodexChat checks the running Desktop and the chat, commits the
// ledger row and adds the request to the chat's queue once.
func (a *Agent) deliverCodexChat(runCtx context.Context, j job, c CodexChat, prompt string) (codexChatRow, *chatOutcome) {
	fail := func(state, why string) (codexChatRow, *chatOutcome) {
		o := chatEnd(state, why)
		return codexChatRow{}, &o
	}
	engine, err := codexChatTarget(c)
	if err != nil {
		return fail(stateNotRun, err.Error()+". Nothing was sent.")
	}
	exit, err := watchProcessExit(engine.PID, engine.Ticks)
	if err != nil {
		return fail(stateNotRun, "Codex Desktop is not running. Open it and that chat, then ask again. Nothing was sent.")
	}
	exit.close()
	var offset int64
	if info, err := os.Stat(c.File); err == nil {
		offset = info.Size()
	}
	env, _ := json.Marshal(engine.Env)
	row := codexChatRow{Thread: c.Thread, File: c.File, Home: c.Home, ClientID: chatUUID(), EnginePID: engine.PID, EngineTicks: engine.Ticks,
		EngineExe: engine.Exe, Offset: offset, ContentSHA: codexContentSHA(prompt), Env: string(env), State: chatSending}
	tx, err := a.store.db.Begin()
	if err != nil {
		return fail(stateJobFailed, err.Error())
	}
	defer tx.Rollback()
	var running int
	if err = tx.QueryRow(`SELECT count(*) FROM inbox WHERE id=? AND state=? AND attempts=?`, j.ID, stateRunning, j.nativeAttempt).Scan(&running); err != nil || running != 1 {
		return fail(stateJobFailed, "not sent to the Codex chat: this attempt is no longer current")
	}
	if _, err = tx.Exec(`INSERT INTO codex_chat_deliveries(request,thread,file,home,client_id,engine_pid,engine_ticks,engine_exe,offset,content_sha,env,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, row.Thread, row.File, row.Home, row.ClientID, row.EnginePID, row.EngineTicks, row.EngineExe, row.Offset, row.ContentSHA, row.Env, row.State); err != nil {
		return fail(stateJobFailed, "not sent to the Codex chat: already sent once: "+err.Error())
	}
	if err = tx.Commit(); err != nil {
		return fail(stateJobFailed, err.Error())
	}
	id, err := codexQueueAdd(runCtx, row.server(), c.Thread, row.ClientID, prompt)
	if err == nil {
		a.setCodexChat(j.ID, chatSending, codexQueued, "", "queue_id", id)
		row.State, row.QueueID = codexQueued, id
		return row, nil
	}
	var refused *codexQueueRefusal
	if errors.As(err, &refused) {
		a.setCodexChat(j.ID, chatSending, chatUnsent, err.Error())
		return fail(stateJobFailed, "not sent to the Codex chat: "+err.Error())
	}
	// The add may or may not have happened: reconcile, never add again.
	a.Logf("%s %s: Codex chat queue add: %v", j.Kind, j.ID, err)
	if out := a.reconcileCodexChat(j, &row); out != nil {
		return codexChatRow{}, out
	}
	return row, nil
}

// reconcileCodexChat settles a "sending" row from the rollout and the
// queue. It returns an end when whether the request arrived is unknown.
// A row it cannot settle stays "sending", so withdrawCodexChats later
// takes the request out of the queue if it is there.
func (a *Agent) reconcileCodexChat(j job, row *codexChatRow) *chatOutcome {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := func() bool {
		p, err := codexChatScan(*row)
		return err == nil && p.started
	}
	if started() {
		a.setCodexChat(j.ID, chatSending, codexQueued, "")
		row.State = codexQueued
		return nil
	}
	id, err := codexQueueFind(ctx, row.server(), row.Thread, row.ClientID)
	if err == nil && id != "" {
		a.setCodexChat(j.ID, chatSending, codexQueued, "", "queue_id", id)
		row.State, row.QueueID = codexQueued, id
		return nil
	}
	if err == nil && started() { // Desktop started it between the two looks
		a.setCodexChat(j.ID, chatSending, codexQueued, "")
		row.State = codexQueued
		return nil
	}
	why := "AgentNet stopped while queueing this in the Codex chat; whether it arrived is unknown. It was not sent again, and AgentNet takes it out of the queue if it is there."
	if err == nil {
		// Neither queued nor started: the add did not land.
		a.setCodexChat(j.ID, chatSending, chatUnsent, why)
	}
	o := chatEnd(stateNeedHuman, why)
	return &o
}

// setCodexChat moves request's row from one state to another, with an
// optional extra column and value.
func (a *Agent) setCodexChat(request, from, to, detail string, extra ...string) {
	set, args := `state=?,detail=?`, []any{to, detail}
	if len(extra) == 2 && (extra[0] == "queue_id" || extra[0] == "turn" || extra[0] == "answer") {
		set += `,` + extra[0] + `=?`
		args = append(args, extra[1])
	}
	if _, err := a.store.db.Exec(`UPDATE codex_chat_deliveries SET `+set+` WHERE request=? AND state=?`, append(args, request, from)...); err != nil {
		a.Logf("Codex chat ledger %s: %v", request, err)
	}
}

// codexWithdrawRetries are the waits between attempts to take a request
// out of the queue; tests shorten them.
var codexWithdrawRetries = []time.Duration{time.Second, 3 * time.Second}

// codexDeleteSettle is how long a deleted entry is watched for Desktop
// having started it just before the delete.
var codexDeleteSettle = 3 * time.Second

// codexStillQueued ends a request AgentNet could not take out of the queue.
const codexStillQueued = "AgentNet could not take it out of the Codex chat's queue yet, so Codex Desktop may still run it when you open that chat; AgentNet removes it when it next can."

// unqueueCodexChat deletes a queued request that Desktop has not started,
// with retries when patient. It reports whether the entry is gone without
// having run; a failure keeps the row queued for withdrawCodexChats.
func (a *Agent) unqueueCodexChat(request string, row codexChatRow, patient bool) bool {
	attempts := 1
	if patient {
		attempts += len(codexWithdrawRetries)
	}
	for i := range attempts {
		if i > 0 {
			time.Sleep(codexWithdrawRetries[i-1])
		}
		gone, err := a.deleteQueuedCodexChat(row)
		if err == nil {
			if !gone {
				return false
			}
			a.setCodexChat(request, row.State, codexRemoved, "")
			return true
		}
		a.Logf("Codex chat %s: taking it out of the queue: %v", request, err)
	}
	a.setCodexChat(request, row.State, row.State, codexStillQueued)
	return false
}

// deleteQueuedCodexChat deletes row's entry. gone is true only when the
// entry is no longer queued and the history, after a short settle, shows
// Desktop never started it.
func (a *Agent) deleteQueuedCodexChat(row codexChatRow) (gone bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	srv := row.server()
	id, err := codexQueueFind(ctx, srv, row.Thread, row.ClientID)
	if err != nil {
		return false, err
	}
	if id != "" {
		if _, err = codexQueueDelete(ctx, srv, row.Thread, id); err != nil {
			return false, err
		}
		if id, err = codexQueueFind(ctx, srv, row.Thread, row.ClientID); err != nil {
			return false, err
		}
		if id != "" {
			return false, errors.New("the entry is still queued")
		}
	}
	// Desktop takes an entry off the queue as it starts it: its user
	// message lands in the history right after.
	watch, werr := watchFileWrites(row.File)
	if werr == nil {
		defer watch.close()
	}
	settle := time.NewTimer(codexDeleteSettle)
	defer settle.Stop()
	for {
		p, err := codexChatScan(row)
		if err != nil {
			return false, err
		}
		if p.started {
			return false, nil
		}
		var changes <-chan struct{}
		if werr == nil {
			changes = watch.changes
		}
		select {
		case <-changes:
		case <-settle.C:
			return true, nil
		}
	}
}

var codexWithdrawMu sync.Mutex

// withdrawCodexChats takes requests out of Codex chat queues whose run
// ended without taking them out (a crash or stop, a failed delete): a
// request nobody is waiting for must not run later, unseen. It runs at
// daemon start and before each new Codex chat request.
func (a *Agent) withdrawCodexChats(ctx context.Context) {
	codexWithdrawMu.Lock()
	defer codexWithdrawMu.Unlock()
	rows, err := a.store.db.Query(`SELECT d.request FROM codex_chat_deliveries d JOIN inbox i ON i.id=d.request
		WHERE d.state IN (?,?) AND i.state NOT IN (?,?)`, chatSending, codexQueued, stateRunning, stateCancelReq)
	if err != nil {
		a.Logf("Codex chat queue check: %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		row, ok, err := codexChatRowIn(a.store.db, id)
		if err != nil || !ok {
			continue
		}
		if !a.unqueueCodexChat(id, row, false) {
			if p, err := codexChatScan(row); err == nil && p.started {
				a.setCodexChat(id, row.State, chatSuperseded, "Codex Desktop started it after AgentNet stopped waiting.")
			}
		}
	}
}

// waitCodexChat waits, without polling, for the request's exact turn to
// end: rollout writes (inotify), the Desktop engine exiting (pidfd), the
// run's own stop, or the deadline.
func (a *Agent) waitCodexChat(runCtx context.Context, j job, row codexChatRow, limit time.Duration, activity *runActivity) chatOutcome {
	// leave ends a wait that has not seen the request start: it takes the
	// request out of the queue, or says it may still run.
	leave := func(removed, kept string) chatOutcome {
		if row.State == codexQueued {
			if a.unqueueCodexChat(j.ID, row, true) {
				return chatEnd(stateNotRun, removed)
			}
			if p, err := codexChatScan(row); err != nil || !p.started {
				return chatEnd(stateNeedHuman, kept+" "+codexStillQueued)
			}
		}
		return chatOutcome{}
	}
	exit, err := watchProcessExit(row.EnginePID, row.EngineTicks)
	var exited <-chan struct{}
	gone := err != nil
	if err == nil {
		defer exit.close()
		exited = exit.done
	}
	watch, err := watchFileWrites(row.File)
	if err != nil {
		why := "AgentNet cannot follow the Codex chat's history (" + err.Error() + ")."
		if o := leave(why+" It was taken out of the chat's queue and not sent.", why); o.state != "" {
			return o
		}
		return chatEnd(stateNeedHuman, why+" It was queued and is not sent again; look in Codex Desktop.")
	}
	defer watch.close()
	if limit <= 0 {
		limit = codexChatDeadline
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	reported := false
	for {
		p, err := codexChatScan(row)
		if err != nil {
			why := "The Codex chat's history could not be read (" + err.Error() + ")."
			if o := leave(why+" It was taken out of the chat's queue and not sent.", why); o.state != "" {
				return o
			}
			return chatEnd(stateNeedHuman, why+" It is not sent again.")
		}
		if p.started && row.State == codexQueued {
			a.setCodexChat(j.ID, codexQueued, chatStarted, "", "turn", p.turn)
			row.State, row.Turn = chatStarted, p.turn
		}
		switch {
		case p.superseded:
			a.setCodexChat(j.ID, chatStarted, chatSuperseded, p.why)
			return chatEnd(stateNeedHuman, p.why)
		case p.done:
			answer := p.answer
			if len(answer) > maxOutput {
				answer = answer[:maxOutput] + "\n[output truncated]"
			}
			a.setCodexChat(j.ID, chatStarted, chatAnswered, "", "answer", answer)
			return chatOutcome{answer: answer}
		case p.started && !reported:
			activity.report(activityWorking)
			reported = true
		}
		if gone {
			if o := leave("Codex Desktop closed before it started this. It was taken out of the chat's queue and not sent.", "Codex Desktop closed before it started this."); o.state != "" {
				return o
			}
			why := "Codex Desktop closed before it answered. It was not sent again."
			a.setCodexChat(j.ID, row.State, chatSuperseded, why)
			return chatEnd(stateNeedHuman, why)
		}
		select {
		case <-runCtx.Done():
			if row.State == codexQueued {
				a.unqueueCodexChat(j.ID, row, false)
			}
			return chatEnd("", "")
		case <-watch.changes:
		case <-exited:
			gone, exited = true, nil // read the history once more: a last write may have landed
		case <-deadline.C:
			if o := leave(fmt.Sprintf("Codex Desktop did not start this within %s. It starts queued requests only for a chat it has opened since it started: open that chat in Codex Desktop, then ask again. It was taken out of the queue and not sent.", limit),
				fmt.Sprintf("Codex Desktop did not start this within %s.", limit)); o.state != "" {
				return o
			}
			why := fmt.Sprintf("No answer from the Codex chat was recorded within %s. It was queued and is not sent again; look in Codex Desktop.", limit)
			a.setCodexChat(j.ID, row.State, chatSuperseded, why)
			return chatEnd(stateNeedHuman, why)
		}
	}
}

// codexChatReached says, from the ledger, how far request got.
func (a *Agent) codexChatReached(request string) string {
	row, ok, err := codexChatRowIn(a.store.db, request)
	switch {
	case err != nil:
		return "Whether it reached the Codex chat is unknown."
	case !ok || row.State == chatUnsent:
		return "It was not sent to the Codex chat."
	case row.State == codexRemoved:
		return "Codex Desktop had not started it; it was taken out of the chat's queue."
	case row.State == chatSending:
		return "Whether it reached the Codex chat is unknown; AgentNet takes it out of the queue if it is there."
	case row.State == codexQueued:
		return "Codex Desktop had not started it. " + codexStillQueued
	}
	return "Codex Desktop had already started it."
}
