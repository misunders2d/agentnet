// Package client is the laptop side of AgentNet: enrollment, trusted peer
// keys, an end-to-end-encrypted outbox and inbox, and the push daemon.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// ErrPeerRevoked means the addressed agent has been revoked.
var ErrPeerRevoked = errors.New("recipient has been revoked")

// KeyChangedError blocks sending until the user explicitly trusts the new key.
type KeyChangedError struct {
	Address, Pinned, Offered string
}

func (e *KeyChangedError) Error() string {
	return fmt.Sprintf("keys for %s changed (trusted %s, directory now offers %s); verify with the owner, then run `agentnet trust %s`",
		e.Address, e.Pinned, e.Offered, e.Address)
}

// Agent is one enrolled agent's local state and Hub connection.
type Agent struct {
	Address string
	Logf    func(format string, args ...any)

	home      string
	id        *identity.Identity
	store     *store
	hub       *hubConn
	heartbeat time.Duration
}

func paths(home string) (identityPath, dbPath string) {
	return filepath.Join(home, "identity.json"), filepath.Join(home, "agent.db")
}

// Join enrolls a new agent named agentName with an invite code.
//
// Keys and the enrollment intent are saved before the Hub is contacted, so if
// the Hub's answer is lost, running Join again with the same invite and name
// re-sends the identical request, which the Hub accepts as a replay.
func Join(ctx context.Context, home, code, agentName string) (*Agent, error) {
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		return nil, err
	}
	address := protocol.Address(inv.Label, agentName)
	if _, _, err := protocol.SplitAddress(address); err != nil {
		return nil, err
	}
	if err := secfile.EnsureDir(home); err != nil {
		return nil, err
	}
	idPath, dbPath := paths(home)
	id, err := identity.Load(idPath)
	if errors.Is(err, os.ErrNotExist) {
		if id, err = identity.Generate(); err == nil {
			err = id.Save(idPath)
		}
	}
	if err != nil {
		return nil, err
	}
	st, err := openStore(dbPath)
	if err != nil {
		return nil, err
	}
	defer st.db.Close()
	if enrolled, _ := st.config("enrolled"); enrolled == "1" {
		return nil, fmt.Errorf("%s already holds an enrolled agent", home)
	}
	if err := st.setConfig(map[string]string{"address": address, "hub": inv.Hub, "hub_cert": inv.CertPEM, "join_secret": inv.Secret}); err != nil {
		return nil, err
	}
	conn, err := newHubConn(inv.Hub, inv.CertPEM, "", nil)
	if err != nil {
		return nil, err
	}
	req := protocol.JoinRequest{Secret: inv.Secret, Public: id.Public(address)}
	protocol.SignJoin(&req, id.Sign)
	if err := conn.do(ctx, "POST", "/v1/join", req, nil); err != nil {
		return nil, fmt.Errorf("enrollment not confirmed: %w (run the same join command again to retry)", err)
	}
	if err := st.setConfig(map[string]string{"enrolled": "1"}); err != nil {
		return nil, err
	}
	if err := st.deleteConfig("join_secret"); err != nil {
		return nil, err
	}
	st.db.Close()
	return Open(home)
}

// Open loads an enrolled agent from home.
func Open(home string) (*Agent, error) {
	idPath, dbPath := paths(home)
	id, err := identity.Load(idPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no enrolled agent in %s; run `agentnet join` first", home)
		}
		return nil, err
	}
	st, err := openStore(dbPath)
	if err != nil {
		return nil, err
	}
	if enrolled, _ := st.config("enrolled"); enrolled != "1" {
		st.db.Close()
		return nil, fmt.Errorf("enrollment in %s is incomplete; run the same `agentnet join` command again", home)
	}
	a := &Agent{home: home, id: id, store: st, heartbeat: protocol.HeartbeatInterval, Logf: func(string, ...any) {}}
	var hubURL, cert string
	if a.Address, err = st.config("address"); err == nil {
		if hubURL, err = st.config("hub"); err == nil {
			cert, err = st.config("hub_cert")
		}
	}
	if err == nil {
		a.hub, err = newHubConn(hubURL, cert, a.Address, id.Sign)
	}
	if err != nil {
		st.db.Close()
		return nil, fmt.Errorf("corrupt agent config: %w", err)
	}
	return a, nil
}

// Close releases local storage.
func (a *Agent) Close() error { return a.store.db.Close() }

// Self returns this agent's own directory entry.
func (a *Agent) Self() identity.Public { return a.id.Public(a.Address) }

func (a *Agent) directory(ctx context.Context, address string) (protocol.DirectoryEntry, error) {
	var e protocol.DirectoryEntry
	label, name, err := protocol.SplitAddress(address)
	if err != nil {
		return e, err
	}
	if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name, nil, &e); err != nil {
		return e, err
	}
	if e.Public.Address != address {
		return e, errors.New("directory answered for a different address")
	}
	return e, e.Public.Verify()
}

func sameKeys(a, b identity.Public) bool {
	return bytes.Equal(a.SignKey, b.SignKey) && a.BoxRecipient == b.BoxRecipient
}

// sendKey returns the trusted key for a recipient. The first key seen is
// pinned; a later different key blocks sending until Trust is called.
//
// When the Hub is unreachable a previously pinned key is used so the message
// can be queued; the Hub still refuses it later if the recipient is revoked.
func (a *Agent) sendKey(ctx context.Context, address string) (identity.Public, error) {
	pinned, _, found, err := a.store.peer(address)
	if err != nil {
		return identity.Public{}, err
	}
	e, err := a.directory(ctx, address)
	if err != nil {
		if found && retryable(err) {
			return pinned, nil
		}
		return identity.Public{}, err
	}
	if e.Revoked {
		return identity.Public{}, ErrPeerRevoked
	}
	if !found {
		return e.Public, a.store.pin(e.Public)
	}
	if sameKeys(pinned, e.Public) {
		return pinned, nil
	}
	if err := a.store.setPending(e.Public); err != nil {
		return identity.Public{}, err
	}
	return identity.Public{}, &KeyChangedError{address, pinned.Fingerprint(), e.Public.Fingerprint()}
}

// SendResult reports what is known about a message after Send.
type SendResult struct {
	ID     string `json:"id"`
	State  string `json:"state"`            // queued, custody or delivered
	Detail string `json:"detail,omitempty"` // why it is still queued
}

// Send encrypts body and any files to the recipient and hands them to the
// Hub. Files are encrypted into a private spool first; if the Hub is
// unreachable the message stays queued and the daemon resumes it.
func (a *Agent) Send(ctx context.Context, to, body, replyTo string, files ...string) (SendResult, error) {
	if len(files) > envelope.MaxAttachments {
		return SendResult{}, fmt.Errorf("at most %d attachments per message", envelope.MaxAttachments)
	}
	peer, err := a.sendKey(ctx, to)
	if err != nil {
		return SendResult{}, err
	}
	recipient, err := peer.Recipient()
	if err != nil {
		return SendResult{}, err
	}
	in := envelope.Inner{
		ID: protocol.NewID(), From: a.Address, To: to, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: body, ReplyTo: replyTo,
	}
	for _, path := range files {
		att, err := a.spoolFile(path, recipient)
		if err != nil {
			a.releaseSpool(envelope.Envelope{ID: in.ID, Blobs: blobsOf(in.Attachments)})
			return SendResult{}, err
		}
		in.Attachments = append(in.Attachments, att)
	}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	if err == nil {
		err = a.store.addOutbox(env, body)
	}
	if err != nil {
		a.releaseSpool(envelope.Envelope{ID: in.ID, Blobs: blobsOf(in.Attachments)})
		return SendResult{}, err
	}
	return a.deliver(ctx, env)
}

func blobsOf(atts []envelope.Attachment) []envelope.Blob {
	var out []envelope.Blob
	for _, a := range atts {
		out = append(out, a.Blob)
	}
	return out
}

// Reply sends body (and files) to the sender of inbox message id.
func (a *Agent) Reply(ctx context.Context, id, body string, files ...string) (SendResult, error) {
	sender, err := a.store.inboxSender(id)
	if err != nil {
		return SendResult{}, fmt.Errorf("no inbox message %s", id)
	}
	return a.Send(ctx, sender, body, id, files...)
}

func (a *Agent) deliver(ctx context.Context, env envelope.Envelope) (SendResult, error) {
	var r protocol.Receipt
	err := a.uploadAll(ctx, env)
	if err == nil {
		err = a.hub.do(ctx, "POST", "/v1/messages", env, &r)
	}
	switch {
	case err == nil:
		a.releaseSpool(env)
		return SendResult{ID: env.ID, State: r.State}, a.store.setOutboxState(env.ID, r.State, "")
	case retryable(err):
		return SendResult{ID: env.ID, State: stateQueued, Detail: err.Error()}, a.store.setOutboxState(env.ID, stateQueued, err.Error())
	default:
		a.store.setOutboxState(env.ID, stateFailed, err.Error())
		return SendResult{ID: env.ID, State: stateFailed}, err
	}
}

// FlushOutbox retries every queued message once.
func (a *Agent) FlushOutbox(ctx context.Context) error {
	envs, err := a.store.queued()
	if err != nil {
		return err
	}
	for _, env := range envs {
		if _, err := a.deliver(ctx, env); err != nil && !retryable(err) {
			a.Logf("message %s to %s rejected: %v", env.ID, env.To, err)
		}
	}
	return nil
}

// Status asks the Hub what it can prove about message id.
func (a *Agent) Status(ctx context.Context, id string) (protocol.Receipt, error) {
	var r protocol.Receipt
	err := a.hub.do(ctx, "GET", "/v1/messages/"+url.PathEscape(id), nil, &r)
	return r, err
}

// Inbox lists received messages, optionally marking the listed ones read.
func (a *Agent) Inbox(unreadOnly, markRead bool) ([]Message, error) {
	msgs, err := a.store.inbox(unreadOnly)
	if err != nil || !markRead {
		return msgs, err
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return msgs, a.store.markRead(ids)
}

// Fingerprints returns the pinned fingerprint ("" if none) and the one the
// directory currently offers.
func (a *Agent) Fingerprints(ctx context.Context, address string) (pinned, current string, err error) {
	p, _, found, err := a.store.peer(address)
	if err != nil {
		return "", "", err
	}
	if found {
		pinned = p.Fingerprint()
	}
	e, err := a.directory(ctx, address)
	if err != nil {
		return pinned, "", err
	}
	return pinned, e.Public.Fingerprint(), nil
}

// Trust pins the directory's current keys for address and promotes messages
// held because of a key change that now verify. Each message moves to the
// inbox atomically, so an interrupted Trust can simply be run again.
func (a *Agent) Trust(ctx context.Context, address string) (string, error) {
	e, err := a.directory(ctx, address)
	if err != nil {
		return "", err
	}
	if err := a.store.pin(e.Public); err != nil {
		return "", err
	}
	held, err := a.store.held(address, reasonKeyChanged)
	if err != nil {
		return "", err
	}
	for _, env := range held {
		in, err := envelope.Open(env, a.id, a.Address, e.Public)
		if err != nil {
			a.Logf("held message %s still does not verify: %v", env.ID, err)
			continue
		}
		if err := a.store.promote(in); err != nil {
			return "", err
		}
	}
	if err := a.flushReceipts(ctx); err != nil {
		return e.Public.Fingerprint(), fmt.Errorf("trusted, but receipts not yet sent (the daemon retries): %w", err)
	}
	return e.Public.Fingerprint(), nil
}

// Invite asks the Hub for an invite code for person label (admin only).
func (a *Agent) Invite(ctx context.Context, label string, ttl time.Duration, admin bool) (string, error) {
	var out struct{ Code string }
	err := a.hub.do(ctx, "POST", "/v1/admin/invites", protocol.InviteRequest{Label: label, TTL: ttl, Admin: admin}, &out)
	return out.Code, err
}

// Revoke revokes address on the Hub (admin only).
func (a *Agent) Revoke(ctx context.Context, address string) error {
	return a.hub.do(ctx, "POST", "/v1/admin/revoke", protocol.RevokeRequest{Address: address}, nil)
}
