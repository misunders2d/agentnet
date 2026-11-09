//go:build !linux

package client

import "errors"

// Run origins need Linux peer credentials and /proc lineage.
func (a *Agent) listenRunOrigin() (func(), error) {
	return nil, errors.New("the run origin socket is supported only on Linux")
}

func procLive(int) (int, uint64, bool) { return 0, 0, false }
