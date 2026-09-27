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
	w("3. Ask the person to confirm this invitation is for them under the name %q", inv.Label)
	w("   (their AgentNet name; it is not a role). If the name is wrong, STOP:")
	if inviter != "" {
		w("   ask %s for a corrected invitation. Do not join with this one.", inviter)
	} else {
		w("   before anyone joins, the Hub operator can restart the Hub with")
		w("   --admin-label THEIR-NAME to issue a corrected invitation.")
	}
	w("4. Ask the person what to call this agent on this computer (lowercase letters,")
	w("   digits and hyphens, e.g. laptop or work-pc). Do not choose it yourself and")
	w("   do not use the host name, user name or your model name. Show the full")
	w("   address %s/NAME and join only after the person explicitly confirms it:", inv.Label)
	w("     agentnet join --agent NAME 'CODE-BELOW'")
	w("5. Keep the daemon running so messages arrive: agentnet daemon")
	w("   To start it at login, follow: agentnet help startup")
	w("6. Check: agentnet doctor")
	if inviter != "" {
		w("7. Tell the inviter it worked:")
		w(`     agentnet send %s "%s/NAME joined AgentNet"`, inviter, inv.Label)
	} else {
		w("7. This agent becomes the Hub's first admin. To invite someone, ask the")
		w("   person for the invitee's name, then: agentnet admin invite THEIR-NAME")
	}
	w("Do not set up an automatic responder or approve anyone unless the person asks")
	w("(agentnet help responder). All commands: agentnet --help")
	w("")
	w("Invite code (private, single use):")
	w("%s", code)
	return b.String(), nil
}
