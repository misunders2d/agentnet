package protocol

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The workspace name is a label the Hub's admin gives the workspace
// ("Mellanni"), shown on every member's devices. It is never identity: the
// realm id is that, and a member may still label it locally.

// MaxWorkspaceName bounds a workspace name, in characters.
const MaxWorkspaceName = 120

// ValidWorkspaceName returns name trimmed and whether it is a workspace
// name: 1–MaxWorkspaceName characters, none a control character (C0, DEL
// or C1).
func ValidWorkspaceName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxWorkspaceName || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", false
	}
	return name, true
}

// WorkspaceNameRequest sets the workspace name (PUT /v1/admin/workspace,
// admin only); an empty Name clears it.
type WorkspaceNameRequest struct {
	Name string `json:"name"`
}
