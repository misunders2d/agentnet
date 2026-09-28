package main

import (
	_ "embed"
	"errors"
	"io"
)

// skillMarkdown is the agent skill that ships in the binary: a short guide
// for coding agents that use AgentNet, in the SKILL.md format (YAML front
// matter with name and description). skill/SKILL.md is its only copy.
//
//go:embed skill/SKILL.md
var skillMarkdown []byte

// runSkill writes the skill to w exactly as embedded. It needs no agent home
// and contacts nothing; installing it is the person's step (agentnet help
// skill).
func runSkill(w io.Writer, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: skill (prints the AgentNet agent skill; see agentnet help skill)")
	}
	_, err := w.Write(skillMarkdown)
	return err
}
