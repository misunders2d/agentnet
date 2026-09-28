package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// Switching a running daemon to an updated program file (agentnet update).
//
// The updater writes an owner-only request into the home and wakes the
// daemon over the local socket. The daemon acts only when the request names
// the program file it was started from. It then starts no new job, waits
// until the job it is running (if any) has finished and stored its result,
// checks that the file now reports the requested version, stops cleanly, and
// Run returns RestartForUpdate so its caller starts the new program in its
// place. The program that starts next records the outcome once and clears
// the request, so a crash, a repeat or a request that no longer applies
// never loops and never stays pending.

const (
	updateRequestFile    = "update-request.json"
	updateActivationFile = "update-activation.json"
	// UpdateRestartEnv carries, into the program started in the daemon's
	// place, the id of the request it completes.
	UpdateRestartEnv = "AGENTNET_UPDATE_RESTART"
)

// UpdateRequest asks a home's daemon to switch to an updated program file.
type UpdateRequest struct {
	ID   string    `json:"id"`
	Exe  string    `json:"exe"`  // the program file that was replaced
	From string    `json:"from"` // the version the updater ran as
	To   string    `json:"to"`   // the version the file now holds
	At   time.Time `json:"at"`
}

// UpdateActivation records what became of the last request.
type UpdateActivation struct {
	ID      string    `json:"id"`
	To      string    `json:"to"`
	Result  string    `json:"result"`            // ActivationRunning, ActivationNotApplied or ActivationFailed
	Running string    `json:"running,omitempty"` // the version the daemon runs
	PID     int       `json:"pid,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	At      time.Time `json:"at"`
}

// Activation results.
const (
	ActivationRunning    = "running"     // the daemon now runs the requested version
	ActivationNotApplied = "not_applied" // the request did not apply to this daemon; nothing was stopped
	ActivationFailed     = "failed"      // the daemon stopped but the updated program did not take over
)

// RestartForUpdate is Run's result when the daemon stopped so that the
// updated program can take its place.
type RestartForUpdate struct{ Request UpdateRequest }

func (e *RestartForUpdate) Error() string {
	return "stopped to switch to agentnet " + e.Request.To
}

// RequestUpdateSwitch asks home's running daemon to switch to r.Exe, which
// now reports r.To.
func RequestUpdateSwitch(home string, r UpdateRequest) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := secfile.Write(filepath.Join(home, updateRequestFile), data); err != nil {
		return err
	}
	notifyDaemon(home)
	return nil
}

// ReadUpdateActivation returns the outcome of the last request, if any.
func ReadUpdateActivation(home string) (UpdateActivation, bool, error) {
	var act UpdateActivation
	data, err := secfile.Read(filepath.Join(home, updateActivationFile))
	if errors.Is(err, os.ErrNotExist) {
		return act, false, nil
	}
	if err != nil {
		return act, false, err
	}
	return act, true, json.Unmarshal(data, &act)
}

// RecordUpdateActivation stores the outcome of request r.
func RecordUpdateActivation(home string, act UpdateActivation) error {
	if act.At.IsZero() {
		act.At = time.Now()
	}
	data, err := json.Marshal(act)
	if err != nil {
		return err
	}
	return secfile.Write(filepath.Join(home, updateActivationFile), data)
}

func readUpdateRequest(home string) (UpdateRequest, bool, error) {
	var r UpdateRequest
	data, err := secfile.Read(filepath.Join(home, updateRequestFile))
	if errors.Is(err, os.ErrNotExist) {
		return r, false, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &r)
	}
	if err == nil && (r.ID == "" || r.To == "") {
		err = errors.New("incomplete")
	}
	return r, true, err
}

// finishUpdateRequest clears the request and records its outcome.
func (a *Agent) finishUpdateRequest(act UpdateActivation) {
	os.Remove(filepath.Join(a.home, updateRequestFile))
	act.Running, act.PID = protocol.Version, os.Getpid()
	if err := RecordUpdateActivation(a.home, act); err != nil {
		a.Logf("update: cannot record the outcome: %v", err)
	}
	if act.Detail != "" {
		a.Logf("update to %s: %s (%s)", act.To, act.Result, act.Detail)
	} else {
		a.Logf("update to %s: %s", act.To, act.Result)
	}
}

// settleUpdate runs once the daemon has started everything local (worker,
// messenger page, direct listener): it completes or clears a request left
// by an update, whatever happened since, and never starts a switch. Then
// the worker may look at new requests.
func (a *Agent) settleUpdate() {
	defer func() {
		a.update.Lock()
		a.update.ready = true
		a.update.Unlock()
		a.wakeWorker()
	}()
	r, found, err := readUpdateRequest(a.home)
	if !found {
		return
	}
	act := UpdateActivation{ID: r.ID, To: r.To}
	switch {
	case err != nil:
		act.Result, act.Detail = ActivationNotApplied, "unreadable request: "+err.Error()
	case r.To == protocol.Version:
		act.Result = ActivationRunning
	case os.Getenv(UpdateRestartEnv) == r.ID:
		act.Result, act.Detail = ActivationFailed, "the program started in the daemon's place reports agentnet "+protocol.Version
	default:
		act.Result, act.Detail = ActivationNotApplied, "this daemon started as agentnet "+protocol.Version
	}
	a.finishUpdateRequest(act)
}

// startFailed records, for a request left by an update, that the daemon
// could not start (so nothing claims the update took effect), and returns
// err.
func (a *Agent) startFailed(err error) error {
	if r, found, rerr := readUpdateRequest(a.home); found {
		detail := "the daemon could not start as agentnet " + protocol.Version + ": " + err.Error()
		if rerr != nil {
			detail = "unreadable request; " + detail
		}
		a.finishUpdateRequest(UpdateActivation{ID: r.ID, To: r.To, Result: ActivationFailed, Detail: detail})
	}
	return err
}

// checkUpdateRequest looks for a request while the daemon runs. A request
// for this daemon's own program file makes it pending: no new job starts.
func (a *Agent) checkUpdateRequest() {
	if a.exe == "" {
		return // no known program file to switch to
	}
	a.update.Lock()
	defer a.update.Unlock()
	if !a.update.ready || a.update.pending != nil {
		return // not started yet (settleUpdate first), or already pending
	}
	r, found, err := readUpdateRequest(a.home)
	switch {
	case !found:
	case err != nil:
		a.finishUpdateRequest(UpdateActivation{ID: r.ID, To: r.To, Result: ActivationNotApplied, Detail: "unreadable request: " + err.Error()})
	case r.Exe != a.exe:
		a.finishUpdateRequest(UpdateActivation{ID: r.ID, To: r.To, Result: ActivationNotApplied,
			Detail: "this daemon runs " + a.exe + ", not the updated " + r.Exe})
	case r.To == protocol.Version:
		a.finishUpdateRequest(UpdateActivation{ID: r.ID, To: r.To, Result: ActivationRunning})
	case a.canSwitch != nil:
		if ok, why := a.canSwitch(); !ok {
			a.finishUpdateRequest(UpdateActivation{ID: r.ID, To: r.To, Result: ActivationNotApplied, Detail: why})
			return
		}
		fallthrough
	default:
		a.update.pending = &r
		a.Logf("update to %s requested: no new job starts; the daemon switches once none runs", r.To)
	}
}

func (a *Agent) updatePending() *UpdateRequest {
	a.update.Lock()
	defer a.update.Unlock()
	return a.update.pending
}

// UpdateSwitching reports the request the daemon is stopping for, if any.
func (a *Agent) UpdateSwitching() *UpdateRequest {
	a.update.Lock()
	defer a.update.Unlock()
	return a.update.switching
}

// switchForUpdate runs in the worker once no job runs. It checks that the
// program file reports the requested version and, if so, stops the daemon so
// that its caller starts the new program. Otherwise the daemon carries on.
func (a *Agent) switchForUpdate(ctx context.Context, r UpdateRequest) {
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, a.exe, "version").Output()
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || !strings.HasPrefix(line, "agentnet "+r.To+" (protocol ") {
		a.update.Lock()
		a.update.pending = nil
		a.update.Unlock()
		a.finishUpdateRequest(UpdateActivation{ID: r.ID, To: r.To, Result: ActivationNotApplied,
			Detail: fmt.Sprintf("%s reports %q, so the daemon keeps running agentnet %s", a.exe, line, protocol.Version)})
		a.wakeWorker() // jobs may start again
		return
	}
	a.update.Lock()
	a.update.switching = &r
	a.update.Unlock()
	a.Logf("switching to agentnet %s: stopping this daemon", r.To)
	if a.stopRun != nil {
		a.stopRun()
	}
}

// SchemaSteps is the number of schema steps this program's home database
// has; a program with fewer cannot open a home this one has opened.
func SchemaSteps() int { return len(schema) }

// PendingUpdateSwitch returns the version a request not yet completed asks
// for, or "".
func PendingUpdateSwitch(home string) (string, error) {
	r, found, err := readUpdateRequest(home)
	if !found || err != nil {
		return "", err
	}
	return r.To, nil
}

// HomeSchema reads the schema version of home's database read-only, without
// migrating it; found is false when the home has no database yet.
func HomeSchema(home string) (version int, found bool, err error) {
	_, path := paths(home)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, true, err
	}
	defer db.Close()
	err = db.QueryRow("PRAGMA user_version").Scan(&version)
	return version, true, err
}
