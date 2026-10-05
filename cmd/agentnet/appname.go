package main

import (
	"strconv"
	"strings"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// autoDeviceName is the name a computer joins with in the AgentNet app,
// chosen for it: people never name devices. A laptop is a computer with a
// system battery (hasBattery, per platform).
func autoDeviceName(goos string, battery bool) string {
	switch goos {
	case "windows":
		if battery {
			return "windows-laptop"
		}
		return "windows-pc"
	case "darwin":
		return "mac"
	case "linux":
		if battery {
			return "linux-laptop"
		}
		return "linux-pc"
	}
	return "computer"
}

// deviceWords says an automatic device name in words, for the first-run
// page ("Linux laptop"); other names are shown with spaces for dashes.
func deviceWords(name string) string {
	base := name
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		if _, err := strconv.Atoi(name[i+1:]); err == nil {
			base = name[:i]
		}
	}
	if w, ok := map[string]string{"windows-laptop": "Windows laptop", "windows-pc": "Windows computer", "linux-laptop": "Linux laptop",
		"linux-pc": "Linux computer", "mac": "Mac", "computer": "computer"}[base]; ok {
		return w
	}
	return strings.ReplaceAll(name, "-", " ")
}

// nameCandidates are the names tried in turn when the server says a name is
// taken: base, base-2, base-3 … (n in all), each shortened to stay a valid
// name. internal/ui/static/wire.mjs nameCandidates makes the same list.
func nameCandidates(base string, n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		c := base
		if i > 1 {
			suffix := "-" + strconv.Itoa(i)
			if max := protocol.MaxName - len(suffix); len(c) > max {
				c = strings.TrimRight(c[:max], "-")
			}
			c += suffix
		}
		out = append(out, c)
	}
	return out
}
