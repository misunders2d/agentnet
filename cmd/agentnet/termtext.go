package main

import (
	"strconv"
	"strings"
	"unicode"
)

// termText makes text that came from elsewhere (a message body, a note, a
// label, a title) safe to print on a terminal: every control character
// (C0, DEL, C1) other than newline, and every bidirectional formatting
// character, is printed escaped the way Go quotes it (\x1b, \r, \u202e).
// Text can then neither drive the terminal (colours, the clipboard,
// clearing it, moving the cursor over earlier lines) nor reorder what is
// printed around it. Each newline is followed by indent, so a body stays
// under its header. Quoted names (%q) are escaped by fmt already.
func termText(s, indent string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteByte('\n')
			b.WriteString(indent)
		case unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r):
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
