package main

import (
	"bytes"
	"context"
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

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

var appUpdateStartTimeout = appStartTimeout

// Reuse the app's authenticated owner-only loopback endpoint. Never replace
// one component of a bundle, follow redirects, or send its cookie elsewhere.
func requestAppUpdate(ctx context.Context, home string, check bool, tag string) error {
	err := requestAppUpdateOnce(ctx, home, check, tag)
	if !errors.Is(err, errAppUpdateNotReady) || check {
		return err
	}
	opened, startErr := openApp(home)
	if startErr != nil {
		return startErr
	}
	if !opened {
		return err
	}
	fmt.Println("Opening the installed AgentNet app for the whole-app update…")
	// Bounded local startup readiness, not a daemon or relay polling loop.
	wait, cancel := context.WithTimeout(ctx, appUpdateStartTimeout)
	defer cancel()
	for {
		select {
		case <-wait.Done():
			return fmt.Errorf("installed app did not become ready: %w", wait.Err())
		case <-time.After(200 * time.Millisecond):
		}
		// The readiness deadline gates retries, not an accepted download or
		// installer handoff. Those retain the caller's update context.
		err = requestAppUpdateOnce(ctx, home, false, tag)
		if !errors.Is(err, errAppUpdateNotReady) {
			return err
		}
	}
}

var errAppUpdateNotReady = errors.New("cannot reach the installed AgentNet app for this home; no standalone-only update was performed")

func requestAppUpdateOnce(ctx context.Context, home string, check bool, tag string) error {
	if tag != "" {
		if _, ok := parseRelease(tag); !ok {
			return errors.New("invalid release (vX.Y.Z)")
		}
	}
	data, err := secfile.Read(filepath.Join(home, uiURLFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errAppUpdateNotReady
		}
		return fmt.Errorf("cannot read the app endpoint: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(string(data)))
	if err != nil || u.Scheme != "http" || u.User != nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" || u.Query().Get("t") == "" {
		return errors.New("no valid local AgentNet app endpoint; open the installed app for this home and retry")
	}
	token := u.Query().Get("t")
	u.RawQuery, u.Fragment = "", ""
	method := http.MethodPost
	u.Path = "/api/app/update"
	body, _ := json.Marshal(map[string]string{"version": tag})
	if check {
		method = http.MethodGet
		u.Path = "/api/app/status"
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: token})
	req.Header.Set("Origin", "http://"+u.Host)
	req.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	c := &http.Client{Transport: transport, Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return errAppUpdateNotReady
		}
		// An ambiguous POST failure may have started the update. Never retry it.
		return errors.New("app update response was interrupted; check agentnet update --status before retrying")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil || len(b) > 8192 {
		return errors.New("invalid app updater response")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("app update refused (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if check {
		var status struct {
			Supported  bool   `json:"app_update_supported"`
			Version    string `json:"version"`
			Problem    string `json:"problem"`
			CLIState   string `json:"cli_state"`
			CLIProblem string `json:"cli_problem"`
		}
		if json.Unmarshal(b, &status) != nil || !status.Supported {
			return fmt.Errorf("whole-app update unavailable: %s", status.Problem)
		}
		if tag == "" {
			tag, err = latestRelease(ctx)
			if err != nil {
				return err
			}
		}
		current := status.Version
		if current == "" {
			current = protocol.Version
		}
		fmt.Printf("AgentNet app: running %s, selected release %s; invoking command %s.\n", current, tag, protocol.Version)
		if status.CLIState == "installed" {
			fmt.Println("App-managed commands match the app; agentnet update includes the invoking official command too.")
		} else {
			fmt.Printf("Terminal command needs reconciliation: %s; agentnet update checks and repairs official copies.\n", status.CLIProblem)
		}
	} else {
		var result struct {
			State   string `json:"state"`
			Version string `json:"version"`
		}
		if json.Unmarshal(b, &result) != nil {
			return errors.New("invalid app updater acknowledgement")
		}
		if result.State == "complete" {
			if _, ok := parseRelease(result.Version); !ok || (tag != "" && result.Version != tag) {
				return errors.New("app updater did not verify the requested version")
			}
			fmt.Printf("App and terminal command verified at %s.\n", result.Version)
			return nil
		}
		if result.State != "restarting" {
			return errors.New("the local page did not acknowledge a whole-app update; open the installed AgentNet app and retry")
		}
		fmt.Println("Whole-app update requested. The app restarts and verifies its terminal command; agentnet update --status shows completion.")
	}
	return nil
}

// The desktop helper's result is distinct from a standalone daemon switch.
func appUpdateStatus(home string) error {
	result, err := appUpdateResultText(home)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println("No whole-app update result recorded. About shows the running app version; agentnet update --check checks the updater.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("Last whole-app update result: %s\n", result)
	return nil
}
