package main

import (
	"context"
	"net/http"
	"net/url"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/ui"
)

// startWorkspaceUI runs inside the existing default Agent.Run Owned callback.
// It never reacquires that daemon's home lock or substitutes its Agent.
func startWorkspaceUI(a *client.Agent, home, host, token string, skins string) (*ui.Server, http.Handler, func(), error) {
	registry, err := client.OpenWorkspaces(home)
	if err != nil {
		return nil, nil, nil, err
	}
	providers := ui.NewWorkspaceProviders()
	items, err := registry.List()
	if err != nil {
		return nil, nil, nil, err
	}
	live := ui.NewLive(a)
	page := ui.New(live, host, token, skins)
	for _, w := range items {
		if w.ID == client.DefaultWorkspace && w.State == "enrolled" {
			if _, err = providers.Bind(w, live); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime := ui.NewWorkspaceRuntime(ctx, registry, providers)
	runtime.Options = func(w client.Workspace) client.RunOptions {
		return client.RunOptions{
			OpenConv: func(conv string) []string {
				return workspacePageCommand(home, conv, w.ID)
			},
		}
	}
	if err = runtime.StartKnown(); err != nil {
		cancel()
		runtime.Close()
		return nil, nil, nil, err
	}
	return page, page.WorkspaceHandler(providers), func() { cancel(); runtime.Close() }, nil
}

func workspacePageCommand(home, conv, workspace string) []string {
	args := convPageCommand(home, conv)
	if len(args) == 0 {
		return nil
	}
	u, err := url.Parse(args[len(args)-1])
	if err != nil {
		return nil
	}
	u.Fragment = (url.Values{"workspace": {workspace}, "conv": {conv}}).Encode()
	args[len(args)-1] = u.String()
	return args
}
