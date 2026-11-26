package client

import (
	"bufio"
	"context"
	"database/sql"
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
// Desktop starts a queued entry only while that chat is open in its window;
// an entry it has not started by the deadline, or when Desktop closes, is
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
	EnginePID                                                                     int
	EngineTicks                                                                   uint64
	Offset                                                                        int64
}

func codexChatRowIn(q dbq, request string) (codexChatRow, bool, error) {
	var r codexChatRow
	err := q.QueryRow(`SELECT thread,file,home,client_id,queue_id,engine_pid,engine_ticks,engine_exe,offset,turn,state,answer,detail FROM codex_chat_deliveries WHERE request=?`, request).
		Scan(&r.Thread, &r.File, &r.Home, &r.ClientID, &r.QueueID, &r.EnginePID, &r.EngineTicks, &r.EngineExe, &r.Offset, &r.Turn, &r.State, &r.Answer, &r.Detail)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// codexEngine is a running Codex Desktop engine process.
type codexEngine struct {
	PID   int
	Ticks uint64
	Exe   string
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
func codexAppServer(ctx context.Context, exe, home string, fn func(call func(method string, params, out any) error) error) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "app-server")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	cmd.Dir = home
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
	defer func() {
		stdin.Close()
		stopGroup(cmd)
		cmd.Wait()
	}()
	type reply struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	replies := make(chan reply, 8)
	go func() {
		defer close(replies)
		s := bufio.NewScanner(stdout)
		s.Buffer(make([]byte, 64<<10), 16<<20)
		for s.Scan() {
			var r reply
			if json.Unmarshal(s.Bytes(), &r) == nil && r.ID != 0 {
				replies <- r
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

func codexQueueAdd(ctx context.Context, exe, home, thread, clientID, text string) (string, error) {
	var out struct {
		Queued codexQueueEntry `json:"queuedSubmission"`
	}
	err := codexAppServer(ctx, exe, home, func(call func(string, any, any) error) error {
		return call("thread/queue/add", map[string]any{"threadId": thread, "clientUserMessageId": clientID,
			"input": []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}}, &out)
	})
	if err == nil && (out.Queued.ID == "" || out.Queued.ClientID != clientID) {
		err = errors.New("Codex queued an entry AgentNet cannot identify")
	}
	return out.Queued.ID, err
}

// codexQueueFind is the queue entry of clientID, or "" when none is queued.
func codexQueueFind(ctx context.Context, exe, home, thread, clientID string) (string, error) {
	id := ""
	err := codexAppServer(ctx, exe, home, func(call func(string, any, any) error) error {
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

func codexQueueDelete(ctx context.Context, exe, home, thread, queueID string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	err := codexAppServer(ctx, exe, home, func(call func(string, any, any) error) error {
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
	superseded bool // another turn started, or this one aborted, before it completed
}

func codexChatScan(file, thread string, offset int64, clientID string) (codexChatProgress, error) {
	var p codexChatProgress
	_, err := codexNativeScan(file, thread, offset, func(raw json.RawMessage) bool {
		var v struct {
			Type    string `json:"type"`
			Payload struct {
				Type   string  `json:"type"`
				Thread string  `json:"thread_id"`
				Turn   string  `json:"turn_id"`
				Last   *string `json:"last_agent_message"`
				Item   struct {
					Type     string `json:"type"`
					ClientID string `json:"client_id"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &v) != nil || v.Type != "event_msg" || p.done || p.superseded {
			return false
		}
		e := v.Payload
		switch {
		case !p.started:
			if e.Type == "item_completed" && e.Item.Type == "UserMessage" && e.Item.ClientID == clientID && e.Thread == thread && e.Turn != "" {
				p.started, p.turn = true, e.Turn
			}
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
	return p, err
}

// runCodexChat queues prompt once and waits for its exact answer.
func (a *Agent) runCodexChat(runCtx context.Context, j job, r *Responder, prompt string, activity *runActivity) chatOutcome {
	c := r.CodexChat
	if !a.claudeChatAsker(j) {
		return chatEnd(stateNotRun, "A Codex Desktop chat agent takes direct requests only from your own devices. Nothing was sent.")
	}
	row, exists, err := codexChatRowIn(a.store.db, j.ID)
	if err != nil {
		return chatEnd(stateJobFailed, err.Error())
	}
	if exists && (row.State == chatUnsent || row.State == codexRemoved) {
		// Nothing ran: a rerun the person asked for may queue it.
		if _, err = a.store.db.Exec(`DELETE FROM codex_chat_deliveries WHERE request=? AND state IN (?,?)`, j.ID, chatUnsent, codexRemoved); err != nil {
			return chatEnd(stateJobFailed, err.Error())
		}
		exists = false
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
		if out := a.reconcileCodexChat(runCtx, j, &row); out != nil {
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
	row := codexChatRow{Thread: c.Thread, File: c.File, Home: c.Home, ClientID: chatUUID(), EnginePID: engine.PID, EngineTicks: engine.Ticks,
		EngineExe: engine.Exe, Offset: offset, State: chatSending}
	tx, err := a.store.db.Begin()
	if err != nil {
		return fail(stateJobFailed, err.Error())
	}
	defer tx.Rollback()
	var running int
	if err = tx.QueryRow(`SELECT count(*) FROM inbox WHERE id=? AND state=? AND attempts=?`, j.ID, stateRunning, j.nativeAttempt).Scan(&running); err != nil || running != 1 {
		return fail(stateJobFailed, "not sent to the Codex chat: this attempt is no longer current")
	}
	if _, err = tx.Exec(`INSERT INTO codex_chat_deliveries(request,thread,file,home,client_id,engine_pid,engine_ticks,engine_exe,offset,state) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		j.ID, row.Thread, row.File, row.Home, row.ClientID, row.EnginePID, row.EngineTicks, row.EngineExe, row.Offset, row.State); err != nil {
		return fail(stateJobFailed, "not sent to the Codex chat: already sent once: "+err.Error())
	}
	if err = tx.Commit(); err != nil {
		return fail(stateJobFailed, err.Error())
	}
	id, err := codexQueueAdd(runCtx, engine.Exe, c.Home, c.Thread, row.ClientID, prompt)
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
	if out := a.reconcileCodexChat(runCtx, j, &row); out != nil {
		return codexChatRow{}, out
	}
	return row, nil
}

// reconcileCodexChat settles a "sending" row from the rollout and the
// queue. It returns an end when whether the request arrived is unknown.
func (a *Agent) reconcileCodexChat(ctx context.Context, j job, row *codexChatRow) *chatOutcome {
	if p, err := codexChatScan(row.File, row.Thread, row.Offset, row.ClientID); err == nil && p.started {
		a.setCodexChat(j.ID, chatSending, codexQueued, "")
		row.State = codexQueued
		return nil
	}
	if id, err := codexQueueFind(ctx, row.EngineExe, row.Home, row.Thread, row.ClientID); err == nil && id != "" {
		a.setCodexChat(j.ID, chatSending, codexQueued, "", "queue_id", id)
		row.State, row.QueueID = codexQueued, id
		return nil
	}
	why := "AgentNet stopped while queueing this in the Codex chat; whether it arrived is unknown. It was not sent again."
	a.setCodexChat(j.ID, chatSending, chatSuperseded, why)
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

// unqueueCodexChat deletes a queued request that Desktop has not started.
// It reports whether the entry is gone without having run.
func (a *Agent) unqueueCodexChat(j job, row codexChatRow) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id := row.QueueID
	if id == "" {
		var err error
		if id, err = codexQueueFind(ctx, row.EngineExe, row.Home, row.Thread, row.ClientID); err != nil || id == "" {
			return false
		}
	}
	deleted, err := codexQueueDelete(ctx, row.EngineExe, row.Home, row.Thread, id)
	if err != nil || !deleted {
		return false
	}
	if p, err := codexChatScan(row.File, row.Thread, row.Offset, row.ClientID); err != nil || p.started {
		return false
	}
	a.setCodexChat(j.ID, codexQueued, codexRemoved, "")
	return true
}

// waitCodexChat waits, without polling, for the request's exact turn to
// end: rollout writes (inotify), the Desktop engine exiting (pidfd), the
// run's own stop, or the deadline.
func (a *Agent) waitCodexChat(runCtx context.Context, j job, row codexChatRow, limit time.Duration, activity *runActivity) chatOutcome {
	exit, err := watchProcessExit(row.EnginePID, row.EngineTicks)
	var exited <-chan struct{}
	gone := err != nil
	if err == nil {
		defer exit.close()
		exited = exit.done
	}
	watch, err := watchFileWrites(row.File)
	if err != nil {
		return chatEnd(stateNeedHuman, "AgentNet cannot follow the Codex chat's history ("+err.Error()+"). It was queued and is not sent again; look in Codex Desktop.")
	}
	defer watch.close()
	if limit <= 0 {
		limit = codexChatDeadline
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	reported := false
	for {
		p, err := codexChatScan(row.File, row.Thread, row.Offset, row.ClientID)
		if err != nil {
			return chatEnd(stateNeedHuman, "The Codex chat's history could not be read ("+err.Error()+"). It is not sent again.")
		}
		if p.started && row.State == codexQueued {
			a.setCodexChat(j.ID, codexQueued, chatStarted, "", "turn", p.turn)
			row.State, row.Turn = chatStarted, p.turn
		}
		switch {
		case p.done:
			answer := p.answer
			if len(answer) > maxOutput {
				answer = answer[:maxOutput] + "\n[output truncated]"
			}
			a.setCodexChat(j.ID, chatStarted, chatAnswered, "", "answer", answer)
			return chatOutcome{answer: answer}
		case p.superseded:
			why := "The Codex chat's turn for this ended without a recorded answer (stopped, or replaced by another turn). It was not sent again."
			a.setCodexChat(j.ID, chatStarted, chatSuperseded, why)
			return chatEnd(stateNeedHuman, why)
		case p.started && !reported:
			activity.report(activityWorking)
			reported = true
		}
		if gone {
			if row.State == codexQueued && a.unqueueCodexChat(j, row) {
				return chatEnd(stateNotRun, "Codex Desktop closed before it started this. It was taken out of the chat's queue and not sent.")
			}
			why := "Codex Desktop closed before it answered. It was not sent again."
			a.setCodexChat(j.ID, row.State, chatSuperseded, why)
			return chatEnd(stateNeedHuman, why)
		}
		select {
		case <-runCtx.Done():
			if row.State == codexQueued {
				a.unqueueCodexChat(j, row)
			}
			return chatEnd("", "")
		case <-watch.changes:
		case <-exited:
			gone, exited = true, nil // read the history once more: a last write may have landed
		case <-deadline.C:
			if row.State == codexQueued && a.unqueueCodexChat(j, row) {
				return chatEnd(stateNotRun, fmt.Sprintf("Codex Desktop did not start this within %s. It runs queued requests only while that chat is open in its window. It was taken out of the queue and not sent.", limit))
			}
			why := fmt.Sprintf("No answer from the Codex chat was recorded within %s. It was queued and is not sent again; look in Codex Desktop.", limit)
			a.setCodexChat(j.ID, row.State, chatSuperseded, why)
			return chatEnd(stateNeedHuman, why)
		}
	}
}

// codexChatRemoved reports whether request was taken out of a Codex chat's
// queue before Desktop started it.
func (a *Agent) codexChatRemoved(request string) bool {
	var n int
	return a.store.db.QueryRow(`SELECT count(*) FROM codex_chat_deliveries WHERE request=? AND state=?`, request, codexRemoved).Scan(&n) == nil && n == 1
}
