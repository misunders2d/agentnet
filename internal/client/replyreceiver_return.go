package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Only the original verified direct request supplies return routing. History,
// replicas and outputs never create a destination or a local execution grant.
func (a *Agent) receiverReturnHost(ctx context.Context, id string) (*ReplyReceiverHost, error) {
	if id == "" {
		return nil, nil
	}
	var from, fp, raw string
	var local, replica bool
	err := a.store.db.QueryRow(`SELECT sender,verified_by,receiver_route,local,replica FROM inbox WHERE id=? AND conv IS NULL AND ref_id IS NULL AND receiver_route IS NOT NULL`, id).Scan(&from, &fp, &raw, &local, &replica)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var route envelope.ReceiverRoute
	if err = json.Unmarshal([]byte(raw), &route); err != nil {
		return nil, err
	}
	if route.Op != "request" || route.RequestRef != id || local || replica {
		return nil, nil
	}
	key, ok, err := pinnedKey(a.store.db, from)
	if err != nil {
		return nil, err
	}
	if !ok || key.Fingerprint() != fp {
		return nil, errors.New("original return-routing author key changed")
	}
	person, err := a.personOfKey(ctx, from, key)
	if err != nil {
		return nil, err
	}
	if !person.has(route.Host, route.HostKey) || !person.has(from, fp) {
		return nil, errors.New("return destination is not the original author's current linked key")
	}
	if route.Host == from {
		return nil, nil
	}
	return &ReplyReceiverHost{Address: route.Host, Fingerprint: route.HostKey}, nil
}
func receiverReturnCurrent(q dbq, request string, host ReplyReceiverHost) error {
	var from, fp, raw string
	if err := q.QueryRow(`SELECT sender,verified_by,receiver_route FROM inbox WHERE id=? AND conv IS NULL AND replica=0 AND local=0`, request).Scan(&from, &fp, &raw); err != nil {
		return err
	}
	var route envelope.ReceiverRoute
	if json.Unmarshal([]byte(raw), &route) != nil || route.Op != "request" || route.RequestRef != request || route.Host != host.Address || route.HostKey != host.Fingerprint {
		return errors.New("return destination changed before reply commit")
	}
	person, found, err := scanPersonIn(q, `person IN (SELECT person FROM person_devices WHERE address=?)`, from)
	if err != nil {
		return err
	}
	if !found || !person.has(from, fp) || !person.has(host.Address, host.Fingerprint) || person.info.State != personPinned && person.info.State != personSelf {
		return errors.New("return destination proof no longer current")
	}
	return nil
}
func (a *Agent) receiverReturnCopy(ctx context.Context, in envelope.Inner, files []OutgoingFile, host ReplyReceiverHost) (outCopy, error) {
	key, err := a.sendKey(ctx, host.Address)
	if err != nil {
		return outCopy{}, err
	}
	if key.Fingerprint() != host.Fingerprint {
		return outCopy{}, errors.New("selected return destination key changed")
	}
	recipient, err := key.Recipient()
	if err != nil {
		return outCopy{}, err
	}
	in.ID = protocol.NewID()
	in.To = host.Address
	in.Attachments = nil
	copy := outCopy{in: in, state: stateQueued, recipientFP: host.Fingerprint}
	for _, file := range files {
		att, e := a.spoolNamed(file, recipient)
		if e != nil {
			a.releaseSpool(envelope.Envelope{Blobs: blobsOf(copy.in.Attachments)})
			return outCopy{}, e
		}
		copy.in.Attachments = append(copy.in.Attachments, att)
	}
	copy.env, err = envelope.Seal(copy.in, a.id.Sign, recipient)
	if err != nil {
		a.releaseSpool(envelope.Envelope{Blobs: blobsOf(copy.in.Attachments)})
	}
	return copy, err
}
