package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// ConversationMessage is one message of a conversation, sent or received,
// as stored locally. Summary and Detail are local notes about the message,
// never part of what the peer wrote.
type ConversationMessage struct {
	ID          string     `json:"id"`
	Dir         string     `json:"dir"` // "in" (received) or "out" (sent)
	From        string     `json:"from"`
	To          string     `json:"to"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status,omitempty"` // outcome carried by an answer or result
	Body        string     `json:"body"`
	ReplyTo     string     `json:"reply_to,omitempty"`
	At          time.Time  `json:"at"`                // when this installation stored it
	SentAt      time.Time  `json:"sent_at,omitempty"` // the sender's timestamp, for received messages
	State       string     `json:"state,omitempty"`   // delivery state (out) or response state (in)
	Path        string     `json:"path,omitempty"`    // relay or direct, for sent messages
	Responder   string     `json:"responder,omitempty"`
	Summary     string     `json:"summary,omitempty"` // follow-up summary written by the local responder
	Detail      string     `json:"detail,omitempty"`  // other local note (failure, needs-human reason)
	Read        *bool      `json:"read,omitempty"`    // received messages only
	Attachments []FileInfo `json:"attachments,omitempty"`
}

// Conversation is one page of a conversation with one peer.
type Conversation struct {
	Peer     string                `json:"peer"`
	Total    int                   `json:"total"`
	Offset   int                   `json:"offset"`
	Messages []ConversationMessage `json:"messages"`
}

// ErrNoMessage means the id is neither a received nor a sent message.
var ErrNoMessage = errors.New("no message with that id")

// Conversation returns the conversation containing message id: every sent
// and received message linked to it through replies, in both directions,
// with the same peer, oldest first. Links to messages with anyone else, or
// to unknown ids, end the conversation there, so a peer cannot pull in
// other conversations by naming their ids. It changes nothing (not even
// read state). limit <= 0 means all.
func (a *Agent) Conversation(id string, offset, limit int) (Conversation, error) {
	peer, err := a.store.peerOf(id)
	if err != nil {
		return Conversation{}, err
	}
	// Find the conversation from ids and links alone (covering indexes, no
	// bodies), then load full messages only for the requested page.
	links, err := a.store.peerLinks(peer)
	if err != nil {
		return Conversation{}, err
	}
	byID := map[string]link{}
	children := map[string][]string{}
	for _, l := range links {
		byID[l.id] = l
		if l.replyTo != "" {
			children[l.replyTo] = append(children[l.replyTo], l.id)
		}
	}
	// Walk links both ways from id; the visited set ends cycles.
	seen := map[string]bool{id: true}
	order := []string{id}
	for i := 0; i < len(order); i++ {
		l := byID[order[i]]
		next := children[l.id]
		if _, ok := byID[l.replyTo]; ok {
			next = append(next, l.replyTo)
		}
		for _, n := range next {
			if !seen[n] {
				seen[n] = true
				order = append(order, n)
			}
		}
	}
	sortThread(order, byID)
	c := Conversation{Peer: peer, Total: len(order), Offset: min(max(offset, 0), len(order))}
	page := order[c.Offset:]
	if limit > 0 && len(page) > limit {
		page = page[:limit]
	}
	full, err := a.store.peerMessages(peer, page)
	if err != nil {
		return Conversation{}, err
	}
	for _, id := range page {
		m, ok := full[id]
		if !ok {
			continue // removed meanwhile
		}
		if m.Dir == "in" {
			m.To = a.Address
			m.Attachments, err = a.store.attachments(m.ID)
		} else {
			m.Attachments, err = a.store.sentAttachments(m.ID)
		}
		if err != nil {
			return Conversation{}, err
		}
		c.Messages = append(c.Messages, m)
	}
	return c, nil
}

// sortThread orders one thread's messages oldest first. Times have whole
// seconds, so a reply stored in the same second as its parent is placed
// after it by its depth in the thread.
func sortThread(order []string, byID map[string]link) {
	in := make(map[string]bool, len(order))
	for _, id := range order {
		in[id] = true
	}
	depth := map[string]int{}
	var depthOf func(id string, guard int) int
	depthOf = func(id string, guard int) int {
		if d, ok := depth[id]; ok {
			return d
		}
		d := 0
		if p := byID[id].replyTo; in[p] && guard > 0 {
			d = depthOf(p, guard-1) + 1
		}
		depth[id] = d
		return d
	}
	for _, id := range order {
		depthOf(id, len(order))
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := byID[order[i]], byID[order[j]]
		if a.at != b.at {
			return a.at < b.at
		}
		if depth[a.id] != depth[b.id] {
			return depth[a.id] < depth[b.id]
		}
		return a.id < b.id
	})
}

// CheckReplyTo refuses to link a new message to id unless id is a message
// stored here (sent or received) whose other party is the recipient to, so
// a reply can only continue a conversation with that same agent.
func (a *Agent) CheckReplyTo(id, to string) error {
	addr, _, err := protocol.SplitTarget(to)
	if err != nil {
		return err
	}
	peer, err := a.store.peerOf(id)
	if errors.Is(err, ErrNoMessage) {
		return fmt.Errorf("no message %s here to reply to", id)
	}
	if errors.Is(err, ErrConversationItem) {
		return err
	}
	if err != nil {
		return err
	}
	if peer != addr {
		return fmt.Errorf("message %s is with %s, not %s: a reply continues the conversation with the same agent", id, peer, addr)
	}
	return nil
}

// peerOf returns the other party of a stored message. A conversation (DM)
// message is not part of any reply thread: it is ErrConversationItem.
func (s *store) peerOf(id string) (string, error) {
	var peer string
	var conv bool
	err := s.db.QueryRow(`SELECT sender, conv IS NOT NULL FROM inbox WHERE id = ?
		UNION ALL SELECT recipient, conv IS NOT NULL FROM outbox WHERE id = ? LIMIT 1`, id, id).Scan(&peer, &conv)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoMessage
	}
	if err == nil && conv {
		return "", ErrConversationItem
	}
	return peer, err
}

// link is a message's place in the reply graph.
type link struct {
	id, replyTo string
	at          int64
}

// peerLinksQuery reads the link indexes (schema step 8), never message
// bodies. Conversation (DM) messages are not in reply threads: a link to
// one ends the thread there, like a link to an unknown id.
const peerLinksQuery = `SELECT id, coalesce(reply_to, ''), received_at FROM inbox INDEXED BY inbox_links WHERE sender = ? AND conv IS NULL
	UNION ALL SELECT id, coalesce(reply_to, ''), created_at FROM outbox INDEXED BY outbox_links WHERE recipient = ? AND conv IS NULL`

// peerLinks returns the reply links of every message exchanged with peer.
func (s *store) peerLinks(peer string) ([]link, error) {
	rows, err := s.db.Query(peerLinksQuery, peer, peer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.id, &l.replyTo, &l.at); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// peerMessages loads the messages ids exchanged with peer, both directions.
func (s *store) peerMessages(peer string, ids []string) (map[string]ConversationMessage, error) {
	out := map[string]ConversationMessage{}
	for len(ids) > 0 {
		chunk := ids[:min(len(ids), 500)]
		ids = ids[len(chunk):]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := []any{peer}
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.db.Query(`SELECT id, kind, coalesce(status, ''), body, coalesce(reply_to, ''), ts, received_at, read_at IS NOT NULL,
			state, coalesce(responder, ''), coalesce(detail, '') FROM inbox WHERE sender = ? AND conv IS NULL AND id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			m := ConversationMessage{Dir: "in", From: peer}
			var ts, recv int64
			var read bool
			var detail string
			if err := rows.Scan(&m.ID, &m.Kind, &m.Status, &m.Body, &m.ReplyTo, &ts, &recv, &read, &m.State, &m.Responder, &detail); err != nil {
				rows.Close()
				return nil, err
			}
			m.SentAt, m.At, m.Read = time.Unix(ts, 0), time.Unix(recv, 0), &read
			if m.State == stateSummary {
				m.Summary = detail
			} else {
				m.Detail = detail
			}
			out[m.ID] = m
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		rows, err = s.db.Query(`SELECT id, envelope, coalesce(status, ''), body, coalesce(reply_to, ''), created_at, state, coalesce(path, ''), coalesce(error, '')
			FROM outbox WHERE recipient = ? AND conv IS NULL AND id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			m := ConversationMessage{Dir: "out", To: peer}
			var data string
			var created int64
			if err := rows.Scan(&m.ID, &data, &m.Status, &m.Body, &m.ReplyTo, &created, &m.State, &m.Path, &m.Detail); err != nil {
				rows.Close()
				return nil, err
			}
			var env envelope.Envelope
			if err := json.Unmarshal([]byte(data), &env); err != nil {
				rows.Close()
				return nil, fmt.Errorf("sent message %s: %w", m.ID, err)
			}
			m.From, m.Kind, m.At = env.From, env.Kind, time.Unix(created, 0)
			out[m.ID] = m
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sentAttachments returns the manifest recorded when a message was sent.
// Messages sent before it was recorded have none.
func (s *store) sentAttachments(messageID string) ([]FileInfo, error) {
	rows, err := s.db.Query(`SELECT blob_id, name, size, sha256 FROM sent_attachments WHERE message_id = ? ORDER BY rowid`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileInfo
	for rows.Next() {
		var f FileInfo
		if err := rows.Scan(&f.BlobID, &f.Name, &f.Size, &f.SHA256); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
