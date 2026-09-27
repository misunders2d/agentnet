package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

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
