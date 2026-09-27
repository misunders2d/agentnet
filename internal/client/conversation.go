package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
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
	all, err := a.store.peerMessages(peer)
	if err != nil {
		return Conversation{}, err
	}
	byID := map[string]*ConversationMessage{}
	children := map[string][]string{}
	for i := range all {
		m := &all[i]
		byID[m.ID] = m
		if m.ReplyTo != "" {
			children[m.ReplyTo] = append(children[m.ReplyTo], m.ID)
		}
	}
	// Walk links both ways from id; the visited set ends cycles.
	seen := map[string]bool{id: true}
	for queue := []string{id}; len(queue) > 0; queue = queue[1:] {
		m := byID[queue[0]]
		next := children[m.ID]
		if _, ok := byID[m.ReplyTo]; ok {
			next = append(next, m.ReplyTo)
		}
		for _, n := range next {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	depth := func(m *ConversationMessage) int {
		d := 0
		for p, ok := byID[m.ReplyTo]; ok && seen[p.ID] && d <= len(seen); p, ok = byID[p.ReplyTo] {
			d++
		}
		return d
	}
	var msgs []ConversationMessage
	depths := map[string]int{}
	for i := range all {
		if seen[all[i].ID] {
			msgs = append(msgs, all[i])
			depths[all[i].ID] = depth(&all[i])
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		if !msgs[i].At.Equal(msgs[j].At) {
			return msgs[i].At.Before(msgs[j].At)
		}
		if depths[msgs[i].ID] != depths[msgs[j].ID] {
			return depths[msgs[i].ID] < depths[msgs[j].ID]
		}
		return msgs[i].ID < msgs[j].ID
	})
	c := Conversation{Peer: peer, Total: len(msgs), Offset: min(max(offset, 0), len(msgs))}
	msgs = msgs[c.Offset:]
	if limit > 0 && len(msgs) > limit {
		msgs = msgs[:limit]
	}
	c.Messages = msgs
	for i := range c.Messages {
		m := &c.Messages[i]
		if m.Dir == "in" {
			m.To = a.Address
			m.Attachments, err = a.store.attachments(m.ID)
		} else {
			m.Attachments, err = a.store.sentAttachments(m.ID)
		}
		if err != nil {
			return Conversation{}, err
		}
	}
	return c, nil
}

// peerOf returns the other party of a stored message.
func (s *store) peerOf(id string) (string, error) {
	var peer string
	err := s.db.QueryRow(`SELECT sender FROM inbox WHERE id = ? UNION ALL SELECT recipient FROM outbox WHERE id = ? LIMIT 1`, id, id).Scan(&peer)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoMessage
	}
	return peer, err
}

// peerMessages loads every message exchanged with peer, both directions.
func (s *store) peerMessages(peer string) ([]ConversationMessage, error) {
	var out []ConversationMessage
	rows, err := s.db.Query(`SELECT id, kind, coalesce(status, ''), body, coalesce(reply_to, ''), ts, received_at, read_at IS NOT NULL,
		state, coalesce(responder, ''), coalesce(detail, '') FROM inbox WHERE sender = ?`, peer)
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
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT id, envelope, coalesce(status, ''), body, coalesce(reply_to, ''), created_at, state, coalesce(path, ''), coalesce(error, '')
		FROM outbox WHERE recipient = ?`, peer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		m := ConversationMessage{Dir: "out", To: peer}
		var data string
		var created int64
		if err := rows.Scan(&m.ID, &data, &m.Status, &m.Body, &m.ReplyTo, &created, &m.State, &m.Path, &m.Detail); err != nil {
			return nil, err
		}
		var env envelope.Envelope
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			return nil, fmt.Errorf("sent message %s: %w", m.ID, err)
		}
		m.From, m.Kind, m.At = env.From, env.Kind, time.Unix(created, 0)
		out = append(out, m)
	}
	return out, rows.Err()
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
