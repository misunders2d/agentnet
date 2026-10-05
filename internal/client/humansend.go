package client

import (
	"context"
	"database/sql"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"time"
)

// ownHumanPID is this device's exact accepted human participation in conv
// ("" for an original member device); anyone else has no author scope.
func (a *Agent) ownHumanPID(conv string) (string, error) {
	m, err := a.dmMembers(conv)
	if err != nil {
		return "", err
	}
	if m.device(a.Address, a.Self().Fingerprint()) {
		return "", nil
	}
	infos, err := a.Participations(conv)
	if err != nil {
		return "", err
	}
	for _, p := range infos {
		if p.HumanActive() && p.HostHere && p.Host.Fingerprint == a.Self().Fingerprint() {
			return p.PID, nil
		}
	}
	return "", errors.New("human: this device is neither an original member nor an accepted guest here")
}

// Human-audience traffic reuses per-recipient encryption, the logical ledger,
// file spool and the authored receiver binding. An ordinary turn creates no
// executor/job; an addressed request goes to its assistant's exact host too,
// whose worker alone decides whether it runs; an assistant output is that
// host's. Everyone else admits the inert conversation turn.
func (a *Agent) sendHumanTurn(ctx context.Context, root protocol.ConvRoot, raw []byte, out ConvOutgoing, h *envelope.HumanTurn, binding *replyBinding) (ConvSent, error) {
	request := out.Target != nil
	output := !request && out.PID != "" && out.PID != h.AuthorPID
	var x ParticipationInfo
	switch {
	case request:
		if out.Kind != envelope.KindQuestion && out.Kind != envelope.KindTask || out.PID == "" || out.PID == h.AuthorPID || out.AgentID != "" || out.sub != "" || envelope.AgentOrigin(out.Origin) || out.claim != nil {
			return ConvSent{}, errors.New("human: an addressed request names its assistant only")
		}
		// Its local reply receiver binds the exact host copy (addConvOutbox);
		// a cross-device receiver would route the request (ReceiverRoute),
		// which a human-audience request never carries.
		if binding != nil && (binding.receiver.Host != nil || binding.remote != nil || binding.setup != nil) {
			return ConvSent{}, errors.New("human: with guests present, a reply receiver is this device's own only")
		}
	case output:
		if h.AuthorPID != "" || out.sub != "" || binding != nil || out.selfJob || !envelope.AgentOrigin(out.Origin) {
			return ConvSent{}, errors.New("human: an assistant output comes only from its host")
		}
	default:
		if out.selfJob || out.AgentID != "" || out.sub != "" || envelope.AgentOrigin(out.Origin) {
			return ConvSent{}, errors.New("human: no execution authority")
		}
	}
	if request || output {
		var err error
		if x, err = a.Participation(out.PID); err != nil {
			return ConvSent{}, err
		}
		if x.Role == protocol.RoleHuman || request && (!x.Claimable() || out.Target.Address != x.Host.Address || out.Target.Fingerprint != x.Host.Fingerprint || out.Target.AgentID != x.AgentID) || output && !x.HostHere {
			return ConvSent{}, errors.New("human: addressed turn does not name this exact active assistant")
		}
	}
	if envelope.Blank(out.Body) && len(out.Files) == 0 {
		return ConvSent{}, errors.New("nothing to send")
	}
	if len(out.Files) > envelope.MaxAttachments {
		return ConvSent{}, errors.New("too many files")
	}
	if out.ReplyTo != "" { // every copy names the parent's logical id
		parent, known, err := humanReplyParent(a.store.db, root.ID(), out.ReplyTo)
		if err != nil {
			return ConvSent{}, err
		}
		if !known {
			return ConvSent{}, errors.New("human reply stays within its conversation")
		}
		out.ReplyTo = parent
	}
	if out.Quote != "" {
		parent, known, err := humanReplyParent(a.store.db, root.ID(), out.Quote)
		if err != nil {
			return ConvSent{}, err
		}
		if !known {
			return ConvSent{}, errors.New("quote stays within its conversation")
		}
		out.Quote = parent
	}
	m, err := a.dmMembers(root.ID())
	if err != nil {
		return ConvSent{}, err
	}
	for id := range m.persons {
		if _, err := a.refreshPerson(ctx, id, false); err != nil {
			return ConvSent{}, err
		}
	}
	m, err = a.dmMembers(root.ID())
	if err != nil {
		return ConvSent{}, err
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: out.Body, ReplyTo: out.ReplyTo, Quote: out.Quote, Conv: root.ID(), LID: protocol.NewID(), Root: raw, PID: h.AuthorPID, Human: h, Origin: out.Origin}
	if request || output {
		in.Kind, in.PID, in.Target, in.AgentID, in.Status, in.Emotion = out.Kind, out.PID, out.Target, out.AgentID, out.status, out.Emotion
		if request && in.Target != nil {
			in.LID = in.ID // the executable host copy's ID is the shared request LID (as external requests)
		}
	}
	if in.Origin == "" {
		in.Origin = envelope.OriginUI
	}
	var devices []identity.Public
	seen := map[string]bool{}
	for _, member := range root.Members {
		p, ok := m.persons[member.Person]
		if !ok {
			return ConvSent{}, errors.New("human: original member proof missing")
		}
		in.Fan = append(in.Fan, envelope.Fan{Person: member.Person, Roster: p.info.Roster})
		for _, d := range p.roster.Devices {
			if d.Address != a.Address && !seen[d.Address] {
				devices = append(devices, d)
				seen[d.Address] = true
			}
		}
	}
	for _, scope := range h.Audience {
		p, err := a.Participation(scope.PID)
		if err != nil {
			return ConvSent{}, err
		}
		host, ok, err := a.store.personByID(p.Host.Person)
		if err != nil {
			return ConvSent{}, err
		}
		if !ok {
			return ConvSent{}, errors.New("human host proof missing")
		}
		d, ok := host.device(p.Host.Address)
		if !ok || d.Fingerprint() != p.Host.Fingerprint {
			return ConvSent{}, errors.New("human host key changed")
		}
		if d.Address != a.Address && !seen[d.Address] {
			devices = append(devices, d)
			seen[d.Address] = true
		}
	}
	if request && x.External { // the outside assistant host reads the addressed request only
		key, err := a.sendKey(ctx, x.Host.Address)
		if err != nil {
			return ConvSent{}, err
		}
		if key.Fingerprint() != x.Host.Fingerprint {
			return ConvSent{}, errors.New("assistant host key changed")
		}
		if err := a.requireParticipationCaps(ctx, key, protocol.CapExternalParticipation); err != nil {
			return ConvSent{}, err
		}
		if !seen[key.Address] {
			devices = append(devices, key)
			seen[key.Address] = true
		}
	}
	if request && x.AgentID != "" && x.Host.Address != a.Address {
		if key, err := a.sendKey(ctx, x.Host.Address); err != nil {
			return ConvSent{}, err
		} else if err := a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity); err != nil {
			return ConvSent{}, err
		}
	}
	if len(devices) == 0 && !out.selfJob {
		return ConvSent{}, errors.New("human: no recipient")
	}
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return ConvSent{}, err
	}
	defer release()
	stored := false
	var copies []outCopy
	var spooled []envelope.Blob
	defer func() {
		if !stored {
			a.releaseSpool(envelope.Envelope{Blobs: spooled})
			if binding != nil && binding.setup != nil {
				a.releaseGroupCopies([]outCopy{*binding.setup})
			}
		}
	}()
	for _, file := range out.Files {
		if err := a.keepSent(file.Path); err != nil {
			return ConvSent{}, err
		}
	}
	for _, device := range devices {
		key, err := a.sendKey(ctx, device.Address)
		if err != nil {
			return ConvSent{}, err
		}
		if key.Fingerprint() != device.Fingerprint() {
			return ConvSent{}, errors.New("human recipient key changed")
		}
		recipient, err := key.Recipient()
		if err != nil {
			return ConvSent{}, err
		}
		copyIn := in
		copyIn.ID, copyIn.To = protocol.NewID(), device.Address
		if request && (device.Address == x.Host.Address && device.Fingerprint() == x.Host.Fingerprint || out.selfJob && len(copies) == 0) {
			copyIn.ID = in.LID // the host's executable copy (asked on the host: the first copy, whose ID its job takes)
		}
		for _, file := range out.Files {
			attachment, err := a.spoolNamed(file, recipient)
			if err != nil {
				return ConvSent{}, err
			}
			copyIn.Attachments = append(copyIn.Attachments, attachment)
			spooled = append(spooled, attachment.Blob)
		}
		sealed, err := envelope.Seal(copyIn, a.id.Sign, recipient)
		if err != nil {
			a.releaseSpool(envelope.Envelope{Blobs: blobsOf(copyIn.Attachments)})
			return ConvSent{}, err
		}
		copies = append(copies, outCopy{in: copyIn, env: sealed, state: stateQueued, required: protocol.CapHumanParticipation, recipientFP: key.Fingerprint()})
	}
	guard := func(tx *sql.Tx, replyID string) error {
		if out.claim != nil { // an output: the worker's claim decides with this write
			if err := out.claim(tx, replyID); err != nil {
				return err
			}
		}
		for _, c := range copies {
			if err := humanTurnAuthorization(tx, c.in, a.Address, a.Self().Fingerprint(), c.env.To, c.recipientFP, false); err != nil {
				return err
			}
		}
		return nil
	}
	if err := a.prepareRemoteCopies(ctx, binding, copies, out.Files); err != nil {
		return ConvSent{}, err
	}
	jobKey, local := "", in
	if out.selfJob { // asked on the host itself: its job is recorded with it
		jobKey, local.ID, local.To = a.Self().Fingerprint(), in.LID, a.Address
	}
	if err := a.store.addConvOutbox(copies, local, guard, jobKey, binding); err != nil {
		return ConvSent{}, err
	}
	stored = true
	release()
	if binding != nil && binding.setup != nil {
		if _, err := a.deliver(ctx, binding.setup.env, nil); err != nil && !retryable(err) {
			return ConvSent{}, err
		}
	}
	sent := ConvSent{ID: local.ID, LID: in.LID, State: protocol.StateDelivered}
	if len(copies) > 0 {
		sent.ID = copies[0].env.ID
	}
	for _, c := range copies {
		cp := ConvCopy{ID: c.env.ID, To: c.env.To, State: c.state}
		result, err := a.deliver(ctx, c.env, nil)
		if err == nil {
			cp.State, cp.Detail = result.State, result.Detail
		} else {
			cp.Detail = err.Error()
			if !retryable(err) {
				cp.State = stateFailed
			}
		}
		sent.Copies = append(sent.Copies, cp)
		if rank(cp.State) < rank(sent.State) {
			sent.State = cp.State
		}
	}
	a.kickNow()
	notifyDaemon(a.home)
	return sent, nil
}
