package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// keepDaemonInstallDirOnPath lets a service-started daemon find responder
// launchers installed beside agentnet. Service managers commonly give the
// daemon only a system PATH; an in-place agentnet update keeps that environment.
func keepDaemonInstallDirOnPath(started string) {
	path := started
	if !filepath.IsAbs(path) {
		found, err := exec.LookPath(path)
		if err != nil {
			return
		}
		path = found
	}
	dir := filepath.Clean(filepath.Dir(path))
	if dir == "." || filepath.Dir(dir) == dir {
		return
	}
	current := os.Getenv("PATH")
	for _, entry := range filepath.SplitList(current) {
		if filepath.Clean(entry) == dir {
			return
		}
	}
	if current == "" {
		_ = os.Setenv("PATH", dir)
		return
	}
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+current)
}
