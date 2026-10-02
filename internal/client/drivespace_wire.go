package client

import (
	"bytes"
	"context"

	"database/sql"

	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/gdrive"
	"github.com/misunders2d/agentnet/internal/identity"
)

const driveSpaceSub = "drive-space"
const driveSpaceCap = "drv1"

// PublishDriveSpace uses a dedicated conversation event, never a chat reference.
func (a *Agent) PublishDriveSpace(ctx context.Context, sp gdrive.Space) error {
	if e := sp.Validate(); e != nil {
		return e
	}
	owner, e := a.driveOwner(sp.Conv)
	if e != nil || owner != sp.Owner {
		return errors.New("Drive metadata owner is not this verified conversation member")
	}
	st, err := a.readDrive()
	if err != nil {
		return err
	}
	if old, ok := st.Spaces[sp.Conv]; ok && old.Owner != owner {
		return errors.New("only original Drive space owner may publish metadata")
	}
	b, e := json.Marshal(sp)
	if e != nil {
		return e
	}
	_, e = a.SendConv(ctx, sp.Conv, ConvOutgoing{Kind: envelope.KindMessage, Body: string(b), sub: driveSpaceSub})
	return e
}
func parseDriveControl(in envelope.Inner) (gdrive.Space, error) {
	var s gdrive.Space
	if in.Sub != driveSpaceSub || in.Conv == "" || in.Ref != nil || in.Target != nil || in.PID != "" || len(in.Attachments) != 0 {
		return s, errors.New("invalid Drive metadata control anchor")
	}
	if json.Unmarshal([]byte(in.Body), &s) != nil || s.Conv != in.Conv {
		return s, errors.New("invalid Drive metadata conversation")
	}
	return s, s.Validate()
}

// admitDriveControl is dispatched after normal conversation-root proof.
// Verify current signed person binding, apply monotonic owner metadata, store a
// quiet control for encrypted history, and forward to newly linked devices.
func (a *Agent) admitDriveControl(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, fromQuarantine bool) error {
	hold := func() error { return a.store.holdAs(env, reasonProof) }
	sp, e := a.personOfKey(ctx, env.From, sender)
	if e != nil {
		return hold()
	}
	m, e := a.dmMembers(in.Conv)
	if e != nil {
		return hold()
	}
	p, ok := m.persons[sp.info.Person]
	if !ok || !p.has(env.From, sender.Fingerprint()) {
		return a.store.holdAs(env, reasonInvalid)
	}
	s, e := parseDriveControl(in)
	if e != nil {
		return a.store.holdAs(env, reasonInvalid)
	}
	if e = a.AdmitDriveSpace(sp.info.Person, s); e != nil {
		return hold()
	}
	me, ok, e := a.store.selfPerson(a.Address)
	if e != nil || !ok {
		return hold()
	}
	_, raw, _, e := a.store.conversation(in.Conv)
	if e != nil {
		return e
	}
	forward := a.forwardStale(me, in, sender.Fingerprint(), raw)
	res, e := a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error { return insertCopies(tx, forward) })
	if e == nil && res == admitted {
		a.NoteChange()
		if len(forward) > 0 {
			a.kickNow()
		}
	}
	return e
}

// admitDriveHistory is called after the standard own-linked-device history
// provenance checks resolve its original author person. It does not execute.
func (a *Agent) admitDriveHistory(owner string, in envelope.Inner) error {
	s, e := parseDriveControl(in)
	if e != nil {
		return e
	}
	return a.AdmitDriveSpace(owner, s)
}

// DriveBrokerList binds the job PID outside model-controlled arguments. Token
// refresh stays local, and independent harness tools keep their permissions.
func (a *Agent) DriveBrokerList(ctx context.Context, conv, pid, page string) (gdrive.Page, error) {
	if e := a.CheckDriveGrant(conv, pid, false); e != nil {
		return gdrive.Page{}, e
	}
	v, e := a.DriveCommand(ctx, DriveRequest{Conv: conv, Action: "list", Page: page, brokerPID: pid})
	if e != nil {
		return gdrive.Page{}, e
	}
	if v.Page == nil {
		return gdrive.Page{}, errors.New("Drive listing unavailable")
	}
	return *v.Page, nil
}
func (a *Agent) DriveBrokerUpload(ctx context.Context, conv, pid, name string, data []byte) (gdrive.File, error) {
	if e := a.CheckDriveGrant(conv, pid, true); e != nil {
		return gdrive.File{}, e
	}
	return a.driveUpload(ctx, conv, name, bytes.NewReader(data), int64(len(data)), true, pid)
}
