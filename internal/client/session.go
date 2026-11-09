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
//
// A request to a conversation's agent (a participation, MEL-746) has no
// reply chain: its session is the latest one in the same conversation,
// participation and topic, asked by the same verified person (any of their
// current devices), in the same mode and for the same agent. Another person,
// participation or topic gets its own session, and a session is not reused
// once more people see the answers than when it last ran (owner rule,
// docs/DECISIONS.md: reuse native context only when history scope and output
// audience are compatible).

// sessionStyle is how a harness saves and resumes headless sessions.
type sessionStyle int

const (
	noSessions     sessionStyle = iota // every job is a fresh, unsaved run
	claudeSessions                     // --session-id UUID creates, --resume UUID continues
	codexSessions                      // app-server thread/start and exact owned thread/resume
	agySessions                        // stream-json reports conversation_id; --conversation ID continues
)

// sessionRef is recorded on the received question or task whose job ran
// in the session.
type sessionRef struct {
	Harness string `json:"harness"`
	Mode    string `json:"mode"` // "question" or "task"
	Dir     string `json:"dir"`  // canonical working directory
	Preset  string `json:"preset"`
	ID      string `json:"id"`

	Agent    string   `json:"agent,omitempty"`    // the executor agent; "": the default responder
	Scope    string   `json:"scope,omitempty"`    // conversation/participation/topic of a chat's agent
	Person   string   `json:"person,omitempty"`   // the verified asker within Scope
	Audience []string `json:"audience,omitempty"` // who sees its answers, sorted
}

// sessionPlan is how one job runs.
type sessionPlan struct {
	args   []string    // harness arguments before the answer-file flag
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
	if h.sessions == noSessions || j.Receiver == nil && j.followUp() { // a follow-up summary is one-shot
		return sessionPlan{args: slices.Clone(base)}, nil
	}
	dir, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return sessionPlan{}, err
	}
	want := sessionRef{Harness: r.Harness, Mode: mode, Dir: dir, Preset: presetID(h, mode, base)}
	var prev sessionRef
	var state, at string
	switch {
	case j.Receiver != nil:
		prev, state, at, err = a.store.receiverSessionAncestor(j.Receiver.ID, j.ID)
	case j.PID != "":
		want.Agent = executorAgent(j)
		var known bool
		if want.Scope, want.Person, want.Audience, known, err = a.sessionScope(j); err != nil {
			return sessionPlan{}, err
		}
		if !known { // no verified asker to keep a context for
			return sessionPlan{args: slices.Clone(base)}, nil
		}
		prev, state, at, err = a.store.scopeSessionAncestor(want, j.ID)
	default:
		want.Agent = executorAgent(j)
		prev, state, at, err = a.store.sessionAncestor(j.From, j.ID)
	}
	if err != nil {
		return sessionPlan{}, err
	}
	var note string
	switch {
	case at == "":
	case at == j.ID:
		note = "this job is being run again, so the session of its earlier attempt was not reused"
	case j.Receiver == nil && prev.Agent != want.Agent:
		note = "the earlier session of this conversation was not reused: it belonged to another agent"
	case prev.Scope != want.Scope || prev.Person != want.Person:
		note = "the earlier session of this conversation was not reused: it was another person's or another chat's"
	case !audienceWithin(want.Audience, prev.Audience):
		note = "the earlier session of this conversation was not reused: more people see the answers now than when it ran"
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
	case a.store.sessionBusy(prev, j.ID):
		note = "the earlier session of this conversation was not reused: another job is running in it"
	default:
		want.ID = prev.ID
		return sessionPlan{args: resumeArgs(h, mode, base, prev.ID), ref: &want, resume: true}, nil
	}
	if h.sessions == claudeSessions {
		want.ID = newUUID()
	}
	return sessionPlan{args: createArgs(h, base, want.ID), ref: &want, note: note}, nil
}

// createArgs starts a new saved session.
func createArgs(h harness, base []string, id string) []string {
	args := withoutFlags(base, sessionOneShot, "--ephemeral")
	switch h.sessions {
	case claudeSessions:
		return append(args, "--session-id", id)
	case codexSessions:
		return slices.Clone(base) // session identity is in app-server messages
	}
	return args
}

// resumeArgs continues session id with the same restrictions as base.
func resumeArgs(h harness, mode string, base []string, id string) []string {
	switch h.sessions {
	case claudeSessions:
		return append(withoutFlags(base, sessionOneShot), "--resume", id)
	case codexSessions:
		return slices.Clone(base) // thread/resume carries the exact owned ID
	case agySessions:
		return append(slices.Clone(base), "--conversation", id)
	}
	return slices.Clone(base)
}

// executorAgent is the agent a job runs as: its admitted executor's, else
// the one it was addressed to ("" for the default responder).
func executorAgent(j job) string {
	if j.Executor != nil {
		return j.Executor.AgentID
	}
	return j.AgentID
}

// audienceWithin reports whether every reader of now already saw the
// session's answers before: a narrower or equal audience may reuse it.
func audienceWithin(now, before []string) bool {
	for _, who := range now {
		if !slices.Contains(before, who) {
			return false
		}
	}
	return true
}

// sessionScope is a conversation agent request's session scope, its
// verified asker and the audience its answer reaches. known is false when
// the asker cannot be named from verified keys: such a run keeps no session.
// Own human devices are one person; guests and asking group agents are keyed
// by their exact participation.
func (a *Agent) sessionScope(j job) (scope, person string, audience []string, known bool, err error) {
	info, err := a.requestContextParticipation(j)
	if err != nil {
		return "", "", nil, false, err
	}
	m, err := a.dmMembers(j.Conv)
	if err != nil {
		return "", "", nil, false, err
	}
	guests, author, err := a.requestGuests(j, m)
	if err != nil {
		return "", "", nil, false, err
	}
	topic := ""
	if info.Topic != nil {
		topic = *info.Topic
	}
	scope = j.Conv + "/" + j.PID + "/" + topic
	switch {
	case j.Local:
		if info.Host.Person != "" {
			person = "person:" + info.Host.Person
		}
	case author != nil:
		person = "guest:" + author.PID
	default:
		for id, p := range m.persons {
			if p.has(j.From, j.Key) && a.personSender(p, j.From, j.Key).Relation != SenderUnverified {
				person = "person:" + id
			}
		}
	}
	if info.Member {
		h, err := storedHuman(a.store.db, "in", j.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", nil, false, err
		}
		if h != nil && h.AgentAuthor() {
			person = "agent:" + h.AuthorPID
		}
	}
	for id := range m.persons {
		audience = append(audience, "person:"+id)
	}
	for _, g := range guests {
		audience = append(audience, "guest:"+g.PID)
	}
	slices.Sort(audience)
	return scope, person, slices.Compact(audience), person != "", nil
}

// sessionScopeKey names the latest job of one conversation agent session scope.
func sessionScopeKey(ref sessionRef) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{ref.Scope, ref.Person, ref.Mode, ref.Agent}, "\x00")))
	return "session_scope:" + hex.EncodeToString(sum[:16])
}

// scopeSessionAncestor returns the latest job in want's scope that recorded
// a session: its session, state and id (empty if none or no longer recorded).
func (s *store) scopeSessionAncestor(want sessionRef, current string) (ref sessionRef, state, at string, err error) {
	id, err := s.config(sessionScopeKey(want))
	if errors.Is(err, sql.ErrNoRows) || err == nil && id == "" {
		return ref, "", "", nil
	}
	if err != nil {
		return ref, "", "", err
	}
	if id == current {
		return ref, "", id, nil // this job is being run again
	}
	var raw sql.NullString
	err = s.db.QueryRow(`SELECT session_ref, state FROM inbox WHERE id = ?`, id).Scan(&raw, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !raw.Valid {
		return ref, "", "", nil
	}
	if err != nil {
		return ref, "", "", err
	}
	if err := json.Unmarshal([]byte(raw.String), &ref); err != nil {
		return sessionRef{}, "unreadable", id, nil // not reusable
	}
	return ref, state, id, nil
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
	if launch, err := resolveNativeLaunch(h.bin); err == nil {
		for _, path := range []string{launch.Path, launch.Resolved} {
			if info, err := os.Stat(path); err == nil {
				fmt.Fprintf(sum, "\x00%s\x00%d\x00%d", path, info.Size(), info.ModTime().UnixNano())
			}
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
func (s *store) setSessionRef(id string, ref sessionRef, requireRunning ...bool) error {
	data, _ := json.Marshal(ref)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	otherState := stateCancelReq
	if len(requireRunning) > 0 && requireRunning[0] {
		otherState = stateRunning
	}
	res, err := tx.Exec(`UPDATE inbox SET session_ref = ? WHERE id = ? AND state IN (?, ?)`, string(data), id, stateRunning, otherState)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("the job is no longer owned by the worker")
	}
	if _, err := tx.Exec(`INSERT INTO config(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, headKey(ref), id); err != nil {
		return err
	}
	if ref.Scope != "" {
		if _, err := tx.Exec(`INSERT INTO config(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, sessionScopeKey(ref), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// sessionBusy reports whether a job other than id is running in ref's
// session: two runs never share one native session.
func (s *store) sessionBusy(ref sessionRef, id string) bool {
	var busy bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM inbox WHERE id<>? AND state IN (?, ?) AND json_extract(session_ref,'$.harness')=? AND json_extract(session_ref,'$.id')=?)`, id, stateRunning, stateCancelReq, ref.Harness, ref.ID).Scan(&busy)
	return err != nil || busy
}

func headKey(ref sessionRef) string { return "session_head:" + ref.Harness + ":" + ref.ID }

// sessionHead returns the id of the latest job in ref's session, or "" if
// none is recorded (e.g. sessions from before heads were kept).
func (s *store) sessionHead(ref sessionRef) string {
	v, _ := s.config(headKey(ref))
	return v
}
