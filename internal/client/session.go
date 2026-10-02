package client

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Background sessions: for harnesses that can save and resume a headless
// session, the worker keeps one session per conversation and mode and
// resumes it for that conversation's next question or accepted task, so the
// harness keeps its own context. The session belongs to the worker: it is
// never one the user opened. Its saved context is the harness's own data on
// disk (e.g. ~/.claude/projects, ~/.codex/sessions), separate from
// AgentNet's history.
//
// A job resumes a session only from the nearest earlier message of the same
// conversation (same peer) that recorded one, only if that job ended cleanly
// and everything that bounds the session is the same: harness, mode,
// directory and preset (flags plus the harness binary's identity).
// Otherwise it starts a new session and says why; it never falls back to an
// older one further up.

// sessionStyle is how a harness saves and resumes headless sessions.
type sessionStyle int

const (
	noSessions     sessionStyle = iota // every job is a fresh, unsaved run
	claudeSessions                     // --session-id UUID creates, --resume UUID continues
	codexSessions                      // exec --json reports thread_id; exec resume ID continues
)

// sessionRef is recorded on the received question or task whose job ran
// in the session.
type sessionRef struct {
	Harness string `json:"harness"`
	Mode    string `json:"mode"` // "question" or "task"
	Dir     string `json:"dir"`  // canonical working directory
	Preset  string `json:"preset"`
	ID      string `json:"id"`
}

// sessionPlan is how one job runs.
type sessionPlan struct {
	args   []string    // harness arguments before the answer-file flag
	tail   []string    // arguments after it (codex resume reads the prompt from "-")
	ref    *sessionRef // nil: a one-shot run, nothing recorded
	resume bool
	note   string // why a new session was started instead of resuming, if it was
}

const sessionOneShot = "--no-session-persistence" // claude's flag for unsaved runs

// planSession decides whether j runs one-shot, in a new session or in a
// resumed one.
func (a *Agent) planSession(j job, r *Responder, h harness) (sessionPlan, error) {
	mode, base := "question", h.question
	if j.Kind == envelope.KindTask {
		mode, base = "task", h.task
	}
	if h.sessions == noSessions || j.Receiver == nil && (j.followUp() || j.PID != "" || j.AgentID != "") { // named remote jobs stay fresh; local selected continuation owns its native session
		return sessionPlan{args: slices.Clone(base)}, nil
	}
	dir, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return sessionPlan{}, err
	}
	want := sessionRef{Harness: r.Harness, Mode: mode, Dir: dir, Preset: presetID(h, mode, base)}
	prev, state, at, err := a.store.sessionAncestor(j.From, j.ID)
	if j.Receiver != nil {
		prev, state, at, err = a.store.receiverSessionAncestor(j.Receiver.ID, j.ID)
	}
	if err != nil {
		return sessionPlan{}, err
	}
	var note string
	switch {
	case at == "":
	case at == j.ID:
		note = "this job is being run again, so the session of its earlier attempt was not reused"
	case prev.Harness != want.Harness:
		note = fmt.Sprintf("the earlier session of this conversation was not reused: it was a %s session", prev.Harness)
	case prev.Mode != want.Mode:
		note = fmt.Sprintf("the earlier session of this conversation was not reused: it was a %s-mode session and this is a %s", prev.Mode, want.Mode)
	case prev.Dir != want.Dir || prev.Preset != want.Preset:
		note = "the earlier session of this conversation was not reused: the responder's directory, flags or program changed since"
	case state != stateAnswered && state != stateSummary && state != stateContinued:
		note = fmt.Sprintf("the earlier session of this conversation was not reused: its last job ended as %q, so its state is uncertain", state)
	case a.store.sessionHead(prev) != at:
		note = "the earlier session of this conversation was not reused: a later job has run in it since, or its latest job is unknown"
	default:
		want.ID = prev.ID
		return sessionPlan{args: resumeArgs(h, mode, base, prev.ID), tail: sessionTail(h), ref: &want, resume: true}, nil
	}
	if h.sessions == claudeSessions {
		want.ID = newUUID()
	}
	return sessionPlan{args: createArgs(h, base, want.ID), tail: sessionTail(h), ref: &want, note: note}, nil
}

// createArgs starts a new saved session.
func createArgs(h harness, base []string, id string) []string {
	args := withoutFlags(base, sessionOneShot, "--ephemeral")
	switch h.sessions {
	case claudeSessions:
		return append(args, "--session-id", id)
	case codexSessions:
		return append(args, "--json") // stdout: JSON events (codexstream.go)
	}
	return args
}

// resumeArgs continues session id with the same restrictions as base.
func resumeArgs(h harness, mode string, base []string, id string) []string {
	switch h.sessions {
	case claudeSessions:
		return append(withoutFlags(base, sessionOneShot), "--resume", id)
	case codexSessions:
		// `codex exec resume` takes the global -c/--enable/--disable options
		// but not --sandbox or --color: the sandbox is given as its
		// documented config key instead.
		args := []string{"exec", "resume", id, "--json"}
		rest := withoutFlags(base, "--ephemeral")
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "exec":
			case "--color":
				i++
			case "--sandbox":
				i++
				args = append(args, "-c", fmt.Sprintf("sandbox_mode=%q", rest[i]))
			default:
				args = append(args, rest[i])
			}
		}
		return args
	}
	return slices.Clone(base)
}

// sessionTail ends the arguments: codex reads the prompt from stdin when
// given "-", as the verified create and resume runs did.
func sessionTail(h harness) []string {
	if h.sessions == codexSessions {
		return []string{"-"}
	}
	return nil
}

// withoutFlags returns args without the given value-less flags.
func withoutFlags(args []string, drop ...string) []string {
	var out []string
	for _, a := range args {
		if !slices.Contains(drop, a) {
			out = append(out, a)
		}
	}
	return out
}

// presetID identifies everything a session was started under apart from
// its directory: harness flags for the mode and the harness binary's path,
// size and modification time (a cheap stand-in for its version).
func presetID(h harness, mode string, base []string) string {
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00%s\x00%s", h.bin, mode, strings.Join(base, "\x00"))
	if path, err := exec.LookPath(h.bin); err == nil {
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(sum, "\x00%s\x00%d\x00%d", path, info.Size(), info.ModTime().UnixNano())
		}
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// sessionAncestor walks from message id (itself included) up its reply
// links with peer, received and sent messages alike, and returns the first
// received message that recorded a session: its session, state and id
// (empty if none).
func (s *store) sessionAncestor(peer, id string) (ref sessionRef, state, at string, err error) {
	seen := map[string]bool{}
	for id != "" && !seen[id] && len(seen) < 10000 {
		seen[id] = true
		var raw sql.NullString
		var next string
		err = s.db.QueryRow(`SELECT session_ref, state, coalesce(reply_to, '') FROM inbox WHERE id = ? AND sender = ?`, id, peer).
			Scan(&raw, &state, &next)
		if errors.Is(err, sql.ErrNoRows) {
			err = s.db.QueryRow(`SELECT coalesce(reply_to, '') FROM outbox WHERE id = ? AND recipient = ?`, id, peer).Scan(&next)
			if errors.Is(err, sql.ErrNoRows) {
				return ref, "", "", nil
			}
			if err != nil {
				return ref, "", "", err
			}
			id = next
			continue
		}
		if err != nil {
			return ref, "", "", err
		}
		if raw.Valid {
			if err := json.Unmarshal([]byte(raw.String), &ref); err != nil {
				return sessionRef{}, "unreadable", id, nil // not reusable
			}
			return ref, state, id, nil
		}
		id = next
	}
	return ref, "", "", nil
}

// setSessionRef records the job's session while the worker still owns it,
// and, in the same transaction, makes this job the session's head: the
// latest job that ran (or runs) in it. Only the head may lead to a resume,
// so a branch from an older message of the conversation, or a job after a
// later failure, starts fresh. Execution order, not arrival order, decides.
func (s *store) setSessionRef(id string, ref sessionRef) error {
	data, _ := json.Marshal(ref)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE inbox SET session_ref = ? WHERE id = ? AND state IN (?, ?)`, string(data), id, stateRunning, stateCancelReq)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("the job is no longer owned by the worker")
	}
	if _, err := tx.Exec(`INSERT INTO config(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, headKey(ref), id); err != nil {
		return err
	}
	return tx.Commit()
}

func headKey(ref sessionRef) string { return "session_head:" + ref.Harness + ":" + ref.ID }

// sessionHead returns the id of the latest job in ref's session, or "" if
// none is recorded (e.g. sessions from before heads were kept).
func (s *store) sessionHead(ref sessionRef) string {
	v, _ := s.config(headKey(ref))
	return v
}
