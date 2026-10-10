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
// daemon start) allows one attempt: a failure is recorded and tried again
// at the next trigger, never on a timer. A release that failed is not tried
// again within autoUpdateRetry, so a connection that keeps reconnecting
// cannot turn its events into a download loop.

const (
	autoUpdateFile       = "auto-update"      // "off": no automatic updates (owner-only)
	autoUpdateRecordFile = "auto-update.json" // what the last attempt did
)

// autoUpdateRetry is how soon a failed release may be tried again in one
// run of the daemon. A variable so tests can shorten it.
var autoUpdateRetry = 30 * time.Minute

// Automatic update states.
const (
	AutoUpdating     = "updating"  // an attempt runs
	AutoUpdated      = "installed" // installed; the switch to it is requested
	AutoUpdateFailed = "failed"    // tried again at the next trigger
)

// AutoUpdateRecord is what the daemon's last automatic update did.
type AutoUpdateRecord struct {
	To     string    `json:"to"`   // the release it installs
	From   string    `json:"from"` // the version that ran
	State  string    `json:"state"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// autoUpdate is a daemon's automatic update (RunOptions.AutoUpdate).
type autoUpdate struct {
	install func(ctx context.Context, release string) (string, error)
	due     atomic.Bool // a trigger came: the worker looks once
	mu      sync.Mutex
	running bool                 // one attempt at a time
	failed  map[string]time.Time // releases that failed in this run, and when
	runs    sync.WaitGroup       // Run waits for an attempt before it returns
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

func (a *Agent) recordAutoUpdate(r AutoUpdateRecord) {
	b, _ := json.Marshal(r)
	if err := secfile.Write(filepath.Join(a.home, autoUpdateRecordFile), b); err != nil {
		a.Logf("automatic update: cannot record what it did: %v", err)
	}
	a.changes.bump()
}

// autoUpdateDue is a trigger: the worker looks once.
func (a *Agent) autoUpdateDue() { a.auto.due.Store(true) }

// autoUpdateTarget is the newest release the Hub names that is newer than
// this build: its recommendation, or the release it requires.
func (a *Agent) autoUpdateTarget() string {
	target := ""
	if r, ok := a.store.updateRecommended(); ok && releaseTag.MatchString(r.Version) {
		target = r.Version
	}
	if u, ok := a.UpdateRequired(); ok && protocol.Newer(u.Latest, protocol.Version) && (target == "" || protocol.Newer(u.Latest, target)) {
		target = u.Latest
	}
	return target
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
	if a.auto.running || time.Since(a.auto.failed[target]) < autoUpdateRetry {
		a.auto.mu.Unlock()
		return
	}
	a.auto.running = true
	a.auto.runs.Add(1)
	a.auto.mu.Unlock()
	from := protocol.Version
	a.recordAutoUpdate(AutoUpdateRecord{To: target, From: from, State: AutoUpdating, At: time.Now()})
	a.Logf("automatic update to agentnet %s: starting", target)
	go func() {
		defer a.auto.runs.Done()
		detail, err := a.auto.install(ctx, target)
		rec := AutoUpdateRecord{To: target, From: from, State: AutoUpdated, Detail: detail, At: time.Now()}
		a.auto.mu.Lock()
		a.auto.running = false
		if err != nil {
			rec.State, rec.Detail = AutoUpdateFailed, err.Error()
			if a.auto.failed == nil {
				a.auto.failed = map[string]time.Time{}
			}
			a.auto.failed[target] = time.Now()
		}
		a.auto.mu.Unlock()
		a.recordAutoUpdate(rec)
		if err != nil {
			a.Logf("automatic update to agentnet %s failed (tried again at the next release event, refusal or daemon start): %v", target, err)
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
			words = "Automatic update to " + r.To + " failed (" + r.Detail + "); it is tried again when the Hub next names the release or the daemon restarts."
		}
	}
	return words
}

// AutoUpdateWords is autoUpdateWords for the page and commands.
func (a *Agent) AutoUpdateWords() string { return a.autoUpdateWords() }
