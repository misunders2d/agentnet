package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

// harness starts a separate background session with the owner's native setup.
// Questions and tasks inherit the same tools, skills, sandbox and approval
// policy. AgentNet admits requests; the native harness decides permitted effects.
type harness struct {
	bin      string
	question []string     // the user's own setup and native permissions
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
const nativeHarnessPermissions = "Questions and accepted tasks use your native settings, skills, plugins, tools and permissions unchanged. AgentNet adds no tool exclusions, sandbox or approval overrides. If your agent needs a native approval that a background session cannot obtain, the request needs your attention. Your open sessions remain untouched."

var Harnesses = map[string]harness{
	"agy": {
		bin: "agy", question: []string{"--input-format", "stream-json", "--output-format", "stream-json"},
		task:  []string{"--input-format", "stream-json", "--output-format", "stream-json"},
		stdin: true, sessions: agySessions, addDir: "--add-dir", addIn: true, limits: nativeHarnessPermissions + " Received files and task output folders are added with --add-dir; your setup decides access.",
	},
	"claude": {
		bin: "claude", question: []string{"-p", "--output-format", "text", "--no-session-persistence"},
		task:  []string{"-p", "--output-format", "text", "--no-session-persistence"},
		stdin: true, sessions: claudeSessions, addDir: "--add-dir", addIn: true, limits: nativeHarnessPermissions + " Received files and task output folders are added with --add-dir; your setup decides access.",
	},
	"codex": {
		bin: "codex", question: []string{"exec", "--ephemeral", "--skip-git-repo-check", "--color", "never"},
		task:  []string{"exec", "--ephemeral", "--skip-git-repo-check", "--color", "never"},
		stdin: true, out: "-o", sessions: codexSessions, addDir: "--add-dir", limits: nativeHarnessPermissions + " A device task gets one extra writable folder, its outbox, through --add-dir. AgentNet never passes --sandbox or a bypass; a resumed session, where codex cannot add a folder, gets no outbox.",
	},
	"omp": {
		bin: "omp", question: []string{"-p", "--no-session"}, task: []string{"-p", "--no-session"}, stdin: true, limits: nativeHarnessPermissions,
	},
	"pi": {
		bin: "pi", question: []string{"-p", "--no-session"}, task: []string{"-p", "--no-session"}, stdin: true, limits: nativeHarnessPermissions + " File paths are supplied in context; no folder is added for Pi.",
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
	if err := checkOMPResponderSetup(r); err != nil {
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

// This capability check runs only for an explicit local activation/change,
// never on discovery, status, claim or each question. It invokes the chosen
// launcher without a prompt; the launcher's own behavior remains its owner's.
func checkOMPResponderSetup(r *Responder) error {
	if r.Harness != "omp" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, Harnesses["omp"].bin, "--help")
	cmd.Dir = r.Dir
	var out, stderr limitedBuffer
	out.max, stderr.max = maxOutput, 4<<10
	cmd.Stdout, cmd.Stderr = &out, &stderr
	ownProcessGroup(cmd)
	defer stopGroup(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("OMP setup unavailable: chosen launcher --help failed; native background invocation support is required (%w)", err)
	}
	if out.truncated {
		return errors.New("OMP setup unavailable: native capability output exceeded the inspection limit")
	}
	for _, flag := range []string{"--print", "--no-session", "--extension"} {
		if !strings.Contains(out.String(), flag) {
			return fmt.Errorf("OMP setup unsupported: chosen launcher does not advertise %s; native OMP 18.4.8/18.7.0 contract is required", flag)
		}
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

// AdvertisesAgent reports whether this device runs an agent people may
// ask: a default responder, or a named agent with a responder. It is what
// the agent hint (protocol.CapAgent) says; it grants nothing.
func (a *Agent) AdvertisesAgent() bool {
	if r, _ := a.Responder(); r != nil {
		return true
	}
	entries, _ := a.LocalAgents()
	for _, e := range entries {
		if e.Responder != nil {
			return true
		}
	}
	return false
}

// Responder returns the selected responder, or nil when none is selected.
func (a *Agent) Responder() (*Responder, error) {
	return responderIn(a.store.db)
}

func responderIn(q dbq) (*Responder, error) {
	var v string
	err := q.QueryRow(`SELECT v FROM config WHERE k='responder'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // not configured
	}
	if err != nil {
		return nil, err
	}
	var r Responder
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return nil, errors.New("corrupt responder configuration")
	}
	return &r, nil
}
