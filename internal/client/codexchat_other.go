//go:build !linux

package client

import "errors"

func trustedInstall(string) error { return nil }

func findCodexDesktopEngine(string) (codexEngine, error) {
	return codexEngine{}, errors.New("Codex Desktop chat delivery is qualified on Linux only")
}

type fileWrites struct{ changes chan struct{} }

func watchFileWrites(string) (*fileWrites, error) {
	return nil, errors.New("Codex Desktop chat delivery is qualified on Linux only")
}
func (w *fileWrites) close() {}
