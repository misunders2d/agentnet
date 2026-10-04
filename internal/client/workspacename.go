package client

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrWorkspaceName and ErrDeviceName refuse a name before anything is
// recorded for it: a joining record with such a name could never enroll.
var (
	ErrWorkspaceName = errors.New("workspace name must be 1–120 readable characters")
	ErrDeviceName    = errors.New("device name must be lowercase letters, digits and dashes, starting with a letter, at most 32 characters (like laptop or work-phone)")
)

// workspaceName is name as a display alias, or ErrWorkspaceName.
func workspaceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 120 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", ErrWorkspaceName
	}
	return name, nil
}

// Rename changes only this installation's existing display alias. Identity,
// realm, endpoint, state, enrollment, grants and histories remain untouched.
func (w *Workspaces) Rename(id, name string) (Workspace, error) {
	name, err := workspaceName(name)
	if err != nil {
		return Workspace{}, err
	}
	var result Workspace
	err = w.modify(func(r *workspaceRegistry) error {
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
