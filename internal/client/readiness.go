package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const readinessLimit = 128 << 10
const readinessTimeout = 2 * time.Second

// AgentReadiness describes launch prerequisites in the responding daemon's
// environment. Available never means authenticated or a successful model run.
type AgentReadiness struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Harness   string `json:"harness"`
	Enabled   bool   `json:"enabled"`
	Default   bool   `json:"default"`
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`
}

// DaemonReadiness is a fresh, read-only observation from the process holding
// this home's daemon lock. It contains no environment, credentials or messages.
type DaemonReadiness struct {
	Address     string           `json:"address"`
	Fingerprint string           `json:"fingerprint"`
	Version     string           `json:"version"`
	PID         int              `json:"pid"`
	Connected   bool             `json:"connected"`
	HumanOnly   bool             `json:"human_only"`
	Agents      []AgentReadiness `json:"agents"`
}

func readinessSockPath(home string) string {
	return homeSockPath(home, "readiness.sock", ".readiness.sock")
}

func daemonRunning(home string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(home, "daemon.lock")); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock"))
	if err == nil {
		release()
		return false, nil
	}
	if errors.Is(err, lockfile.ErrLocked) {
		return true, nil
	}
	return false, err
}

func (a *Agent) readiness(humanOnly bool) (DaemonReadiness, error) {
	entries, err := a.LocalAgents()
	if err != nil {
		return DaemonReadiness{}, err
	}
	defaultID, err := a.DefaultAgentID()
	if err != nil {
		return DaemonReadiness{}, err
	}
	v := DaemonReadiness{Address: a.Address, Fingerprint: a.Self().Fingerprint(), Version: protocol.Version,
		PID: os.Getpid(), Connected: a.streamConnected.Load(), HumanOnly: humanOnly, Agents: []AgentReadiness{}}
	for _, entry := range entries {
		r := AgentReadiness{ID: entry.Record.ID, Label: entry.Record.Label, Harness: entry.Record.Harness,
			Enabled: entry.Responder != nil, Default: entry.Record.ID == defaultID}
		switch {
		case entry.Responder == nil:
			r.Problem = "disabled"
		case humanOnly:
			r.Problem = "this daemon receives messages only; it does not run agents"
		default:
			r.Harness = entry.Responder.Harness
			h, ok := Harnesses[r.Harness]
			if !ok {
				r.Problem = "unsupported harness"
			} else if _, err := resolveNativeLaunch(h.bin); err != nil {
				r.Problem = "harness is not available in the daemon's launch environment"
			} else if info, err := os.Stat(entry.Responder.Dir); err != nil || !info.IsDir() {
				r.Problem = "responder directory is unavailable"
			} else {
				r.Available = true
			}
		}
		v.Agents = append(v.Agents, r)
	}
	return v, nil
}

// listenReadiness uses the same lock-scoped local lifetime as the existing
// sockets, without changing their byte-only protocols. A connection reads one
// bounded snapshot; the listener accepts no commands and cannot start work.
func (a *Agent) listenReadiness(humanOnly bool) (func(), error) {
	path := readinessSockPath(a.home)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		os.Remove(path)
		return nil, err
	}
	var mu sync.Mutex
	var active net.Conn
	stopped := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if stopped {
				mu.Unlock()
				c.Close()
				return
			}
			active = c
			mu.Unlock()
			c.SetWriteDeadline(time.Now().Add(readinessTimeout))
			if v, err := a.readiness(humanOnly); err == nil {
				if data, err := json.Marshal(v); err == nil && len(data) <= readinessLimit {
					c.Write(data)
				}
			}
			c.Close()
			mu.Lock()
			active = nil
			mu.Unlock()
		}
	}()
	return func() {
		mu.Lock()
		stopped = true
		if active != nil {
			active.Close()
		}
		mu.Unlock()
		ln.Close()
		<-done
		os.Remove(path)
	}, nil
}

// ReadDaemonReadiness asks only this home's current daemon. A stopped daemon,
// an older daemon without diagnostics, and a lock/filesystem failure remain
// distinct; it never starts a process, contacts the Hub or wakes the worker.
func (a *Agent) ReadDaemonReadiness(ctx context.Context) (DaemonReadiness, error) {
	return readDaemonReadiness(ctx, a.home, a.Address, a.Self().Fingerprint())
}

// ReadHomeDaemonReadiness bypasses Open: diagnostics must not migrate a
// stopped/older home's database, repair its catalog or recover its history.
func ReadHomeDaemonReadiness(ctx context.Context, home string) (DaemonReadiness, error) {
	if running, err := daemonRunning(home); err != nil {
		return DaemonReadiness{}, fmt.Errorf("cannot inspect daemon ownership: %w", err)
	} else if !running {
		return DaemonReadiness{}, ErrDaemonNotRunning
	}
	id, err := identity.Load(filepath.Join(home, "identity.json"))
	if err != nil {
		return DaemonReadiness{}, err
	}
	return readDaemonReadiness(ctx, home, "", id.Public("").Fingerprint())
}

func readDaemonReadiness(ctx context.Context, home, address, fingerprint string) (DaemonReadiness, error) {
	running, err := daemonRunning(home)
	if err != nil {
		return DaemonReadiness{}, fmt.Errorf("cannot inspect daemon ownership: %w", err)
	}
	if !running {
		return DaemonReadiness{}, ErrDaemonNotRunning
	}
	ctx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", readinessSockPath(home))
	if err != nil {
		return DaemonReadiness{}, errors.New("daemon lock is held, but its readiness is unavailable; it may still be starting or need an update")
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	c.SetReadDeadline(deadline)
	data, err := io.ReadAll(io.LimitReader(c, readinessLimit+1))
	if err != nil {
		return DaemonReadiness{}, fmt.Errorf("daemon readiness response interrupted: %w", err)
	}
	var v DaemonReadiness
	if len(data) > readinessLimit || json.Unmarshal(data, &v) != nil || address != "" && v.Address != address ||
		v.Fingerprint != fingerprint || v.PID <= 0 || len(v.Agents) > protocol.MaxAgentCatalog {
		return DaemonReadiness{}, errors.New("invalid daemon readiness response")
	}
	if _, _, err := protocol.SplitAddress(v.Address); err != nil {
		return DaemonReadiness{}, errors.New("invalid daemon readiness address")
	}
	return v, nil
}
