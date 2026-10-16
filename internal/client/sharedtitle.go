package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// One latest admitted register, independent of all per-person private labels.
const sharedTopicTitleSchema = `
CREATE TABLE topic_shared_titles(conv TEXT NOT NULL,topic TEXT NOT NULL,title TEXT NOT NULL,rev INTEGER NOT NULL,writer TEXT NOT NULL,lid TEXT NOT NULL,PRIMARY KEY(conv,topic));
ALTER TABLE outbox ADD COLUMN shared_title_cap INTEGER NOT NULL DEFAULT 0;
`

// SharedTopicTitleRecord includes presence even when Title is the empty reset.
type SharedTopicTitleRecord struct {
	Title  string `json:"title"`
	Rev    int64  `json:"rev"`
	Writer string `json:"writer"`
	LID    string `json:"lid"`
}

var ErrSharedTopicTitleStale = errors.New("shared topic title changed; retry the rename")
var ErrSharedTopicTitleConflict = errors.New("shared topic title conflicts at the same revision and writer")

func sharedTopicTitle(q dbq, conv, topic string) (SharedTopicTitleRecord, bool, error) {
	var r SharedTopicTitleRecord
	err := q.QueryRow(`SELECT title,rev,writer,lid FROM topic_shared_titles WHERE conv=? AND topic=?`, conv, topic).Scan(&r.Title, &r.Rev, &r.Writer, &r.LID)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// SharedTopicTitle reads admitted shared presence. Private names never seed it.
func (a *Agent) SharedTopicTitle(conv, topic string) (SharedTopicTitleRecord, bool, error) {
	return sharedTopicTitle(a.store.db, conv, topic)
}

// RenameSharedTopic queues ordinary human metadata through the usual native
// audience/admission route. A stale local allocation is an explicit retry error.
func (a *Agent) RenameSharedTopic(ctx context.Context, conv, topic, title string) (ConvSent, error) {
	old, _, err := a.SharedTopicTitle(conv, topic)
	if err != nil {
		return ConvSent{}, err
	}
	if old.Rev >= protocol.MaxTopicTitleRevision {
		return ConvSent{}, errors.New("shared topic title revision limit reached")
	}
	title = envelope.NormalizeSharedTopicTitle(title)
	pid, err := a.ownHumanPID(conv, topic)
	if err != nil {
		return ConvSent{}, err
	}
	return a.SendConv(ctx, conv, ConvOutgoing{PID: pid, Topic: topic, TopicEvent: &envelope.TopicEvent{Action: "rename", Title: &title, Rev: old.Rev + 1}})
}

func (a *Agent) prepareSharedTopicTitle(conv string, m *ConvOutgoing) error {
	if !envelope.TopicRename(m.TopicEvent) {
		return nil
	}
	old, present, err := a.SharedTopicTitle(conv, m.Topic)
	if err != nil {
		return err
	}
	if old.Rev >= protocol.MaxTopicTitleRevision || m.TopicEvent.Rev != old.Rev+1 {
		return ErrSharedTopicTitleStale
	}
	kind := m.Kind
	if kind == "" {
		kind = envelope.KindMessage
	}
	in := envelope.Inner{V: envelope.Version2, Conv: conv, Topic: m.Topic, TopicEvent: m.TopicEvent, Kind: kind, Body: m.Body, Target: m.Target, Status: m.status, Sub: m.sub, PID: m.PID, Human: m.human, AgentID: m.AgentID, Origin: m.Origin, Quote: m.Quote, Followup: m.Followup, TopicDone: m.TopicDone, SendGroup: m.SendGroup}
	// Captured Human is assembled by the normal send planner after this check.
	if in.Human == nil {
		in.PID = ""
	}
	if len(m.Files) != 0 {
		return errors.New("shared topic rename cannot attach files")
	}
	if err = envelope.CheckTopic(in); err != nil {
		return err
	}
	prior := m.beforeStore
	m.beforeStore = func(tx *sql.Tx, copies []outCopy, local envelope.Inner) error {
		current, found, e := sharedTopicTitle(tx, conv, m.Topic)
		if e != nil {
			return e
		}
		if present != found || current != old {
			return ErrSharedTopicTitleStale
		}
		if prior != nil {
			if e = prior(tx, copies, local); e != nil {
				return e
			}
		}
		var actual *envelope.Inner
		for i := range copies {
			if envelope.TopicRename(copies[i].in.TopicEvent) {
				actual = &copies[i].in
				break
			}
		}
		if actual == nil && envelope.TopicRename(local.TopicEvent) {
			actual = &local
		}
		if actual == nil {
			return errors.New("shared topic rename has no committed metadata copy")
		}
		if actual.Conv != conv || actual.Topic != m.Topic || actual.TopicEvent.Rev != old.Rev+1 {
			return ErrSharedTopicTitleStale
		}
		return saveSharedTopicRename(tx, *actual, a.Self().Fingerprint())
	}
	return nil
}

// Called only after the caller's exact live/historical original-author checks.
func saveSharedTopicRename(tx *sql.Tx, in envelope.Inner, writer string) error {
	if !envelope.TopicRename(in.TopicEvent) {
		return nil
	}
	if err := envelope.CheckTopic(in); err != nil {
		return err
	}
	if !protocol.ValidFingerprint(writer) || !protocol.ValidID(in.LID) {
		return errors.New("shared topic rename lacks exact original identity")
	}
	e := in.TopicEvent
	old, found, err := sharedTopicTitle(tx, in.Conv, in.Topic)
	if err != nil {
		return err
	}
	if found {
		if e.Rev < old.Rev || e.Rev == old.Rev && writer < old.Writer {
			return nil
		}
		if e.Rev == old.Rev && writer == old.Writer {
			if *e.Title != old.Title {
				return ErrSharedTopicTitleConflict
			}
			return nil
		}
	}
	_, err = tx.Exec(`INSERT INTO topic_shared_titles(conv,topic,title,rev,writer,lid) VALUES(?,?,?,?,?,?) ON CONFLICT(conv,topic) DO UPDATE SET title=excluded.title,rev=excluded.rev,writer=excluded.writer,lid=excluded.lid`, in.Conv, in.Topic, *e.Title, e.Rev, writer, in.LID)
	return err
}

func sharedTopicRenameCopy(in envelope.Inner) (bool, error) {
	if in.Sub == envelope.SubHistory && in.Body != "" {
		var item HistoryItem
		if err := json.Unmarshal([]byte(in.Body), &item); err != nil {
			return false, err
		}
		return envelope.TopicRename(item.TopicEvent), nil
	}
	return envelope.TopicRename(in.TopicEvent), nil
}

func storeSharedTopicRequirement(tx *sql.Tx, c outCopy) error {
	needed, err := sharedTopicRenameCopy(c.in)
	if err != nil {
		return err
	}
	needed = needed || c.sharedTitleCap
	if !needed {
		return nil
	}
	_, err = tx.Exec(`UPDATE outbox SET shared_title_cap=1 WHERE id=?`, c.env.ID)
	return err
}
