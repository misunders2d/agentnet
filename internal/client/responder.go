package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// harness is how one installed coding agent is run headless, in its own
// background session. Question mode uses the recipient's own harness setup
// (skills, plugins, MCP servers, permissions): never blanket-disable skills
// or swap in an empty configuration. It only takes away what a question does
// not need (editing, new approvals); limits says exactly what it allows.
type harness struct {
	bin      string
	question []string     // the user's own setup; no editing, no new approvals
	task     []string     // the harness's normal permissions; nothing bypassed
	stdin    bool         // prompt on stdin; otherwise as the last argument
	out      string       // flag naming a file for the final answer; otherwise stdout
	limits   string       // what question mode allows, for the person choosing
	tested   string       // what was run live with the real harness (docs/revival/M4.md); empty: nothing
	sessions sessionStyle // how the worker keeps a background session per conversation (session.go)
}

// Harnesses lists the supported automatic responders. Flags were checked
// against each tool's --help; see docs/revival/M4.md for what was actually
// run. Other harnesses can still read and reply through the CLI by hand.
var Harnesses = map[string]harness{
	"claude": {
		bin: "claude",
		// The user's own settings, skills, plugins and MCP servers load as
		// usual; dontAsk runs only tools those settings already allow and
		// refuses the rest; file-editing tools are off for questions.
		question: []string{"-p", "--output-format", "text", "--no-session-persistence",
			"--permission-mode", "dontAsk", "--disallowedTools", "Edit,Write,NotebookEdit"},
		task:     []string{"-p", "--output-format", "text", "--no-session-persistence"},
		stdin:    true,
		tested:   "tasks tested live; the current question mode not yet",
		sessions: claudeSessions,
		limits: "claude questions use your Claude settings, skills, plugins and MCP servers; only tools your settings already allow run " +
			"(permission mode dontAsk: anything else is refused, never asked) and Edit, Write and NotebookEdit are off, " +
			"but Bash commands and MCP tools your settings allow keep whatever effects they have",
	},
	"codex": {
		bin: "codex",
		// The user's own config (skills, MCP servers with their own approval
		// modes) in a read-only sandbox; approval "never" refuses anything
		// that would need an approval.
		question: []string{"exec", "--ephemeral", "--sandbox", "read-only", "--skip-git-repo-check", "--color", "never",
			"-c", `approval_policy="never"`},
		task:     []string{"exec", "--ephemeral", "--skip-git-repo-check", "--color", "never"},
		stdin:    true,
		out:      "-o",
		tested:   "tasks tested live; the current question mode not yet",
		sessions: codexSessions,
		limits: "codex questions use your Codex config, skills and MCP servers; shell commands run in a read-only sandbox and anything that would need an approval is refused, " +
			"but MCP tools your config auto-approves are not covered by the sandbox and keep whatever effects they have",
	},
	"pi": {
		bin:      "pi",
		question: []string{"-p", "--no-session", "--tools", "read,grep,find,ls"},
		task:     []string{"-p", "--no-session"},
		limits:   "pi questions load your skills but may use only the read, grep, find and ls tools; skills that need bash, edit or extension tools cannot work there",
	},
}

// HarnessLimits describes how a responder's question mode differs from
// "no tools", or is empty when it has no tools.
func HarnessLimits(name string) string { return Harnesses[name].limits }

// HarnessInfo describes a supported responder as installed here. Found
// means only that its executable is on PATH: nothing is run to find out,
// so it says nothing about login or whether it works.
type HarnessInfo struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`          // empty when not found on PATH
	Tested string `json:"tested_live,omitempty"`   // empty: not tested live
	Limits string `json:"question_mode,omitempty"` // what questions may use
}

// ListHarnesses reports which supported responders are on PATH.
func ListHarnesses() []HarnessInfo {
	var out []HarnessInfo
	for _, name := range HarnessNames() {
		h := Harnesses[name]
		path, _ := exec.LookPath(h.bin)
		out = append(out, HarnessInfo{Name: name, Path: path, Tested: h.tested, Limits: h.limits})
	}
	return out
}

// HarnessNames lists supported responders.
func HarnessNames() []string {
	var out []string
	for k := range Harnesses {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SetResponder selects the default responder, or chooses manual handling
// (no automatic responder) with nil. Either way the choice is recorded, so
// setup does not ask again. The change applies to the next job; a running
// job keeps the responder it started with.
func (a *Agent) SetResponder(r *Responder) error {
	if r == nil {
		if err := a.store.deleteConfig("responder"); err != nil {
			return err
		}
		if err := a.store.setConfig(map[string]string{"responder_manual": "1"}); err != nil {
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
	if err := a.store.deleteConfig("responder_manual"); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
}

// ResponderChosen reports whether the local person has chosen how questions
// and tasks are handled: a responder, or manual handling.
func (a *Agent) ResponderChosen() (bool, error) {
	if r, err := a.Responder(); r != nil || err != nil {
		return r != nil, err
	}
	_, err := a.store.config("responder_manual")
	return err == nil, nil
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
