package client

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

// This is presentation grammar, matching skins/shared/collective-mentions.mjs
// and model.ts. It neither resolves recipients nor grants any authority.
var previewMention = regexp.MustCompile(`\[@([^\[\]\r\n]{1,80})\]\((agentnet:[^\s()]+)\)`)

// BodyPreview makes complete canonical mention labels readable before bounding
// a message's first line. Literal names must use their existing plain clipping.
func BodyPreview(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	line = previewMention.ReplaceAllStringFunc(line, func(anchor string) string {
		parts := previewMention.FindStringSubmatch(anchor)
		units := 0
		for _, r := range parts[1] {
			units += utf16.RuneLen(r)
		}
		if units > 80 || !previewMentionTarget(parts[2]) {
			return anchor
		}
		return "@" + parts[1]
	})
	return firstLine(line)
}

func previewMentionTarget(href string) bool {
	const collective = "agentnet:collective/v1/"
	if payload, ok := strings.CutPrefix(href, collective); ok {
		if len(payload) == 0 || len(payload) > 280000 {
			return false
		}
		refs := strings.Split(payload, ",")
		if len(refs) > 4096 {
			return false
		}
		for _, ref := range refs {
			if len(ref) < 3 || ref[1] != '.' || !strings.ContainsRune("pga", rune(ref[0])) || !previewMentionID(ref[2:]) {
				return false
			}
		}
		return true
	}
	for _, kind := range []string{"person", "guest", "agent"} {
		if id, ok := strings.CutPrefix(href, "agentnet:"+kind+"/"); ok {
			return previewMentionID(id)
		}
	}
	return false
}

func previewMentionID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
