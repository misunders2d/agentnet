package main

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf16"
)

// On Windows a daemon started by the scheduled task \agentnet (agentnet help
// startup) switches to an updated program by having the task start again.
// That is only done when the task is exactly the daemon that runs: these are
// the checks on the task's definition, kept free of Windows calls so they
// are tested everywhere.

// winTaskName is the task agentnet help startup creates.
const winTaskName = `\agentnet`

// updateHelperCmd is the hidden command, run from the updated program, that
// has the scheduled task start the daemon again (restart_windows.go).
const updateHelperCmd = "update-helper"

// taskDef is the part of a task definition the switch relies on.
type taskDef struct {
	UserID  string `xml:"Principals>Principal>UserId"`
	Policy  string `xml:"Settings>MultipleInstancesPolicy"`
	Actions struct {
		Any []struct {
			XMLName   xml.Name
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:",any"`
	} `xml:"Actions"`
}

// parseTaskDef reads `schtasks /query /xml ONE` output (UTF-16 or UTF-8).
func parseTaskDef(out []byte) (taskDef, error) {
	if len(out) >= 2 && (out[0] == 0xff && out[1] == 0xfe || out[1] == 0) {
		out = bytes.TrimPrefix(out, []byte{0xff, 0xfe})
		u := make([]uint16, len(out)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(out[2*i:])
		}
		out = []byte(string(utf16.Decode(u)))
	}
	var def taskDef
	dec := xml.NewDecoder(bytes.NewReader(out))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil } // already UTF-8
	if err := dec.Decode(&def); err != nil {
		return def, fmt.Errorf("task definition: %w", err)
	}
	return def, nil
}

// taskEnv is what checkTaskDef compares against, supplied by the platform.
type taskEnv struct {
	exe      string                         // this program's file
	args     []string                       // this process's arguments (os.Args[1:])
	user     string                         // DOMAIN\user
	sid      string                         // S-1-...
	sameFile func(a, b string) bool         // same file on disk
	split    func(string) ([]string, error) // the platform's command-line splitting
}

// checkTaskDef reports whether def starts exactly this daemon: one Exec
// action, of this program's file, with exactly these arguments, for this
// user, and a policy that does not stop a running instance.
func checkTaskDef(def taskDef, env taskEnv) error {
	if len(def.Actions.Any) != 1 || def.Actions.Any[0].XMLName.Local != "Exec" {
		return fmt.Errorf("the task %s does not have exactly one program to start", winTaskName)
	}
	a := def.Actions.Any[0]
	cmd := strings.Trim(strings.TrimSpace(a.Command), `"`)
	if cmd == "" || !env.sameFile(cmd, env.exe) {
		return fmt.Errorf("the task %s starts %s, not %s", winTaskName, cmd, env.exe)
	}
	parts, err := env.split("agentnet " + a.Arguments)
	if err != nil {
		return fmt.Errorf("the task's arguments: %w", err)
	}
	if !slices.Equal(parts[1:], env.args) {
		return fmt.Errorf("the task %s starts agentnet %q, but this daemon runs as agentnet %q", winTaskName, parts[1:], env.args)
	}
	if !sameUser(def.UserID, env.user, env.sid) {
		return fmt.Errorf("the task %s runs as %s, not as %s", winTaskName, def.UserID, env.user)
	}
	if strings.EqualFold(def.Policy, "StopExisting") {
		return errors.New("the task stops a running instance when started again (MultipleInstancesPolicy StopExisting)")
	}
	return nil
}

// sameUser compares a task principal (a SID, DOMAIN\user or user) with this
// process's user.
func sameUser(principal, user, sid string) bool {
	p := strings.TrimSpace(principal)
	switch {
	case p == "":
		return false
	case strings.HasPrefix(strings.ToUpper(p), "S-1-"):
		return strings.EqualFold(p, sid)
	case strings.Contains(p, `\`):
		return strings.EqualFold(p, user)
	}
	_, name, _ := strings.Cut(user, `\`)
	return strings.EqualFold(p, name)
}
