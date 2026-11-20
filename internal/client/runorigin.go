package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Run origins (opt-in, `agentnet daemon --run-origin`, Linux only): a local
// program another process asks, live, whether a process is a background run
// this daemon started and for whom (docs/revival/INSTALL.md, "Run origin
// socket"). Heimdall uses it to let the person's own AgentNet request manage
// schedules. Nothing a run is given (its environment, files) carries this:
// the answer is about the live process, asked again before every use.

const (
	runOriginFile    = "run-origin.sock"
	runOriginLimit   = 4 << 10
	runOriginTimeout = 2 * time.Second
	runOriginMaxText = 128

	runOriginHuman = "human"
	runOriginAgent = "agent"
)

// runOrigin is what this daemon vouches for one run it started: frozen when
// it starts, so the answer never changes while it runs.
type runOrigin struct {
	start               uint64 // /proc/PID/stat field 22 of the run's process
	job                 job    // only the request's identity fields
	attempt             int64
	run, person, author string
	chain               runOriginChain // version 2's fields (runoriginchain.go)
	chainErr            error          // why version 2 cannot describe this run
}

// runOrigins holds the runs of this daemon process only, in memory: a run an
// earlier daemon started is never vouched for.
type runOrigins struct {
	mu    sync.Mutex
	byPID map[int]runOrigin
}

type runOriginRequest struct {
	V     int    `json:"v"`
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
}

// runOriginAnswer is version 1's answer; version 2 (asked with "v":2)
// adds kind, chain and reply, and agent and device when they have a plain
// name. Every field is a string, so two answers compare with ==.
type runOriginAnswer struct {
	V      int    `json:"v"`
	Run    string `json:"run,omitempty"`
	Person string `json:"person,omitempty"`
	Author string `json:"author,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Chain  string `json:"chain,omitempty"`
	Reply  string `json:"reply,omitempty"`
	Agent  string `json:"agent,omitempty"`
	Device string `json:"device,omitempty"`
	Error  string `json:"error,omitempty"`
}

func runOriginRefusal(why string) runOriginAnswer { return runOriginAnswer{V: 1, Error: why} }

// runOriginRefusalV refuses a request that was read, in its own version.
func runOriginRefusalV(v int, why string) runOriginAnswer { return runOriginAnswer{V: v, Error: why} }

// runOriginPeerAllowed: only root and this daemon's own user may ask.
func runOriginPeerAllowed(uid, self uint32) bool { return uid == 0 || uid == self }

// vouchRun records that pid, just started by this process for j, may be
// vouched for while j runs. It returns the release to call when the
// process has ended. Off (no socket), or for a requester without a current
// verified person, it records nothing.
func (a *Agent) vouchRun(j job, pid int) func() {
	o := a.runOrigin.Load()
	if o == nil {
		return func() {}
	}
	_, start, alive := procLive(pid)
	if !alive {
		return func() {}
	}
	who := job{ID: j.ID, From: j.From, Key: j.Key, Kind: j.Kind, Local: j.Local, Receiver: j.Receiver}
	person, author, err := a.runOriginIdentity(who)
	if err != nil || person == "" {
		if err != nil {
			a.Logf("%s %s: run origin not recorded: %v", j.Kind, j.ID, err)
		}
		return func() {}
	}
	e := runOrigin{start: start, job: who, attempt: j.nativeAttempt, run: j.ID + "/" + strconv.FormatInt(j.nativeAttempt, 10), person: person, author: author}
	if e.chain, e.chainErr = a.runOriginChainOf(who, person, author); e.chainErr != nil {
		a.Logf("%s %s: run origin version 2 not available: %v", j.Kind, j.ID, e.chainErr)
	}
	o.mu.Lock()
	o.byPID[pid] = e
	o.mu.Unlock()
	return func() {
		o.mu.Lock()
		if cur, ok := o.byPID[pid]; ok && cur.run == e.run {
			delete(o.byPID, pid)
		}
		o.mu.Unlock()
	}
}

// runOriginIdentity is who made request j, from AgentNet's own checks: the
// verified roster person ID of the exact key that signed it, and "human"
// only for that person's own request from their own current verified human
// device that an agent did not write (runOriginAgentWritten). A local ask
// (this device's key is shared with its local agents), a continuation and a
// reply are "agent". The vouch and every re-check use this one function.
func (a *Agent) runOriginIdentity(j job) (person, author string, err error) {
	if person, err = a.runOriginPerson(j.From, j.Key); err != nil || person == "" {
		return "", "", err
	}
	author = runOriginAgent
	if j.Receiver == nil && !j.Local && !j.followUp() {
		human, e := a.ownHumanAuthor(a.store.db, j.From, j.Key)
		if e != nil {
			return "", "", e
		}
		if human {
			agent, e := runOriginAgentWritten(a.store.db, j.ID)
			if e != nil {
				return "", "", e
			}
			if !agent {
				author = runOriginHuman
			}
		}
	}
	return person, author, nil
}

// runOriginAgentWritten reports whether stored request id says an agent
// wrote it, whatever key signed it: a human device's key also signs the
// asks of the agents it hosts (agentnet room ask, MEL-713). An agent wrote
// it when its origin is "agent:<harness>" (envelope.AgentOrigin), or when its
// captured audience names an author participation (AuthorPID) and no proof
// scope of that participation has the human role: an agent room
// participant, or one this device cannot show to be a human guest. A
// member's own turn (no AuthorPID) with a "ui" or no origin is a person's.
// A missing row or an unreadable audience is an error, never "human".
func runOriginAgentWritten(q dbq, id string) (bool, error) {
	var origin, raw string
	if err := q.QueryRow(`SELECT coalesce(origin,''), coalesce(human,'') FROM inbox WHERE id=?`, id).Scan(&origin, &raw); err != nil {
		return false, err
	}
	if envelope.AgentOrigin(origin) {
		return true, nil
	}
	if raw == "" {
		return false, nil
	}
	var h envelope.HumanTurn
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return false, err
	}
	if h.AuthorPID == "" {
		return false, nil
	}
	for _, e := range h.Proof {
		if e.Type == protocol.EventScope && e.PID == h.AuthorPID && e.Role == protocol.RoleHuman {
			return false, nil
		}
	}
	return true, nil
}

// answerRunOrigin vouches for req only when it names a process this daemon
// started as a run, still its direct child with that start time, for a
// request still running, whose requester still verifies as recorded.
// live reports a process's parent and start time, and whether it runs.
func (a *Agent) answerRunOrigin(o *runOrigins, req runOriginRequest, live func(int) (int, uint64, bool)) runOriginAnswer {
	refuse := func(why string) runOriginAnswer { return runOriginRefusalV(req.V, why) }
	o.mu.Lock()
	e, ok := o.byPID[req.PID]
	o.mu.Unlock()
	if !ok {
		return refuse("this daemon did not start that process as a run")
	}
	if e.start != req.Start {
		return refuse("the process start time does not match that run")
	}
	if ppid, start, alive := live(req.PID); !alive || ppid != os.Getpid() || start != e.start {
		return refuse("that run is no longer running under this daemon")
	}
	var state string
	var attempt int64
	if err := a.store.db.QueryRow(`SELECT state,attempts FROM inbox WHERE id=?`, e.job.ID).Scan(&state, &attempt); err != nil || state != stateRunning || attempt != e.attempt {
		return refuse("that request is no longer running")
	}
	person, author, err := a.runOriginIdentity(e.job)
	if err != nil || person != e.person {
		return refuse("the requester no longer verifies as the same person")
	}
	if e.author == runOriginHuman && author != runOriginHuman {
		return refuse("the request no longer verifies as its person's own, from their own human device")
	}
	ans := runOriginAnswer{V: 1, Run: e.run, Person: e.person, Author: e.author}
	if req.V == 2 {
		if e.chainErr != nil {
			return refuse("that run's request chain cannot be described")
		}
		now, err := a.runOriginChainOf(e.job, e.person, e.author)
		if err != nil || now != e.chain {
			return refuse("that run's request chain changed")
		}
		reply, err := a.replyHandleFor(e.job.ID, e.person, e.chain.target)
		if err != nil {
			return refuse("that run's reply handle cannot be recorded")
		}
		ans.V, ans.Kind, ans.Chain, ans.Reply, ans.Agent, ans.Device = 2, e.chain.kind, e.chain.chain, reply, e.chain.agent, e.chain.device
		for _, s := range []string{ans.Kind, ans.Chain} {
			if !runOriginText(s) {
				return refuse("that run cannot be described")
			}
		}
	}
	for _, s := range []string{ans.Run, ans.Person, ans.Author} {
		if !runOriginText(s) {
			return refuse("that run cannot be described")
		}
	}
	return ans
}

// runOriginText: non-empty, at most 128 bytes, no control characters.
func runOriginText(s string) bool {
	if s == "" || len(s) > runOriginMaxText || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return false
		}
	}
	return true
}

// readRunOriginRequest reads exactly one JSON object on one line.
func readRunOriginRequest(r io.Reader) (runOriginRequest, error) {
	line, err := bufio.NewReaderSize(io.LimitReader(r, runOriginLimit), runOriginLimit).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return runOriginRequest{}, err
	}
	if errors.Is(err, io.EOF) && len(line) >= runOriginLimit {
		return runOriginRequest{}, errors.New("request too long")
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var req runOriginRequest
	if err := dec.Decode(&req); err != nil {
		return runOriginRequest{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return runOriginRequest{}, errors.New("trailing data")
	}
	if req.V != 1 && req.V != 2 || req.PID <= 0 || req.Start == 0 {
		return runOriginRequest{}, errors.New("invalid request")
	}
	return req, nil
}

// serveRunOriginConn answers one asker, already admitted by its peer
// credentials, then closes.
func (a *Agent) serveRunOriginConn(o *runOrigins, c io.ReadWriteCloser, live func(int) (int, uint64, bool)) {
	defer c.Close()
	ans := runOriginRefusal("invalid request")
	if req, err := readRunOriginRequest(c); err == nil {
		ans = a.answerRunOrigin(o, req, live)
	}
	data, err := json.Marshal(ans)
	if err != nil {
		return
	}
	c.Write(append(data, '\n'))
}
