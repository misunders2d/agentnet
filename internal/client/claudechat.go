package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A Claude Desktop (or any Claude Code) chat the person opted in, by hand,
// as a named local agent (MEL-829). A request to that agent is delivered
// once to the chat's own documented inbox socket
// (code.claude.com/docs/en/cross-session-messaging) and its answer is the
// chat's next completed turn, recorded by that chat's own Stop hook.
//
// Nothing is launched and no permission mode, token or bypass claim is
// sent: the chat keeps its own settings, approvals and inbound controls.
// A delivery is written at most once per request. The ledger row is
// committed before the socket is opened; a run that finds one never writes
// again, it only reconciles what the row and the chat's hooks recorded.
// Only an exact turn counts as the answer: the frame text must start a turn
// (UserPromptSubmit with exactly that text) and the next Stop of the same
// registered session closes it. A frame folded into a turn the person
// started, a turn replaced or stopped before its Stop, a refused or expired
// frame, a closed chat or the deadline all end as "not captured", never as
// someone's answer.

// ClaudeChatHarness is the executor key of an opted-in chat. It is not in
// Harnesses: nothing can launch it, list it as a program or pick it in a
// setup screen.
const ClaudeChatHarness = "claude-chat"

// claudeChatDeadline bounds a wait without the person's own limit.
var claudeChatDeadline = 2 * time.Hour

// claudeChatMax keeps a frame under the inbox's documented ~1M characters.
const claudeChatMax = 900_000

// ClaudeChat names the exact registered native session and process.
type ClaudeChat struct {
	Handle    string `json:"handle"`
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	Ticks     uint64 `json:"ticks"`
}

const claudeChatSchema = `
CREATE TABLE claude_chat_deliveries(
 request TEXT PRIMARY KEY REFERENCES inbox(id) ON DELETE CASCADE,
 handle TEXT NOT NULL,
 session TEXT NOT NULL,
 pid INTEGER NOT NULL,
 ticks INTEGER NOT NULL,
 msg_id TEXT NOT NULL,
 sender TEXT NOT NULL,
 content_sha TEXT NOT NULL,
 writer_pid INTEGER NOT NULL,
 offset INTEGER NOT NULL,
 state TEXT NOT NULL,
 answer TEXT NOT NULL DEFAULT '',
 detail TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL DEFAULT (unixepoch()));
CREATE INDEX claude_chat_live ON claude_chat_deliveries(session) WHERE state IN ('sending','written','started');
CREATE TRIGGER erase_claude_chat AFTER UPDATE OF body ON inbox
 WHEN NEW.body='' BEGIN UPDATE claude_chat_deliveries SET answer='' WHERE request=NEW.id; END;
`

// Ledger states. sending, written and started are live; the rest are final.
const (
	chatSending    = "sending"    // committed before connect; bytes may or may not have left
	chatWritten    = "written"    // the frame was written to the verified socket
	chatStarted    = "started"    // a turn started with exactly the frame text
	chatAnswered   = "answered"   // that turn's Stop recorded its last message
	chatFolded     = "folded"     // the frame was read into a turn it did not start
	chatSuperseded = "superseded" // another prompt started before this turn's Stop
	chatUnsent     = "unsent"     // nothing was written
	chatRefused    = "refused"    // the chat reported it did not deliver it
)

type chatRow struct {
	Handle, Session, MsgID, Sender, ContentSHA, State, Answer, Detail string
	PID, WriterPID                                                    int
	Ticks                                                             uint64
	Offset                                                            int64
}

func chatRowIn(q dbq, request string) (chatRow, bool, error) {
	var r chatRow
	err := q.QueryRow(`SELECT handle,session,pid,ticks,msg_id,sender,content_sha,writer_pid,offset,state,answer,detail FROM claude_chat_deliveries WHERE request=?`, request).
		Scan(&r.Handle, &r.Session, &r.PID, &r.Ticks, &r.MsgID, &r.Sender, &r.ContentSHA, &r.WriterPID, &r.Offset, &r.State, &r.Answer, &r.Detail)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func chatUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func chatSHA(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

// chatTagLike is any opening or closing envelope tag, in any case or
// spacing: the body can neither close the envelope nor open a second one.
var chatTagLike = regexp.MustCompile(`(?i)<(\s*/?\s*cross-session-message)`)

// claudeChatContent is the exact message content: the documented peer
// envelope with a per-request sender session token. The body cannot close
// the envelope early.
func claudeChatContent(sender, token, body string) string {
	body = chatTagLike.ReplaceAllString(body, "&lt;$1")
	return `<cross-session-message from="` + sender + `" from-session="` + token + `" from-name="AgentNet">` + "\n" + body + "\n</cross-session-message>"
}

func claudeChatFrame(msgID, sender, sid, content string) []byte {
	frame, _ := json.Marshal(map[string]any{"msgV": 1, "msg_id": msgID, "type": "user", "priority": "next", "from": sender, "session_id": sid,
		"message": map[string]string{"role": "user", "content": content}})
	return append(frame, '\n')
}

// claudeInboxPath accepts only the documented per-process inbox of pid:
// <runtime>/cc-socks/<pid>.sock or /tmp/cc-socks-<uid>/<pid>.sock, a socket
// this user owns.
func claudeInboxPath(path string, pid int) error {
	if path == "" {
		return errors.New("this Claude session has no inbox socket")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != strconv.Itoa(pid)+".sock" {
		return errors.New("Claude inbox socket does not belong to the registered process")
	}
	dir := filepath.Base(filepath.Dir(path))
	if dir != "cc-socks" && dir != "cc-socks-"+strconv.Itoa(os.Getuid()) {
		return errors.New("Claude inbox socket is not in Claude's socket directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 || !ownedByMe(info) {
		return errors.New("Claude inbox socket is not this user's socket")
	}
	return nil
}

// claudeChatTarget is the current registration of c, or why it is not.
func (a *Agent) claudeChatTarget(q dbq, c ClaudeChat) (replySessionRecord, error) {
	r, err := replySessionIn(q, c.Handle)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errors.New("the Claude chat is no longer registered here")
	}
	if err != nil {
		return r, err
	}
	if err = a.checkReplySession(q, r); err != nil {
		return r, err
	}
	if !r.Active || r.Harness != "claude" || r.Claude == nil || r.SessionID != c.SessionID {
		return r, errors.New("the Claude chat has closed")
	}
	if r.Claude.PID != c.PID || r.Claude.Ticks != c.Ticks {
		return r, errors.New("the Claude chat was restarted; connect it again to use it")
	}
	return r, nil
}

// ClaudeChatCandidate is a registered Claude session that can be opted in.
type ClaudeChatCandidate struct {
	Handle    string `json:"handle"`
	SessionID string `json:"session_id"`
	Dir       string `json:"dir"`
	Inbox     bool   `json:"inbox"`
}

// ClaudeChatCandidates lists the active registered Claude sessions here.
func (a *Agent) ClaudeChatCandidates() ([]ClaudeChatCandidate, error) {
	rows, err := a.store.db.Query(`SELECT record FROM reply_sessions WHERE json_extract(record,'$.harness')='claude' AND json_extract(record,'$.active')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaudeChatCandidate
	for rows.Next() {
		var raw string
		var r replySessionRecord
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &r) != nil || r.Claude == nil || r.Claude.verify(false) != nil {
			continue
		}
		out = append(out, ClaudeChatCandidate{Handle: r.Handle, SessionID: r.SessionID, Dir: r.Claude.CWD, Inbox: claudeInboxPath(r.ClaudeInbox, r.Claude.PID) == nil})
	}
	return out, rows.Err()
}

// ConnectClaudeChat makes the registered session sid a named local agent.
// Only the person runs it, by hand (cmd/agentnet refuses a session or run).
func (a *Agent) ConnectClaudeChat(sid, label string) (string, error) {
	var found *replySessionRecord
	rows, err := a.store.db.Query(`SELECT record FROM reply_sessions WHERE json_extract(record,'$.harness')='claude' AND json_extract(record,'$.session_id')=?`, sid)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var raw string
		var r replySessionRecord
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &r)
		}
		if err != nil {
			rows.Close()
			return "", err
		}
		if found != nil {
			rows.Close()
			return "", errors.New("ambiguous native Claude registration")
		}
		found = &r
	}
	rows.Close()
	if found == nil || found.Claude == nil {
		return "", errors.New("no registered Claude session has that ID; open the chat and send it one message first")
	}
	c := ClaudeChat{Handle: found.Handle, SessionID: sid, PID: found.Claude.PID, Ticks: found.Claude.Ticks}
	if _, err = a.claudeChatTarget(a.store.db, c); err != nil {
		return "", err
	}
	if err = found.Claude.verify(false); err != nil {
		return "", err
	}
	if err = claudeInboxPath(found.ClaudeInbox, c.PID); err != nil {
		return "", err
	}
	entries, err := a.localAgentsIn(a.store.db)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Responder != nil && e.Responder.Chat != nil && e.Responder.Chat.Handle == c.Handle {
			if *e.Responder.Chat == c {
				return e.Record.ID, nil
			}
			chat := c
			next := copyResponder(*e.Responder)
			next.Chat, next.Dir = &chat, found.Claude.CWD
			return e.Record.ID, a.SetLocalAgentResponder(e.Record.ID, &next)
		}
	}
	record, err := a.CreateLocalAgent(label, Responder{Harness: ClaudeChatHarness, Dir: found.Claude.CWD, Chat: &c})
	return record.ID, err
}

// chatOutcome is how a chat run ends: state "" with the answer, or an end
// state with its reason.
type chatOutcome struct{ state, detail, answer string }

func chatEnd(state, detail string) chatOutcome { return chatOutcome{state: state, detail: detail} }

// claudeChatAsker reports whether j comes from this person: asked here, or
// from a current verified human device of this computer's own person. The
// chat takes it as a message from the person's own other session, so no
// one else's request goes in.
func (a *Agent) claudeChatAsker(j job) bool {
	if j.Local {
		return true
	}
	var ok bool
	return a.store.db.QueryRow(`SELECT `+ownHumanQuestionHolds("?", "?"), j.From, j.Key, j.From, j.Key).Scan(&ok) == nil && ok
}

// runClaudeChat delivers prompt once and waits for its exact answer.
func (a *Agent) runClaudeChat(runCtx context.Context, j job, r *Responder, prompt string, activity *runActivity) chatOutcome {
	c := r.Chat
	if !a.claudeChatAsker(j) {
		return chatEnd(stateNotRun, "A Claude chat agent takes requests only from your own devices. Nothing was sent.")
	}
	row, exists, err := chatRowIn(a.store.db, j.ID)
	if err != nil {
		return chatEnd(stateJobFailed, err.Error())
	}
	if exists && row.State == chatUnsent {
		// Nothing was written: a rerun the person asked for may send it.
		if _, err = a.store.db.Exec(`DELETE FROM claude_chat_deliveries WHERE request=? AND state=?`, j.ID, chatUnsent); err != nil {
			return chatEnd(stateJobFailed, err.Error())
		}
		exists = false
	}
	var listener *chatListener
	if !exists {
		if len(prompt) > claudeChatMax {
			return chatEnd(stateJobFailed, "the request is too long for a Claude chat message")
		}
		if listener, err = newChatListener(); err != nil {
			return chatEnd(stateJobFailed, "no private reply socket: "+err.Error())
		}
		defer listener.close()
		var why string
		if row, why = a.deliverClaudeChat(j, *c, prompt, listener.address); why != "" {
			return chatEnd(stateJobFailed, "not sent to the Claude chat: "+why)
		}
	} else if row.PID != c.PID || row.Ticks != c.Ticks || row.Handle != c.Handle {
		return chatEnd(stateNeedHuman, "This was delivered to an earlier Claude chat process; it is not sent again.")
	} else if row.State == chatSending {
		// A run stopped between committing the row and recording the write:
		// the chat's transcript says whether the frame arrived. It is never
		// written again.
		if seen, _ := a.claudeChatSeen(row); seen {
			a.setChatState(j.ID, chatSending, chatWritten, "")
			row.State = chatWritten
		} else {
			why := "AgentNet stopped while sending this to the Claude chat; whether it arrived is unknown. It was not sent again."
			a.setChatState(j.ID, chatSending, chatSuperseded, why)
			return chatEnd(stateNeedHuman, why)
		}
	}
	return a.waitClaudeChat(runCtx, j, *c, r.Timeout, listener, activity)
}

// deliverClaudeChat verifies the exact chat process and writes one frame.
func (a *Agent) deliverClaudeChat(j job, c ClaudeChat, prompt, sender string) (chatRow, string) {
	reg, err := a.claudeChatTarget(a.store.db, c)
	if err != nil {
		return chatRow{}, err.Error()
	}
	if err = reg.Claude.verify(false); err != nil {
		return chatRow{}, err.Error()
	}
	if err = claudeInboxPath(reg.ClaudeInbox, c.PID); err != nil {
		return chatRow{}, err.Error()
	}
	exit, err := watchProcessExit(c.PID, c.Ticks)
	if err != nil {
		return chatRow{}, err.Error()
	}
	exit.close() // the wait opens its own; this one proved the pid is that process now
	var offset int64
	if info, err := os.Stat(reg.File); err == nil {
		offset = info.Size()
	}
	token := chatUUID()
	content := claudeChatContent(sender, token, prompt)
	row := chatRow{Handle: c.Handle, Session: c.SessionID, PID: c.PID, Ticks: c.Ticks, MsgID: chatUUID(), Sender: sender,
		ContentSHA: chatSHA(content), WriterPID: os.Getpid(), Offset: offset, State: chatSending}
	tx, err := a.store.db.Begin()
	if err != nil {
		return chatRow{}, err.Error()
	}
	defer tx.Rollback()
	var running int
	if err = tx.QueryRow(`SELECT count(*) FROM inbox WHERE id=? AND state=? AND attempts=?`, j.ID, stateRunning, j.nativeAttempt).Scan(&running); err != nil || running != 1 {
		return chatRow{}, "this attempt is no longer current"
	}
	if _, err = tx.Exec(`INSERT INTO claude_chat_deliveries(request,handle,session,pid,ticks,msg_id,sender,content_sha,writer_pid,offset,state) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, row.Handle, row.Session, row.PID, row.Ticks, row.MsgID, row.Sender, row.ContentSHA, row.WriterPID, row.Offset, row.State); err != nil {
		return chatRow{}, "already sent once: " + err.Error()
	}
	if err = tx.Commit(); err != nil {
		return chatRow{}, err.Error()
	}
	conn, err := net.DialTimeout("unix", reg.ClaudeInbox, 5*time.Second)
	if err != nil {
		a.setChatState(j.ID, chatSending, chatUnsent, err.Error())
		return chatRow{}, "the chat's inbox did not answer"
	}
	defer conn.Close()
	if pid, err := peerPID(conn); err != nil || pid != c.PID {
		a.setChatState(j.ID, chatSending, chatUnsent, "inbox socket owned by another process")
		return chatRow{}, "the inbox socket belongs to another process"
	}
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err = conn.Write(claudeChatFrame(row.MsgID, sender, c.SessionID, content)); err != nil {
		// Some bytes may have left: the ledger stays "sending" and the
		// transcript decides (claudeChatSeen); it is not written again.
		a.Logf("%s %s: Claude chat write: %v", j.Kind, j.ID, err)
		return row, ""
	}
	a.setChatState(j.ID, chatSending, chatWritten, "")
	row.State = chatWritten
	return row, ""
}

func (a *Agent) setChatState(request, from, to, detail string) {
	if _, err := a.store.db.Exec(`UPDATE claude_chat_deliveries SET state=?,detail=? WHERE request=? AND state=?`, to, detail, request, from); err != nil {
		a.Logf("Claude chat ledger %s: %v", request, err)
	}
}

// waitClaudeChat waits, without polling, for the ledger to reach an end:
// local changes (the chat's hooks write the ledger and wake the daemon),
// the chat process exiting (pidfd), the chat's status frames, the run's own
// stop, or the deadline.
func (a *Agent) waitClaudeChat(runCtx context.Context, j job, c ClaudeChat, limit time.Duration, listener *chatListener, activity *runActivity) chatOutcome {
	exit, err := watchProcessExit(c.PID, c.Ticks)
	var exited <-chan struct{}
	if err == nil {
		defer exit.close()
		exited = exit.done
	}
	var statuses <-chan string
	if listener != nil {
		statuses = listener.statuses
	}
	if limit <= 0 {
		limit = claudeChatDeadline
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	reported := false
	gone := err != nil
	for {
		_, changed := a.Changed()
		row, ok, err := chatRowIn(a.store.db, j.ID)
		if err != nil || !ok {
			return chatEnd(stateJobFailed, "the Claude chat delivery record is gone")
		}
		switch row.State {
		case chatAnswered:
			return chatOutcome{answer: row.Answer}
		case chatFolded:
			return chatEnd(stateNeedHuman, "The Claude chat read this during a turn it was already running, so its answer could not be told apart. It was not sent again.")
		case chatSuperseded:
			why := row.Detail
			if why == "" {
				why = "The Claude chat's turn for this ended without a recorded answer (stopped, or replaced by another prompt). It was not sent again."
			}
			return chatEnd(stateNeedHuman, why)
		case chatRefused:
			return chatEnd(stateJobFailed, "The Claude chat did not take this message: "+row.Detail)
		case chatStarted:
			if !reported {
				activity.report(activityWorking)
				reported = true
			}
		}
		if gone {
			return chatEnd(stateNeedHuman, "The Claude chat closed before it answered. It was not sent again.")
		}
		if _, err := a.claudeChatTarget(a.store.db, c); err != nil {
			return chatEnd(stateNeedHuman, err.Error()+" before it answered. It was not sent again.")
		}
		select {
		case <-runCtx.Done():
			return chatEnd("", "")
		case <-changed:
		case <-exited:
			gone, exited = true, nil // read the ledger once more: a last Stop may have landed
		case s := <-statuses:
			switch s {
			case "refused", "denied", "expired", "dropped":
				for _, from := range []string{chatSending, chatWritten, chatStarted} {
					a.setChatState(j.ID, from, chatRefused, s)
				}
			case "held":
				a.Logf("%s %s: the Claude chat holds it for approval", j.Kind, j.ID)
			}
		case <-deadline.C:
			why := fmt.Sprintf("No answer from the Claude chat was recorded within %s. It was delivered and is not sent again; look in the chat.", limit)
			a.setChatState(j.ID, row.State, chatSuperseded, why)
			return chatEnd(stateNeedHuman, why)
		}
	}
}

// claudeChatSeen reports whether the chat's transcript holds the frame's
// exact peer record, written by this writer.
func (a *Agent) claudeChatSeen(row chatRow) (bool, error) {
	reg, err := replySessionIn(a.store.db, row.Handle)
	if err != nil {
		return false, err
	}
	return claudeChatTranscriptHas(reg.File, row.Session, row)
}

// ClaudeChatHook records, from the exact registered session's own hook, the
// turn its delivered frame started and that turn's answer. It only reads
// unless that session has a live delivery.
func (a *Agent) ClaudeChatHook(handle, event, sid, _, prompt, last string) {
	if event != "UserPromptSubmit" && event != "Stop" {
		return
	}
	var live int
	if a.store.db.QueryRow(`SELECT count(*) FROM claude_chat_deliveries WHERE session=? AND handle=? AND state IN ('sending','written','started')`, sid, handle).Scan(&live) != nil || live == 0 {
		return
	}
	reg, err := replySessionIn(a.store.db, handle)
	if err != nil || !reg.Active || reg.Claude == nil || reg.SessionID != sid {
		return
	}
	rows, err := a.store.db.Query(`SELECT request FROM claude_chat_deliveries WHERE session=? AND handle=? AND pid=? AND ticks=? AND state IN ('sending','written','started')`, sid, handle, reg.Claude.PID, reg.Claude.Ticks)
	if err != nil {
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
	changed := false
	for _, id := range ids {
		row, ok, err := chatRowIn(a.store.db, id)
		if err != nil || !ok {
			continue
		}
		// The writer may move sending to written at the same moment: each
		// change names the states it may follow, not the snapshot read here.
		to, detail, answer, from := "", "", "", []any{chatSending, chatWritten}
		switch {
		case event == "UserPromptSubmit" && chatSHA(prompt) == row.ContentSHA && row.State != chatStarted:
			to = chatStarted
		case event == "UserPromptSubmit" && row.State == chatStarted && chatSHA(prompt) != row.ContentSHA:
			to, detail, from = chatSuperseded, "The Claude chat's turn for this ended without a recorded answer (stopped, or replaced by another prompt). It was not sent again.", []any{chatStarted}
		case event == "Stop" && row.State == chatStarted:
			to, answer, from = chatAnswered, last, []any{chatStarted}
			if len(answer) > maxOutput {
				answer = answer[:maxOutput] + "\n[output truncated]"
			}
		case event == "Stop":
			// A Stop before this frame started a turn: if the chat already
			// read it, that was into a turn it did not start.
			if seen, _ := claudeChatTranscriptHas(reg.File, sid, row); seen {
				to = chatFolded
			}
		}
		if to == "" {
			continue
		}
		res, err := a.store.db.Exec(`UPDATE claude_chat_deliveries SET state=?,detail=?,answer=? WHERE request=? AND state IN (?`+strings.Repeat(",?", len(from)-1)+`)`, append([]any{to, detail, answer, id}, from...)...)
		if n, _ := res.RowsAffected(); err == nil && n == 1 {
			changed = true
		}
	}
	if changed {
		a.store.changed()
		notifyDaemon(a.home)
	}
}

// claudeChatTranscriptHas reports whether the session's transcript, from
// the delivery's starting size, holds its exact peer record: msg_id, our
// sender address and the writing process the chat itself verified.
func claudeChatTranscriptHas(file, sid string, row chatRow) (bool, error) {
	return claudeNativeScan(file, sid, true, row.Offset, func(raw json.RawMessage) bool {
		var rec struct {
			Type   string
			Origin struct {
				Kind            string
				From            string
				MsgID           string `json:"msg_id"`
				VerifiedPeerPid int    `json:"verifiedPeerPid"`
			}
		}
		return json.Unmarshal(raw, &rec) == nil && rec.Type == "user" && rec.Origin.Kind == "peer" &&
			rec.Origin.MsgID == row.MsgID && rec.Origin.From == row.Sender && rec.Origin.VerifiedPeerPid == row.WriterPID
	})
}

// chatListener is a private reply socket for one delivery: the chat sends
// its peer_message_status frames (held, refused, expired...) to it.
type chatListener struct {
	dir, address string
	ln           net.Listener
	statuses     chan string
}

func newChatListener() (*chatListener, error) {
	dir, err := os.MkdirTemp("", "an-chat-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "r.sock")
	if len(path) > 100 {
		os.Remove(dir)
		if dir, err = os.MkdirTemp("/tmp", "an-chat-"); err != nil {
			return nil, err
		}
		path = filepath.Join(dir, "r.sock")
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		os.Remove(dir)
		return nil, err
	}
	os.Chmod(path, 0o600)
	l := &chatListener{dir: dir, address: "uds:" + path, ln: ln, statuses: make(chan string, 8)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go l.read(c)
		}
	}()
	return l, nil
}

func (l *chatListener) read(c net.Conn) {
	defer c.Close()
	if pid, err := peerPID(c); err != nil || pid < 1 {
		return
	}
	dec := json.NewDecoder(io.LimitReader(c, 1<<20))
	for {
		var f struct {
			Type, Action, Status string
		}
		if dec.Decode(&f) != nil {
			return
		}
		if f.Type == "control" && f.Action == "peer_message_status" && f.Status != "" {
			select {
			case l.statuses <- f.Status:
			default:
			}
		}
	}
}

func (l *chatListener) close() {
	l.ln.Close()
	os.RemoveAll(l.dir)
}
