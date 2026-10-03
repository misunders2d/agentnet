package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/misunders2d/agentnet/internal/client"
)

// hostile is text a remote sender could write: colours, an OSC 52
// clipboard write, bells, a carriage return that would draw a fake header
// over the indent, a right-to-left override, a C1 CSI and DEL.
const hostile = "ESC \x1b[31mRED\x1b[0m \x1b]52;c;ZWNobyBwd25lZA==\x07 bell\x07\n\r2026-10-03 22:11  in bohdan/desk [ui] message\nrtl\u202egpj.exe c1\u009b2J del\x7f"

// checkTerminalSafe fails unless out holds no control character other than
// newline and no bidirectional control, and shows hostile's escapes.
func checkTerminalSafe(t *testing.T, where, out string) {
	t.Helper()
	for _, r := range out {
		if (unicode.IsControl(r) && r != '\n') || unicode.Is(unicode.Bidi_Control, r) {
			t.Fatalf("%s printed %U raw: %q", where, r, out)
		}
	}
	for _, want := range []string{`ESC \x1b[31mRED\x1b[0m \x1b]52;c;ZWNobyBwd25lZA==\a bell\a`, "\n  \\r2026-10-03 22:11  in bohdan/desk", `rtl\u202egpj.exe`, `c1\u009b2J`, `del\x7f`} {
		if !strings.Contains(out, want) {
			t.Fatalf("%s does not show %q: %q", where, want, out)
		}
	}
}

// What a sender wrote reaches the terminal as text, never as terminal
// commands, in every printer of message text (inbox, conversation, dm
// show); a newline stays the body's indented newline.
func TestTerminalOutputEscapesSenderControls(t *testing.T) {
	a, home := diagnosticAgent(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const id = "33333333333333333333333333333333"
	if _, err := db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state, detail) VALUES(?, 'peer/device', 1, 'task', ?, 1, 'needs_human', ?)`,
		id, hostile, "agent said \x1b[2J"); err != nil {
		t.Fatal(err)
	}
	out, err := diagnosticOutput(t, func() error { return runInbox(a, []string{"--peek"}) })
	if err != nil {
		t.Fatal(err)
	}
	checkTerminalSafe(t, "inbox", out)
	out, err = diagnosticOutput(t, func() error { return runConversation(a, []string{id}) })
	if err != nil {
		t.Fatal(err)
	}
	checkTerminalSafe(t, "conversation", out)
	var show bytes.Buffer
	printConvMessages(&show, []client.ConvMessage{{ID: id, LID: id, Dir: "in", From: "peer/device", Kind: "message", Body: hostile, State: "delivered", Detail: "x\x1b[2J", At: 1}})
	checkTerminalSafe(t, "dm show", show.String())
}
