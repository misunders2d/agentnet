package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// ErrNotSent means no message with that id was sent from here.
var ErrNotSent = errors.New("no such sent message")

// Sent is a message this agent sent and what is known about it: its
// delivery state and, once received, the reply that answers it.
type Sent struct {
	ID        string
	To        string
	Kind      string
	Body      string
	State     string // queued, custody, delivered, quarantined, expired or failed
	Path      string
	CreatedAt time.Time
	Reply     *Message // nil until the recipient's answer, result or reply arrives
}

// SentMessage looks up a message this agent sent.
func (a *Agent) SentMessage(id string) (Sent, error) {
	r, err := a.store.sent(id)
	if errors.Is(err, sql.ErrNoRows) {
		return Sent{}, ErrNotSent
	}
	if err != nil {
		return Sent{}, err
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(r.Envelope), &env); err != nil {
		return Sent{}, fmt.Errorf("stored message %s: %w", id, err)
	}
	s := Sent{ID: r.ID, To: r.To, Kind: env.Kind, Body: r.Body, State: r.State, Path: r.Path, CreatedAt: time.Unix(r.Created, 0)}
	replyID, err := a.store.replyTo(id, r.To)
	if err != nil || replyID == "" {
		return s, err
	}
	s.Reply, err = a.store.inboxMessage(replyID)
	return s, err
}
