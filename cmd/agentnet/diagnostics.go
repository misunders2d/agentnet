package main

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/misunders2d/agentnet/internal/client"
)

var readBuildInfo = debug.ReadBuildInfo

func runWhoami(ctx context.Context, a *client.Agent, w io.Writer) error {
	_, err := fmt.Fprintf(w, "%s\nfingerprint %s\nhub role %s\n", a.Address, a.Self().Fingerprint(), hubRoleDescription(ctx, a))
	return err
}

func hubRoleDescription(ctx context.Context, a *client.Agent) string {
	role, err := a.HubRole(ctx)
	if err != nil {
		return "unknown (could not verify with the Hub)"
	}
	switch role {
	case "admin", "member":
		return role + " (reported by the Hub)"
	default:
		return "unknown (Hub did not report a recognized role)"
	}
}

// Build metadata identifies an ordinary source build without changing the
// release stamp or the version/schema lines consumed by the updater.
func currentBuildDescription() string {
	info, ok := readBuildInfo()
	if !ok {
		info = nil
	}
	return buildDescription(info)
}

func buildDescription(info *debug.BuildInfo) string {
	revision, tree := "unknown", "unknown"
	if info != nil {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if setting.Value != "" {
					revision = setting.Value
				}
			case "vcs.modified":
				switch setting.Value {
				case "true":
					tree = "dirty"
				case "false":
					tree = "clean"
				}
			}
		}
	}
	return fmt.Sprintf("build revision %s, working tree %s", revision, tree)
}
