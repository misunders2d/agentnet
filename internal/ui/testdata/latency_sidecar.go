//go:build ignore

// Test-only portable sidecar for the disposable native responsiveness shell.
package main

import (
	"bufio"
	"encoding/json"
	"net/url"
	"os"
)

func main() {
	page := os.Getenv("AGENTNET_FIXTURE_URL")
	u, err := url.Parse(page)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		os.Exit(2)
	}
	if json.NewEncoder(os.Stdout).Encode(map[string]string{"event": "page", "mode": "daemon", "url": page}) != nil {
		os.Exit(2)
	}
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		if input.Text() == "quit" {
			return
		}
	}
}
