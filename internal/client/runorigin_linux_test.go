//go:build linux

package client

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// askRunOrigin sends one raw request line and returns the raw answer line.
func askRunOrigin(t *testing.T, a *Agent, line string) string {
	t.Helper()
	c, err := net.DialTimeout("unix", filepath.Join(a.home, runOriginFile), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _ := c.Read(buf)
	return string(buf[:n])
}

func askRunOriginPID(t *testing.T, a *Agent, pid int, start uint64) runOriginAnswer {
	t.Helper()
	raw := askRunOrigin(t, a, `{"v":1,"pid":`+strconv.Itoa(pid)+`,"start":`+strconv.FormatUint(start, 10)+`}`)
	var ans runOriginAnswer
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ans); err != nil || !strings.HasSuffix(raw, "\n") || ans.V != 1 {
		t.Fatalf("answer %q: %v", raw, err)
	}
	if (ans.Error == "") == (ans.Run == "") || ans.Error != "" && (ans.Person != "" || ans.Author != "") {
		t.Fatalf("answer mixes fields: %q", raw)
	}
	return ans
}

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	return c
}

func liveStart(t *testing.T, pid int) uint64 {
	t.Helper()
	_, start, ok := procLive(pid)
	if !ok {
		t.Fatalf("pid %d not running", pid)
	}
	return start
}

func setRunning(t *testing.T, a *Agent, id string) {
	t.Helper()
	if _, err := a.store.db.Exec(`UPDATE inbox SET state=?, attempts=1 WHERE id=?`, stateRunning, id); err != nil {
		t.Fatal(err)
	}
}

// The socket vouches only for a live direct child this daemon registered for
// a running request: the person's own human request is "human" with their
// roster person ID; their agent host's request is "agent"; wrong start,
// unknown, grandchild, cancelled and ended processes are refused.
func TestRunOriginVouchesOnlyLiveRuns(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	stop, err := w.alice.listenRunOrigin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if fi, err := os.Stat(filepath.Join(w.alice.home, runOriginFile)); err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode %v %v", fi, err)
	}
	self, _, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil || self.info.Person == "" {
		t.Fatal(self, err)
	}
	person := self.info.Person
	phone := linkedVia(t, w.alice, "phone", approveLink(w.alice))
	host := linkedVia(t, w.alice, "host", w.alice.ApproveAgentLink)

	humanID := ownPendingTask(t, w.alice, phone, "list my schedules")
	setRunning(t, w.alice, humanID)
	child := startSleeper(t)
	pid := child.Process.Pid
	release := w.alice.vouchRun(job{ID: humanID, From: phone.Address, Key: phone.Self().Fingerprint(), Kind: envelope.KindTask, nativeAttempt: 1}, pid)
	start := liveStart(t, pid)
	raw := askRunOrigin(t, w.alice, `{"v":1,"pid":`+strconv.Itoa(pid)+`,"start":`+strconv.FormatUint(start, 10)+`}`)
	if want := `{"v":1,"run":"` + humanID + `/1","person":"` + person + `","author":"human"}` + "\n"; raw != want {
		t.Fatalf("own human run:\n got %q\nwant %q", raw, want)
	}
	if a := askRunOriginPID(t, w.alice, pid, start+1); a.Error == "" {
		t.Fatalf("start mismatch vouched: %+v", a)
	}
	if a := askRunOriginPID(t, w.alice, os.Getpid(), liveStart(t, os.Getpid())); a.Error == "" {
		t.Fatalf("unregistered process vouched: %+v", a)
	}
	for _, line := range []string{`{"v":1,"pid":` + strconv.Itoa(pid) + `,"start":` + strconv.FormatUint(start, 10) + `,"person":"x"}`,
		`{"v":2,"pid":` + strconv.Itoa(pid) + `,"start":` + strconv.FormatUint(start, 10) + `}`, `not json`} {
		if raw := askRunOrigin(t, w.alice, line); raw != `{"v":1,"error":"invalid request"}`+"\n" {
			t.Fatalf("%s -> %q", line, raw)
		}
	}

	// The agent host is the same person, but not a human device.
	task, err := host.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "agent's own task", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "host task stored", func() bool { _, err := w.alice.store.jobState(task.ID); return err == nil })
	setRunning(t, w.alice, task.ID)
	agentChild := startSleeper(t)
	defer w.alice.vouchRun(job{ID: task.ID, From: host.Address, Key: host.Self().Fingerprint(), Kind: envelope.KindTask, nativeAttempt: 1}, agentChild.Process.Pid)()
	if a := askRunOriginPID(t, w.alice, agentChild.Process.Pid, liveStart(t, agentChild.Process.Pid)); a.Author != runOriginAgent || a.Person != person || a.Run != task.ID+"/1" {
		t.Fatalf("agent-host request: %+v", a)
	}
	// A local ask, a continuation and a reply are never the person's own.
	phoneJob := job{ID: humanID, From: phone.Address, Key: phone.Self().Fingerprint(), Kind: envelope.KindTask}
	for name, j := range map[string]job{
		"local":        {ID: humanID, From: phone.Address, Key: phoneJob.Key, Kind: envelope.KindTask, Local: true},
		"continuation": {ID: humanID, From: phone.Address, Key: phoneJob.Key, Kind: envelope.KindTask, Receiver: &ReplyReceiverBinding{ID: "r"}},
		"reply":        {ID: humanID, From: phone.Address, Key: phoneJob.Key, Kind: envelope.KindResult},
	} {
		if p, author, err := w.alice.runOriginIdentity(j); err != nil || p != person || author != runOriginAgent {
			t.Fatalf("%s: %q %q %v", name, p, author, err)
		}
	}
	if p, _, err := w.alice.runOriginIdentity(job{From: phone.Address, Key: w.bob.Self().Fingerprint(), Kind: envelope.KindTask}); err != nil || p != "" {
		t.Fatalf("wrong key has a person: %q %v", p, err)
	}

	// Cancelled: the request is no longer running, though its process is.
	if _, err := w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, humanID); err != nil {
		t.Fatal(err)
	}
	if a := askRunOriginPID(t, w.alice, pid, start); a.Error == "" {
		t.Fatalf("cancelled request vouched: %+v", a)
	}
	setRunning(t, w.alice, humanID)
	if a := askRunOriginPID(t, w.alice, pid, start); a.Author != runOriginHuman {
		t.Fatalf("running again: %+v", a)
	}
	// Ended: refused even before its release.
	child.Process.Kill()
	child.Wait()
	if a := askRunOriginPID(t, w.alice, pid, start); a.Error == "" {
		t.Fatalf("ended process vouched: %+v", a)
	}
	release()
	if a := askRunOriginPID(t, w.alice, pid, start); a.Error == "" {
		t.Fatalf("released process vouched: %+v", a)
	}
	// A registered pid that is not this daemon's direct child.
	sh := exec.Command("sh", "-c", "sleep 30 & echo $!; wait")
	out, err := sh.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sh.Process.Kill(); sh.Wait() })
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	grand, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(grand); err == nil {
			p.Kill()
		}
	})
	phoneJob.nativeAttempt = 1 // everything but its parent matches
	defer w.alice.vouchRun(phoneJob, grand)()
	if a := askRunOriginPID(t, w.alice, grand, liveStart(t, grand)); a.Error == "" {
		t.Fatalf("grandchild vouched: %+v", a)
	}
}

// Through the real worker: the harness process it starts is vouched for
// while it runs, its own child is not, and after Stop nothing is.
func TestRunOriginWorkerRun(t *testing.T) {
	st := installStub(t, "sleep")
	w := newWorld(t, "")
	stop, err := w.alice.listenRunOrigin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	runAgent(t, w.alice)
	persons(t, w.alice)
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	phone := linkedVia(t, w.alice, "phone", approveLink(w.alice))
	task, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "make a schedule", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	o := w.alice.runOrigin.Load()
	var pid int
	var e runOrigin
	eventually(t, "the run vouched for", func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		for p, v := range o.byPID {
			if v.job.ID == task.ID {
				pid, e = p, v
				return true
			}
		}
		return false
	})
	self, _, _ := w.alice.store.selfPerson(w.alice.Address)
	if a := askRunOriginPID(t, w.alice, pid, liveStart(t, pid)); a.Author != runOriginHuman || a.Person != self.info.Person || a.Run != e.run || !strings.HasPrefix(a.Run, task.ID+"/") {
		t.Fatalf("worker run: %+v", a)
	}
	var grand int
	eventually(t, "the harness's own child", func() bool {
		data, _ := os.ReadFile(st.log + ".child")
		grand, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return grand > 0
	})
	if a := askRunOriginPID(t, w.alice, grand, liveStart(t, grand)); a.Error == "" {
		t.Fatalf("descendant vouched: %+v", a)
	}
	start := e.start
	if err := w.alice.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run released", func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		_, ok := o.byPID[pid]
		return !ok
	})
	if a := askRunOriginPID(t, w.alice, pid, start); a.Error == "" {
		t.Fatalf("stopped run vouched: %+v", a)
	}
}

func TestRunOriginPeerAllowed(t *testing.T) {
	for _, c := range []struct {
		uid, self uint32
		ok        bool
	}{{0, 1000, true}, {1000, 1000, true}, {1001, 1000, false}, {65534, 0, false}, {0, 0, true}} {
		if got := runOriginPeerAllowed(c.uid, c.self); got != c.ok {
			t.Fatalf("uid %d self %d: %v", c.uid, c.self, got)
		}
	}
}

// Off by default: nothing is recorded and no socket exists.
func TestRunOriginOffByDefault(t *testing.T) {
	a := &Agent{home: t.TempDir()}
	a.vouchRun(job{ID: "x"}, os.Getpid())()
	if _, err := os.Stat(filepath.Join(a.home, runOriginFile)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
