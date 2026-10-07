package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/secfile"
)

type appUpdateResult struct {
	Version     string                `json:"version"`
	State       string                `json:"state"`
	Problem     string                `json:"problem,omitempty"`
	Independent *appIndependentDaemon `json:"independent,omitempty"`
	SwitchID    string                `json:"switch_id,omitempty"`
}

func validAppUpdateResultState(state string) bool {
	return state == "pending" || state == "failed" || state == "partial" || state == "complete"
}

// Older helpers can install the package but fail to launch it. Only that
// specific failure may resume verification when the requested app is reopened.
func appUpdateRestartFailure(r appUpdateResult) bool {
	return r.State == "failed" && strings.HasPrefix(r.Problem, "App could not restart: ")
}

func writeAppUpdateResult(home, version, state, problem string) error {
	if !validAppUpdateResultState(state) {
		return errors.New("invalid app update result state")
	}
	r, err := readAppUpdateResult(home)
	if err != nil || r.Version != version {
		r = appUpdateResult{Version: version}
	}
	r.State, r.Problem = state, problem
	return saveAppUpdateResult(home, r)
}

func saveAppUpdateResult(home string, r appUpdateResult) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return secfile.Write(filepath.Join(home, appUpdateResultFile), b)
}

func readAppUpdateResult(home string) (appUpdateResult, error) {
	b, err := secfile.Read(filepath.Join(home, appUpdateResultFile))
	if err != nil {
		return appUpdateResult{}, err
	}
	text := strings.TrimSpace(string(b))
	if strings.HasPrefix(text, "{") {
		var result appUpdateResult
		if json.Unmarshal(b, &result) != nil || !validAppUpdateResultState(result.State) {
			return appUpdateResult{}, errors.New("app update result is unreadable")
		}
		return result, nil
	}
	if strings.HasPrefix(text, "Updated to ") {
		return appUpdateResult{Version: strings.TrimSpace(strings.TrimPrefix(text, "Updated to ")), State: "pending"}, nil
	}
	return appUpdateResult{State: "failed", Problem: text}, nil
}

func appUpdateResultText(home string) (string, error) {
	r, err := readAppUpdateResult(home)
	if err != nil {
		return "", err
	}
	return formatAppUpdateResult(r), nil
}

func formatAppUpdateResult(r appUpdateResult) string {
	var text string
	switch r.State {
	case "pending":
		text = "Update to " + r.Version + " pending verification."
	case "complete":
		text = "Updated app and CLI to " + r.Version + "."
	case "partial":
		text = "Update to " + r.Version + " incomplete."
	case "failed":
		text = "Update failed."
	}
	if r.Problem != "" {
		text += " " + r.Problem
	}
	return text
}

// Called only once the app is ready, with a fresh exact-copy command check.
func reconcileAppUpdateResult(home, runningVersion string, status appCommandStatus) error {
	r, err := projectedAppUpdateResult(home, runningVersion, status)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.State == "failed" {
		return nil
	}
	return saveAppUpdateResult(home, r)
}

// Projection reads activation proof without changing any update or command file.
func projectedAppUpdateResult(home, runningVersion string, status appCommandStatus) (appUpdateResult, error) {
	r, err := readAppUpdateResult(home)
	if err != nil {
		return r, err
	}
	if r.State == "failed" && (!appUpdateRestartFailure(r) || r.Version == "" || r.Version != runningVersion || status.State != "installed") {
		return r, nil
	}
	state, problem := "complete", ""
	if r.Version == "" || r.Version != runningVersion {
		state, problem = "partial", fmt.Sprintf("Running app version %s does not match requested version %s.", runningVersion, r.Version)
	} else if status.State != "installed" {
		state, problem = "partial", "CLI copies have not been verified."
		if status.Problem != "" {
			problem += " " + status.Problem
		}
	} else if r.Independent != nil {
		act, found, err := client.ReadUpdateActivation(home)
		if r.State == "partial" && r.SwitchID == "" && r.Problem != "" {
			state, problem = r.State, r.Problem
		} else if err != nil || !found || r.SwitchID == "" || act.ID != r.SwitchID {
			state, problem = "pending", "Waiting for the independently managed daemon to finish its active jobs and switch."
		} else if act.Result != client.ActivationRunning || act.To != r.Version || act.Running != r.Version || act.PID <= 0 {
			state, problem = "partial", "The independently managed daemon did not confirm the requested version. "+act.Detail
		}
	}
	r.State, r.Problem = state, problem
	return r, nil
}
