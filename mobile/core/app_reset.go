package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/ui"
)

// SetupStartAgain reuses the established appSetAside move-and-rollback
// strategy. Android keeps its entire old home together in a private sibling,
// including other workspaces, keys, history and incomplete enrollment data.
// It runs only on an explicit click with no live provider/database in this App.
func (a *appSetup) SetupStartAgain() (ui.SetupView, error) {
	if !a.join.TryLock() {
		return ui.SetupView{}, ui.Refuse("Already joining. Wait a moment.")
	}
	defer a.join.Unlock()
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.mu.Lock()
	closed, hasSession := a.closed, a.session != nil || len(a.sessions) != 0
	a.mu.Unlock()
	if closed || hasSession {
		return ui.SetupView{}, ui.Refuse("Close the enrolled session before starting again.")
	}
	info, err := os.Lstat(a.home)
	if errors.Is(err, os.ErrNotExist) {
		return a.SetupState(), nil
	}
	if err != nil {
		return ui.SetupView{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ui.SetupView{}, ui.Refuse("The mobile home is not a private directory.")
	}
	release, err := lockfile.Acquire(filepath.Join(a.home, "daemon.lock"))
	if err != nil {
		return ui.SetupView{}, ui.Refuse("This home is in use. Close its session first.")
	}
	defer release()
	state, err := client.EnrollmentState(a.home)
	if err != nil {
		return ui.SetupView{}, err
	}
	if !client.EnrollEnded(state) && state != client.EnrollIncomplete {
		return ui.SetupView{}, ui.Refuse("Only an ended or incomplete enrollment can start again.")
	}
	if _, err = mobileSetAside(a.home, time.Now()); err != nil {
		return ui.SetupView{}, ui.Refuse("Could not preserve the previous enrollment: " + err.Error())
	}
	// The loopback origin and authentication are independent of the home.
	// No registered providers existed, and the fresh directory has no identity.
	return ui.SetupView{State: client.EnrollNone, Device: "android-phone", DeviceWords: "Android phone"}, nil
}

func mobileSetAside(home string, now time.Time) (string, error) {
	parent := filepath.Dir(home)
	backup, err := os.MkdirTemp(parent, filepath.Base(home)+"-previous-"+now.UTC().Format("20060102-150405")+"-")
	if err != nil {
		return "", err
	}
	// MkdirTemp creates mode0700. Never delete backup directories, even when a
	// move fails: failures leave an inspectable, recoverable location.
	saved := filepath.Join(backup, "home")
	if err = os.Rename(home, saved); err != nil {
		return backup, err
	}
	if err = os.Mkdir(home, 0700); err != nil {
		if rollback := os.Rename(saved, home); rollback != nil {
			return backup, fmt.Errorf("fresh home failed (%v); previous home remains at %s (%v)", err, saved, rollback)
		}
		return backup, err
	}
	return backup, nil
}
