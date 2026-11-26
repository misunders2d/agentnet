package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

func runCodexChatCmd(a *client.Agent, args []string, out io.Writer) error {
	usage := errors.New("usage: codex-chat list | codex-chat connect THREAD_ID NAME")
	if len(args) == 0 {
		return usage
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usage
		}
		list, err := client.CodexChatCandidates(20)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(out, "No Codex Desktop chat was found in this Codex home.")
		}
		for _, c := range list {
			name := c.Name
			if name == "" {
				name = "(untitled)"
			}
			fmt.Fprintf(out, "%s  %s  %s  %s\n", c.Thread, c.Updated.Local().Format("2006-01-02 15:04"), name, c.Dir)
		}
		return nil
	case "connect":
		if len(args) != 3 || strings.TrimSpace(args[2]) == "" {
			return usage
		}
		if err := chatPersonOnly("a Codex Desktop chat"); err != nil {
			return err
		}
		fmt.Fprintf(out, "Requests from your own devices that name this agent will be added to Codex Desktop chat %s's queue, and Codex runs them with that chat's own settings and approvals. Type yes to connect: ", args[1])
		var answer string
		fmt.Fscanln(os.Stdin, &answer)
		if answer != "yes" {
			return errors.New("not connected")
		}
		id, err := a.ConnectCodexChat(args[1], strings.TrimSpace(args[2]))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Connected as agent %s. Keep that chat open in Codex Desktop to run requests; disable it in Agents to stop.\n", id)
		return nil
	}
	return usage
}
