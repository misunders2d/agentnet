package client

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rename changes only this installation's existing display alias. Identity,
// realm, endpoint, state, enrollment, grants and histories remain untouched.
func (w *Workspaces) Rename(id, name string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 120 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return Workspace{}, errors.New("workspace name must be 1–120 readable characters")
	}
	var result Workspace
	err := w.modify(func(r *workspaceRegistry) error {
		for i := range r.Items {
			if r.Items[i].ID == id && r.Items[i].State == "enrolled" {
				r.Items[i].Name = name
				result = r.Items[i]
				return nil
			}
		}
		return ErrWorkspaceUnknown
	})
	return result, err
}
