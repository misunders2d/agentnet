package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A question may look up this device's own AgentNet state through the exact
// installed agentnet program, with fixed read-only arguments. These lookups
// add no shell, network or write permission: each harness's native rules decide.
// Describing a lookup is never an allow rule or an execution policy.

// questionLookups are the fixed read-only argument lists, by name.
var questionLookups = []struct{ name, args string }{
	{"version", "version"},
	{"whoami", "whoami"},
	{"inbox", "inbox --peek"},
	{"inbox-json", "inbox --peek --json"},
	{"approvals", "approvals"},
}

var executable = os.Executable // tests bind a disposable lookup program

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
// program that cannot be resolved or a Pi extension that could not be written.
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
	case "claude", "codex":
		var lists []string
		for _, l := range questionLookups {
			lists = append(lists, "`"+l.args+"`")
		}
		return questionLookup{text: "To look up this AgentNet device's own state you may run the program " + strconv.Quote(exe) + ", quoted as your shell needs, under your native permissions, for example with these argument lists: " +
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

export function roomArgs(params) {
	if (!ID.test(process.env.AGENTNET_ROOM_REQUEST || "")) throw new Error("room tools require this running group request");
	if (params?.action === "ask") {
		if (!ID.test(params.pid || "") || typeof params.text !== "string" || !params.text.trim() || params.text.length > 65536) throw new Error("choose an exact group PID and question");
		return ["room", "ask", "--pid", params.pid, "--kind", "question", "--", params.text];
	}
	if (params?.action === "wait" && ID.test(params.id || "")) return ["room", "wait", params.id];
	throw new Error("choose ask or wait with its exact identifier");
}

export default function (pi) {
	if (ID.test(process.env.AGENTNET_ROOM_REQUEST || "")) pi.registerTool({
		name: "agentnet_room", label: "Ask a group agent",
		description: "Ask another active agent in this exact group a question by roster PID, or wait for an existing permitted request. Returns its correlated reply. Existing request authority and cancellation apply; cannot assign tasks or grant permissions.",
		parameters: Type.Object({ action: Type.Union([Type.Literal("ask"), Type.Literal("wait")]), pid: Type.Optional(Type.String()), text: Type.Optional(Type.String()), id: Type.Optional(Type.String()) }),
		async execute(_id, params, signal) {
			const args = roomArgs(params);
			const result = await new Promise((resolve, reject) => execFile(PROGRAM, args, { shell: false, maxBuffer: 1 << 20, signal }, (err, stdout, stderr) => {
				if (err) reject(new Error("AgentNet room request failed: " + (String(stderr).trim() || err.message)));
				else resolve(String(stdout));
			}));
			return { content: [{ type: "text", text: result }], details: undefined };
		},
	});
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
		content = bytes.Replace(content, []byte(`parameters: Type.Object({ action: Type.Union([Type.Literal("ask"), Type.Literal("wait")]), pid: Type.Optional(Type.String()), text: Type.Optional(Type.String()), id: Type.Optional(Type.String()) }),`), []byte(`parameters: pi.zod.object({ action: pi.zod.enum(["ask", "wait"]), pid: pi.zod.string().optional(), text: pi.zod.string().optional(), id: pi.zod.string().optional() }),`), 1)
		content = bytes.Replace(content, []byte(`name: "agentnet_room",`), []byte(`name: "agentnet_room", approval: "read",`), 1)
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
