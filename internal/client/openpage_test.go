package client

import (
	"strings"
	"testing"
)

// In the AgentNet app (RunOptions.OpenPage), every click opens the app's
// window on its item: alerts on their conversation, reviews on the review
// list or the message, never a terminal or a browser tab, whatever the
// platform and whether or not a terminal launcher exists.
func TestOpenPageClicksOpenTheApp(t *testing.T) {
	w := newWorld(t, "")
	stubTerminal(t)
	var asked []string
	w.bob.openPage = func(fragment string) []string {
		asked = append(asked, fragment)
		return []string{"/opt/AgentNet.AppImage", "agentnet://open#" + fragment}
	}
	w.bob.openConv = func(string) []string { t.Fatal("OpenConv used in the app"); return nil }
	for _, os := range []string{"linux", "windows", "darwin"} {
		old := clickOS
		clickOS = os
		for target, want := range map[string]string{"": "review", strings.Repeat("a", 32): "msg=" + strings.Repeat("a", 32) + "&dir=in"} {
			argv, click := w.bob.reviewClick(target)
			if len(argv) != 2 || argv[0] != "/opt/AgentNet.AppImage" || argv[1] != "agentnet://open#"+want || click == nil {
				t.Fatalf("%s review click %q: %v", os, target, argv)
			}
		}
		clickOS = old
	}
	conv := strings.Repeat("c", 64)
	if argv, click := w.bob.convClick(conv); len(argv) != 2 || argv[1] != "agentnet://open#conv="+conv || click == nil {
		t.Fatalf("alert click: %v", argv)
	}
	if argv, _ := w.bob.convClick("not a conversation"); len(argv) != 2 || argv[1] != "agentnet://open#" {
		t.Fatalf("alert click without a conversation: %v", argv)
	}
	if asked[len(asked)-1] != "" {
		t.Fatalf("fragments asked: %q", asked)
	}
	// An app that cannot be started from here (no command) gives a banner
	// only, never the terminal.
	w.bob.openPage = func(string) []string { return nil }
	if argv, click := w.bob.reviewClick(""); argv != nil || click != nil {
		t.Fatalf("fell back to %v", argv)
	}
}
