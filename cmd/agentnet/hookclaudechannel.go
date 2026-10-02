package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Frozen official SDK bundle. Runtime install never invokes npm/network, and
// these assets do not enable a native channel or change user MCP configuration.
//
//go:embed hooks/claude-channel/vendor/agentnet.bundle.mjs
var claudeChannelBundle []byte

//go:embed hooks/claude-channel/vendor/LICENSES.txt
var claudeChannelLicenses []byte

//go:embed hooks/claude-channel/vendor/SHA256SUMS
var claudeChannelSums []byte

const claudeBundleMarker = "// agentnet-claude-channel v1\n"
const claudeLicenseMarker = "agentnet-claude-channel licenses v1\n"
const claudeSumMarker = "# agentnet-claude-channel checksums v1\n"

func claudeChannelFiles() (map[string][]byte, error) {
	embedded := map[string][]byte{"agentnet.bundle.mjs": claudeChannelBundle, "LICENSES.txt": claudeChannelLicenses}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(claudeChannelSums)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || seen[fields[1]] {
			return nil, errors.New("invalid embedded Claude channel checksums")
		}
		data, ok := embedded[fields[1]]
		if !ok || fields[0] != fmt.Sprintf("%x", sha256.Sum256(data)) {
			return nil, errors.New("embedded Claude channel checksum mismatch")
		}
		seen[fields[1]] = true
	}
	if len(seen) != 2 {
		return nil, errors.New("embedded Claude channel checksum absent")
	}
	files := map[string][]byte{
		"agentnet.bundle.mjs": append([]byte(claudeBundleMarker), claudeChannelBundle...),
		"LICENSES.txt":        append([]byte(claudeLicenseMarker), claudeChannelLicenses...),
	}
	sums := claudeSumMarker
	for _, name := range []string{"agentnet.bundle.mjs", "LICENSES.txt"} {
		sums += fmt.Sprintf("%x  %s\n", sha256.Sum256(files[name]), name)
	}
	files["SHA256SUMS"] = []byte(sums)
	return files, nil
}

// applyClaudeChannelAssets is the only proposed shared runHooks seam. It returns
// one standard local MCP fragment, never writes .claude.json/settings, launches
// Node/Claude, installs dependencies, closes a receiver or prints private state.
// The shared command owner retains existing metadata-hook merge and stdout.
func applyClaudeChannelAssets(home, action string) (json.RawMessage, error) {
	if action != "show" && action != "install" && action != "remove" {
		return nil, errors.New("invalid Claude channel hook action")
	}
	if runtime.GOOS != "linux" {
		return nil, errors.New("native Claude channel is qualified on Linux only")
	}
	files, e := claudeChannelFiles()
	if e != nil {
		return nil, e
	}
	home, e = filepath.Abs(home)
	if e != nil {
		return nil, e
	}
	dir := filepath.Join(home, "hooks", "claude-channel")
	var config json.RawMessage
	if action != "remove" {
		node, e := exec.LookPath("node")
		if e != nil {
			return nil, errors.New("Claude channel requires local Node; tested with Node26.10.0, no runtime download")
		}
		node, e = filepath.Abs(node)
		if e != nil {
			return nil, e
		}
		binary, e := os.Executable()
		if e != nil {
			return nil, e
		}
		config, e = json.Marshal(map[string]any{"mcpServers": map[string]any{"agentnet": map[string]any{
			"command": node, "args": []string{filepath.Join(dir, "agentnet.bundle.mjs")},
			"env": map[string]string{"AGENTNET_BIN": binary, "AGENTNET_HOME": home},
		}}})
		if e != nil {
			return nil, e
		}
	}
	if action == "show" {
		return config, nil
	}
	// Same owned-file/refuse-to-clobber model as Pi. Preflight every owned path
	// before writing any; foreign files and symlinks are never overwritten.
	for _, parent := range []string{filepath.Dir(dir), dir} {
		stat, e := os.Lstat(parent)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
		if e == nil && (!stat.IsDir() || stat.Mode()&os.ModeSymlink != 0) {
			return nil, fmt.Errorf("%s is not an owned plain channel directory; nothing changed", parent)
		}
	}
	for name, contents := range files {
		file := filepath.Join(dir, name)
		stat, e := os.Lstat(file)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if !stat.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not an owned plain channel file; nothing changed", file)
		}
		old, e := os.ReadFile(file)
		if e != nil {
			return nil, e
		}
		marker := contents[:bytes.IndexByte(contents, '\n')+1]
		if !bytes.HasPrefix(old, marker) {
			return nil, fmt.Errorf("%s exists and was not written by AgentNet; nothing changed", file)
		}
	}
	if action == "install" {
		if e = os.MkdirAll(dir, 0700); e != nil {
			return nil, e
		}
	}
	for _, name := range []string{"agentnet.bundle.mjs", "LICENSES.txt", "SHA256SUMS"} {
		file := filepath.Join(dir, name)
		if action == "remove" {
			if e = os.Remove(file); e != nil && !errors.Is(e, os.ErrNotExist) {
				return nil, e
			}
		} else if e = writeFileAtomic(file, files[name]); e != nil {
			return nil, e
		}
	}
	return config, nil
}
