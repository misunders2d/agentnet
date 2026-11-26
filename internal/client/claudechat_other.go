//go:build !linux

package client

import (
	"errors"
	"net"
	"os"
)

type processExit struct{ done chan struct{} }

func watchProcessExit(int, uint64) (*processExit, error) {
	return nil, errors.New("Claude chat delivery is qualified on Linux only")
}
func (e *processExit) close() {}
func peerPID(net.Conn) (int, error) {
	return 0, errors.New("Claude chat delivery is qualified on Linux only")
}
func ownedByMe(os.FileInfo) bool { return false }
