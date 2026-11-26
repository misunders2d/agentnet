package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

// claudeChatPersonOnly refuses a connect that is not the person at a
// terminal: from a Claude or Codex session, an AgentNet run, or a pipe.
func claudeChatPersonOnly() error { return chatPersonOnly("a Claude chat") }

// chatPersonOnly is claudeChatPersonOnly for the chat named what.
func chatPersonOnly(what string) error {
	for _, name := range []string{"CLAUDECODE", client.ClaudeSessionEnv, client.CodexThreadEnv, "AGENTNET_REPLY_SESSION"} {
		if os.Getenv(name) != "" {
			return fmt.Errorf("connect %s from your own terminal, not from an agent session (%s is set)", what, name)
		}
	}
	if inRun() {
		return errors.New("connect " + what + " from your own terminal, not from an AgentNet run")
	}
	if name := agentAncestor(); name != "" {
		return fmt.Errorf("connect %s from your own terminal, not from an agent session (started under %s)", what, name)
	}
	if info, err := os.Stdin.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return errors.New("connect " + what + " from your own terminal (stdin is not a terminal)")
	}
	return nil
}

// agentAncestor names a coding agent among this process's ancestors (Linux
// /proc), or is empty. It stops a shell an agent opened, even with its
// variables cleared; a process detached from that tree is not seen.
func agentAncestor() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	agents := map[string]bool{"claude": true, "codex": true, "agy": true, "pi": true, "omp": true}
	pid := os.Getppid()
	for range 64 {
		if pid <= 1 {
			return ""
		}
		names := []string{}
		if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
			names = append(names, filepath.Base(exe))
		}
		if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
			for i, arg := range strings.Split(string(raw), "\x00") {
				if i > 1 || arg == "" {
					break
				}
				names = append(names, filepath.Base(arg))
			}
		}
		for _, n := range names {
			if agents[n] {
				return n
			}
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return ""
		}
		i := strings.LastIndexByte(string(stat), ')')
		fields := strings.Fields(string(stat)[i+1:])
		if i < 0 || len(fields) < 2 {
			return ""
		}
		next, err := strconv.Atoi(fields[1])
		if err != nil || next == pid {
			return ""
		}
		pid = next
	}
	return ""
}

func runClaudeChatCmd(a *client.Agent, args []string, out io.Writer) error {
	usage := errors.New("usage: claude-chat list | claude-chat connect SESSION_ID NAME")
	if len(args) == 0 {
		return usage
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usage
		}
		list, err := a.ClaudeChatCandidates()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(out, "No open Claude chat is registered here.")
		}
		for _, c := range list {
			inbox := "inbox ready"
			if !c.Inbox {
				inbox = "no inbox"
			}
			fmt.Fprintf(out, "%s  %s  %s\n", c.SessionID, c.Dir, inbox)
		}
		return nil
	case "connect":
		if len(args) != 3 || strings.TrimSpace(args[2]) == "" {
			return usage
		}
		if err := claudeChatPersonOnly(); err != nil {
			return err
		}
		fmt.Fprintf(out, "Requests that name this agent will be sent into Claude chat %s, as messages it answers with its own settings and approvals. Type yes to connect: ", args[1])
		var answer string
		fmt.Fscanln(os.Stdin, &answer)
		if answer != "yes" {
			return errors.New("not connected")
		}
		id, err := a.ConnectClaudeChat(args[1], strings.TrimSpace(args[2]))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Connected as agent %s. It ends when that chat closes or restarts; disable it in Agents to stop sooner.\n", id)
		return nil
	}
	return usage
}
