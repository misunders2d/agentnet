package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Responder is the recipient's choice of local harness for answering
// approved questions and running accepted tasks. It never comes from a
// message.
type Responder struct {
	Harness string        `json:"harness"`           // a key of Harnesses
	Dir     string        `json:"dir"`               // working directory; the harness's own instructions for it apply
	Context []string      `json:"context,omitempty"` // files whose text is given with every question
	Timeout time.Duration `json:"timeout"`           // wall-clock limit per question or task
}

// harness is how one installed coding agent is run headless, one-shot, in
// its own session. Question mode must hold what it claims: no tools, unless
// limits says precisely what it does instead.
type harness struct {
	bin      string
	question []string // no tools, no MCP servers, no persisted session
	task     []string // the harness's normal permissions; nothing bypassed
	stdin    bool     // prompt on stdin; otherwise as the last argument
	out      string   // flag naming a file for the final answer; otherwise stdout
	limits   string   // how question mode falls short of "no tools", if it does
}

// Harnesses lists the supported automatic responders. Flags were checked
// against each tool's --help; see docs/revival/M4.md for what was actually
// run. Other harnesses can still read and reply through the CLI by hand.
var Harnesses = map[string]harness{
	"claude": {
		bin: "claude",
		question: []string{"-p", "--output-format", "text", "--no-session-persistence",
			"--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--permission-mode", "dontAsk"},
		task:  []string{"-p", "--output-format", "text", "--no-session-persistence"},
		stdin: true,
	},
	"codex": {
		bin: "codex",
		question: []string{"exec", "--ephemeral", "--ignore-user-config", "--sandbox", "read-only",
			"--skip-git-repo-check", "--color", "never", "-c", `web_search="disabled"`,
			"--disable", "shell_tool", "--disable", "apps", "--disable", "plugins", "--disable", "browser_use",
			"--disable", "computer_use", "--disable", "image_generation", "--disable", "multi_agent",
			"--disable", "memories", "--disable", "hooks", "--disable", "skill_search"},
		task:  []string{"exec", "--ephemeral", "--skip-git-repo-check", "--color", "never"},
		stdin: true,
		out:   "-o",
		limits: "codex questions run restricted, not tool-free: read-only sandbox, no user config (so no configured MCP servers), " +
			"web search, shell, apps, plugins, browser, computer use, image generation, sub-agents, memories, hooks and skill search off; " +
			"Codex has no switch that removes every built-in tool, so it may still read files",
	},
	"pi": {
		bin:      "pi",
		question: []string{"-p", "--no-session", "--no-tools"},
		task:     []string{"-p", "--no-session"},
	},
}

// HarnessLimits describes how a responder's question mode differs from
// "no tools", or is empty when it has no tools.
func HarnessLimits(name string) string { return Harnesses[name].limits }

// HarnessNames lists supported responders.
func HarnessNames() []string {
	var out []string
	for k := range Harnesses {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SetResponder selects the default responder, or turns automatic handling
// off with nil. The change applies to the next job; a running job keeps the
// responder it started with.
func (a *Agent) SetResponder(r *Responder) error {
	if r == nil {
		if err := a.store.deleteConfig("responder"); err != nil {
			return err
		}
		notifyDaemon(a.home)
		return nil
	}
	if _, ok := Harnesses[r.Harness]; !ok {
		return fmt.Errorf("unsupported responder %q (supported: %v)", r.Harness, HarnessNames())
	}
	dir, err := filepath.Abs(r.Dir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("responder directory %s is not a directory", dir)
	}
	r.Dir = dir
	for i, c := range r.Context {
		if r.Context[i], err = filepath.Abs(c); err != nil {
			return err
		}
		if _, err := os.Stat(r.Context[i]); err != nil {
			return err
		}
	}
	if r.Timeout <= 0 {
		r.Timeout = 5 * time.Minute
	}
	data, _ := json.Marshal(r)
	if err := a.store.setConfig(map[string]string{"responder": string(data)}); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
}

// Responder returns the selected responder, or nil when none is selected.
func (a *Agent) Responder() (*Responder, error) {
	v, err := a.store.config("responder")
	if err != nil {
		return nil, nil // not configured
	}
	var r Responder
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return nil, errors.New("corrupt responder configuration")
	}
	return &r, nil
}
