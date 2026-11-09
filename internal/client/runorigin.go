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

type runOriginAnswer struct {
	V      int    `json:"v"`
	Run    string `json:"run,omitempty"`
	Person string `json:"person,omitempty"`
	Author string `json:"author,omitempty"`
	Error  string `json:"error,omitempty"`
}

func runOriginRefusal(why string) runOriginAnswer { return runOriginAnswer{V: 1, Error: why} }

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
// device. A local ask (this device's key is shared with its local agents), a
// continuation and a reply are "agent".
func (a *Agent) runOriginIdentity(j job) (person, author string, err error) {
	person, err = permissionPersonIn(a.store.db, j.From, j.Key)
	if err != nil {
		return "", "", err
	}
	if person == "" && j.From == a.Address && j.Key != "" && j.Key == a.Self().Fingerprint() {
		p, found, e := personBySelfIn(a.store.db, a.Address)
		if e != nil {
			return "", "", e
		}
		if found && p.info.State == personSelf && p.has(j.From, j.Key) {
			person = p.info.Person
		}
	}
	if person == "" {
		return "", "", nil
	}
	author = runOriginAgent
	if j.Receiver == nil && !j.Local && !j.followUp() {
		human, e := a.ownHumanAuthor(a.store.db, j.From, j.Key)
		if e != nil {
			return "", "", e
		}
		if human {
			author = runOriginHuman
		}
	}
	return person, author, nil
}

// answerRunOrigin vouches for req only when it names a process this daemon
// started as a run, still its direct child with that start time, for a
// request still running, whose requester still verifies as recorded.
// live reports a process's parent and start time, and whether it runs.
func (a *Agent) answerRunOrigin(o *runOrigins, req runOriginRequest, live func(int) (int, uint64, bool)) runOriginAnswer {
	o.mu.Lock()
	e, ok := o.byPID[req.PID]
	o.mu.Unlock()
	if !ok {
		return runOriginRefusal("this daemon did not start that process as a run")
	}
	if e.start != req.Start {
		return runOriginRefusal("the process start time does not match that run")
	}
	if ppid, start, alive := live(req.PID); !alive || ppid != os.Getpid() || start != e.start {
		return runOriginRefusal("that run is no longer running under this daemon")
	}
	var state string
	var attempt int64
	if err := a.store.db.QueryRow(`SELECT state,attempts FROM inbox WHERE id=?`, e.job.ID).Scan(&state, &attempt); err != nil || state != stateRunning || attempt != e.attempt {
		return runOriginRefusal("that request is no longer running")
	}
	person, author, err := a.runOriginIdentity(e.job)
	if err != nil || person != e.person {
		return runOriginRefusal("the requester no longer verifies as the same person")
	}
	if e.author == runOriginHuman && author != runOriginHuman {
		return runOriginRefusal("the requester's device no longer verifies as their own human device")
	}
	ans := runOriginAnswer{V: 1, Run: e.run, Person: e.person, Author: e.author}
	for _, s := range []string{ans.Run, ans.Person, ans.Author} {
		if !runOriginText(s) {
			return runOriginRefusal("that run cannot be described")
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
	if req.V != 1 || req.PID <= 0 || req.Start == 0 {
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
