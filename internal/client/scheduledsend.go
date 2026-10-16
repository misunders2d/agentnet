package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

const scheduledSendSchema = `
CREATE TABLE scheduled_sends(
 id TEXT PRIMARY KEY, conv TEXT NOT NULL, topic TEXT NOT NULL,
 body TEXT NOT NULL, reply_to TEXT NOT NULL, quote TEXT NOT NULL,
 due_at INTEGER NOT NULL, timezone TEXT NOT NULL, rev INTEGER NOT NULL,
 state TEXT NOT NULL, audience TEXT NOT NULL, files TEXT NOT NULL,
 detail TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE INDEX scheduled_sends_due ON scheduled_sends(state,due_at,id);
`
const (
	ScheduledPending     = "pending"
	ScheduledDispatching = "dispatching"
	ScheduledSent        = "sent"
	ScheduledCancelled   = "cancelled"
	ScheduledNeedsReview = "needs_review"
)

var (
	ErrScheduledIDConflict = errors.New("the scheduled message ID already names different content")
	ErrScheduledStale      = errors.New("the scheduled message changed; review it again")
	ErrScheduledAudience   = errors.New("the scheduled audience changed; review it again")
	errScheduledEarly      = errors.New("the scheduled message is not due")
)

// ScheduledAudience is the approved ceiling, not a pinned device list. Current
// verified devices of these persons remain eligible; participation IDs retain
// their exact topic/host proofs. The ordinary sender still decides authority.
type ScheduledAudience struct {
	Persons   []string `json:"persons"`
	PIDs      []string `json:"pids"`
	AuthorPID string   `json:"author_pid,omitempty"`
}
type ScheduledFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Name   string `json:"name"`
}

// ScheduledSend belongs only to this device. Due is an absolute UTC instant;
// Timezone is display context. Sent means durably queued, never delivered.
type ScheduledSend struct {
	ID       string            `json:"id"`
	Conv     string            `json:"conv"`
	Topic    string            `json:"topic"`
	Body     string            `json:"body"`
	ReplyTo  string            `json:"reply_to,omitempty"`
	Quote    string            `json:"quote,omitempty"`
	Due      time.Time         `json:"due"`
	Timezone string            `json:"timezone"`
	Rev      int64             `json:"rev"`
	State    string            `json:"state"`
	Audience ScheduledAudience `json:"audience"`
	Files    []ScheduledFile   `json:"files"`
	Detail   string            `json:"detail,omitempty"`
}

// Files are consumed while saving, never saved as filesystem paths. On edit,
// nil retains the existing descriptors; a non-nil empty slice removes them.
type ScheduledSendInput struct {
	ID, Conv, Topic, Body, ReplyTo, Quote string
	Due                                   time.Time
	Timezone                              string
	Files                                 []OutgoingFile
	// Optional exact preview the person approved. A stale preview is refused.
	// An edit without it retains the saved audience in an unchanged scope.
	Audience *ScheduledAudience
}

const scheduledCols = `id,conv,topic,body,reply_to,quote,due_at,timezone,rev,state,audience,files,detail`

func scanScheduled(row interface{ Scan(...any) error }) (ScheduledSend, error) {
	var s ScheduledSend
	var due int64
	var audience, files string
	err := row.Scan(&s.ID, &s.Conv, &s.Topic, &s.Body, &s.ReplyTo, &s.Quote, &due, &s.Timezone, &s.Rev, &s.State, &audience, &files, &s.Detail)
	if err != nil {
		return s, err
	}
	s.Due = time.Unix(0, due).UTC()
	if err = json.Unmarshal([]byte(audience), &s.Audience); err == nil {
		err = json.Unmarshal([]byte(files), &s.Files)
	}
	return s, err
}
func (a *Agent) ScheduledSend(id string) (ScheduledSend, bool, error) {
	s, err := scanScheduled(a.store.db.QueryRow(`SELECT `+scheduledCols+` FROM scheduled_sends WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, false, nil
	}
	return s, err == nil, err
}
func (a *Agent) ScheduledSends(all bool) ([]ScheduledSend, error) {
	rows, err := a.store.db.Query(`SELECT `+scheduledCols+` FROM scheduled_sends WHERE ? OR state IN ('pending','dispatching','needs_review') ORDER BY due_at,id`, all)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduledSend{}
	for rows.Next() {
		s, e := scanScheduled(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Use the same resolved membership and participation helpers as the normal send,
// without publishing lazy public scope controls before the scheduled deadline.
func (a *Agent) scheduledAudience(q dbq, conv, topic string) (ScheduledAudience, dmMembers, error) {
	out := ScheduledAudience{Persons: []string{}, PIDs: []string{}}
	m, err := membersIn(q, conv)
	if err != nil {
		return out, m, err
	}
	for person := range m.persons {
		out.Persons = append(out.Persons, person)
	}
	slices.Sort(out.Persons)
	rows, err := q.Query(`SELECT DISTINCT pid FROM participation_events WHERE conv=? ORDER BY pid`, conv)
	if err != nil {
		return out, m, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, m, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, m, err
	}
	member := m.device(a.Address, a.Self().Fingerprint())
	for _, pid := range ids {
		p, e := participationIn(q, conv, pid, m, a.Address)
		if e != nil {
			return out, m, e
		}
		if !p.Following() || p.Topic != nil && *p.Topic != topic {
			continue
		}
		out.PIDs = append(out.PIDs, pid)
		if !member && p.Role == protocol.RoleHuman && p.HostHere && p.Host.Fingerprint == a.Self().Fingerprint() {
			out.AuthorPID = pid
		}
	}
	if !member && out.AuthorPID == "" {
		return out, m, errors.New("this device cannot send a human message in this topic")
	}
	return out, m, nil
}

// ScheduleAudience previews verified scope without sending controls, messages
// or execution requests. Pass it back only after explicit audience review.
func (a *Agent) ScheduleAudience(conv, topic string) (ScheduledAudience, error) {
	if !protocol.ValidHash(conv) || topic != "" && !protocol.ValidID(topic) {
		return ScheduledAudience{}, errors.New("invalid conversation or topic")
	}
	audience, _, err := a.scheduledAudience(a.store.db, conv, topic)
	return audience, err
}

func (a *Agent) scheduledFilesLock() (func(), error) {
	return lockfile.Wait(filepath.Join(a.home, "scheduled-files.lock"))
}

// This predicate is used under scheduledFilesLock by both kept-file GC paths.
const scheduledKeptReference = `EXISTS (SELECT 1 FROM scheduled_sends schedule,json_each(schedule.files) file WHERE schedule.state IN ('pending','dispatching','needs_review') AND json_extract(file.value,'$.sha256')=s.sha256)`

func (a *Agent) keepScheduledFiles(files []OutgoingFile) ([]ScheduledFile, error) {
	out := []ScheduledFile{}
	if len(files) > envelope.MaxAttachments {
		return nil, errors.New("too many files")
	}
	dir := filepath.Join(a.home, "opened")
	if err := secfile.EnsureDir(dir); err != nil {
		return nil, err
	}
	for _, f := range files {
		name := f.Name
		if name == "" {
			name = filepath.Base(f.Path)
		}
		if !validSendName(name) {
			return nil, errors.New("invalid attachment name")
		}
		src, _, err := openRegular(f.Path)
		if err != nil {
			return nil, err
		}
		hash := newCountingHash()
		_, err = io.Copy(hash, io.LimitReader(src, MaxFileSize+1))
		src.Close()
		if err != nil {
			return nil, err
		}
		if hash.n > MaxFileSize {
			return nil, errors.New("attachment too large")
		}
		if err = a.keepSent(f.Path); err != nil {
			return nil, err
		}
		// keepSent may find an older existing ciphertext: verify that too, before
		// claiming a durable schedule, including a racing replacement of the source.
		plain, err := a.openKept(dir, hash.hex(), hash.n)
		if err != nil {
			return nil, errors.New("the kept attachment could not be verified")
		}
		os.Remove(plain)
		if err = syncDir(filepath.Join(a.home, "kept")); err != nil {
			return nil, err
		}
		out = append(out, ScheduledFile{SHA256: hash.hex(), Size: hash.n, Name: name})
	}
	return out, nil
}
func (a *Agent) prepareScheduled(in ScheduledSendInput, keep []ScheduledFile) (ScheduledSend, error) {
	now := time.Now()
	if !in.Due.After(now) || in.Due.Sub(now) > maxReminderAhead {
		return ScheduledSend{}, errors.New("choose a future time within ten years")
	}
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return ScheduledSend{}, errors.New("choose a valid display timezone")
	}
	if !protocol.ValidHash(in.Conv) {
		return ScheduledSend{}, errors.New("invalid conversation")
	}
	if in.Topic == "new" {
		return ScheduledSend{}, errors.New("open the topic before scheduling a message")
	}
	topic, err := a.outgoingTopic(in.Conv, in.Topic, in.ReplyTo)
	if err != nil {
		return ScheduledSend{}, err
	}
	if topic == "new" {
		return ScheduledSend{}, errors.New("open the topic before scheduling a message")
	}
	if len(in.Body) > protocol.MaxBody {
		return ScheduledSend{}, errors.New("message is too long")
	}
	files := keep
	if in.Files != nil {
		files, err = a.keepScheduledFiles(in.Files)
		if err != nil {
			return ScheduledSend{}, err
		}
	} else if len(files) > 0 {
		// Rescheduling retains bytes, not just old descriptors. An edit must
		// not report saved against content that disappeared since creation.
		dir := filepath.Join(a.home, "opened")
		if err = secfile.EnsureDir(dir); err != nil {
			return ScheduledSend{}, err
		}
		for _, f := range files {
			plain, e := a.openKept(dir, f.SHA256, f.Size)
			if e != nil {
				return ScheduledSend{}, errors.New("a retained attachment is unavailable or changed")
			}
			os.Remove(plain)
		}
	}
	if files == nil {
		files = []ScheduledFile{}
	}
	if envelope.Blank(in.Body) && len(files) == 0 {
		return ScheduledSend{}, errors.New("nothing to schedule")
	}
	audience, _, err := a.scheduledAudience(a.store.db, in.Conv, topic)
	if err != nil {
		return ScheduledSend{}, err
	}
	if in.Audience != nil && (!scheduledWithin(audience, *in.Audience) || !scheduledWithin(*in.Audience, audience)) {
		return ScheduledSend{}, ErrScheduledAudience
	}
	// Resolve reply/quote with the existing local chat authority, never a remote hunt.
	reply, quote := in.ReplyTo, in.Quote
	for _, ref := range []*string{&reply, &quote} {
		if *ref != "" {
			var known bool
			*ref, known, err = humanReplyParent(a.store.db, in.Conv, *ref)
			if err != nil {
				return ScheduledSend{}, err
			}
			if !known {
				return ScheduledSend{}, errors.New("reply and quote stay within the conversation")
			}
		}
	}
	return ScheduledSend{ID: protocol.NewID(), Conv: in.Conv, Topic: topic, Body: in.Body, ReplyTo: reply, Quote: quote, Due: in.Due.UTC(), Timezone: in.Timezone, Rev: 1, State: ScheduledPending, Audience: audience, Files: files}, nil
}
func (a *Agent) ScheduleSend(in ScheduledSendInput) (ScheduledSend, error) {
	return a.saveScheduled("", 0, in)
}
func (a *Agent) EditScheduledSend(id string, rev int64, in ScheduledSendInput) (ScheduledSend, error) {
	return a.saveScheduled(id, rev, in)
}

// A retry proves the actual supplied bytes without keeping files or consulting
// the current audience. Terminal records and past deadlines remain retryable.
func (a *Agent) sameScheduledCreate(old ScheduledSend, in ScheduledSendInput) (bool, error) {
	timezone := in.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	if old.Conv != in.Conv || old.Body != in.Body || !old.Due.Equal(in.Due) || old.Timezone != timezone {
		return false, nil
	}
	topic, err := a.outgoingTopic(in.Conv, in.Topic, in.ReplyTo)
	if err != nil {
		return false, err
	}
	if old.Topic != topic {
		return false, nil
	}
	refs := []string{in.ReplyTo, in.Quote}
	for i, ref := range refs {
		if ref != "" {
			var known bool
			refs[i], known, err = humanReplyParent(a.store.db, in.Conv, ref)
			if err != nil {
				return false, err
			}
			if !known {
				return false, nil
			}
		}
	}
	if refs[0] != old.ReplyTo || refs[1] != old.Quote || len(in.Files) != len(old.Files) || len(in.Files) > envelope.MaxAttachments {
		return false, nil
	}
	for i, f := range in.Files {
		name := f.Name
		if name == "" {
			name = filepath.Base(f.Path)
		}
		if !validSendName(name) {
			return false, nil
		}
		src, _, e := openRegular(f.Path)
		if e != nil {
			return false, errors.New("a retry attachment is unavailable")
		}
		hash := newCountingHash()
		_, e = io.Copy(hash, io.LimitReader(src, MaxFileSize+1))
		src.Close()
		if e != nil || hash.n > MaxFileSize {
			return false, errors.New("a retry attachment could not be verified")
		}
		if old.Files[i] != (ScheduledFile{SHA256: hash.hex(), Size: hash.n, Name: name}) {
			return false, nil
		}
	}
	return true, nil
}
func (a *Agent) saveScheduled(id string, rev int64, in ScheduledSendInput) (ScheduledSend, error) {
	if in.ID != "" && (!protocol.ValidID(in.ID) || id != "" && in.ID != id) {
		return ScheduledSend{}, ErrScheduledIDConflict
	}

	release, err := a.scheduledFilesLock()
	if err != nil {
		return ScheduledSend{}, err
	}
	defer release()
	if id == "" && in.ID != "" {
		existing, found, e := a.ScheduledSend(in.ID)
		if e != nil {
			return ScheduledSend{}, e
		}
		if found {
			same, e := a.sameScheduledCreate(existing, in)
			if e != nil {
				return ScheduledSend{}, e
			}
			if !same {
				return ScheduledSend{}, ErrScheduledIDConflict
			}
			return existing, nil
		}
	}
	if id == "" && in.ID != "" {
		var collision bool
		if e := a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox WHERE lid=?) OR EXISTS(SELECT 1 FROM inbox WHERE lid=? AND local=1)`, in.ID, in.ID).Scan(&collision); e != nil {
			return ScheduledSend{}, e
		}
		if collision {
			return ScheduledSend{}, ErrScheduledIDConflict
		}
	}
	var old ScheduledSend
	if id != "" {
		var found bool
		old, found, err = a.ScheduledSend(id)
		if err != nil {
			return old, err
		}
		if !found || old.Rev != rev || old.State != ScheduledPending && old.State != ScheduledNeedsReview {
			return old, ErrScheduledStale
		}
	}
	s, err := a.prepareScheduled(in, old.Files)
	if err != nil {
		return s, err
	}
	if id != "" && in.Audience == nil {
		if s.Conv != old.Conv || s.Topic != old.Topic {
			return ScheduledSend{}, ErrScheduledAudience
		}
		s.Audience = old.Audience
	}
	if id == "" && in.ID != "" {
		s.ID = in.ID
	}
	audience, _ := json.Marshal(s.Audience)
	files, _ := json.Marshal(s.Files)
	now := time.Now().UnixNano()
	if id == "" {
		var result sql.Result
		result, err = a.store.db.Exec(`INSERT INTO scheduled_sends(id,conv,topic,body,reply_to,quote,due_at,timezone,rev,state,audience,files,created_at,updated_at) SELECT ?,?,?,?,?,?,?,?,1,'pending',?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM outbox WHERE lid=?) AND NOT EXISTS(SELECT 1 FROM inbox WHERE lid=? AND local=1)`, s.ID, s.Conv, s.Topic, s.Body, s.ReplyTo, s.Quote, s.Due.UnixNano(), s.Timezone, string(audience), string(files), now, now, s.ID, s.ID)
		if err == nil {
			n, _ := result.RowsAffected()
			if n != 1 {
				err = ErrScheduledIDConflict
			}
		}
	} else {
		s.ID = id
		s.Rev = rev + 1
		var result sql.Result
		result, err = a.store.db.Exec(`UPDATE scheduled_sends SET conv=?,topic=?,body=?,reply_to=?,quote=?,due_at=?,timezone=?,rev=rev+1,state='pending',audience=?,files=?,detail='',updated_at=? WHERE id=? AND rev=? AND state IN ('pending','needs_review')`, s.Conv, s.Topic, s.Body, s.ReplyTo, s.Quote, s.Due.UnixNano(), s.Timezone, string(audience), string(files), now, id, rev)
		if err == nil {
			n, _ := result.RowsAffected()
			if n != 1 {
				err = ErrScheduledStale
			}
		}
	}
	if err = a.store.done(err); err != nil {
		return ScheduledSend{}, err
	}
	notifyDaemon(a.home)
	return s, nil
}
func (a *Agent) CancelScheduledSend(id string, rev int64) (ScheduledSend, error) {
	result, err := a.store.db.Exec(`UPDATE scheduled_sends SET state='cancelled',rev=rev+1,detail='',updated_at=? WHERE id=? AND rev=? AND state IN ('pending','needs_review')`, time.Now().UnixNano(), id, rev)
	if err != nil {
		return ScheduledSend{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ScheduledSend{}, ErrScheduledStale
	}
	a.store.changed()
	notifyDaemon(a.home)
	s, _, err := a.ScheduledSend(id)
	return s, err
}
func (a *Agent) SendScheduledNow(ctx context.Context, id string, rev int64) (ScheduledSend, error) {
	result, err := a.store.db.Exec(`UPDATE scheduled_sends SET state='pending',due_at=?,rev=rev+1,detail='',updated_at=? WHERE id=? AND rev=? AND state IN ('pending','needs_review')`, time.Now().UnixNano(), time.Now().UnixNano(), id, rev)
	if err != nil {
		return ScheduledSend{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ScheduledSend{}, ErrScheduledStale
	}
	a.store.changed()
	notifyDaemon(a.home)
	return a.dispatchScheduled(ctx, id)
}
func scheduledWithin(current, approved ScheduledAudience) bool {
	if current.AuthorPID != approved.AuthorPID {
		return false
	}
	for _, id := range current.Persons {
		if !slices.Contains(approved.Persons, id) {
			return false
		}
	}
	for _, id := range current.PIDs {
		if !slices.Contains(approved.PIDs, id) {
			return false
		}
	}
	return true
}
func (a *Agent) scheduledCommit(s ScheduledSend) beforeConvStore {
	return func(tx *sql.Tx, copies []outCopy, local envelope.Inner) error {
		if time.Now().Before(s.Due) {
			return errScheduledEarly
		}
		current, m, err := a.scheduledAudience(tx, s.Conv, s.Topic)
		if err != nil {
			return err
		}
		if !scheduledWithin(current, s.Audience) {
			return ErrScheduledAudience
		}
		valid := func(in envelope.Inner) bool {
			return in.Conv == s.Conv && in.Topic == s.Topic && in.LID == s.ID && in.From == a.Address && in.Body == s.Body && in.PID == s.Audience.AuthorPID && in.Kind == envelope.KindMessage && in.Sub == "" && in.Target == nil && in.AgentID == "" && in.Status == "" && in.TopicEvent == nil && in.Followup == nil && (in.Origin == "" || in.Origin == envelope.OriginUI)
		}
		if local.ID != "" && !valid(local) {
			return ErrScheduledAudience
		}
		for _, c := range copies {
			if !valid(c.in) || c.env.To != c.in.To {
				return ErrScheduledAudience
			}
			allowed := false
			for _, person := range s.Audience.Persons {
				if p, ok := m.persons[person]; ok && p.has(c.env.To, c.recipientFP) {
					allowed = true
					break
				}
			}
			if !allowed {
				for _, pid := range s.Audience.PIDs {
					p, e := participationIn(tx, s.Conv, pid, m, a.Address)
					if e != nil {
						return e
					}
					if p.Following() && (p.Topic == nil || *p.Topic == s.Topic) && p.Host.Address == c.env.To && p.Host.Fingerprint == c.recipientFP {
						allowed = true
						break
					}
				}
			}
			if !allowed {
				return ErrScheduledAudience
			}
		}
		result, err := tx.Exec(`UPDATE scheduled_sends SET state='sent',detail='',updated_at=? WHERE id=? AND rev=? AND state='dispatching' AND conv=? AND topic=? AND due_at=? AND due_at<=?`, time.Now().UnixNano(), s.ID, s.Rev, s.Conv, s.Topic, s.Due.UnixNano(), time.Now().UnixNano())
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return ErrScheduledStale
		}
		return nil
	}
}
func (a *Agent) dispatchScheduled(ctx context.Context, id string) (ScheduledSend, error) {
	// Serializes dispatch/recovery across daemon and local API processes. The
	// ordinary sender takes its own distinct spool lock; no recursive lock.
	release, err := lockfile.Wait(filepath.Join(a.home, "scheduled-send.lock"))
	if err != nil {
		return ScheduledSend{}, err
	}
	defer release()
	s, found, err := a.ScheduledSend(id)
	if err != nil {
		return s, err
	}
	if !found {
		return s, ErrScheduledStale
	}
	if s.State != ScheduledPending && s.State != ScheduledDispatching {
		return s, nil
	}
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	if time.Now().Before(s.Due) {
		return s, errScheduledEarly
	}
	var committed bool
	if err = a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox WHERE conv=? AND lid=?)`, s.Conv, s.ID).Scan(&committed); err != nil {
		return s, err
	}
	if committed {
		_, err = a.store.db.Exec(`UPDATE scheduled_sends SET state='sent',detail='',updated_at=? WHERE id=? AND state IN ('pending','dispatching')`, time.Now().UnixNano(), id)
		if err == nil {
			a.store.changed()
		}
		s, _, _ = a.ScheduledSend(id)
		return s, err
	}
	if s.State == ScheduledPending {
		result, e := a.store.db.Exec(`UPDATE scheduled_sends SET state='dispatching',rev=rev+1,updated_at=? WHERE id=? AND rev=? AND state='pending' AND due_at<=?`, time.Now().UnixNano(), id, s.Rev, time.Now().UnixNano())
		if e != nil {
			return s, e
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return s, ErrScheduledStale
		}
		s.State = ScheduledDispatching
		s.Rev++
	}
	fail := func(state, detail string, cause error) (ScheduledSend, error) {
		result, e := a.store.db.Exec(`UPDATE scheduled_sends SET state=?,detail=?,updated_at=? WHERE id=? AND rev=? AND state='dispatching'`, state, detail, time.Now().UnixNano(), id, s.Rev)
		if e != nil {
			return s, e
		}
		n, _ := result.RowsAffected()
		if n == 1 && (state == ScheduledNeedsReview || detail != s.Detail) {
			a.store.changed()
		}
		out, _, e := a.ScheduledSend(id)
		if e != nil {
			return out, e
		}
		return out, cause
	}
	current, _, err := a.scheduledAudience(a.store.db, s.Conv, s.Topic)
	if err != nil {
		if scheduledWait(err) {
			return fail(ScheduledPending, "Waiting for current chat information or connectivity.", err)
		}
		return fail(ScheduledNeedsReview, "This chat or topic is no longer available. Review the schedule.", err)
	}
	if !scheduledWithin(current, s.Audience) {
		return fail(ScheduledNeedsReview, "The chat audience changed. Review the schedule.", ErrScheduledAudience)
	}
	dir := filepath.Join(a.home, "opened")
	if err = secfile.EnsureDir(dir); err != nil {
		return fail(ScheduledPending, "Waiting for local attachment storage.", err)
	}
	var files []OutgoingFile
	defer func() {
		for _, f := range files {
			os.Remove(f.Path)
		}
	}()
	for _, f := range s.Files {
		path, e := a.openKept(dir, f.SHA256, f.Size)
		if e != nil {
			return fail(ScheduledNeedsReview, "An attachment is unavailable or changed. Review the schedule.", e)
		}
		files = append(files, OutgoingFile{Path: path, Name: f.Name})
	}
	dispatchCtx := context.WithValue(WithQueuedSend(ctx, s.ID), scheduledDispatchKey{}, s.ID)
	_, err = a.SendConv(dispatchCtx, s.Conv, ConvOutgoing{Kind: envelope.KindMessage, Origin: envelope.OriginUI, Body: s.Body, Topic: s.Topic, ReplyTo: s.ReplyTo, Quote: s.Quote, PID: s.Audience.AuthorPID, Files: files, beforeStore: a.scheduledCommit(s)})
	if err != nil {
		// An ordinary authority guard may reject a changed group before our
		// neutral guard runs. Growth still requires explicit audience review.
		latest, _, audienceErr := a.scheduledAudience(a.store.db, s.Conv, s.Topic)
		if audienceErr == nil && !scheduledWithin(latest, s.Audience) {
			return fail(ScheduledNeedsReview, "The chat audience changed. Review the schedule.", ErrScheduledAudience)
		}
		if scheduledWait(err) {
			return fail(ScheduledPending, "Waiting for current chat information or connectivity.", err)
		}
		return fail(ScheduledNeedsReview, "The message could not be queued safely. Review the schedule.", err)
	}
	out, _, err := a.ScheduledSend(id)
	return out, err
}

func scheduledWait(err error) bool {
	return hubUnreachable(err) || transient(err) || errors.Is(err, ErrGroupContextPending) || errors.Is(err, errParticipationTopicPending) || errors.Is(err, errScheduledEarly)
}

// scheduledDue is called by the existing reminder loop. Overdue schedules that
// cannot plan wait for a real Changed/wake, never a hot overdue wall timer.
func (a *Agent) scheduledDue(ctx context.Context, now time.Time) (time.Time, error) {
	rows, err := a.store.db.Query(`SELECT id FROM scheduled_sends WHERE state IN ('pending','dispatching') AND due_at<=? ORDER BY due_at,id`, now.UnixNano())
	if err != nil {
		return time.Time{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return time.Time{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return time.Time{}, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return time.Time{}, ctx.Err()
		}
		_, _ = a.dispatchScheduled(ctx, id)
	}
	var next sql.NullInt64
	err = a.store.db.QueryRow(`SELECT min(due_at) FROM scheduled_sends WHERE state IN ('pending','dispatching') AND due_at>?`, time.Now().UnixNano()).Scan(&next)
	if err != nil || !next.Valid {
		return time.Time{}, err
	}
	return time.Unix(0, next.Int64), nil
}
