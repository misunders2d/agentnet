//go:build !windows

package client

import "errors"

// startInOwnConsole is Windows-only: elsewhere a terminal launcher gives the
// program its window.
func startInOwnConsole(argv []string, dir string) error {
	return errors.New("a console of its own is a Windows launch")
}
