package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const (
	maxNoticeMetadata  = 32768
	maxNoticeReminders = 256
	maxNoticeRestore   = 32
)

// Private native-host data, not a message, permission or sync contract.
type noticeMetadata struct {
	V         int                     `json:"v"`
	Workspace string                  `json:"workspace"`
	Tag       string                  `json:"tag"`
	Instance  string                  `json:"instance"`
	Category  string                  `json:"category"`
	Ref       *protocol.ReadRef       `json:"ref,omitempty"`
	Reminders []client.NativeReminder `json:"reminders,omitempty"`
}

func canonicalNotice(workspace, fragment string) (tag, route string, err error) {
	bad := errors.New("invalid native notification destination")
	if workspace != client.DefaultWorkspace && !protocol.ValidID(workspace) || len(fragment) > 2048 || strings.Contains(fragment, "#") {
		return "", "", bad
	}
	q, err := url.ParseQuery(fragment)
	if err != nil {
		return "", "", bad
	}
	for k, values := range q {
		if len(values) != 1 || k != "workspace" && k != "review" && k != "msg" && k != "dir" && k != "conv" {
			return "", "", bad
		}
	}
	if w, present := q["workspace"]; present && w[0] != workspace {
		return "", "", bad
	}
	delete(q, "workspace")
	if conv, ok := q["conv"]; ok && !protocol.ValidHash(conv[0]) {
		return "", "", bad
	}
	if msg, ok := q["msg"]; ok && !protocol.ValidID(msg[0]) {
		return "", "", bad
	}
	if dir, ok := q["dir"]; ok && (q.Get("msg") == "" || dir[0] != "in" && dir[0] != "out") {
		return "", "", bad
	}
	switch {
	case len(q) == 0:
	case q.Has("review"):
		if len(q) != 1 || q.Get("review") != "" {
			return "", "", bad
		}
		route = "review"
	case q.Has("msg"):
		route = "msg=" + q.Get("msg")
		if q.Has("dir") {
			route += "&dir=" + q.Get("dir")
		}
	case len(q) == 1 && q.Has("conv"):
		route = "conv=" + q.Get("conv")
	default:
		return "", "", bad
	}
	tag = "workspace=" + workspace
	if route != "" {
		tag = route + "&" + tag
	}
	return tag, route, nil
}

func validNoticeRef(ref protocol.ReadRef) bool {
	return (ref.Conv == "" || protocol.ValidHash(ref.Conv)) && protocol.ValidFingerprint(ref.Fingerprint) && protocol.ValidID(ref.LID)
}

func (m noticeMetadata) valid(workspace, tag string) bool {
	if m.V != 1 || m.Workspace != workspace || m.Tag != tag || !protocol.ValidID(m.Instance) || len(m.Reminders) > maxNoticeReminders {
		return false
	}
	if m.Ref != nil && !validNoticeRef(*m.Ref) {
		return false
	}
	seen := map[string]bool{}
	for _, r := range m.Reminders {
		if !protocol.ValidID(r.Message) || r.Revision < 1 || r.Revision > protocol.MaxAttentionRevision || seen[r.Message] {
			return false
		}
		seen[r.Message] = true
	}
	switch m.Category {
	case "activity":
		return m.Ref != nil && len(m.Reminders) == 0
	case "reminder":
		return len(m.Reminders) > 0
	case "reminder-summary":
		return m.Ref == nil && len(m.Reminders) == 0
	case "decision", "security", "unknown":
		return len(m.Reminders) == 0
	}
	return false
}

func decodeNoticeJSON(raw string, target any) error {
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid native notification metadata")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid native notification metadata")
	}
	return nil
}

func parseNoticeMetadata(raw, workspace, tag string) (noticeMetadata, error) {
	var m noticeMetadata
	var fields map[string]json.RawMessage
	if len(raw) > maxNoticeMetadata || json.Unmarshal([]byte(raw), &fields) != nil || decodeNoticeJSON(raw, &m) != nil || !m.valid(workspace, tag) {
		return m, errors.New("invalid native notification metadata")
	}
	for _, field := range []string{"ref", "reminders"} {
		if value, present := fields[field]; present && string(value) == "null" {
			return m, errors.New("invalid native notification metadata")
		}
	}
	return m, nil
}

func newNoticeMetadata(workspace string, n client.NativeNotification) (noticeMetadata, string) {
	tag, _, _ := canonicalNotice(workspace, n.Fragment)
	m := noticeMetadata{V: 1, Workspace: workspace, Tag: tag, Instance: protocol.NewID(), Category: n.Category, Reminders: n.Reminders}
	if n.ReadRef.Fingerprint != "" {
		ref := n.ReadRef
		m.Ref = &ref
	}
	raw, _ := json.Marshal(m)
	// Preserve an explicitly unknown notice if its complete generation set
	// cannot fit. Never silently truncate an aggregate into another generation.
	if len(raw) > maxNoticeMetadata || !m.valid(workspace, tag) {
		m.Category, m.Ref, m.Reminders = "unknown", nil, nil
		raw, _ = json.Marshal(m)
	}
	return m, string(raw)
}

type restoredNotice struct {
	Workspace string `json:"workspace"`
	Tag       string `json:"tag"`
	Metadata  string `json:"metadata"`
}

func (entry *restoredNotice) UnmarshalJSON(raw []byte) error {
	var value struct {
		Workspace *string `json:"workspace"`
		Tag       *string `json:"tag"`
		Metadata  *string `json:"metadata"`
	}
	if decodeNoticeJSON(string(raw), &value) != nil || value.Workspace == nil || value.Tag == nil || value.Metadata == nil {
		return errors.New("invalid native notification snapshot entry")
	}
	*entry = restoredNotice{*value.Workspace, *value.Tag, *value.Metadata}
	return nil
}

type noticeSnapshot struct {
	V       int              `json:"v"`
	Known   *bool            `json:"known"`
	Unknown int              `json:"unknown"`
	Entries []restoredNotice `json:"entries"`
}

// RestoreNotificationsJSON imports one bounded startup snapshot chunk. Empty
// metadata means a pre-metadata legacy tag; malformed metadata is never legacy.
// Import only schedules existing Session reconciliation and never displays.
func (a *App) RestoreNotificationsJSON(raw string) (string, error) {
	var snapshot noticeSnapshot
	if len(raw) > (maxNoticeMetadata+4096)*maxNoticeRestore || decodeNoticeJSON(raw, &snapshot) != nil || snapshot.V != 1 || snapshot.Known == nil || snapshot.Unknown < 0 || snapshot.Unknown > 1000000 || len(snapshot.Entries) > maxNoticeRestore || !*snapshot.Known && len(snapshot.Entries) != 0 {
		return "", errors.New("invalid native notification snapshot")
	}
	imported, unknown := 0, snapshot.Unknown
	for _, entry := range snapshot.Entries {
		tag, route, err := canonicalNotice(entry.Workspace, entry.Tag)
		if err != nil || tag != entry.Tag || len(entry.Metadata) > maxNoticeMetadata {
			unknown++
			continue
		}
		var m noticeMetadata
		if entry.Metadata != "" {
			m, err = parseNoticeMetadata(entry.Metadata, entry.Workspace, tag)
			if err != nil {
				unknown++
				continue
			}
		}
		q, _ := url.ParseQuery(route)
		notice := phoneNotice{fragment: route, category: "legacy", id: q.Get("msg")}
		if entry.Metadata == "" && (!protocol.ValidID(notice.id) || q.Get("dir") != "in" && q.Get("dir") != "out") {
			unknown++
		}
		if entry.Metadata != "" {
			notice.instance, notice.category, notice.reminders = m.Instance, m.Category, m.Reminders
			if m.Ref != nil {
				notice.ref = *m.Ref
			}
			if m.Category == "unknown" {
				unknown++
			}
		}
		a.mu.Lock()
		s := a.sessions[entry.Workspace]
		if a.closed || !a.active[entry.Workspace] || s == nil {
			a.mu.Unlock()
			unknown++
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			a.mu.Unlock()
			unknown++
			continue
		}
		s.restoredNotices = append(s.restoredNotices, notice)
		s.mu.Unlock()
		a.mu.Unlock()
		s.signal()
		imported++
	}
	return marshal(struct {
		Restored int  `json:"restored"`
		Unknown  int  `json:"unknown"`
		Known    bool `json:"known"`
	}{imported, unknown, *snapshot.Known}, nil)
}
