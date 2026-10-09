package client

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Private setup has no visible reply choice or human-scoped turn. Use the
// protocol constants here so queue ordering and history agree on its type.
const privateReceiverSetupOutbox = `conv IS NULL AND reply_receiver IS NULL AND
	(coalesce(required_cap,'')='` + protocol.CapReplyReceiver + `' OR
	coalesce(required_cap,'')='` + protocol.CapHumanParticipation + `' AND human IS NULL)`

// Direction is part of source identity. Never join an inbound same-ID row to
// an unrelated outbound commitment, or turn forwarded metadata into setup.
func receiverStoredRoute(q dbq, dir, id string) (*envelope.ReceiverRoute, error) {
	if dir == "in" {
		var raw sql.NullString
		if err := q.QueryRow(`SELECT receiver_route FROM inbox WHERE id=?`, id).Scan(&raw); err != nil {
			return nil, err
		}
		if !raw.Valid || raw.String == "" {
			return nil, nil
		}
		var route envelope.ReceiverRoute
		if err := json.Unmarshal([]byte(raw.String), &route); err != nil {
			return nil, err
		}
		return &route, route.Validate()
	}
	if dir != "out" {
		return nil, errors.New("receiver history requires exact source direction")
	}
	b, _, err := receiverOriginCopy(q, id)
	if err != nil || b == nil {
		return nil, err
	}
	route := b.remote.Route
	route.Op = "request"
	return &route, route.Validate()
}

func receiverHistoryRoute(q dbq, in envelope.Inner, key string) error {
	r := in.ReceiverRoute
	if r == nil {
		return nil
	}
	if r.Op != "request" {
		return errors.New("receiver setup cannot be forwarded as conversation history")
	}
	if err := envelope.ValidateReceiverRoute(in); err != nil {
		return err
	}
	rows, err := q.Query(`SELECT c.record FROM person_chain c JOIN persons p ON p.person=c.person WHERE p.state IN (?,?) ORDER BY c.seq DESC`, personPinned, personSelf)
	if err != nil {
		return err
	}
	proved := false
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var roster protocol.PersonRoster
		if json.Unmarshal([]byte(raw), &roster) != nil {
			continue
		}
		author, aok := roster.Device(key)
		host, hok := roster.Device(r.HostKey)
		if aok && hok && author.Address == in.From && host.Address == r.Host {
			proved = true
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if !proved {
		return errors.New("historical return destination lacks verified original-person chain provenance")
	}
	return nil // provenance only; addHistoryInbox never creates input/binding/grant
}
