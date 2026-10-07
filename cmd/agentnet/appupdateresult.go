package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/misunders2d/agentnet/internal/secfile"
)

type appUpdateResult struct {
	Version string `json:"version"`
	State   string `json:"state"`
	Problem string `json:"problem,omitempty"`
}

func validAppUpdateResultState(state string) bool {
	return state == "pending" || state == "failed" || state == "partial" || state == "complete"
}

func writeAppUpdateResult(home, version, state, problem string) error {
	if !validAppUpdateResultState(state) {
		return errors.New("invalid app update result state")
	}
	b, err := json.Marshal(appUpdateResult{version, state, problem})
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
	return text, nil
}

// Called only once the app is ready, with a fresh exact-copy command check.
func reconcileAppUpdateResult(home, runningVersion string, status appCommandStatus) error {
	r, err := readAppUpdateResult(home)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.State == "failed" {
		return nil
	}
	state, problem := "complete", ""
	if r.Version == "" || r.Version != runningVersion {
		state, problem = "partial", fmt.Sprintf("Running app version %s does not match requested version %s.", runningVersion, r.Version)
	} else if status.State != "installed" {
		state, problem = "partial", "CLI copies have not been verified."
		if status.Problem != "" {
			problem += " " + status.Problem
		}
	}
	return writeAppUpdateResult(home, r.Version, state, problem)
}
