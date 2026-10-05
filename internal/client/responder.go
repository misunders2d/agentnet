package client

import (
	"database/sql"
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
	Timeout time.Duration `json:"timeout"`           // the person's own wall-clock limit per question or task; 0: none
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
	addDir   string       // flag adding a run's folder for the harness to use (runfiles.go); empty: none
	addIn    bool         // a run's in/ is added too, not only a task's out/
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
		tested:   "tasks and a skill-backed question tested live",
		sessions: claudeSessions,
		addDir:   "--add-dir",
		addIn:    true,
		limits: "claude questions use your Claude settings, skills, plugins and MCP servers; only tools your settings already allow run " +
			"(permission mode dontAsk: anything else is refused, never asked) and Edit, Write and NotebookEdit are off, " +
			"but Bash commands and MCP tools your settings allow keep whatever effects they have; " +
			"questions may also run fixed read-only AgentNet lookups of this device (version, whoami, inbox without marking read, approvals, status of a message this device sent) " +
			"through the exact installed agentnet program, as exact allow rules your own deny and ask rules still override (none when the program's path would need shell quoting); " +
			"files a question or task receives are read-only copies, under names AgentNet chooses, in a run folder added with --add-dir, " +
			"and a device task's outbox folder is added the same way (your settings decide whether Claude may write there)",
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
		tested:   "tasks and a skill-backed question tested live",
		sessions: codexSessions,
		addDir:   "--add-dir",
		limits: "codex questions use your Codex config, skills and MCP servers; shell commands run in a read-only sandbox and anything that would need an approval is refused, " +
			"but MCP tools your config auto-approves are not covered by the sandbox and keep whatever effects they have; " +
			"questions are told the read-only AgentNet lookups of this device, which run inside that sandbox (status shows the local record without network); " +
			"files a question or task receives are read-only copies it is told the paths of, read as your sandbox allows; " +
			"a device task run gets one extra writable folder, its outbox, through --add-dir (which codex documents as writable alongside the workspace); " +
			"that folder is the only change to your sandbox: AgentNet never passes --sandbox or a bypass to a task; " +
			"a resumed session, where codex cannot add a folder, gets no outbox and is not told of one",
	},
	"pi": {
		bin: "pi",
		// The user's own Pi setup: settings (defaultTools), skills and
		// extensions with their tools load as usual; --exclude-tools removes
		// only the built-ins that change the machine. Pi 0.87.1 has no
		// unattended permission or read-only mode for its shell, so bash and
		// powershell are off too (an allowlist would also switch every
		// extension tool off, which is not the recipient's setup).
		question: []string{"-p", "--no-session", "--exclude-tools", "bash,edit,write,powershell"},
		task:     []string{"-p", "--no-session"},
		// The request on stdin, never in its arguments, which any local
		// user may read (/proc/PID/cmdline): Pi 0.87.1 reads a piped stdin
		// as its first message (dist/main.js readPipedStdin).
		stdin: true,
		limits: "pi questions use your Pi settings, skills and extensions with their tools; only bash, edit, write and powershell are off " +
			"(Pi cannot run its shell read-only or ask), and extension tools keep whatever effects your setup gives them; " +
			"an AgentNet lookup tool runs only the installed agentnet program's fixed read-only lookups (version, whoami, inbox without marking read, approvals, status of a message this device sent); " +
			"files a question or task receives are read-only copies it is told the paths of, and a device task is told its outbox folder; no folder is added for Pi, whose own tools decide",
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
	if err := validateResponder(r); err != nil {
		return err
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

// oldDefaultLimit is the time limit earlier builds stored for every
// responder and named agent saved without one. It was never the person's
// choice, and AgentNet sets no limit on agent work, so clearOldDefaultLimit
// removes it once per home: a stored value equal to it cannot be told
// apart from that default. A person who wants exactly five minutes sets it
// again.
const (
	oldDefaultLimit     = 5 * time.Minute
	oldDefaultLimitGone = "old_default_limit_cleared" // config: set once the stored defaults were cleared
)

// clearOldDefaultLimit clears, once per home (Open), a stored time limit
// equal to oldDefaultLimit on the default responder and on every named
// agent of the local catalog.
func (s *store) clearOldDefaultLimit() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int
	if err := tx.QueryRow(`SELECT count(*) FROM config WHERE k = ?`, oldDefaultLimitGone).Scan(&done); err != nil || done > 0 {
		return err
	}
	var raw string
	switch err := tx.QueryRow(`SELECT v FROM config WHERE k = 'responder'`).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		var r Responder
		if json.Unmarshal([]byte(raw), &r) == nil && r.Timeout == oldDefaultLimit {
			r.Timeout = 0
			data, _ := json.Marshal(r)
			if _, err := tx.Exec(`UPDATE config SET v = ? WHERE k = 'responder'`, string(data)); err != nil {
				return err
			}
		}
	}
	switch err := tx.QueryRow(`SELECT v FROM config WHERE k = ?`, agentCatalogConfig).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		var entries []LocalAgentInfo
		changed := false
		if decodeStrict([]byte(raw), &entries) == nil {
			for i := range entries {
				if r := entries[i].Responder; r != nil && r.Timeout == oldDefaultLimit {
					r.Timeout, changed = 0, true
				}
			}
		}
		if changed {
			data, _ := json.Marshal(entries)
			if _, err := tx.Exec(`UPDATE config SET v = ? WHERE k = ?`, string(data), agentCatalogConfig); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO config(k, v) VALUES(?, '1')`, oldDefaultLimitGone); err != nil {
		return err
	}
	return tx.Commit()
}

// validateResponder is shared by the default and host-local named agents.
// It validates/normalizes local configuration only, never a received choice.
func validateResponder(r *Responder) error {
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
	// No platform limit on agent work: a run takes as long as it takes
	// unless the person set a limit of their own (Timeout > 0).
	if r.Timeout < 0 {
		r.Timeout = 0
	}
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
