package client

import (
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Imported originals exist only after exact-key local setup approval. Read the
// typed record, not a first ref hit or JSON text search. Canceled records remain
// correlated data so the default worker cannot steal their selected input.
func importedReceiverOriginals(q dbq, in envelope.Inner) ([]ReplyReceiverBinding, error) {
	rows, err := q.Query(`SELECT id,receiver FROM reply_receivers WHERE conv=? AND request_ref=?`, in.Conv, in.ReplyTo)
	if err != nil {
		return nil, err
	}
	var result []ReplyReceiverBinding
	for rows.Next() {
		var b ReplyReceiverBinding
		var raw string
		if err = rows.Scan(&b.ID, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err = decodeReceiverBinding(raw, &b); err != nil {
			rows.Close()
			return nil, err
		}
		r := b.remote
		if r == nil || r.Role != "imported" {
			continue
		}
		digest, e := envelope.ReceiverDigest(r.Route, r.Request, r.Choice)
		if (!r.Redacted && (e != nil || digest != r.Route.RequestDigest)) || !r.Ready || r.Route.DelegationID != b.ID || r.Request.Conv != in.Conv || r.Route.RequestRef != in.ReplyTo {
			rows.Close()
			return nil, errors.New("imported original commitment is invalid")
		}
		result = append(result, b)
	}
	err = rows.Err()
	rows.Close()
	return result, err
}
func importedReceiverMatches(q dbq, b ReplyReceiverBinding, in envelope.Inner, fp string) (bool, error) {
	r := b.remote.Request
	if r.PID != in.PID || r.Conv != in.Conv {
		return false, nil
	}
	if r.Target != nil {
		t := r.Target
		if t.Address != in.From || t.Fingerprint != fp || in.AgentID != "" && (in.AgentID != t.AgentID || in.Kind != envelope.KindAnswer && in.Kind != envelope.KindResult) {
			return false, nil
		}
		if in.Kind == envelope.KindAnswer || in.Kind == envelope.KindResult {
			if replyKind(r.Kind) != in.Kind {
				return false, nil
			}
		}
		return true, nil
	}
	if in.AgentID != "" {
		return false, nil
	}
	if r.Conv == "" {
		return r.To == in.From && r.ToKey == fp, nil
	}
	root, e := protocol.ParseConvRoot(r.Root)
	if e != nil {
		return false, e
	}
	if root.Kind == protocol.ConvKindGroup {
		for _, k := range r.GroupReplies {
			if k.Key == fp {
				return true, nil
			}
		}
		return false, nil
	}
	own, found, e := scanPersonIn(q, "state=?", personSelf)
	if e != nil || !found {
		return false, e
	}
	peer := ""
	for _, m := range root.Members {
		if m.Person != own.info.Person {
			peer = m.Person
		}
	}
	p, found, e := scanPersonIn(q, `person IN (SELECT person FROM person_devices WHERE address=?)`, in.From)
	return found && p.roster.Person == peer && p.has(in.From, fp) && (p.info.State == personPinned || p.info.State == personSelf), e
}
func remoteReceiverAuthority(q dbq, b ReplyReceiverBinding) error {
	r := b.remote
	if r == nil {
		return nil
	}
	if _, e := replyReceiverHostIn(q, ReplyReceiverHost{Address: r.Route.Host, Fingerprint: r.Route.HostKey}); e != nil {
		return e
	}
	if _, e := replyReceiverHostIn(q, ReplyReceiverHost{Address: r.Request.From, Fingerprint: r.Request.FromKey}); e != nil {
		return e
	}
	if r.Role != "origin" && r.Role != "imported" {
		return errors.New("unknown receiver delegation role")
	}
	return nil
}
func receiverOriginalBody(q dbq, b ReplyReceiverBinding) (string, error) {
	if b.remote != nil && b.remote.Role == "imported" {
		return b.remote.Request.Body, nil
	}
	var body string
	err := q.QueryRow(`SELECT body FROM outbox WHERE reply_receiver=? AND (id=? OR lid=?) ORDER BY id LIMIT 1`, b.ID, b.RequestRef, b.RequestRef).Scan(&body)
	return body, err
}
