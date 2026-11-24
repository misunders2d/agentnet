package client

import (
	"regexp"
	"strings"
)

const hiddenText = "[Hidden text]"

var spoilerTickRuns = regexp.MustCompile("`+|\n[ \t]*\r?\n")

// MaskSpoilers projects message text for use outside its open body. The
// delimiters and signed body stay intact. Mirrors ui/static/spoilers.mjs.
func MaskSpoilers(text string) string {
	if !strings.Contains(text, "||") {
		return text
	}
	ticks := spoilerTickRuns.FindAllStringIndex(text, -1)
	runs, next := map[int]int{}, map[int]int{}
	paragraph := len(text)
	for i := len(ticks) - 1; i >= 0; i-- {
		start, end := ticks[i][0], ticks[i][1]
		if text[start] == '\n' {
			paragraph = start
			continue
		}
		if later, ok := runs[end-start]; ok && later < paragraph {
			next[start] = later + end - start
		}
		runs[end-start] = start
	}
	var out strings.Builder
	open, copied, fenceSize := -1, 0, 0
	var fence byte
	for i := 0; i < len(text); {
		if i == 0 || text[i-1] == '\n' {
			end := len(text)
			if line := strings.IndexByte(text[i:], '\n'); line >= 0 {
				end = i + line
			}
			p := i
			for p < end && p-i < 4 && text[p] == ' ' {
				p++
			}
			q := p
			var ch byte
			if p < end {
				ch = text[p]
			}
			if p-i < 4 && (ch == '`' || ch == '~') {
				for q < end && text[q] == ch {
					q++
				}
			}
			if fence != 0 {
				if ch == fence && q-p >= fenceSize && strings.TrimRight(strings.TrimSuffix(text[q:end], "\r"), " \t") == "" {
					fence = 0
				}
				i = end + 1
				continue
			}
			if q-p >= 3 && (ch == '~' || !strings.ContainsRune(text[q:end], '`')) {
				fence, fenceSize = ch, q-p
				i = end + 1
				continue
			}
		}
		switch text[i] {
		case '\\':
			i++
			if i < len(text) && text[i] == '|' {
				for i+1 < len(text) && text[i+1] == '|' {
					i++
				}
			}
			i++
		case '`':
			if end, ok := next[i]; ok {
				i = end
			} else {
				i++
				for i < len(text) && text[i] == '`' {
					i++
				}
			}
		case '|':
			end := i + 1
			for end < len(text) && text[end] == '|' {
				end++
			}
			if end-i == 2 {
				if open < 0 {
					open = i
				} else {
					out.WriteString(text[copied:open])
					out.WriteString(hiddenText)
					copied, open = end, -1
				}
			}
			i = end
		default:
			i++
		}
	}
	if copied == 0 {
		return text
	}
	out.WriteString(text[copied:])
	return out.String()
}
