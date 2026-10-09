package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

type appIndependentDaemon struct {
	Exe  string `json:"exe"`
	From string `json:"from"`
	PID  int    `json:"pid"`
}

// The home's owner-only UI endpoint authenticates a fresh local Overview.
// No redirects, proxy or other origin receives its cookie.
func attachedVersion(ctx context.Context, home string) (version, endpoint string, err error) {
	version, endpoint, err = attachedOverview(ctx, home)
	if err != nil {
		return "", "", err
	}
	if _, ok := parseRelease(version); !ok {
		return "", "", errors.New("The attached daemon is not a supported release build.")
	}
	return version, endpoint, nil
}

// Attaching a page also supports development daemons. Update qualification
// separately requires an official release version in attachedVersion.
func attachedOverview(ctx context.Context, home string) (version, endpoint string, err error) {
	b, err := secfile.Read(filepath.Join(home, uiURLFile))
	if err != nil {
		return "", "", err
	}
	endpoint = strings.TrimSpace(string(b))
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Port() == "" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Query().Get("t") == "" {
		return "", "", errors.New("The attached daemon has no valid local page.")
	}
	token := u.Query().Get("t")
	u.Path = "/api/overview"
	u.RawQuery = ""
	u.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: token})
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	c := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return "", "", errors.New("The attached daemon's page is unavailable.")
	}
	defer resp.Body.Close()
	var status struct {
		Version string `json:"version"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&status) != nil || status.Version == "" {
		return "", "", errors.New("The attached daemon's version could not be verified.")
	}
	return status.Version, endpoint, nil
}

func noDaemonUpdateRequest(home string) error {
	if _, err := secfile.Read(filepath.Join(home, "update-request.json")); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return errors.New("The daemon's existing update request could not be inspected.")
		}
		return errors.New("The daemon already has an update pending. Wait for it before updating the app.")
	}
	return nil
}

// Same-version requests qualify only an already registered official file.
// The existing daemon checks sameProgramFile before its same-version branch,
// records ActivationRunning and never fences jobs or restarts for that branch.
// Holding the file's existing update lock prevents managed byte replacement
// between the fresh version/file checks and this bounded qualification.
func qualifyIndependentDaemon(ctx context.Context, home string) (*appIndependentDaemon, error) {
	if err := noDaemonUpdateRequest(home); err != nil {
		return nil, err
	}
	record, err := readAppCommandRecord(home)
	if err != nil {
		return nil, err
	}
	targets := append([]appCommandTarget{{Path: record.Path, Sum: record.Sum}}, record.Targets...)
	seen := map[string]bool{}
	for _, target := range targets {
		if target.Path == "" || seen[target.Path] {
			continue
		}
		seen[target.Path] = true
		if validateAppCommandTarget(target.Path) != nil {
			continue
		}
		release, err := lockfile.Acquire(updateLockPath(target.Path))
		if err != nil {
			return nil, errors.New("A registered command is already being updated. Wait and retry.")
		}
		qualified, err := qualifyIndependentTarget(ctx, home, target)
		release()
		if err != nil {
			return nil, err
		}
		if qualified != nil {
			return qualified, nil
		}
	}
	return nil, errors.New("The independently managed daemon does not run an unchanged registered official command. Its app update remains unavailable; restore/register that official command before retrying.")
}

func qualifyIndependentTarget(ctx context.Context, home string, target appCommandTarget) (*appIndependentDaemon, error) {
	want, err := hex.DecodeString(target.Sum)
	if err != nil || len(want) != 32 {
		return nil, nil
	}
	have, err := fileSum(target.Path)
	if err != nil || !bytes.Equal(want, have) {
		return nil, nil
	}
	version, endpoint, err := attachedVersion(ctx, home)
	if err != nil {
		return nil, err
	}
	line, err := fileVersion(ctx, target.Path)
	if err != nil || line != versionLine(version) {
		return nil, nil
	} // never request an old-version switch as a probe
	if err := noDaemonUpdateRequest(home); err != nil {
		return nil, err
	}
	request := client.UpdateRequest{ID: protocol.NewID(), Exe: target.Path, From: version, To: version, At: time.Now()}
	if err := client.RequestUpdateSwitch(home, request); err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, appStartTimeout)
	defer cancel()
	for {
		act, ok, err := client.ReadUpdateActivation(home)
		if err != nil {
			return nil, err
		}
		if ok && act.ID == request.ID {
			if act.Running != version {
				return nil, errors.New("The daemon changed version during identity qualification. Retry once its update completes.")
			}
			if act.Result == client.ActivationNotApplied {
				return nil, nil
			}
			if act.Result != client.ActivationRunning || act.To != version || act.PID <= 0 {
				return nil, errors.New("The daemon's identity could not be qualified.")
			}
			after, afterEndpoint, err := attachedVersion(wait, home)
			if err != nil || after != version || afterEndpoint != endpoint {
				return nil, errors.New("The attached daemon changed during identity qualification. Retry from its current page.")
			}
			return &appIndependentDaemon{Exe: target.Path, From: version, PID: act.PID}, nil
		}
		select {
		case <-wait.Done():
			return nil, fmt.Errorf("The daemon did not answer identity qualification: %w", wait.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Continue the user's persisted app update through the daemon's existing
// idle-safe switch. Reopening the app never reissues an acknowledged switch.
func (r *appRunner) beginIndependentSwitch() (err error) {
	r.independentSwitchMu.Lock()
	defer r.independentSwitchMu.Unlock()
	result, readErr := readAppUpdateResult(r.home)
	if readErr != nil || result.Independent == nil || result.Version != protocol.Version || (result.State == "failed" && !appUpdateRestartFailure(result)) || result.SwitchID != "" {
		return nil
	}
	defer func() {
		if err != nil {
			result.State = "partial"
			result.Problem = err.Error()
			_ = saveAppUpdateResult(r.home, result)
		}
	}()
	if status := r.currentCommandStatus(); status.State != "installed" {
		return errors.New("The app's commands must be verified before its independent daemon can switch.")
	}
	record, err := readAppCommandRecord(r.home)
	if err != nil {
		return err
	}
	registered := record.Path == result.Independent.Exe
	for _, target := range record.Targets {
		registered = registered || target.Path == result.Independent.Exe
	}
	if !registered {
		return errors.New("The qualified daemon command is no longer a registered app target.")
	}
	request := client.UpdateRequest{ID: protocol.NewID(), Exe: result.Independent.Exe, From: result.Independent.From, To: result.Version, At: time.Now()}
	if b, e := secfile.Read(filepath.Join(r.home, "update-request.json")); e == nil {
		var existing client.UpdateRequest
		if json.Unmarshal(b, &existing) != nil || existing.ID == "" || existing.Exe != request.Exe || existing.To != request.To {
			return errors.New("Another daemon update is pending; its request was preserved.")
		}
		request = existing // crash after writing the request, before saving its id
	} else if !errors.Is(e, os.ErrNotExist) {
		return errors.New("The daemon's pending update could not be inspected.")
	} else if err := client.RequestUpdateSwitch(r.home, request); err != nil {
		return err
	}
	result.SwitchID = request.ID
	result.State = "pending"
	result.Problem = "Waiting for the independently managed daemon to finish its active jobs and switch."
	return saveAppUpdateResult(r.home, result)
}
