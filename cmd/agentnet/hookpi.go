package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// piExtension is AgentNet's Pi extension, with the agentnet binary and home
// filled in at install (hooks/agentnet-pi.ts). Pi loads every file in its
// extensions directory, so AgentNet writes exactly one file there and never
// a backup.
//
//go:embed hooks/agentnet-pi.ts
var piExtension []byte

// piMarker is the first line of every AgentNet Pi extension; only a file
// starting with it is replaced or removed.
var piMarker = []byte("// agentnet-pi-extension ")

// piExtensionPath is where Pi loads user extensions from.
func piExtensionPath() (string, error) {
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".pi", "agent")
	}
	return filepath.Join(dir, "extensions", "agentnet.ts"), nil
}

// renderPiExtension fills in this agentnet binary and home as string
// literals (JSON strings are valid TypeScript string literals).
func renderPiExtension(home string) ([]byte, error) {
	return renderNativeExtension(home, "pi")
}
func renderNativeExtension(home, harness string) ([]byte, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("hooks are not supported on Windows yet")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return nil, err
	}
	if home, err = filepath.Abs(home); err != nil {
		return nil, err
	}
	bin, _ := json.Marshal(exe)
	h, _ := json.Marshal(home)
	out := bytes.Replace(piExtension, []byte(`"__AGENTNET_BIN__"`), bin, 1)
	out = bytes.Replace(out, []byte(`"__AGENTNET_HOME__"`), h, 1)
	native, _ := json.Marshal(harness)
	out = bytes.Replace(out, []byte(`"__AGENTNET_HARNESS__"`), native, 1)
	return out, nil
}

// runPiHooks shows, installs or removes AgentNet's Pi extension. It never
// replaces or removes a file it did not write.
func runPiHooks(home, action string, rest []string) error {
	return runNativeHooks(home, "pi", action, rest)
}
func runNativeHooks(home, harness, action string, rest []string) error {
	usage := errors.New("usage: hooks show|install|remove pi|omp [--file PATH]")
	file := ""
	switch {
	case len(rest) == 2 && rest[0] == "--file":
		file = rest[1]
	case len(rest) != 0:
		return usage
	}
	ext, err := renderNativeExtension(home, harness)
	if err != nil {
		return err
	}
	if action == "show" {
		os.Stdout.Write(ext)
		return nil
	}
	if action != "install" && action != "remove" {
		return usage
	}
	if file == "" {
		if harness == "omp" {
			return errors.New("OMP extension install/remove requires an explicit --file PATH")
		}
		if file, err = piExtensionPath(); err != nil {
			return err
		}
	}
	old, err := os.ReadFile(file)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if exists && !bytes.HasPrefix(old, piMarker) {
		return fmt.Errorf("%s exists and was not written by AgentNet; nothing changed", file)
	}
	if action == "remove" {
		if !exists {
			fmt.Printf("%s: no AgentNet Pi extension\n", file)
			return nil
		}
		if err := os.Remove(file); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", file)
		fmt.Println("next: restart Pi; running sessions keep the extension until then")
		return nil
	}
	if exists && bytes.Equal(old, ext) {
		fmt.Printf("%s already up to date\n", file)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(file, ext); err != nil {
		return err
	}
	fmt.Printf("wrote %s: AgentNet notices for Pi (session start, prompt, end of run, and while idle)\n", file)
	fmt.Println("next: start a new Pi session; it is loaded only when Pi starts, and background AgentNet runs stay silent")
	return nil
}
