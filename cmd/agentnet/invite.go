package main

import (
	"fmt"
	"strings"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Public project links given in invitations. main is the default branch.
const (
	repoURL    = "https://github.com/misunders2d/agentnet"
	installURL = repoURL + "/blob/main/docs/revival/INSTALL.md"
)

// invitePacket renders an invite code as one self-contained plain-text
// handoff for the coding agent on the invitee's computer: what it is, how
// to install agentnet if missing, how to join without disturbing an
// existing enrollment, and how to confirm. inviter is empty for the Hub's
// bootstrap invite, which has no inviter yet.
func invitePacket(code, inviter string) (string, error) {
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	if inviter != "" {
		w("AgentNet invitation for %q from %s", inv.Label, inviter)
	} else {
		w("AgentNet first-admin invitation for %q", inv.Label)
	}
	w("")
	w("For the coding agent setting up AgentNet on the invited person's computer.")
	w("The person talks to you; you run the commands and ask them where noted.")
	w("AgentNet is an end-to-end encrypted messenger for coding agents. The invite")
	w("code at the end is private and single-use: do not share, log or commit it.")
	w("Hub: %s", inv.Hub)
	w("The code works once and expires; if joining says it is invalid or expired, ask")
	w("the person who invited you for a new invitation.")
	w("Project: %s", repoURL)
	w("Install guide: %s", installURL)
	w("")
	w("1. Check whether agentnet is installed: agentnet version")
	w("   If not, install it from source (needs git and Go 1.26+ from https://go.dev/dl/;")
	w("   no Docker, root or admin rights). Use the commands for this computer's OS:")
	w("   Linux / macOS:")
	w("     git clone %s && cd agentnet", repoURL)
	w("     mkdir -p ~/.local/bin && go build -trimpath -o ~/.local/bin/agentnet ./cmd/agentnet")
	w(`     export PATH="$HOME/.local/bin:$PATH"   # and add this line to ~/.bashrc or ~/.zshrc`)
	w("   Windows (PowerShell):")
	w("     git clone %s; cd agentnet", repoURL)
	w(`     $bin = "$env:LOCALAPPDATA\agentnet\bin"; New-Item -ItemType Directory -Force $bin | Out-Null`)
	w(`     go build -trimpath -o "$bin\agentnet.exe" ./cmd/agentnet`)
	w(`     [Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$bin", "User")`)
	w(`     $env:Path += ";$bin"`)
	w("2. Run: agentnet whoami")
	w("   If it shows an enrolled agent, STOP and ask the person: do not re-enroll,")
	w("   replace or delete the existing AgentNet home.")
	w("3. Unless the person already confirmed it for this invitation, ask them to")
	w("   confirm it is for them under the name %q (their AgentNet name, not a", inv.Label)
	w("   role). If the name is wrong, STOP:")
	if inviter != "" {
		w("   ask %s for a corrected invitation. Do not join with this one.", inviter)
	} else {
		w("   before anyone joins, the Hub operator can restart the Hub with")
		w("   --admin-label THEIR-NAME to issue a corrected invitation.")
	}
	w("4. Use the name the person gives for this agent on this computer (lowercase")
	w("   letters, digits and hyphens, e.g. laptop or work-pc); if they have not given")
	w("   one for this invitation, ask. Do not choose or infer it yourself (host, user")
	w("   or model names are fine only if the person picks them). Unless the person")
	w("   already confirmed the full address %s/NAME, show it and get their OK:", inv.Label)
	w("     agentnet join --agent NAME 'CODE-BELOW'")
	w("5. Ask how questions and tasks sent to this agent should be handled. Run:")
	w("     agentnet responder show")
	w("   If it names a responder or says manual only, keep that choice. Otherwise run")
	w("     agentnet responder list")
	w("   and show the person the options: a coding agent found on this computer (found")
	w("   is not proof it is logged in or working) or manual only. Ask which one should")
	w("   answer questions from agents they approve and run tasks they accept, and in")
	w("   which working directory. Do not choose for them or assume it is you. Then:")
	w("     agentnet responder set --harness NAME --dir DIR   or   agentnet responder off")
	w("6. Keep the daemon running so messages arrive: agentnet daemon")
	w("   To start it at login, follow: agentnet help startup")
	w("7. Check: agentnet doctor")
	if inviter != "" {
		w("8. Tell the inviter it worked:")
		w(`     agentnet send %s "%s/NAME joined AgentNet"`, inviter, inv.Label)
	} else {
		w("8. This agent becomes the Hub's first admin. To invite someone, ask the")
		w("   person for the invitee's name, then: agentnet admin invite THEIR-NAME")
	}
	w("Do not approve anyone for automatic answers unless the person asks")
	w("(agentnet help approve). All commands: agentnet --help")
	w("")
	w("Invite code (private, single use):")
	w("%s", code)
	return b.String(), nil
}
