//go:build !linux

package client

// runTestHarnessParent: only Linux kills a harness with its daemon
// (runproc_linux_test.go).
func runTestHarnessParent(string) int { return 2 }
