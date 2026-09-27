package main

import (
	"os/exec"
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
		"sha256sum -c -", "Get-FileHash", "checksum mismatch",
		"agentnet version must print agentnet v0.2.0",
		"git clone --branch v0.2.0",
	} {
		if !strings.Contains(packet, want) {
			t.Fatalf("release packet lacks %q:\n%s", want, packet)
		}
	}
	if strings.Contains(packet, "go build") {
		t.Fatal("release packet asks for Go")
	}
	// The POSIX lines parse.
	var sh []string
	for _, l := range strings.Split(packet, "\n") {
		if strings.HasPrefix(l, "     ") && !strings.Contains(l, "$env:") && !strings.Contains(l, "Invoke-WebRequest") && !strings.Contains(l, "if ($want") {
			sh = append(sh, strings.TrimSpace(l))
		}
		if strings.Contains(l, "Windows (PowerShell)") {
			break
		}
	}
	if out, err := exec.Command("sh", "-n", "-c", strings.Join(sh, "\n")).CombinedOutput(); err != nil {
		t.Fatalf("shell steps do not parse: %v %s\n%s", err, out, strings.Join(sh, "\n"))
	}

	for _, dev := range []string{"dev", "v0.2.0-mvp.1+570b58d", "570b58d"} {
		protocol.Version = dev
		packet, _ := invitePacket(code, "admin/laptop")
		if strings.Contains(packet, "releases/download") || !strings.Contains(packet, "go build -trimpath") {
			t.Fatalf("%s: expected the source build steps:\n%s", dev, packet)
		}
	}
}
