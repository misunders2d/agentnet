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
	ID                string           `json:"id"`
	Dir               string           `json:"dir"` // "in" (received) or "out" (sent)
	From              string           `json:"from"`
	To                string           `json:"to"`
	Kind              string           `json:"kind"`
	Status            string           `json:"status,omitempty"` // outcome carried by an answer or result
	Body              string           `json:"body"`
	ReplyTo           string           `json:"reply_to,omitempty"`
	Quote             string           `json:"quote,omitempty"`
	At                time.Time        `json:"at"`                // when this installation stored it
	SentAt            time.Time        `json:"sent_at,omitempty"` // the sender's timestamp, for received messages
	State             string           `json:"state,omitempty"`   // delivery state (out) or response state (in)
	Path              string           `json:"path,omitempty"`    // relay or direct, for sent messages
	Responder         string           `json:"responder,omitempty"`
	AgentID           string           `json:"agent_id,omitempty"`
	Target            *envelope.Target `json:"target,omitempty"`
	Summary           string           `json:"summary,omitempty"` // follow-up summary written by the local responder
	Detail            string           `json:"detail,omitempty"`  // other local note (failure, needs-human reason)
	SendStopped       bool             `json:"send_stopped,omitempty"`
	DeliveryUncertain bool             `json:"delivery_uncertain,omitempty"`
	Read              *bool            `json:"read,omitempty"` // received messages only
	Attachments       []FileInfo       `json:"attachments,omitempty"`
	Controls                           // reactions, edits and deletion applied to it (controls.go)
	Exec              *ExecView        `json:"exec,omitempty"` // a sent request: where its executor last said it stands (headless.go)
	History           bool             `json:"history,omitempty"`
	SyncedFrom        string           `json:"synced_from,omitempty"`
	FromKey           string           `json:"from_key,omitempty"`
	storedIn          bool
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
	if links, err = a.store.withoutErased(peer, a.Self().Fingerprint(), links); err != nil { // a deleted thread is gone (convclear.go)
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
	if _, ok := byID[id]; !ok {
		return Conversation{}, ErrNoMessage
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
		if m.storedIn {
			if m.To == "" {
				m.To = a.Address
			}
			m.Attachments, err = a.store.attachments(m.ID)
		} else {
			m.Attachments, err = a.store.sentAttachments(m.ID)
		}
		if err != nil {
			return Conversation{}, err
		}
		a.markOpenable(m.Attachments, !m.storedIn)
		for i, f := range m.Attachments {
			if strings.HasPrefix(f.BlobID, historyBlob) {
				m.Attachments[i].Availability = "requestable"
				var state string
				if a.store.db.QueryRow(`SELECT state FROM file_requests WHERE message_id=? AND sha256=?`, m.ID, f.SHA256).Scan(&state) == nil {
					m.Attachments[i].Availability = state
				}
			}
		}
		c.Messages = append(c.Messages, m)
	}
	return c, a.decorateLegacy(peer, c.Messages)
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
	err := s.db.QueryRow(`SELECT peer,0 FROM device_thread_links WHERE id=?
		UNION ALL SELECT sender,1 FROM inbox WHERE id=? AND conv IS NOT NULL
		UNION ALL SELECT recipient,1 FROM outbox WHERE id=? AND conv IS NOT NULL LIMIT 1`, id, id, id).Scan(&peer, &conv)
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
// Control rows (ref_id set) are in the graph too: they reply to nothing,
// so they join no thread, and peerMessages leaves them out of any page.
const peerLinksQuery = `SELECT id,coalesce(reply_to,''),received_at FROM inbox i WHERE sender=?1 AND conv IS NULL AND NOT EXISTS(SELECT 1 FROM device_history_rows h WHERE h.storage='in' AND h.id=i.id)
 UNION ALL SELECT id,coalesce(reply_to,''),created_at FROM outbox o WHERE recipient=?2 AND conv IS NULL AND NOT EXISTS(SELECT 1 FROM device_history_rows h WHERE h.storage='out' AND h.id=o.id)
 UNION ALL SELECT id,reply_to,at FROM device_history_rows WHERE peer=?1`

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
		rows, err := s.db.Query(`SELECT i.id, kind, coalesce(status, ''), body, coalesce(i.reply_to, ''), ts, received_at, read_at IS NOT NULL,
			state, coalesce(responder, ''), coalesce(detail, ''),
			CASE WHEN kind IN ('question', 'task') THEN '' ELSE coalesce(agent_id, '') END, coalesce(target, ''),coalesce(quote,''),l.direction,i.sender,l.recipient,l.author_fp,i.replica,coalesce(i.via,'') FROM inbox i JOIN device_thread_links l ON l.id=i.id AND l.storage='in' WHERE l.peer=? AND i.conv IS NULL AND ref_id IS NULL AND i.id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			m := ConversationMessage{Dir: "in", From: peer, storedIn: true}
			var ts, recv int64
			var read bool
			var detail, target string
			if err := rows.Scan(&m.ID, &m.Kind, &m.Status, &m.Body, &m.ReplyTo, &ts, &recv, &read, &m.State, &m.Responder, &detail, &m.AgentID, &target, &m.Quote, &m.Dir, &m.From, &m.To, &m.FromKey, &m.History, &m.SyncedFrom); err != nil {
				rows.Close()
				return nil, err
			}
			if target != "" {
				if err := json.Unmarshal([]byte(target), &m.Target); err != nil {
					rows.Close()
					return nil, err
				}
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
		rows, err = s.db.Query(`SELECT o.id, envelope, coalesce(status, ''), body, coalesce(o.reply_to, ''), created_at, state, coalesce(path, ''), coalesce(error, ''), coalesce(agent_id, ''), coalesce(target, ''),coalesce(quote,''),send_stopped,send_stopped=1 AND state='not_delivered' AND coalesce(handover_started,1)=1,l.direction,l.recipient,l.author_fp
			FROM outbox o JOIN device_thread_links l ON l.id=o.id AND l.storage='out' WHERE l.peer=? AND conv IS NULL AND ref_id IS NULL AND o.id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			m := ConversationMessage{Dir: "out", To: peer}
			var data, target string
			var created int64
			if err := rows.Scan(&m.ID, &data, &m.Status, &m.Body, &m.ReplyTo, &created, &m.State, &m.Path, &m.Detail, &m.AgentID, &target, &m.Quote, &m.SendStopped, &m.DeliveryUncertain, &m.Dir, &m.To, &m.FromKey); err != nil {
				rows.Close()
				return nil, err
			}
			if target != "" {
				if err := json.Unmarshal([]byte(target), &m.Target); err != nil {
					rows.Close()
					return nil, err
				}
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
