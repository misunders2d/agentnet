//go:build tools

// Package tools pins the official native Go binding toolchain independently
// of the AgentNet runtime module.
package tools

import (
	_ "golang.org/x/mobile/cmd/gobind"
	_ "golang.org/x/mobile/cmd/gomobile"
)
