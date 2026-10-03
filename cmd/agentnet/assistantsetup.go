package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/ui"
)

// Serialize this UI's review/apply boundary across workspace providers. File
// content is rechecked before apply; no durable installer or new state store.
var assistantSetupMu sync.Mutex
var setupHarnesses = []string{"codex", "claude", "pi", "omp"}

type setupPlan struct {
	row           ui.AssistantSetupHarness
	before, after []byte
}
type assistantSetupController struct {
	home     string
	sessions func() ([]client.ReplySessionView, error)
	install  func(string, string) error
}

func newAssistantSetup(home string, a *client.Agent) ui.AssistantSetupFunc {
	c := &assistantSetupController{home: home, sessions: a.ReplySessions}
	c.install = func(harness, file string) error { return runHooks(home, []string{"install", harness, "--file", file}) }
	return c.run
}

// OMP's canonical extension folder follows its native config/profile model;
// no executable is run for discovery (wrappers can install/update software).
// Source: can1357/oh-my-pi packages/utils/src/dirs.ts and config.ts.
func ompSetupPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	profile, exists := os.LookupEnv("OMP_PROFILE")
	if !exists {
		profile = os.Getenv("PI_PROFILE")
	}
	profile = strings.TrimSpace(profile)
	root := os.Getenv("PI_CONFIG_DIR")
	if root == "" {
		root = ".omp"
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(home, root)
	}
	dir := filepath.Join(root, "agent")
	if profile != "" && profile != "default" {
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`).MatchString(profile) || profile == "." || profile == ".." || strings.HasSuffix(profile, ".") {
			return "", errors.New("invalid OMP profile")
		}
		dir = filepath.Join(root, "profiles", profile, "agent")
	} else if override := os.Getenv("PI_CODING_AGENT_DIR"); override != "" {
		dir = override
	}
	return filepath.Abs(filepath.Join(dir, "extensions", "agentnet.ts"))
}
func setupTarget(harness string) (string, error) {
	switch harness {
	case "pi":
		return piExtensionPath()
	case "omp":
		return ompSetupPath()
	default:
		return hookConfigPath(harness)
	}
}
func (c *assistantSetupController) inspect(harness string, sessions []client.ReplySessionView) setupPlan {
	labels := map[string]string{"codex": "Codex", "claude": "Claude", "pi": "Pi", "omp": "OMP"}
	row := ui.AssistantSetupHarness{ID: harness, Label: labels[harness], Supported: runtime.GOOS != "windows", State: "not_detected", Note: "Not found on this computer's AgentNet PATH."}
	if _, err := exec.LookPath(harness); err == nil {
		row.Detected = true
		row.State = "detected"
		row.Note = "Installed; AgentNet integration is not configured."
	}
	plan := setupPlan{row: row}
	if !row.Supported {
		plan.row.State = "unsupported"
		plan.row.Note = "Native hook installation is not supported on Windows yet. No software was changed."
		return plan
	}
	file, err := setupTarget(harness)
	if err != nil {
		plan.row.State = "error"
		plan.row.Note = "Cannot resolve this tool's configuration directory. Check its native profile settings."
		plan.row.Supported = false
		return plan
	}
	file, err = filepath.Abs(file)
	if err != nil {
		plan.row.State = "error"
		plan.row.Note = "Configuration directory is unavailable."
		plan.row.Supported = false
		return plan
	}
	plan.row.Target = file
	info, statErr := os.Lstat(file)
	if statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 4<<20) {
		plan.row.State = "error"
		plan.row.Note = "Configuration is a link, not a regular file, or too large. Review it with the native CLI; nothing changed."
		plan.row.Supported = false
		return plan
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		plan.row.State = "error"
		plan.row.Note = "Existing configuration could not be inspected; nothing changed."
		plan.row.Supported = false
		return plan
	}
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		plan.row.State = "error"
		plan.row.Note = "Existing configuration could not be read; nothing changed."
		plan.row.Supported = false
		return plan
	}
	plan.before = old
	err = nil // an absent file is a valid fresh setup, not a merge failure
	if harness == "pi" || harness == "omp" {
		if statErr == nil && !bytes.HasPrefix(old, piMarker) {
			plan.row.State = "error"
			plan.row.Note = "An unowned file occupies AgentNet's extension path. It will not be replaced."
			plan.row.Supported = false
			return plan
		}
		plan.after, err = renderNativeExtension(c.home, harness)
	} else {
		var config map[string]any
		if len(bytes.TrimSpace(old)) > 0 {
			err = json.Unmarshal(old, &config)
		}
		if err == nil {
			var command string
			command, err = hookCommand(c.home, harness)
			if err == nil {
				config, err = mergeHooks(config, harness, command)
			}
			if err == nil {
				plan.after, err = json.MarshalIndent(config, "", "  ")
				plan.after = append(plan.after, '\n')
			}
		}
	}
	if err != nil {
		plan.row.State = "error"
		plan.row.Note = "Existing configuration cannot be safely merged. Review it with the native CLI; nothing changed."
		plan.row.Supported = false
		return plan
	}
	plan.row.Configured = bytes.Equal(plan.before, plan.after)
	for _, s := range sessions {
		if s.Harness == harness && s.Active {
			plan.row.Registered = true
		}
	}
	if plan.row.Configured {
		plan.row.State = "needs_activation"
		plan.row.Note = "Integration configured. Start a new native session to activate it."
		if plan.row.Registered {
			plan.row.State = "connected"
			plan.row.Note = "Integration configured; a native context is registered. Registration is not a delivery or sign-in test."
		}
	} else if len(old) > 0 && row.Detected {
		plan.row.State = "needs_setup"
		plan.row.Note = "Installed. AgentNet integration needs configuration or repair."
	}
	// A removed executable must not appear connected merely because old hooks
	// or a registration remain. Retain all history/config; do not auto-remove.
	if !plan.row.Detected {
		plan.row.State = "not_detected"
		plan.row.Note = "Tool not found on AgentNet's PATH. Existing configuration and history are retained."
	}
	plan.row.Change = "Add or repair AgentNet integration; unrelated settings are preserved."
	if plan.row.Configured {
		plan.row.Change = "Keep existing integration; no file change."
	}
	plan.row.Next = "Start a new " + labels[harness] + " session. Existing sessions are not changed."
	if harness == "codex" {
		plan.row.Next = "In Codex, open /hooks and review/trust AgentNet hooks, then start a new session. Native trust is not granted by this setup."
	}
	return plan
}
func (c *assistantSetupController) run(ctx context.Context, request ui.AssistantSetupRequest) (ui.AssistantSetupView, error) {
	assistantSetupMu.Lock()
	defer assistantSetupMu.Unlock()
	view := ui.AssistantSetupView{Local: true, Harnesses: []ui.AssistantSetupHarness{}}
	if err := ctx.Err(); err != nil {
		return view, err
	}
	var sessions []client.ReplySessionView
	if c.sessions != nil {
		var err error
		sessions, err = c.sessions()
		if err != nil {
			return view, ui.Refuse("Native context registration could not be read. Retry setup; nothing changed.")
		}
	}
	plans := map[string]setupPlan{}
	for _, name := range setupHarnesses {
		p := c.inspect(name, sessions)
		plans[name] = p
		view.Harnesses = append(view.Harnesses, p.row)
	}
	if request.Action == "" {
		return view, nil
	}
	if request.Action != "review" && request.Action != "apply" {
		return view, ui.Refuse("Unknown setup action.")
	}
	if len(request.Harnesses) == 0 || len(request.Harnesses) > len(setupHarnesses) {
		return view, ui.Refuse("Choose at least one detected tool.")
	}
	chosen := map[string]bool{}
	paths := map[string]bool{}
	digest := sha256.New()
	// Canonical order makes selecting the same subset in another order idempotent.
	for _, name := range request.Harnesses {
		p, ok := plans[name]
		if !ok || chosen[name] || !p.row.Detected || !p.row.Supported {
			return view, ui.Refuse("A selected tool is missing or cannot be configured safely. Check the tool list and review again.")
		}
		chosen[name] = true
	}
	for _, name := range setupHarnesses {
		if !chosen[name] {
			continue
		}
		p := plans[name]
		if paths[p.row.Target] {
			return view, ui.Refuse("Selected tools share an extension file. Choose separate native configuration directories before connecting both.")
		}
		paths[p.row.Target] = true
		digest.Write([]byte(name + "\x00" + p.row.Target + "\x00"))
		before := sha256.Sum256(p.before)
		after := sha256.Sum256(p.after)
		digest.Write(before[:])
		digest.Write(after[:])
	}
	view.ReviewID = hex.EncodeToString(digest.Sum(nil))
	if request.Action == "review" {
		view.Note = "Only selected integrations will be updated for this workspace. Default responder, grants, skills, MCP configuration and history are unchanged."
		return view, nil
	}
	if request.ReviewID == "" || request.ReviewID != view.ReviewID {
		return view, ui.Refuse("Setup changed since your review. Review the current changes again; nothing was applied.")
	}
	changed := false
	for _, name := range setupHarnesses {
		if !chosen[name] {
			continue
		}
		p := plans[name]
		if p.row.Configured {
			continue
		}
		if err := ctx.Err(); err != nil {
			return view, err
		}
		if c.install == nil {
			return view, ui.Refuse("Native installer unavailable; nothing more was applied.")
		}
		if err := c.install(name, p.row.Target); err != nil {
			return view, ui.Refuse("Setup did not finish for " + p.row.Label + ". Earlier selected changes may have been saved. Check the tool list before retrying; no defaults or grants were changed.")
		}
		changed = true
	}
	view.Harnesses = nil
	for _, name := range setupHarnesses {
		view.Harnesses = append(view.Harnesses, c.inspect(name, sessions).row)
	}
	view.ReviewID = ""
	view.Note = "Selected integrations saved. Complete native trust/activation steps; configuration does not prove reply delivery."
	if !changed {
		view.Note = "No setup changes needed. Existing integrations, defaults and history are unchanged."
	}
	return view, nil
}
