package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// Automatic update (owner policy "latest only", v0.8.17). A daemon that
// learns of a release newer than the one it runs (the Hub's release or
// update_required event, or its update_required refusal) installs it the
// way this installation updates: RunOptions.AutoUpdate, which is the
// standalone command's agentnet update and daemon switch, or the AgentNet
// app's whole-app update. The Hub only names the version; what is
// downloaded, and from where, is the updater's: one fixed origin, checked
// against the release's checksums, tried before it is put in place.
//
// It is on unless the person turns it off (agentnet update --auto off, an
// owner-only file in the home: nothing received changes it). A development
// build never updates itself. One attempt runs at a time, and only while no
// job runs. Each trigger (a release or update_required event, a refusal, a
// daemon start) allows one attempt, never a timer. Every attempt is
// recorded in the home before it starts (auto-update.json: from, to, when,
// what came of it, how many in a row), and the next program reads it: the
// same release is not tried again from the same version until
// autoUpdateWait has passed, whatever the attempt's outcome, so neither a
// connection that keeps reconnecting nor an app that its update helper
// starts again after a failed install (a start is a trigger) turns into a
// download or restart loop. Each further attempt doubles the wait.

const (
	autoUpdateFile       = "auto-update"      // "off": no automatic updates (owner-only)
	autoUpdateRecordFile = "auto-update.json" // what the last attempt did
)

// autoUpdateRetry is how soon after an attempt the same release may be
// tried again from the same version; autoUpdateRetryMax bounds it as it
// doubles. Variables so tests can shorten them.
var (
	autoUpdateRetry    = 30 * time.Minute
	autoUpdateRetryMax = 24 * time.Hour
)

// autoUpdateWait is how long after the tries-th attempt in a row of one
// release another may start: autoUpdateRetry, doubled for each attempt
// after the first, at most autoUpdateRetryMax.
func autoUpdateWait(tries int) time.Duration {
	d := autoUpdateRetry
	for i := 1; i < tries && d < autoUpdateRetryMax; i++ {
		d *= 2
	}
	return min(d, autoUpdateRetryMax)
}

// Automatic update states.
const (
	AutoUpdating     = "updating"  // an attempt runs
	AutoUpdated      = "installed" // installed, or handed to the app; the switch to it is requested
	AutoUpdateFailed = "failed"    // tried again at a later trigger
)

// AutoUpdateRecord is what the daemon's last automatic update did.
type AutoUpdateRecord struct {
	To     string    `json:"to"`   // the release it installs
	From   string    `json:"from"` // the version that ran
	State  string    `json:"state"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
	Tries  int       `json:"tries,omitempty"` // attempts in a row of To from From
}

// autoUpdate is a daemon's automatic update (RunOptions.AutoUpdate).
type autoUpdate struct {
	install func(ctx context.Context, release string) (string, error)
	due     atomic.Bool // a trigger came: the worker looks once
	mu      sync.Mutex
	running bool           // one attempt at a time in this program; the record is the rest
	runs    sync.WaitGroup // Run waits for an attempt before it returns
}

// AutoUpdateOn reports whether this home updates itself (on unless the
// person turned it off). An unreadable or unexpected setting is not on.
func AutoUpdateOn(home string) (bool, error) {
	b, err := secfile.Read(filepath.Join(home, autoUpdateFile))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(b)) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, fmt.Errorf("%s says %q, not on or off", filepath.Join(home, autoUpdateFile), strings.TrimSpace(string(b)))
}

// SetAutoUpdate turns this home's automatic updates on or off.
func SetAutoUpdate(home string, on bool) error {
	if err := secfile.EnsureDir(home); err != nil {
		return err
	}
	v := "off\n"
	if on {
		v = "on\n"
	}
	return secfile.Write(filepath.Join(home, autoUpdateFile), []byte(v))
}

// ReadAutoUpdate returns what the last automatic update did, if any.
func ReadAutoUpdate(home string) (AutoUpdateRecord, bool, error) {
	var r AutoUpdateRecord
	b, err := secfile.Read(filepath.Join(home, autoUpdateRecordFile))
	if errors.Is(err, os.ErrNotExist) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	return r, true, json.Unmarshal(b, &r)
}

func writeAutoUpdate(home string, r AutoUpdateRecord) error {
	b, _ := json.Marshal(r)
	return secfile.Write(filepath.Join(home, autoUpdateRecordFile), b)
}

func (a *Agent) recordAutoUpdate(r AutoUpdateRecord) error {
	err := writeAutoUpdate(a.home, r)
	if err != nil {
		a.Logf("automatic update: cannot record what it did: %v", err)
	}
	a.changes.bump()
	return err
}

// NoteAutoUpdateFailed records that the automatic update to release from
// this version failed after it was handed over: the AgentNet app's update
// helper could not install it and started this version again. The record
// then says so, and the wait before the next attempt runs from now. A
// record of another attempt is left as it is.
func NoteAutoUpdateFailed(home, release, detail string) error {
	r, ok, err := ReadAutoUpdate(home)
	if err != nil || !ok || r.To != release || r.From != protocol.Version || r.State == AutoUpdateFailed {
		return err
	}
	r.State, r.Detail, r.At = AutoUpdateFailed, detail, time.Now()
	return writeAutoUpdate(home, r)
}

// autoUpdateDue is a trigger: the worker looks once.
func (a *Agent) autoUpdateDue() { a.auto.due.Store(true) }

// autoUpdateTarget is the release to install. While the Hub refuses this
// build, the release it requires, its own, which lifts the suspension:
// never a newer recommendation, an admin's notice, which may name a release
// not published (yet) for this platform and must keep nobody suspended; it
// comes next, once the Hub serves this device again. Otherwise the Hub's
// recommendation when it names a release newer than this build.
func (a *Agent) autoUpdateTarget() string {
	if u, ok := a.UpdateRequired(); ok && releaseTag.MatchString(u.Latest) && protocol.Newer(u.Latest, protocol.Version) {
		return u.Latest
	}
	if r, ok := a.store.updateRecommended(); ok && releaseTag.MatchString(r.Version) {
		return r.Version
	}
	return ""
}

// maybeAutoUpdate runs on the worker after a trigger: it starts one attempt
// when there is a newer release, automatic updates are on, this is a
// release build and no job runs (a running job keeps the trigger for when
// it ends).
func (a *Agent) maybeAutoUpdate(ctx context.Context) {
	if a.auto.install == nil || !a.auto.due.Load() || !a.executorsIdle() || a.updatePending() != nil || a.UpdateSwitching() != nil {
		return
	}
	a.auto.due.Store(false)
	target := a.autoUpdateTarget()
	if target == "" || !releaseTag.MatchString(protocol.Version) {
		return // nothing newer, or a development build: it never updates itself
	}
	if on, err := AutoUpdateOn(a.home); err != nil || !on {
		if err != nil {
			a.Logf("automatic update to agentnet %s not started: %v", target, err)
		}
		return
	}
	a.auto.mu.Lock()
	defer a.auto.mu.Unlock()
	if a.auto.running {
		return
	}
	// The last attempt, by whichever program made it: the same release
	// from this version waits its turn, whatever came of it (still under
	// way, handed over yet this version runs again, or failed). An
	// unreadable record is not taken as none.
	from, tries := protocol.Version, 1
	prev, found, err := ReadAutoUpdate(a.home)
	if err != nil {
		a.Logf("automatic update to agentnet %s not started: its record is unreadable: %v", target, err)
		return
	}
	if found && prev.From == from && prev.To == target {
		if wait := autoUpdateWait(prev.Tries); time.Since(prev.At) < wait {
			return
		}
		tries = prev.Tries + 1
	}
	// Recorded before it starts: a program that stops meanwhile leaves the
	// record, and the next one waits.
	if a.recordAutoUpdate(AutoUpdateRecord{To: target, From: from, State: AutoUpdating, At: time.Now(), Tries: tries}) != nil {
		return
	}
	a.auto.running = true
	a.auto.runs.Add(1)
	a.Logf("automatic update to agentnet %s: starting", target)
	go func() {
		defer a.auto.runs.Done()
		detail, err := a.auto.install(ctx, target)
		rec := AutoUpdateRecord{To: target, From: from, State: AutoUpdated, Detail: detail, At: time.Now(), Tries: tries}
		if err != nil {
			rec.State, rec.Detail = AutoUpdateFailed, err.Error()
		}
		a.recordAutoUpdate(rec)
		a.auto.mu.Lock()
		a.auto.running = false
		a.auto.mu.Unlock()
		if err != nil {
			a.Logf("automatic update to agentnet %s failed (tried again at a release event, refusal or daemon start after %s; agentnet update tries now): %v", target, autoUpdateWait(tries), err)
			return
		}
		a.Logf("automatic update to agentnet %s: %s", target, detail)
		a.wakeWorker() // a switch it requested is looked at now
	}()
}

// autoUpdateWords says, in a sentence, what this home's automatic update
// does: for doctor, the hook line and the page.
func (a *Agent) autoUpdateWords() string {
	if !releaseTag.MatchString(protocol.Version) {
		return "This development build never updates itself."
	}
	on, err := AutoUpdateOn(a.home)
	switch {
	case err != nil:
		return "Automatic update is not on: " + err.Error() + "."
	case !on:
		return "Automatic update is off (agentnet update --auto on turns it on)."
	}
	words := "Automatic update is on."
	if r, ok, _ := ReadAutoUpdate(a.home); ok && r.From == protocol.Version {
		switch r.State {
		case AutoUpdating:
			words = "Automatic update to " + r.To + " is under way."
		case AutoUpdated:
			words = "Automatic update installed " + r.To + "; AgentNet switches to it once no job runs."
		case AutoUpdateFailed:
			words = "Automatic update to " + r.To + " failed (" + strings.TrimSuffix(r.Detail, ".") + "); it is tried again when the Hub names the release or the daemon starts after " + r.At.Add(autoUpdateWait(r.Tries)).Local().Format("15:04 Jan 2") + ". To update now: agentnet update (or Update in the AgentNet app)."
		}
	}
	return words
}

// AutoUpdateWords is autoUpdateWords for the page and commands.
func (a *Agent) AutoUpdateWords() string { return a.autoUpdateWords() }
