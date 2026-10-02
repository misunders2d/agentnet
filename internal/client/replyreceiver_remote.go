package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const stateReceiverWaiting = "receiver_waiting" // exact sealed originals await authorized ready

const receiverRouteSchema = `ALTER TABLE inbox ADD COLUMN receiver_route TEXT;`

func receiverRouteJSON(route *envelope.ReceiverRoute) string {
	if route == nil {
		return ""
	}
	raw, _ := json.Marshal(route)
	return string(raw)
}

// This is private state in the existing binding JSON. Only accepted setup
// creates an imported binding; an originating obligation remains passive.
type receiverRemoteState struct {
	Role           string                   `json:"role"`
	Route          envelope.ReceiverRoute   `json:"route"`
	Request        envelope.ReceiverRequest `json:"request"`
	Choice         envelope.ReceiverChoice  `json:"choice"`
	OriginalIDs    []string                 `json:"original_ids,omitempty"`
	OriginalHashes []string                 `json:"original_hashes,omitempty"`
	Redacted       bool                     `json:"redacted,omitempty"` // local retention transition, never accepted from setup
	Ready          bool                     `json:"ready,omitempty"`    // setup authorization, never completion
	Refusal        string                   `json:"refusal,omitempty"`  // authenticated selected-host refusal, never a fallback
}

func (a *Agent) prepareRemoteReplyReceiver(r ReplyReceiver) (*replyBinding, error) {
	if r.Host == nil || r.Preset != "" {
		return nil, errors.New("remote receiver requires an exact host and locally derived preset")
	}
	host := *r.Host
	r.Host = &host
	r.Instructions = strings.TrimSpace(r.Instructions)
	if r.OnClose != nil {
		backup := *r.OnClose
		backup.Instructions = strings.TrimSpace(backup.Instructions)
		r.OnClose = &backup
	}
	if err := receiverChoice(r).Validate(); err != nil {
		return nil, err
	}
	b := &replyBinding{receiver: r, existing: r.BindingID}
	b.resolve = func(dbq) (*ExecutorStamp, error) { return nil, nil } // receiver host resolves its own executor
	b.guard = func(q dbq) error {
		if b.remote == nil {
			return errors.New("cross-device receiver setup has not frozen an original request")
		}
		digest, err := envelope.ReceiverDigest(b.remote.Route, b.remote.Request, b.remote.Choice)
		if err != nil || b.remote.Role != "origin" || digest != b.remote.Route.RequestDigest || b.remote.Route.Host != host.Address || b.remote.Route.HostKey != host.Fingerprint || b.remote.Request.From != a.Address || b.remote.Request.FromKey != a.Self().Fingerprint() || receiverRouteJSONChoice(r) != receiverRouteJSONChoice(localReceiverChoice(b.remote.Choice)) {
			return errors.New("cross-device receiver differs from the frozen local request and choice")
		}
		if err := frozenReceiverBatch(q, b.remote); err != nil {
			return err
		}
		if _, err := replyReceiverHostIn(q, host); err != nil {
			return err
		}
		if b.existing == "" {
			return nil
		}
		old, err := replyReceiverIn(q, b.existing)
		if err != nil {
			return err
		}
		if old.State == "canceled" || old.Receiver.Host == nil || *old.Receiver.Host != host || receiverRouteJSONChoice(old.Receiver) != receiverRouteJSONChoice(r) {
			return errors.New("remote reply binding is canceled or differs from its frozen choice")
		}
		return groupReceiverBinding(q, old)
	}
	return b, nil
}

func receiverRouteJSONChoice(r ReplyReceiver) string {
	raw, _ := json.Marshal(receiverChoice(r))
	return string(raw)
}

func receiverChoice(r ReplyReceiver) envelope.ReceiverChoice {
	choice := envelope.ReceiverChoice{Kind: r.Kind, AgentID: r.AgentID, SessionHandle: r.SessionHandle, Instructions: r.Instructions, Mode: r.Mode}
	if r.OnClose != nil {
		choice.OnClose = &envelope.ReceiverOnClose{AgentID: r.OnClose.AgentID, Instructions: r.OnClose.Instructions, Mode: r.OnClose.Mode}
	}
	return choice
}

func localReceiverChoice(choice envelope.ReceiverChoice) ReplyReceiver {
	r := ReplyReceiver{Kind: choice.Kind, AgentID: choice.AgentID, SessionHandle: choice.SessionHandle, Instructions: choice.Instructions, Mode: choice.Mode}
	if choice.OnClose != nil {
		r.OnClose = &ManagedReplyHandoff{AgentID: choice.OnClose.AgentID, Instructions: choice.OnClose.Instructions, Mode: choice.OnClose.Mode}
	}
	return r
}

// ReplyReceiverHost names an exact current linked device. Discovery and
// membership never grant that device permission to accept a delegation.
type ReplyReceiverHost struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

func replyReceiverHostIn(q dbq, selected ReplyReceiverHost) (identity.Public, error) {
	if _, _, err := protocol.SplitAddress(selected.Address); err != nil || !protocol.ValidFingerprint(selected.Fingerprint) {
		return identity.Public{}, errors.New("selected reply receiver requires an exact linked host key")
	}
	own, found, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return identity.Public{}, err
	}
	if !found || !own.has(selected.Address, selected.Fingerprint) {
		return identity.Public{}, errors.New("selected reply receiver host is not a current device of this person")
	}
	key, found := own.device(selected.Address)
	if !found || key.Verify() != nil {
		return identity.Public{}, errors.New("selected reply receiver host proof is invalid")
	}
	return key, nil
}

// Freeze once before the owning outbox transaction. Original recipients see
// only the committed return destination; selected instructions stay in setup.
func (a *Agent) prepareRemoteCopies(ctx context.Context, b *replyBinding, copies []outCopy, files []OutgoingFile) error {
	if b == nil || b.receiver.Host == nil {
		return nil
	}
	if b.existing != "" {
		return errors.New("cross-device follow-up requires a separately frozen original delegation")
	}
	var selected []int
	first := -1
	for i, c := range copies {
		if c.in.Sub == "" && c.in.AgentID == "" {
			selected = append(selected, i)
			if first < 0 || c.in.Target != nil && c.env.To == c.in.Target.Address {
				first = i
			}
		}
	}
	if first < 0 {
		return errors.New("cross-device receiver has no original request")
	}
	original := copies[first].in
	request := envelope.ReceiverRequest{ID: original.ID, LID: original.LID, From: original.From, FromKey: a.Self().Fingerprint(), TS: original.TS, Conv: original.Conv, Root: original.Root, Kind: original.Kind, Body: original.Body, ReplyTo: original.ReplyTo, Origin: original.Origin, Emotion: original.Emotion, Target: original.Target, PID: original.PID}
	if original.Conv == "" {
		request.To, request.ToKey = original.To, copies[first].recipientFP
	}
	for _, att := range original.Attachments {
		att.Blob = envelope.Blob{}
		request.Attachments = append(request.Attachments, att)
	}
	scope, e := snapshotGroupReceiver(a.store.db, original, copies, ReplyReceiverBinding{})
	if e != nil {
		return e
	}
	if s, ok := scope[original.LID]; ok {
		request.GroupAdmission = s.Admission
		for fp, admission := range s.Replies {
			request.GroupReplies = append(request.GroupReplies, envelope.ReceiverReplyKey{Key: fp, Admission: admission})
		}
		sort.Slice(request.GroupReplies, func(i, j int) bool { return request.GroupReplies[i].Key < request.GroupReplies[j].Key })
	}
	ref := request.ID
	if request.Conv != "" {
		ref = request.LID
	}
	route := envelope.ReceiverRoute{Op: "request", Host: b.receiver.Host.Address, HostKey: b.receiver.Host.Fingerprint, RequestRef: ref, DelegationID: protocol.NewID()}
	choice := receiverChoice(b.receiver)
	route.RequestDigest, e = envelope.ReceiverDigest(route, request, choice)
	if e != nil {
		return e
	}
	op := envelope.ReceiverOperation{V: 1, Request: &request, Receiver: &choice}
	body, e := json.Marshal(op)
	if e != nil || len(body) > envelope.MaxReceiverSetup {
		return errors.New("receiver delegation exceeds existing setup bound")
	}
	remote := &receiverRemoteState{Role: "origin", Route: route, Request: request, Choice: choice}
	for _, i := range selected {
		c := &copies[i]
		c.in.ReceiverRoute = &route
		c.in.TS = request.TS
		key, e := a.sendKey(ctx, c.in.To)
		if e != nil {
			return e
		}
		if key.Fingerprint() != c.recipientFP {
			return errors.New("original recipient key changed before receiver delegation")
		}
		recipient, e := key.Recipient()
		if e != nil {
			return e
		}
		c.env, e = envelope.Seal(c.in, a.id.Sign, recipient)
		if e != nil {
			return e
		}
		c.state = stateReceiverWaiting
		c.why = "awaiting exact selected-host delegation approval"
		raw, _ := json.Marshal(c.env)
		remote.OriginalIDs = append(remote.OriginalIDs, c.env.ID)
		remote.OriginalHashes = append(remote.OriginalHashes, fmt.Sprintf("%x", sha256.Sum256(raw)))
	}
	key, e := replyReceiverHostIn(a.store.db, *b.receiver.Host)
	if e != nil {
		return e
	}
	recipient, e := key.Recipient()
	if e != nil {
		return e
	}
	setupRoute := route
	setupRoute.Op = "delegate"
	in := envelope.Inner{V: envelope.Version, ID: route.DelegationID, From: a.Address, To: route.Host, TS: time.Now().Unix(), Kind: envelope.KindTask, Body: string(body), ReceiverRoute: &setupRoute}
	setup := &outCopy{in: in, state: stateQueued, required: protocol.CapReplyReceiver, recipientFP: route.HostKey}
	b.setup = setup // caller releases any partial encrypted spool on failure
	for _, file := range files {
		att, e := a.spoolNamed(file, recipient)
		if e != nil {
			return e
		}
		setup.in.Attachments = append(setup.in.Attachments, att)
	}
	setup.env, e = envelope.Seal(setup.in, a.id.Sign, recipient)
	if e != nil {
		return e
	}
	b.remote = remote
	return nil
}
func frozenReceiverBatch(q dbq, remote *receiverRemoteState) error {
	if remote == nil || len(remote.OriginalIDs) == 0 || len(remote.OriginalIDs) != len(remote.OriginalHashes) {
		return errors.New("receiver original batch is incomplete")
	}
	for i, id := range remote.OriginalIDs {
		var raw, state string
		if err := q.QueryRow(`SELECT envelope,state FROM outbox WHERE id=?`, id).Scan(&raw, &state); err != nil {
			return err
		}
		if state != stateReceiverWaiting || fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) != remote.OriginalHashes[i] {
			return errors.New("receiver original batch changed before exact ready")
		}
	}
	return nil
}
