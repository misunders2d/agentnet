//go:build !linux && !windows

package main

// hasBattery is not looked at here: a Mac is named "mac" either way.
func hasBattery() bool { return false }
