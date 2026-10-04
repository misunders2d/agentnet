package ui

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/misunders2d/agentnet/internal/client"
)

// Topics (client topics.go, docs/plans/TOPICS.md): an agent's separate
// conversations. The overview lists the active and done ones; archived
// topics are counted per peer (PeerTopics) and listed a page at a time
// through GET /api/topics. A name and Mark done / Reopen are set through
// POST /api/topic/{rename,done,reopen} and kept on this device only.

// Topic states (ThreadSummary.State).
const (
	TopicActive   = client.TopicActive
	TopicDone     = client.TopicDone
	TopicArchived = client.TopicArchived
)

// Topics is implemented by providers that keep topic state: the paged list
// and the person's own changes.
type Topics interface {
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

// TopicQuery is GET /api/topics: peer ("" every peer), state ("" every
// state, active, done or archived), q (words in a title or the last
// line), before (the Next of the page before) and limit (0: the default
// page; at most client.TopicPageMax).
type TopicQuery struct {
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

// TopicChange names a topic (its peer and ThreadSummary.ID) for POST
// /api/topic/rename (Title; "" gives back the automatic name), /done and
// /reopen.
type TopicChange struct {
	Peer  string `json:"peer"`
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

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
	q := TopicQuery{Peer: v.Get("peer"), State: v.Get("state"), Q: v.Get("q"), Before: v.Get("before")}
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
	return ThreadSummary{ID: t.ID, Peer: t.Peer, Title: t.Title, Last: t.Last, LastAt: t.LastAt,
		Count: t.Count, Review: t.Review, Unread: t.Unread, Running: t.Running, Waiting: t.Waiting, KeyChanged: keyChanged,
		Notices: t.Notices, NoticeOnly: t.NoticeOnly, State: t.State, DoneBy: t.DoneBy, Conclusion: t.Conclusion,
		ConcludedBy: t.ConcludedBy, Pending: t.Pending, Renamed: t.Renamed, AutoTitle: t.AutoTitle}
}

// TopicList implements Topics.
func (l *Live) TopicList(q TopicQuery) (TopicPage, error) {
	page, err := l.a.Topics(client.TopicQuery{Peer: q.Peer, State: q.State, Query: q.Q, Before: q.Before, Limit: q.Limit})
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
		if !seen {
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
	var err error
	note := ""
	switch what {
	case TopicRename:
		err = l.a.RenameTopic(c.Peer, c.ID, c.Title)
		note = "Topic renamed on this device."
		if c.Title == "" {
			note = "Topic named after its first message again."
		}
	case TopicMark:
		err = l.a.MarkTopicDone(c.Peer, c.ID)
		note = "Marked done on this device. A new message makes it active again."
	case TopicReopen:
		err = l.a.ReopenTopic(c.Peer, c.ID)
		note = "Reopened on this device."
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
	l.a.NoteChange()
	return note, nil
}
