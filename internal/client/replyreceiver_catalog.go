package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// ReplyReceiverCatalog is authenticated registration data, never liveness.
type ReplyReceiverCatalog struct {
	Host     string                     `json:"host"`
	HostKey  string                     `json:"host_key"`
	Local    bool                       `json:"local"`
	Sessions []envelope.ReceiverSession `json:"sessions"`
	Status   string                     `json:"status"`
	At       int64                      `json:"at,omitempty"`
	Detail   string                     `json:"detail,omitempty"`
}

func (a *Agent) ReplyReceiverCatalog(ctx context.Context, host ReplyReceiverHost) (ReplyReceiverCatalog, error) {
	v := ReplyReceiverCatalog{Host: host.Address, HostKey: host.Fingerprint, Sessions: []envelope.ReceiverSession{}, Status: "pending"}
	key, err := replyReceiverHostIn(a.store.db, host)
	if err != nil {
		return v, err
	}
	if host.Address == a.Address {
		rows, e := a.ReplySessions()
		if e != nil {
			return v, e
		}
		v.Local = true
		v.Status = "ready"
		for _, s := range rows {
			v.Sessions = append(v.Sessions, envelope.ReceiverSession{Handle: s.Handle, Harness: s.Harness, Label: s.Label, Active: s.Active})
		}
		return v, nil
	}
	rows, err := a.store.db.Query(`SELECT id FROM outbox WHERE recipient=? AND recipient_fp=? AND required_cap=? AND conv IS NULL AND coalesce(reply_to,'')='' AND body=? ORDER BY created_at DESC,id DESC`, host.Address, host.Fingerprint, protocol.CapReplyReceiver, `{"v":1}`)
	if err != nil {
		return v, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return v, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	for _, id := range ids {
		var body, raw, from, fp string
		var at int64
		e := a.store.db.QueryRow(`SELECT body,receiver_route,sender,verified_by,received_at FROM inbox WHERE reply_to=? AND receiver_route IS NOT NULL ORDER BY arrival DESC LIMIT 1`, id).Scan(&body, &raw, &from, &fp, &at)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return v, e
		}
		var route envelope.ReceiverRoute
		if json.Unmarshal([]byte(raw), &route) != nil || route.Op != "catalog" || route.Host != host.Address || route.HostKey != host.Fingerprint || from != host.Address || fp != host.Fingerprint {
			continue
		}
		op, e := envelope.ParseReceiverOperation([]byte(body), route, id, nil)
		if e != nil {
			return v, e
		}
		v.Sessions = op.Sessions
		if v.Sessions == nil {
			v.Sessions = []envelope.ReceiverSession{}
		}
		v.Status = "ready"
		v.At = at
		return v, nil
	}
	if len(ids) > 0 {
		state, detail, _, e := a.store.outboxState(ids[0])
		if e != nil {
			return v, e
		}
		if state == stateFailed || state == stateNotDelivered {
			v.Status = "unavailable"
			v.Detail = detail
		}
		return v, nil
	} // outstanding request: repeated GET never sends another
	route := envelope.ReceiverRoute{Op: "catalog", Host: host.Address, HostKey: host.Fingerprint}
	in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: a.Address, To: host.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: `{"v":1}`, ReceiverRoute: &route}
	recipient, err := key.Recipient()
	if err != nil {
		return v, err
	}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		return v, err
	}
	// Serialize the final outstanding check and insertion with the existing outbox transaction.
	err = a.store.addConvOutbox([]outCopy{{in: in, env: env, state: stateQueued, required: protocol.CapReplyReceiver, recipientFP: host.Fingerprint}}, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		if _, e := replyReceiverHostIn(tx, host); e != nil {
			return e
		}
		var n int
		e := tx.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND recipient_fp=? AND required_cap=? AND conv IS NULL AND coalesce(reply_to,'')='' AND body=?`, host.Address, host.Fingerprint, protocol.CapReplyReceiver, in.Body).Scan(&n)
		if e != nil {
			return e
		}
		if n != 0 {
			return errCatalogOutstanding
		}
		return nil
	}, "")
	if errors.Is(err, errCatalogOutstanding) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	notifyDaemon(a.home)
	if _, err = a.deliver(ctx, env, nil); err != nil && !retryable(err) {
		v.Status = "unavailable"
		v.Detail = err.Error()
	}
	return v, nil
}

var errCatalogOutstanding = errors.New("receiver catalog already requested")

func (a *Agent) receiverSetupSender(q dbq, in envelope.Inner, fp string) error {
	if in.ReceiverRoute == nil {
		return errors.New("receiver setup route missing")
	}
	if _, err := replyReceiverHostIn(q, ReplyReceiverHost{Address: in.From, Fingerprint: fp}); err != nil {
		return err
	}
	route := in.ReceiverRoute
	if route.Op == "delegate" || route.Op == "catalog" && in.ReplyTo == "" {
		if route.Host != a.Address || route.HostKey != a.Self().Fingerprint() {
			return errors.New("receiver setup targets another host key")
		}
	} else if route.Host != in.From || route.HostKey != fp {
		return errors.New("receiver response differs from exact signing host")
	}
	if route.Op == "delegate" {
		op, err := envelope.ParseReceiverOperation([]byte(in.Body), *route, in.ReplyTo, in.Attachments)
		if err != nil {
			return err
		}
		r := localReceiverChoice(*op.Receiver)
		if r.OnClose != nil {
			registered, err := replySessionIn(q, r.SessionHandle)
			if err == nil && registered.Harness == "claude" {
				if err = a.checkReplySession(q, registered); err != nil {
					return err
				}
				return checkNewReplyHandoff(r, registered)
			}
		}
	}
	return nil
}
func (a *Agent) processReceiverCatalog(in envelope.Inner, fp string) error {
	if in.ReceiverRoute == nil || in.ReceiverRoute.Op != "catalog" || in.ReplyTo != "" {
		return nil
	}
	if err := a.receiverSetupSender(a.store.db, in, fp); err != nil {
		return err
	}
	sessions, err := a.ReplySessions()
	if err != nil {
		return err
	}
	safe := []envelope.ReceiverSession{}
	for _, s := range sessions {
		private, e := replySessionIn(a.store.db, s.Handle)
		if e != nil {
			return e
		}
		if s.Label == s.Harness+" "+private.SessionID {
			s.Label = s.Harness + " session"
		}
		safe = append(safe, envelope.ReceiverSession{Handle: s.Handle, Harness: s.Harness, Label: s.Label, Active: s.Active})
	}
	sort.Slice(safe, func(i, j int) bool { return safe[i].Handle < safe[j].Handle })
	if len(safe) > envelope.MaxReceiverSessions {
		safe = safe[:envelope.MaxReceiverSessions]
	}
	body, _ := json.Marshal(envelope.ReceiverOperation{V: 1, Sessions: safe})
	reply := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: a.Address, To: in.From, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: string(body), ReplyTo: in.ID, ReceiverRoute: in.ReceiverRoute}
	key, err := replyReceiverHostIn(a.store.db, ReplyReceiverHost{Address: in.From, Fingerprint: fp})
	if err != nil {
		return err
	}
	recipient, err := key.Recipient()
	if err != nil {
		return err
	}
	env, err := envelope.Seal(reply, a.id.Sign, recipient)
	if err != nil {
		return err
	}
	err = a.store.addConvOutbox([]outCopy{{in: reply, env: env, state: stateQueued, required: protocol.CapReplyReceiver, recipientFP: fp}}, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		if e := a.receiverSetupSender(tx, in, fp); e != nil {
			return e
		}
		var n int
		e := tx.QueryRow(`SELECT count(*) FROM outbox WHERE reply_to=? AND recipient=? AND required_cap=?`, in.ID, in.From, protocol.CapReplyReceiver).Scan(&n)
		if e != nil {
			return e
		}
		if n > 0 {
			return errCatalogOutstanding
		}
		return nil
	}, "")
	if errors.Is(err, errCatalogOutstanding) {
		return nil
	}
	if err == nil {
		notifyDaemon(a.home)
	}
	return err
}
