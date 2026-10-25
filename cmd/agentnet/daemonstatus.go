package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/misunders2d/agentnet/internal/client"
)

func runDaemonStatus(ctx context.Context, home string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "return daemon and caller readiness separately as JSON")
	agentID := fs.String("agent", "", "inspect this exact configured agent ID")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("usage: daemon status [--agent ID] [--json]")
	}
	result := struct {
		State   string                  `json:"state"`
		Daemon  *client.DaemonReadiness `json:"daemon,omitempty"`
		Caller  []client.HarnessInfo    `json:"caller_harnesses"`
		Problem string                  `json:"problem,omitempty"`
	}{State: "unknown", Caller: client.ListHarnesses()}
	v, err := client.ReadHomeDaemonReadiness(ctx, home)
	switch {
	case errors.Is(err, client.ErrDaemonNotRunning):
		result.State, result.Problem = "stopped", "Start the existing service, or run agentnet daemon start in the foreground."
	case err != nil:
		result.Problem = err.Error()
	default:
		result.State, result.Daemon = "reconnecting", &v
		if v.Connected {
			result.State = "connected"
		}
		if *agentID != "" {
			found := false
			for _, agent := range v.Agents {
				if agent.ID == *agentID {
					result.Daemon.Agents = []client.AgentReadiness{agent}
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("agent %s is not configured on this daemon", *agentID)
			}
		}
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(result)
	}
	fmt.Fprintf(out, "Daemon: %s\n", result.State)
	if result.Problem != "" {
		fmt.Fprintln(out, result.Problem)
	}
	if d := result.Daemon; d != nil {
		fmt.Fprintf(out, "Host: %s (process %d, AgentNet %s)\n", d.Address, d.PID, d.Version)
		for _, agent := range d.Agents {
			state := "available to launch"
			if !agent.Available {
				state = agent.Problem
			}
			def := ""
			if agent.Default {
				def = "; default"
			}
			fmt.Fprintf(out, "  %s  %s (%s%s): %s\n", agent.ID, agent.Label, agent.Harness, def, state)
		}
		fmt.Fprintln(out, "Daemon availability checks executable and directory only; model authentication and successful execution remain unverified.")
	}
	fmt.Fprintln(out, "Caller shell (separate from the daemon):")
	for _, h := range result.Caller {
		state := "not found"
		if h.Path != "" {
			state = h.Path
		}
		fmt.Fprintf(out, "  %s: %s\n", h.Name, state)
	}
	return nil
}
