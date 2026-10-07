package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A question may look up this device's own AgentNet state through the exact
// installed agentnet program, with fixed read-only arguments. These lookups
// add no shell, network or write permission: each harness's own rules still
// decide (Claude's deny and ask rules win over these exact allow rules; Pi's
// own tool_call handlers still run; Codex keeps its read-only sandbox).

// questionLookups are the fixed read-only argument lists, by name.
var questionLookups = []struct{ name, args string }{
	{"version", "version"},
	{"whoami", "whoami"},
	{"inbox", "inbox --peek"},
	{"inbox-json", "inbox --peek --json"},
	{"approvals", "approvals"},
}

// maxLookupIDs bounds the exact status lookups a question is given.
const maxLookupIDs = 8

var (
	lookupID    = regexp.MustCompile(`\b[0-9a-f]{32}\b`)
	unixRule    = regexp.MustCompile(`^/[A-Za-z0-9._/+-]+$`)
	windowsRule = regexp.MustCompile(`^[A-Za-z]:/[A-Za-z0-9._/+-]+$`)
	executable  = os.Executable // a variable so tests can bind another program
)

// claudeRulePath is exe as it must appear, unquoted, both in the command
// Claude runs and in its exact Bash allow rule; ok is false when no such
// spelling exists (spaces or other characters a shell would need quoted).
// A Windows drive path is given with forward slashes, as Claude's Bash
// (Git Bash) accepts it; a backslash would be a shell escape.
func claudeRulePath(goos, exe string) (string, bool) {
	if goos == "windows" {
		p := strings.ReplaceAll(exe, `\`, "/")
		return p, windowsRule.MatchString(p)
	}
	return exe, unixRule.MatchString(exe)
}

// agentnetProgram is this program's own resolved absolute path: the exact
// binary lookups may run, never a name looked up on PATH or in a directory.
func agentnetProgram() (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if !filepath.IsAbs(exe) {
		return "", errors.New("agentnet program path is not absolute")
	}
	if info, err := os.Stat(exe); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("agentnet program is not a regular file")
	}
	if _, err := exec.LookPath(exe); err != nil { // the platform's own executable test
		return "", err
	}
	return exe, nil
}

// questionLookup is what one question run is given: the arguments that
// configure the lookups for its harness and the prompt text describing
// exactly those, or saying plainly that there are none.
type questionLookup struct {
	args []string
	text string
}

const lookupsUnavailable = "No lookups of this AgentNet device's own state are available to this question here.\n"

// questionSetup configures the lookups for a question run by harness h.
// Whatever cannot be bound exactly is not offered, and the text says so: a
// program that cannot be resolved, a Claude rule that would need quoting,
// or a Pi extension that could not be written.
func (a *Agent) questionSetup(j job, h string) questionLookup {
	if j.Kind != envelope.KindQuestion || h != "claude" && h != "codex" && h != "pi" && h != "omp" {
		return questionLookup{}
	}
	none := questionLookup{text: lookupsUnavailable}
	exe, err := agentnetProgram()
	if err != nil {
		a.Logf("question lookups unavailable: %v", err)
		return none
	}
	direct := " Answer such a harmless lookup directly; never ask the coworker to resend it as a task.\n"
	switch h {
	case "claude":
		exe, ok := claudeRulePath(runtime.GOOS, exe)
		if !ok {
			a.Logf("question lookups unavailable for claude: %s would need shell quoting, which an exact rule cannot express", exe)
			return none
		}
		args, cmds := []string{"--allowedTools"}, []string{}
		for _, l := range questionLookups {
			args, cmds = append(args, "Bash("+exe+" "+l.args+")"), append(cmds, exe+" "+l.args)
		}
		for _, id := range a.lookupIDs(j.Body) {
			args, cmds = append(args, "Bash("+exe+" status "+id+")"), append(cmds, exe+" status "+id)
		}
		return questionLookup{args: args, text: "To look up this AgentNet device's own state you may run exactly these read-only commands, with no other arguments or shell syntax: " +
			strings.Join(cmds, "; ") + "." + direct}
	case "codex":
		var lists []string
		for _, l := range questionLookups {
			lists = append(lists, "`"+l.args+"`")
		}
		return questionLookup{text: "To look up this AgentNet device's own state you may run the program " + strconv.Quote(exe) + ", quoted as your shell needs, in your read-only sandbox with exactly one of these argument lists: " +
			strings.Join(lists, ", ") + ", or `status ID` for a message this device sent (without network it shows the local record, marked as such)." + direct}
	default: // pi/omp
		path, err := a.writeQuestionLookup(exe, h)
		if err != nil {
			a.Logf("question lookups unavailable for %s: %v", h, err)
			return none
		}
		return questionLookup{args: []string{"--extension", path},
			text: "To look up this AgentNet device's own state use the agentnet_lookup tool (version, whoami, inbox, approvals, or status with the id of a message this device sent)." + direct}
	}
}

// lookupIDs are the ids named in a request that are messages this device
// sent: the only ones whose status a question may look up by exact rule.
func (a *Agent) lookupIDs(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range lookupID.FindAllString(body, -1) {
		if seen[id] || len(out) == maxLookupIDs {
			continue
		}
		seen[id] = true
		if _, _, found, err := a.store.outboxState(id); err == nil && found {
			out = append(out, id)
		}
	}
	return out
}

// piLookupSource is the Pi extension: one tool that runs the bound agentnet
// program with fixed arguments, never a shell. Any failure fails the call.
const piLookupSource = `// Written by AgentNet for questions; regenerated on each run.
import { Type } from "@earendil-works/pi-ai";
import { execFile } from "node:child_process";

const PROGRAM = %s;
const LOOKUPS = { version: ["version"], whoami: ["whoami"], inbox: ["inbox", "--peek"], approvals: ["approvals"], status: ["status"] };
const ID = /^[0-9a-f]{32}$/;

export function lookupArgs(params) {
	const args = Object.hasOwn(LOOKUPS, params?.lookup) ? LOOKUPS[params.lookup] : undefined;
	if (!args) throw new Error("unknown AgentNet lookup");
	if (params.lookup === "status") {
		if (typeof params.id !== "string" || !ID.test(params.id)) throw new Error("status needs the 32-character id of a message this device sent");
		return [...args, params.id];
	}
	if (params.id !== undefined) throw new Error("only status takes an id");
	return [...args];
}

export function runLookup(params, signal) {
	const args = lookupArgs(params);
	return new Promise((resolve, reject) => {
		execFile(PROGRAM, args, { shell: false, timeout: 20000, maxBuffer: 1 << 16, signal }, (err, stdout, stderr) => {
			if (err) reject(new Error("agentnet " + args[0] + " failed: " + (String(stderr).trim() || err.message)));
			else resolve(String(stdout).slice(0, 8000));
		});
	});
}

export default function (pi) {
	pi.registerTool({
		name: "agentnet_lookup",
		label: "AgentNet lookup",
		description: "Read-only lookup of this AgentNet device's own state: version, whoami, inbox (not marked read), approvals, or status of a message this device sent.",
		parameters: Type.Object({
			lookup: Type.Union([Type.Literal("version"), Type.Literal("whoami"), Type.Literal("inbox"), Type.Literal("approvals"), Type.Literal("status")]),
			id: Type.Optional(Type.String({ description: "status only: the message id" })),
		}),
		async execute(_id, params, signal) {
			return { content: [{ type: "text", text: await runLookup(params, signal) }], details: undefined };
		},
	});
}
`

// writePiLookup writes the Pi extension bound to exe into this home,
// owner-only, and returns its path.
func (a *Agent) writePiLookup(exe string) (string, error) {
	return a.writeQuestionLookup(exe, "pi")
}

func (a *Agent) writeQuestionLookup(exe, harness string) (string, error) {
	program, err := json.Marshal(exe)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(a.home, "harness")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := "agentnet-question.mjs"
	if harness == "omp" {
		name = "agentnet-omp-question.mjs"
	}
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	content := []byte(strings.Replace(piLookupSource, "%s", string(program), 1))
	if harness == "omp" {
		content = bytes.Replace(content, []byte(`import { Type } from "@earendil-works/pi-ai";`), nil, 1)
		content = bytes.Replace(content, []byte(`parameters: Type.Object({
			lookup: Type.Union([Type.Literal("version"), Type.Literal("whoami"), Type.Literal("inbox"), Type.Literal("approvals"), Type.Literal("status")]),
			id: Type.Optional(Type.String({ description: "status only: the message id" })),
		}),`), []byte(`parameters: pi.zod.object({
			lookup: pi.zod.enum(["version", "whoami", "inbox", "approvals", "status"]),
			id: pi.zod.string().optional(),
		}),`), 1)
		content = bytes.Replace(content, []byte(`name: "agentnet_lookup",`), []byte(`name: "agentnet_lookup",
 approval: "read",`), 1)
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return path, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}
