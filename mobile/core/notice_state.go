package core

import (
	"net/url"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui"
)

// Emitted notices and the OS startup snapshot retain identity, never previews.
type phoneNotice struct {
	fragment, category, id, instance string
	ref                              protocol.ReadRef
	reminders                        []client.NativeReminder
}

// Android's existing tag deliberately excludes a message's routing hint.
func noticeKey(fragment string) string {
	query, err := url.ParseQuery(fragment)
	if err == nil && protocol.ValidID(query.Get("msg")) {
		return "msg=" + query.Get("msg") + "&dir=" + query.Get("dir")
	}
	return fragment
}

func (s *Session) trackNotice(workspace string, n client.NativeNotification) string {
	if s.notices == nil {
		s.notices = map[string]phoneNotice{}
	}
	query, _ := url.ParseQuery(n.Fragment)
	m, raw := newNoticeMetadata(workspace, n)
	s.notices[noticeKey(n.Fragment)] = phoneNotice{fragment: n.Fragment, category: m.Category, id: query.Get("msg"), instance: m.Instance, ref: n.ReadRef, reminders: m.Reminders}
	return raw
}

func (s *Session) importNotices(restored []phoneNotice) {
	for _, notice := range restored {
		if s.notices == nil {
			s.notices = map[string]phoneNotice{}
		}
		key := noticeKey(notice.fragment)
		if _, current := s.notices[key]; !current {
			s.notices[key] = notice
		}
	}
}

// Called with App.mu held. Binding generations fence retained/disconnected
// sessions as well as replacements of the provider for the same session.
func (a *App) phoneCurrentLocked(id string, s *Session, generation uint64) bool {
	if a.closed || !a.active[id] || a.sessions[id] != s {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.contentGeneration == generation
}

// The Session event lane serializes display, replacement and reconciliation.
// Reuse the overview already read for the badge and its compact unread index;
// only displayed reminder targets need an exact local generation lookup.
func (a *App) refreshNotices(id string, s *Session, content, generation uint64, view ui.Overview) {
	if len(s.notices) == 0 {
		return
	}
	s.data.RLock()
	if s.available() != nil {
		s.data.RUnlock()
		return
	}
	refs := []protocol.ReadRef{}
	for key, notice := range s.notices {
		if notice.category == "legacy" && notice.ref.Fingerprint == "" {
			q, _ := url.ParseQuery(notice.fragment)
			ref, err := s.a.NativeNotificationRef(notice.id, q.Get("dir"))
			if err == nil && validNoticeRef(ref) {
				notice.ref = ref
				s.notices[key] = notice
			}
		}
		if (notice.category == "activity" || notice.category == "legacy") && notice.ref.Fingerprint != "" {
			refs = append(refs, notice.ref)
		}
	}
	unread := map[protocol.ReadRef]bool{}
	for start := 0; start < len(refs); start += protocol.MaxNotifySeenIDs {
		batch, err := s.live.UnreadNotificationRefs(content, refs[start:min(start+protocol.MaxNotifySeenIDs, len(refs))])
		if err != nil {
			s.data.RUnlock()
			return
		}
		for ref, active := range batch {
			unread[ref] = active
		}
	}
	review := map[string]bool{}
	for _, item := range view.Review {
		review[item.ID] = true
	}
	for _, item := range view.NeedsYou {
		review[item.ID] = true
	}
	for _, item := range view.Held {
		review[item.ID] = true
	}
	stale := []string{}
	for key, notice := range s.notices {
		// A reminder can replace an aggregate decision on the shared Review
		// tag. Keep that exact tag while any current decision still needs it.
		if review[notice.id] || key == "review" && len(review) > 0 {
			continue
		}
		keep := true // An unknown/system notice cannot be cleared by a chat read.
		switch notice.category {
		case "activity":
			if notice.ref.Fingerprint != "" {
				keep = unread[notice.ref]
			}
		case "legacy":
			// A legacy tag might have been activity, a decision or a reminder.
			// Clear only when every possible cause is definitively absent.
			if notice.ref.Fingerprint != "" {
				reminder, found, err := s.a.Reminder(notice.id)
				if err != nil {
					s.data.RUnlock()
					return
				}
				keep = unread[notice.ref] || found && reminder.State == client.ReminderPending && reminder.Overdue && reminder.Alerted
			}
		case "decision":
			keep = notice.id == "" && len(review) > 0
		case "reminder":
			if len(notice.reminders) == 0 {
				continue // No captured generation: never infer it from current state.
			}
			keep = false
			for _, target := range notice.reminders {
				reminder, found, err := s.a.Reminder(target.Message)
				if err != nil {
					s.data.RUnlock()
					return
				}
				keep = keep || found && reminder.State == client.ReminderPending && reminder.Overdue && reminder.Alerted && reminder.Revision == target.Revision
			}
		case "reminder-summary":
			var err error
			keep, err = s.a.HasAlertedDueReminders()
			if err != nil {
				s.data.RUnlock()
				return
			}
		}
		if !keep {
			stale = append(stale, key)
		}
	}
	s.data.RUnlock()
	a.mu.Lock()
	current := a.phoneCurrentLocked(id, s, generation)
	revision, _ := s.a.ViewChanges()
	n := a.notifier
	a.mu.Unlock()
	if !current || revision.Content != content || n == nil {
		return
	}
	for _, key := range stale {
		notice := s.notices[key]
		delete(s.notices, key)
		n.Dismiss(id, notice.fragment, notice.instance)
	}
}
