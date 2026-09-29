package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestInviteLinkKeepsCodeInFragment(t *testing.T) {
	for _, hub := range []string{"https://hub.example.test", "https://hub.example.test:8443/", "https://[::1]:8443"} {
		code := protocol.Invite{Hub: hub, Label: "bob", Secret: "synthetic<&>#?"}.Encode()
		link, err := inviteLink(" \n" + code + "\n")
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal("invalid link")
		}
		if u.Fragment != code || u.RawQuery != "" || u.Path != "/" || u.User != nil || u.Scheme != "https" {
			t.Fatal("invite not isolated in the HTTPS URL fragment")
		}
		r, err := http.NewRequest("GET", link, nil)
		if err != nil || r.URL.RequestURI() != "/" {
			t.Fatal("request target carries invitation data")
		}
		inv, err := protocol.DecodeInvite(u.Fragment)
		if err != nil || inv.Secret != "synthetic<&>#?" || inv.Label != "bob" {
			t.Fatal("fragment changed the invitation")
		}
	}
}

func TestInviteLinkRefusesUnsupportedInvites(t *testing.T) {
	for _, inv := range []protocol.Invite{
		{Hub: "https://hub.example.test", Label: "bob", Secret: "synthetic", CertPEM: "pin"},
		{Hub: "http://hub.example.test", Label: "bob", Secret: "synthetic"},
		{Hub: "https://hub.example.test/path", Label: "bob", Secret: "synthetic"},
		{Hub: "https://hub.example.test/?secret=synthetic", Label: "bob", Secret: "synthetic"},
		{Hub: "https://synthetic@hub.example.test", Label: "bob", Secret: "synthetic"},
	} {
		link, err := inviteLink(inv.Encode())
		if err == nil || link != "" || strings.Contains(err.Error(), "synthetic") {
			t.Fatal("unsupported invite produced a link or leaked its contents")
		}
	}
	if link, err := inviteLink("not-an-invite"); err == nil || link != "" {
		t.Fatal("invalid code accepted")
	}
}

func TestInviteOutputFlagsCheckedBeforeCreating(t *testing.T) {
	// A nil client proves the conflicting flags are refused before contacting
	// the Hub or creating an otherwise unused invitation.
	err := runAdmin(context.Background(), nil, []string{"invite", "--raw", "--link", "bob"})
	if err == nil || !strings.Contains(err.Error(), "either --raw or --link") {
		t.Fatalf("conflicting flags: %v", err)
	}
}

// An invitation from a release build installs that release, checked against
// its SHA256SUMS; one from a development build says to build from source.
func TestInvitePacketInstallsInvitersRelease(t *testing.T) {
	code := protocol.Invite{Hub: "https://hub.example.test:8443", Label: "bob", Secret: "s"}.Encode()
	old := protocol.Version
	t.Cleanup(func() { protocol.Version = old })

	protocol.Version = "v0.2.0"
	packet, err := invitePacket(code, "admin/laptop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"install release v0.2.0",
		"https://github.com/misunders2d/agentnet/releases/download/v0.2.0/$f",
		"https://github.com/misunders2d/agentnet/releases/download/v0.2.0/SHA256SUMS",
		"sha256sum -c -", "[Security.Cryptography.SHA256]::Create()", "checksum mismatch",
		"agentnet version must print agentnet v0.2.0",
		"git clone --branch v0.2.0",
	} {
		if !strings.Contains(packet, want) {
			t.Fatalf("release packet lacks %q:\n%s", want, packet)
		}
	}
	if i := strings.Index(packet, "go build"); i >= 0 && i < strings.Index(packet, "To build it from source instead") {
		t.Fatal("the release install asks for Go")
	}
	if strings.Contains(packet, "If not (or older") || !strings.Contains(packet, "do not\n   replace it from this invitation") {
		t.Fatal("an existing installation must not be replaced")
	}
	if !strings.Contains(packet, `-ldflags "-X github.com/misunders2d/agentnet/internal/protocol.Version=v0.2.0"`) {
		t.Fatal("the source alternative must stamp the version")
	}

	for _, dev := range []string{"dev", "v0.2.0-mvp.1+570b58d", "570b58d"} {
		protocol.Version = dev
		packet, _ := invitePacket(code, "admin/laptop")
		if strings.Contains(packet, "releases/download") || !strings.Contains(packet, "go build -trimpath") {
			t.Fatalf("%s: expected the source build steps:\n%s", dev, packet)
		}
	}
}

// fakeRelease serves agentnet-* files and a SHA256SUMS that is right, or
// wrong for every file when bad is set.
func fakeRelease(t *testing.T, bad bool) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "SHA256SUMS" {
			for _, p := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64.exe", "windows-arm64.exe"} {
				sum := sha256.Sum256([]byte("binary agentnet-" + p))
				if bad {
					sum[0] ^= 1
				}
				fmt.Fprintf(w, "%x  agentnet-%s\n", sum, p)
			}
			return
		}
		if !strings.HasPrefix(name, "agentnet-") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "binary "+name)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The generated install commands, run for real against a stand-in release:
// the file is installed only when its checksum matches, and nothing is left
// on a mismatch or a failed download.
func TestReleaseInstallCommands(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bad     bool
		path    string
		install bool
	}{{"good", false, "", true}, {"checksum mismatch", true, "", false}, {"missing file", false, "/nothing-here", false}} {
		base := fakeRelease(t, tc.bad) + tc.path
		posix, windows := releaseInstall(base)
		home := t.TempDir()
		var cmd *exec.Cmd
		var target string
		if runtime.GOOS == "windows" {
			cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", strings.Join(windows, "\n"))
			cmd.Env = append(os.Environ(), "LOCALAPPDATA="+home, "TEMP="+t.TempDir())
			target = filepath.Join(home, "agentnet", "bin", "agentnet.exe")
		} else {
			cmd = exec.Command("sh", "-c", strings.Join(posix, "\n"))
			cmd.Env = append(os.Environ(), "HOME="+home, "TMPDIR="+t.TempDir())
			target = filepath.Join(home, ".local", "bin", "agentnet")
		}
		out, err := cmd.CombinedOutput()
		data, readErr := os.ReadFile(target)
		if tc.install {
			if err != nil || readErr != nil || !strings.HasPrefix(string(data), "binary agentnet-") {
				t.Fatalf("%s: %v %q %v\n%s", tc.name, err, data, readErr, out)
			}
		} else if err == nil || readErr == nil {
			t.Fatalf("%s: succeeded or installed anyway (err %v, installed %v)\n%s", tc.name, err, readErr == nil, out)
		}
	}
}

// The packet says which part of the address the invitation fixes and what
// a taken address means, without the agent choosing a name itself.
func TestInvitePacketExplainsTakenAddress(t *testing.T) {
	code := protocol.Invite{Hub: "https://hub.example.test:8443", Label: "bernard", Secret: "s"}.Encode()
	packet, err := invitePacket(code, "admin/laptop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"bernard/ is fixed by this invitation; only NAME is theirs to choose",
		"If join says\n   the address is taken, nothing was enrolled and this code still works",
		"join again only with the one they confirm",
		"Do not choose or infer it yourself",
	} {
		if !strings.Contains(packet, want) {
			t.Fatalf("packet lacks %q:\n%s", want, packet)
		}
	}
}

// Every packet, including the first-admin one, tells the installing agent
// to keep things plain for the person and explains the default assistant
// before asking for it, without overstating what it can or cannot do.
func TestInvitePacketPlainLanguageGuidance(t *testing.T) {
	code := protocol.Invite{Hub: "https://hub.example.test:8443", Label: "vitalii", Secret: "s"}.Encode()
	for _, inviter := range []string{"admin/laptop", ""} {
		packet, err := invitePacket(code, inviter)
		if err != nil {
			t.Fatal(err)
		}
		talk := strings.Index(packet, "How to talk with the person")
		if talk < 0 || talk > strings.Index(packet, "1. Check whether agentnet is installed") {
			t.Fatalf("guidance does not lead the packet (%q):\n%s", inviter, packet)
		}
		for _, want := range []string{
			"Use short, friendly, plain sentences",
			"Ask one thing at a time, and say what it is for and what happens next",
			"do not ask the same thing again",
			"its own background conversation, not in any chat",
			"Choosing it lets\n     nobody in by itself",
			"keep handling everything yourself",
			"you can change this later",
			"do not promise it is free",
			"keep their effects, so do not describe it as unable to change anything",
			"which folder it should work in",
			"Do not choose for them or assume it is you",
			"they can take it back",
		} {
			if !strings.Contains(packet, want) {
				t.Fatalf("packet (%q) lacks %q:\n%s", inviter, want, packet)
			}
		}
	}
}
