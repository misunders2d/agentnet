package ui

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

// Topics (client topics.go, docs/plans/TOPICS.md): an agent's separate
// conversations. GET /api/overview?topics=1 lists the active and done
// ones; archived topics are counted per peer (PeerTopics) and listed a page
// at a time through GET /api/topics. Without the flag the overview lists
// every thread, archived topics too (a page that knows nothing of topics
// still reaches them all). A name and Mark done / Reopen are set through
// POST /api/topic/{rename,done,reopen}. Names, Mark done / Reopen and Archive
// sync across own linked human devices (client topicsync.go, topicstatesync.go).

// Topic states (ThreadSummary.State).
const (
	TopicActive   = client.TopicActive
	TopicDone     = client.TopicDone
	TopicArchived = client.TopicArchived
)

// Topics is implemented by providers that keep topic state: the paged list
// and the person's own changes.
type Topics interface {
	TopicOverview() (Overview, error) // Overview with archived topics counted, not listed
	TopicList(TopicQuery) (TopicPage, error)
	ChangeTopic(what string, c TopicChange) (string, error)
}

// PeerTopics is one peer's topics as the overview counts them (archived
// ones are counted, not listed): Latest is the most recently active topic,
// whatever its state, so a peer whose topics are all archived still has
// its chat.
type PeerTopics struct {
	Peer           string        `json:"peer"`
	Total          int           `json:"total"`
	Archived       int           `json:"archived"`
	ArchivedUnread int           `json:"archived_unread"`
	Latest         ThreadSummary `json:"latest"`
}

// TopicQuery is GET /api/topics: conv scopes a people chat; otherwise peer
// scopes agent chats ("" every peer). State ("" every
// state, active, done or archived), q (words in a title or the last
// line), before (the Next of the page before) and limit (0: the default
// page; at most client.TopicPageMax).
type TopicQuery struct {
	Conv   string `json:"conv,omitempty"`
	Peer   string `json:"peer,omitempty"`
	State  string `json:"state,omitempty"`
	Q      string `json:"q,omitempty"`
	Before string `json:"before,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// TopicPage is one page of topics, most recently active first; Matched
// counts the topics matching on every page.
type TopicPage struct {
	Topics  []ThreadSummary `json:"topics"`
	Next    string          `json:"next,omitempty"`
	Matched int             `json:"matched"`
}

// TopicChange names a topic by Conv or Peer and ThreadSummary.ID for POST
// /api/topic/{create,rename,done,reopen,archive,delete}. Rename uses Title
// ("" gives back the automatic name). IDs batches done/archive/delete;
// Counts holds each selected topic's displayed count. Count is how many
// messages a single topic's page showed (its
// ThreadSummary.Count; 0: all it has): a mark never covers a message the
// person has not seen.
type TopicChange struct {
	Root   bool           `json:"root,omitempty"`
	Conv   string         `json:"conv,omitempty"`
	IDs    []string       `json:"ids,omitempty"`
	Counts map[string]int `json:"counts,omitempty"`
	Peer   string         `json:"peer"`
	ID     string         `json:"id"`
	Title  string         `json:"title,omitempty"`
	Count  int            `json:"count,omitempty"`
}

// topicNewer is the note when a message came after what the page showed
// Mark done on: the mark is kept but does not hold, that message is newer.
const topicNewer = "A newer message came in, so the topic stays active. Read it, then mark it done again."

// Topic changes (POST /api/topic/{what}).
const (
	TopicRename = "rename"
	TopicMark   = "done"
	TopicReopen = "reopen"
)

// topics implements GET /api/topics.
func (s *Server) topics(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Topics)
	if !ok {
		writeErr(w, NotFound("topics are not listed here"))
		return
	}
	v := r.URL.Query()
	q := TopicQuery{Conv: v.Get("conv"), Peer: v.Get("peer"), State: v.Get("state"), Q: v.Get("q"), Before: v.Get("before")}
	if l := v.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil {
			writeErr(w, Refuse("That topic list request is not valid."))
			return
		}
		q.Limit = n
	}
	page, err := p.TopicList(q)
	writeResult(w, page, err)
}

// changeTopic implements POST /api/topic/{what}.
func (s *Server) changeTopic(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Topics)
	if !ok {
		writeErr(w, NotFound("topics cannot be changed here"))
		return
	}
	var c TopicChange
	if !readJSON(w, r, &c) {
		return
	}
	note, err := p.ChangeTopic(r.PathValue("what"), c)
	writeResult(w, map[string]string{"note": note}, err)
}

// threadSummary is a client thread summary as the page lists it.
func threadSummary(t client.ThreadSummary, keyChanged bool) ThreadSummary {
	return ThreadSummary{Conv: t.Conv, ID: t.ID, Peer: t.Peer, Title: t.Title, Last: t.Last, LastAt: t.LastAt,
		Count: t.Count, Review: t.Review, Unread: t.Unread, Running: t.Running, Waiting: t.Waiting, Unconfirmed: t.Unconfirmed, KeyChanged: keyChanged,
		Notices: t.Notices, NoticeOnly: t.NoticeOnly, State: t.State, DoneBy: t.DoneBy, Conclusion: t.Conclusion,
		ConcludedBy: t.ConcludedBy, Pending: t.Pending, PendingIDs: t.PendingIDs, Renamed: t.Renamed, AutoTitle: t.AutoTitle, QuietSince: t.QuietSince, AgentID: t.AgentID, Redirect: t.Redirect}
}

// TopicList implements Topics.
func (l *Live) TopicList(q TopicQuery) (TopicPage, error) {
	page, err := l.a.Topics(client.TopicQuery{Conv: q.Conv, Peer: q.Peer, State: q.State, Query: q.Q, Before: q.Before, Limit: q.Limit})
	if errors.Is(err, client.ErrTopicQuery) {
		return TopicPage{}, Refuse("That topic list request is not valid.")
	}
	if err != nil {
		return TopicPage{}, err
	}
	out := TopicPage{Topics: []ThreadSummary{}, Next: page.Next, Matched: page.Matched}
	changed := map[string]bool{}
	for _, t := range page.Topics {
		kc, seen := changed[t.Peer]
		if !seen && t.Conv == "" {
			k, err := l.a.PeerKeyOf(t.Peer)
			if err != nil {
				return out, err
			}
			kc = k.Pending != ""
			changed[t.Peer] = kc
		}
		out.Topics = append(out.Topics, threadSummary(t, kc))
	}
	return out, nil
}

// ChangeTopic implements Topics.
func (l *Live) ChangeTopic(what string, c TopicChange) (string, error) {
	if c.Root {
		if c.Conv == "" || c.Peer != "" || c.ID != "" || len(c.IDs) > 0 || len(c.Counts) > 0 {
			return "", Refuse("Invalid Main flow change.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
		defer cancel()
		covered, err := l.a.ChangeChatMainTopic(client.WithQueuedSend(ctx, ""), c.Conv, what, c.Title, c.Count)
		if err != nil {
			return "", err
		}
		if !covered {
			return "Newer Main flow messages remain active.", nil
		}
		return map[string]string{"rename": "Main flow renamed across your linked devices.", "archive": "Archived. It syncs across your linked devices. Nothing deleted.", "reopen": "Reopened. It syncs across your linked devices.", "delete": "Deleted for you and your devices. Other people keep their copies."}[what], nil
	}

	if len(c.IDs) > 0 {
		if len(c.IDs) > client.TopicPageMax || what != "done" && what != "archive" && what != "delete" {
			return "", Refuse("Invalid bulk action.")
		}
		changed, stale := 0, 0
		seen := map[string]bool{}
		for _, id := range c.IDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			one := c
			one.IDs = nil
			one.ID = id
			if c.Counts != nil {
				one.Count = c.Counts[id]
				if one.Count < 1 {
					return "", Refuse("Invalid topic count.")
				}
			}
			note, err := l.ChangeTopic(what, one)
			if err != nil {
				return "", Refuse(strconv.Itoa(changed) + " topics changed; remaining topics unchanged: " + sentence(err))
			}
			if note == topicNewer {
				stale++
			} else {
				changed++
			}
		}
		note := strconv.Itoa(changed) + " topics changed."
		if stale > 0 {
			note += " " + strconv.Itoa(stale) + " topics kept active because newer messages arrived."
		}
		return note, nil
	}
	if c.Conv != "" {
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
		defer cancel()
		ctx = client.WithQueuedSend(ctx, "")
		covered, err := l.a.ChangeChatTopic(ctx, c.Conv, c.ID, what, c.Title, c.Count)
		if err != nil {
			return "", err
		}
		if !covered {
			return topicNewer, nil
		}
		note := map[string]string{"create": "Topic created. Its replies stay here.", "done": "Marked done for everyone. A new message reopens it.", "reopen": "Reopened for everyone.", "rename": "Topic renamed. The name syncs across your linked devices.", "archive": "Archived. It syncs across your linked devices. Nothing deleted.", "delete": "Deleted for you and your devices. Other people keep their copies."}[what]
		if what == TopicRename && strings.Join(strings.Fields(c.Title), " ") == "" {
			note = "Topic named after its first message again."
		}
		return note, nil
	}
	var err error
	note, covered := "", true
	switch what {
	case "archive":
		err = l.a.ArchiveTopic(c.Peer, c.ID)
		note = "Archived. It syncs across your linked devices. Nothing deleted."
	case "delete":
		_, err = l.a.DeleteThread(c.Peer, c.ID)
		note = "Deleted on this device. Others keep their copies."
	case TopicRename:
		err = l.a.RenameTopic(c.Peer, c.ID, c.Title)
		note = "Topic renamed. The name syncs across your linked devices."
		if strings.Join(strings.Fields(c.Title), " ") == "" { // as RenameTopic reads it
			note = "Topic named after its first message again."
		}
	case TopicMark:
		covered, err = l.a.MarkTopicDone(c.Peer, c.ID, c.Count)
		note = "Marked done. It syncs across your linked devices; a new message makes it active again."
	case TopicReopen:
		covered, err = l.a.ReopenTopic(c.Peer, c.ID, c.Count)
		note = "Reopened. It syncs across your linked devices."
	default:
		return "", NotFound("no such topic change")
	}
	switch {
	case errors.Is(err, client.ErrNoMessage):
		return "", NotFound("no such topic here")
	case errors.Is(err, client.ErrTopicTitle):
		return "", Refuse(sentence(err) + ".")
	case err != nil:
		return "", err
	}
	if !covered && what == TopicMark { // Reopen: active either way
		note = topicNewer
	}
	l.a.NoteChange()
	return note, nil
}
