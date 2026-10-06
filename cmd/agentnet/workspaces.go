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
//
// openPage, in the AgentNet app (app.go), opens the app's window on a
// notification's destination; nil keeps the browser page command.
func startWorkspaceUI(a *client.Agent, home, host, token string, skins string, openPage ...func(workspace string) func(fragment string) []string) (*ui.Server, http.Handler, func(), error) {
	registry, err := client.OpenWorkspaces(home)
	if err != nil {
		return nil, nil, nil, err
	}
	providers := ui.NewWorkspaceProviders()
	items, err := registry.List()
	if err != nil {
		return nil, nil, nil, err
	}
	inApp := len(openPage) > 0 && openPage[0] != nil
	live := ui.NewLive(a)
	live.SetAssistantSetup(newAssistantSetup(home, a))
	live.SetApp(inApp)
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
	runtime.ConfigureLive = func(l *ui.Live, agent *client.Agent, workspace client.Workspace) {
		if workspaceHome, e := registry.Home(workspace.ID); e == nil {
			l.SetAssistantSetup(newAssistantSetup(workspaceHome, agent))
		}
		l.SetApp(inApp)
	}
	runtime.Options = func(w client.Workspace) client.RunOptions {
		if len(openPage) > 0 && openPage[0] != nil {
			return client.RunOptions{OpenPage: openPage[0](w.ID)}
		}
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
