package client

import (
	"sort"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Read-only views for the local messenger page. Threads are derived from
// today's reply links on every read: nothing new is stored, so a message
// whose parent arrives late simply joins its thread next time.

// ThreadSummary is one reply-linked conversation with one peer.
type ThreadSummary struct {
	ID      string    `json:"id"` // earliest message of the thread (derived, not stored)
	Peer    string    `json:"peer"`
	Title   string    `json:"title"` // first line of the first message
	Last    string    `json:"last"`  // first line of the latest message
	LastAt  time.Time `json:"last_at"`
	Count   int       `json:"count"`
	Review  int       `json:"review"`  // received items waiting for a decision here
	Unread  int       `json:"unread"`  // received messages not yet read (review notices not counted)
	Running int       `json:"running"` // received items the worker is on
	Waiting bool      `json:"waiting"` // a question or task sent here has no reply yet
	// Notices counts open review notices: reports from another machine that
	// requests wait for a person there. They are not decisions here.
	Notices    int  `json:"notices"`
	NoticeOnly bool `json:"notice_only"` // every message in the thread is a review notice
}

// threadRow is what a summary needs to know about one message.
type threadRow struct {
	link
	in      bool
	kind    string
	state   string
	unread  bool
	notice  bool // in only: a review notice (see envelope.StatusReviewNotice)
	replied bool // out only: a received message replies to it
}

// Threads lists every thread with every peer, most recent first.
func (a *Agent) Threads() ([]ThreadSummary, error) {
	peers, err := a.store.conversationPeers()
	if err != nil {
		return nil, err
	}
	var out []ThreadSummary
	for _, peer := range peers {
		ts, err := a.peerThreads(peer)
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastAt.Equal(out[j].LastAt) {
			return out[i].LastAt.After(out[j].LastAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (a *Agent) peerThreads(peer string) ([]ThreadSummary, error) {
	links, err := a.store.peerLinks(peer)
	if err != nil {
		return nil, err
	}
	rows, err := a.store.threadRows(peer)
	if err != nil {
		return nil, err
	}
	// Union reply-linked messages into threads.
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for _, l := range links {
		parent[l.id] = l.id
	}
	for _, l := range links {
		if _, ok := parent[l.replyTo]; ok && l.replyTo != "" {
			parent[find(l.id)] = find(l.replyTo)
		}
	}
	byID := map[string]link{}
	groups := map[string][]string{}
	for _, l := range links {
		byID[l.id] = l
		r := find(l.id)
		groups[r] = append(groups[r], l.id)
	}
	var out []ThreadSummary
	for _, g := range groups {
		sortThread(g, byID)
		first, last := byID[g[0]], byID[g[len(g)-1]]
		t := ThreadSummary{ID: first.id, Peer: peer, Count: len(g), LastAt: time.Unix(last.at, 0), NoticeOnly: true}
		for _, id := range g {
			r := rows[id]
			t.NoticeOnly = t.NoticeOnly && r.notice
			switch {
			case r.notice:
				if r.state == stateNeedHuman {
					t.Notices++
				}
				continue
			case r.in && (r.state == stateHeld || r.state == stateAwaiting || r.state == stateNeedHuman):
				t.Review++
			case r.in && (r.state == stateRunning || r.state == stateCancelReq):
				t.Running++
			}
			if r.in && r.unread {
				t.Unread++
			}
			if !r.in && (r.kind == envelope.KindQuestion || r.kind == envelope.KindTask) && !r.replied {
				t.Waiting = true
			}
		}
		full, err := a.store.peerMessages(peer, []string{first.id, last.id})
		if err != nil {
			return nil, err
		}
		t.Title, t.Last = firstLine(full[first.id].Body), firstLine(full[last.id].Body)
		out = append(out, t)
	}
	return out, nil
}

// conversationPeers lists every address this installation has exchanged
// messages with.
func (s *store) conversationPeers() ([]string, error) {
	rows, err := s.db.Query(`SELECT sender FROM inbox UNION SELECT recipient FROM outbox`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// threadRows reads direction, kind, state and read state for every message
// exchanged with peer, and which sent questions or tasks have a reply.
func (s *store) threadRows(peer string) (map[string]threadRow, error) {
	out := map[string]threadRow{}
	rows, err := s.db.Query(`SELECT id, kind, state, read_at IS NULL, coalesce(reply_to, ''), coalesce(status, '') = ? FROM inbox WHERE sender = ?`,
		envelope.StatusReviewNotice, peer)
	if err != nil {
		return nil, err
	}
	replies := map[string]bool{}
	for rows.Next() {
		var r threadRow
		if err := rows.Scan(&r.id, &r.kind, &r.state, &r.unread, &r.replyTo, &r.notice); err != nil {
			rows.Close()
			return nil, err
		}
		r.in = true
		r.notice = r.notice && r.kind == envelope.KindMessage
		out[r.id] = r
		if r.replyTo != "" {
			replies[r.replyTo] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT id, coalesce(json_extract(envelope, '$.kind'), '') FROM outbox WHERE recipient = ?`, peer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r threadRow
		if err := rows.Scan(&r.id, &r.kind); err != nil {
			return nil, err
		}
		r.replied = replies[r.id]
		out[r.id] = r
	}
	return out, rows.Err()
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// Quarantined is a received envelope held back from the inbox.
type Quarantined struct {
	ID         string    `json:"id"`
	Sender     string    `json:"sender"`
	Reason     string    `json:"reason"` // "key_changed" (waits for trust) or "invalid"
	ReceivedAt time.Time `json:"received_at"`
}

// Quarantine lists held-back envelopes, newest first. Their content is not
// shown: it did not verify, or the sender's key changed.
func (a *Agent) Quarantine() ([]Quarantined, error) {
	rows, err := a.store.db.Query(`SELECT id, sender, reason, received_at FROM quarantine ORDER BY received_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Quarantined
	for rows.Next() {
		var q Quarantined
		var at int64
		if err := rows.Scan(&q.ID, &q.Sender, &q.Reason, &at); err != nil {
			return nil, err
		}
		q.ReceivedAt = time.Unix(at, 0)
		out = append(out, q)
	}
	return out, rows.Err()
}

// PeerKey is what this installation knows about a peer's keys.
type PeerKey struct {
	Known   bool   `json:"known"`
	Pinned  string `json:"pinned,omitempty"`  // fingerprint of the pinned key
	Pending string `json:"pending,omitempty"` // fingerprint of a changed key waiting for trust
}

// PeerKeyOf reports the pinned and any pending key of address, locally.
func (a *Agent) PeerKeyOf(address string) (PeerKey, error) {
	pinned, pending, found, err := a.store.peer(address)
	if err != nil || !found {
		return PeerKey{}, err
	}
	k := PeerKey{Known: true, Pinned: pinned.Fingerprint()}
	if pending != nil {
		k.Pending = pending.Fingerprint()
	}
	return k, nil
}

// MarkRead marks received messages as read.
func (a *Agent) MarkRead(ids []string) error {
	if err := a.store.markRead(ids); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
}
