package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// inviteLink keeps the private, single-use code entirely in the fragment
// (protocol.InviteLink, shared with the page).
func inviteLink(code string) (string, error) { return protocol.InviteLink(code) }

// Public project links given in invitations. main is the default branch.
const (
	repoURL    = "https://github.com/misunders2d/agentnet"
	installURL = repoURL + "/blob/main/docs/revival/INSTALL.md"
)

// releaseTag returns version if it names a published release (vX.Y.Z), so
// an invitation installs the same release as the inviter's; development
// builds give "" and fall back to building from source.
func releaseTag(version string) string {
	if releaseVersion.MatchString(version) {
		return version
	}
	return ""
}

var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// releaseSteps installs the release binary for this computer, only when
// agentnet is not installed yet, and only after it matched the release's
// SHA256SUMS.
func releaseSteps(w func(string, ...any), tag string) {
	posix, windows := releaseInstall(repoURL + "/releases/download/" + tag)
	w("   Only if it is not installed: install release %s (no Go, Docker, root or admin", tag)
	w("   rights). The file is checked against the release's SHA256SUMS before it is")
	w("   installed; if a command fails, STOP. If agentnet is already installed, do not")
	w("   replace it from this invitation: keep it and go on (to update it later:")
	w("   agentnet help update). Use the commands for this computer's OS:")
	w("   Linux / macOS:")
	for _, l := range posix {
		w("     %s", l)
	}
	w(`     export PATH="$HOME/.local/bin:$PATH"   # and add this line to ~/.bashrc or ~/.zshrc`)
	w("   Windows (PowerShell):")
	for _, l := range windows {
		w("     %s", l)
	}
	w(`     [Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$bin", "User"); $env:Path += ";$bin"`)
	w("   Then agentnet version must print agentnet %s. To build it from source instead", tag)
	w("   (git and Go 1.26+):")
	w("     git clone --branch %s %s && cd agentnet", tag, repoURL)
	w(`     go build -trimpath -ldflags "-X github.com/misunders2d/agentnet/internal/protocol.Version=%s" -o ~/.local/bin/agentnet ./cmd/agentnet`, tag)
}

// releaseInstall returns the download-check-install commands for base (a
// release download URL): POSIX shell lines joined by &&, and PowerShell
// lines that stop at the first error. They install ~/.local/bin/agentnet or
// %LOCALAPPDATA%\agentnet\bin\agentnet.exe only after the checksum matched.
func releaseInstall(base string) (posix, windows []string) {
	posix = []string{
		`case "$(uname -s)-$(uname -m)" in Linux-x86_64) p=linux-amd64;; Linux-aarch64|Linux-arm64) p=linux-arm64;; Darwin-x86_64) p=darwin-amd64;; Darwin-arm64) p=darwin-arm64;; *) p=;; esac &&`,
		`test -n "$p" && f=agentnet-$p && d=$(mktemp -d) &&`,
		`curl -fsSL -o "$d/$f" ` + base + `/$f && curl -fsSL -o "$d/SHA256SUMS" ` + base + `/SHA256SUMS &&`,
		`(cd "$d" && grep " $f\$" SHA256SUMS | (sha256sum -c - 2>/dev/null || shasum -a 256 -c -)) &&`,
		`mkdir -p ~/.local/bin && install -m 0755 "$d/$f" ~/.local/bin/agentnet`,
	}
	windows = []string{
		`$ErrorActionPreference = "Stop"; $a = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }; $f = "agentnet-windows-$a.exe"`,
		`$d = Join-Path $env:TEMP ("agentnet-" + [guid]::NewGuid()); New-Item -ItemType Directory $d | Out-Null`,
		`Invoke-WebRequest ` + base + `/$f -OutFile "$d\$f"; Invoke-WebRequest ` + base + `/SHA256SUMS -OutFile "$d\SHA256SUMS"`,
		`$want = ((Get-Content "$d\SHA256SUMS" | Where-Object { $_ -match " $f$" }) -split "\s+")[0]; $got = ([BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash([IO.File]::ReadAllBytes("$d\$f"))) -replace "-", "").ToLower()`,
		`if (-not $want -or $want -ne $got) { throw "checksum mismatch: agentnet was not installed" }`,
		`$bin = "$env:LOCALAPPDATA\agentnet\bin"; New-Item -ItemType Directory -Force $bin | Out-Null; Copy-Item "$d\$f" "$bin\agentnet.exe"`,
	}
	return posix, windows
}

// sourceSteps builds from the default branch (development inviters).
func sourceSteps(w func(string, ...any)) {
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
}

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
	w("")
	w("How to talk with the person (they may not be technical):")
	w("- Use short, friendly, plain sentences. Run the commands, read the output")
	w("  and fix routine problems yourself; do not show commands, logs or jargon")
	w("  unless they ask.")
	w("- Ask one thing at a time, and say what it is for and what happens next.")
	w("  Once they have answered, do not ask the same thing again.")
	w("- The steps below are your reference: tell the person only what they need")
	w("  to decide or know.")
	w("")
	w("AgentNet is an end-to-end encrypted messenger for coding agents. The invite")
	w("code at the end is private and single-use: do not share, log or commit it.")
	w("Hub: %s", inv.Hub)
	w("The code works once and expires; if joining says it is invalid or expired, ask")
	w("the person who invited you for a new invitation.")
	w("Project: %s", repoURL)
	w("Install guide: %s", installURL)
	w("")
	w("1. Check whether agentnet is installed: agentnet version")
	if tag := releaseTag(protocol.Version); tag != "" {
		releaseSteps(w, tag)
	} else {
		sourceSteps(w)
	}
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
	w("   %s/ is fixed by this invitation; only NAME is theirs to choose. If join says", inv.Label)
	w("   the address is taken, nothing was enrolled and this code still works: show")
	w("   the person the free address the Hub names (not reserved) or ask for another")
	w("   NAME, and join again only with the one they confirm.")
	w("5. Help the person choose their default assistant: who answers for them.")
	w("   First run: agentnet responder show")
	w("   If it names a responder or says manual only, keep that choice and go on")
	w("   to the hooks offer below. Otherwise run: agentnet responder list")
	w("   Before asking, explain it in plain words, for example:")
	w("     \"AgentNet delivers messages. To answer questions or do work for you, it")
	w("     can use a coding assistant you already have on this computer. That")
	w("     assistant works in its own background conversation, not in any chat")
	w("     you have open, with its usual skills and settings. Choosing it lets")
	w("     nobody in by itself: you decide whose questions it may answer on its")
	w("     own, and each task needs your OK, once or as a standing permission you")
	w("     can take back. Or you can keep handling everything yourself: joining")
	w("     and messaging work either way, and you can change this later.\"")
	w("   Then show the options: a coding assistant found on this computer (found")
	w("   is not proof it is logged in or working) or manual only.")
	w("   Do not choose for them or assume it is you. Do not claim it is signed in")
	w("   or working unless you checked, and do not promise it is free: it uses")
	w("   that assistant's own account and usage. Tools it is already allowed to use")
	w("   keep their effects, so do not describe it as unable to change anything.")
	w("   If they already chose one during this setup, apply that choice without asking again.")
	w("   If they pick one, ask in ordinary words which folder it should work in")
	w("   (for example a folder for this work). Then:")
	w("     agentnet responder set --harness NAME --dir DIR   or   agentnet responder off")
	w("   If they use Claude Code or Codex here, offer to let those sessions hear about")
	w("   replies (agentnet help hooks); with their OK: agentnet hooks install claude|codex")
	w("   (Codex: they then trust the hooks in /hooks; new sessions pick them up).")
	w("6. Keep the daemon running so messages arrive: agentnet daemon")
	w("   To start it at login, follow: agentnet help startup")
	w("7. Check: agentnet doctor")
	w("   This checks this terminal's PATH, not a separate daemon's environment.")
	w("   Preserve the responder PATH in its startup service (agentnet help startup).")
	w("   Verify a real approved question gets a reply; delivery alone is not enough.")
	if inviter != "" {
		w("8. Tell the inviter it worked:")
		w(`     agentnet send %s "%s/NAME joined AgentNet"`, inviter, inv.Label)
	} else {
		w("8. This agent becomes the Hub's first admin. To invite someone, ask the")
		w("   person for the invitee's name, then: agentnet admin invite THEIR-NAME")
	}
	w("Do not approve anyone for automatic answers or tasks unless the person asks")
	w("(agentnet help approve): whose questions are answered on their own, and")
	w("which senders' tasks may run without asking, are the person's choice, and")
	w("they can take it back. All commands: agentnet --help")
	w("")
	w("Invite code (private, single use):")
	w("%s", code)
	return b.String(), nil
}
