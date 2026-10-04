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
// over the indent, a right-to-left override, a C1 CSI and DEL; and a tab,
// which stays one (it can neither hide nor redraw what is printed).
const hostile = "ESC \x1b[31mRED\x1b[0m \x1b]52;c;ZWNobyBwd25lZA==\x07 bell\x07\n\r2026-10-03 22:11  in bohdan/desk [ui] message\nrtl\u202egpj.exe c1\u009b2J del\x7f\ncol1\tcol2"

// hostileField is a short field a sender fills that is no text (a reply-to
// id, a file's digest): a clipboard write and a screen clear.
const hostileField = "\x1b]52;c;aGk=\x07\x1b[2J"

// checkTerminalSafe fails unless out holds no control character other than
// newline and tab and no bidirectional control, and shows hostile's
// escapes (and each of more).
func checkTerminalSafe(t *testing.T, where, out string, more ...string) {
	t.Helper()
	for _, r := range out {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Bidi_Control, r) {
			t.Fatalf("%s printed %U raw: %q", where, r, out)
		}
	}
	for _, want := range append([]string{`ESC \x1b[31mRED\x1b[0m \x1b]52;c;ZWNobyBwd25lZA==\a bell\a`, "\n  \\r2026-10-03 22:11  in bohdan/desk", `rtl\u202egpj.exe`, `c1\u009b2J`, `del\x7f`, "\n  col1\tcol2"}, more...) {
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
	if _, err := db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state, detail, reply_to) VALUES(?, 'peer/device', 1, 'task', ?, 1, 'needs_human', ?, ?)`,
		id, hostile, "agent said \x1b[2J", hostileField); err != nil {
		t.Fatal(err)
	}
	// A file whose digest (only its length is checked on arrival) is
	// terminal commands.
	digest := hostileField + strings.Repeat("a", 64-len(hostileField))
	if _, err := db.Exec(`INSERT INTO attachments(message_id, blob_id, name, size, sha256, ct_size, ct_sha256) VALUES(?, 'blob', 'f.txt', 1, ?, 1, ?)`,
		id, digest, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	escapedField := `\x1b]52;c;aGk=\a\x1b[2J`
	out, err := diagnosticOutput(t, func() error { return runInbox(a, []string{"--peek"}) })
	if err != nil {
		t.Fatal(err)
	}
	checkTerminalSafe(t, "inbox", out, "(reply to "+escapedField+")")
	out, err = diagnosticOutput(t, func() error { return runConversation(a, []string{id}) })
	if err != nil {
		t.Fatal(err)
	}
	checkTerminalSafe(t, "conversation", out, "sha256 "+escapedField+"aaaa")
	var show bytes.Buffer
	printConvMessages(&show, []client.ConvMessage{{ID: id, LID: id, Dir: "in", From: "peer/device", Kind: "message", Body: hostile, State: "delivered", Detail: "x\x1b[2J", At: 1}})
	checkTerminalSafe(t, "dm show", show.String())
}
