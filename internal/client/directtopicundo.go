package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Undo is one local atomic inverse. A subsequent placement, even a move back to
// the same topic, prevents undo from overwriting newer organizational choices.
func (a *Agent) undoDirectTopicOrganization(request TopicOrganizationUndo) ([]ConvSent, error) {
	if request.Conv != "" || !protocol.ValidID(request.LID) || !protocol.ValidID(request.ID) {
		return nil, ErrNoMessage
	}
	_, _, err := protocol.SplitAddress(request.Peer)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(request)
	kept := func(q dbq) (bool, error) {
		var old string
		e := q.QueryRow(`SELECT review FROM direct_topic_operations WHERE id=?`, request.ID).Scan(&old)
		if errors.Is(e, sql.ErrNoRows) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if old != string(raw) {
			return false, ErrTopicOrganizationChanged
		}
		return true, nil
	}
	result := []ConvSent{{ID: request.ID, LID: request.ID, State: "stored"}}
	if found, e := kept(a.store.db); found || e != nil {
		return result, e
	}
	var original string
	var revision int64
	if err = a.store.db.QueryRow(`SELECT review,revision FROM direct_topic_operations WHERE id=?`, request.LID).Scan(&original, &revision); err != nil {
		return nil, err
	}
	var review TopicOrganizationReview
	if json.Unmarshal([]byte(original), &review) != nil || review.Peer != request.Peer || len(review.Moves) == 0 || review.Destination == nil || !review.Destination.Valid() {
		return nil, ErrNoMessage
	}
	stamp, err := a.directTopicStamp(a.store.db, review.TopicOrganizationRequest)
	if err != nil {
		return nil, err
	}
	_, rows, err := a.peerThreadGroups(request.Peer)
	if err != nil {
		return nil, err
	}
	for _, m := range review.Moves {
		r, ok := rows[m.LID]
		_, source := rows[m.Topic]
		if !ok || !source && !slices.Contains(review.SyntheticTopics, m.Topic) || r.key != m.Author || r.destination != *review.Destination {
			return nil, ErrTopicOrganizationChanged
		}
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if found, e := kept(tx); found || e != nil {
		return result, e
	}
	current, err := a.directTopicStamp(tx, review.TopicOrganizationRequest)
	if err != nil {
		return nil, err
	}
	if current != stamp {
		return nil, ErrTopicOrganizationChanged
	}
	owner, err := a.ownTopicWriter(tx)
	if err != nil {
		return nil, err
	}
	var rev int64
	if err = tx.QueryRow(`SELECT coalesce(max(revision),0) FROM direct_topic_placements WHERE owner=?`, owner).Scan(&rev); err != nil {
		return nil, err
	}
	if rev >= protocol.MaxTopicTitleRevision {
		return nil, errors.New("direct topic revision limit reached")
	}
	for _, m := range review.Moves {
		var topic, writer string
		var synthetic bool
		var destination protocol.DirectTopicDestination
		var at int64
		if err = tx.QueryRow(`SELECT topic,revision,writer,synthetic,host,host_key,agent_id FROM direct_topic_placements WHERE owner=? AND author=? AND id=?`, owner, m.Author, m.LID).Scan(&topic, &at, &writer, &synthetic, &destination.Host, &destination.Fingerprint, &destination.AgentID); err != nil {
			return nil, err
		}
		if topic != review.Topic || at != revision || writer != a.Self().Fingerprint() || destination != *review.Destination || synthetic != slices.Contains(review.SyntheticTopics, review.Topic) {
			return nil, ErrTopicOrganizationChanged
		}
		if err = saveDirectTopicPlacement(tx, owner, protocol.DirectTopicPlacement{Peer: request.Peer, ID: m.LID, Author: m.Author, Topic: m.Topic, Revision: rev + 1, Writer: a.Self().Fingerprint(), Synthetic: slices.Contains(review.SyntheticTopics, m.Topic), Destination: *review.Destination}); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO direct_topic_operations(id,review,revision) VALUES(?,?,?)`, request.ID, string(raw), rev+1); err != nil {
		return nil, err
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.topicTitlesChanged()
	}
	return result, err
}
