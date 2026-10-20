package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type TopicOrganizationUndo struct {
	Conv   string `json:"conv,omitempty"`
	Peer   string `json:"peer,omitempty"`
	LID    string `json:"lid"`
	Author string `json:"author"`
	ID     string `json:"id"` // one human action's durable retry identity
}

// Only this bounded inverse may stage several already-reviewed source groups.
// Normal message sends and native actions never use this local batch.
type topicUndoBatch struct {
	copies []outCopy
	claims []func(*sql.Tx, string) error
	ids    []string
}

func (b *topicUndoBatch) stage(a *Agent, copies []outCopy, local envelope.Inner, claim func(*sql.Tx, string) error) (ConvSent, error) {
	if len(copies) == 0 {
		return ConvSent{}, ErrNoMessage
	}
	for _, copy := range copies {
		if !envelope.TopicOrganization(copy.in.TopicEvent) || copy.in.PID != "" || copy.in.Target != nil || copy.in.Human != nil || len(copy.in.Attachments) != 0 || envelope.AgentOrigin(copy.in.Origin) {
			return ConvSent{}, errors.New("only a human topic inverse may join this batch")
		}
	}
	b.copies = append(b.copies, copies...)
	b.claims = append(b.claims, claim)
	b.ids = append(b.ids, copies[0].env.ID)
	sent := ConvSent{ID: copies[0].env.ID, LID: copies[0].in.LID, State: protocol.StateDelivered}
	for _, copy := range copies {
		sent.Copies = append(sent.Copies, ConvCopy{ID: copy.env.ID, To: copy.env.To, State: copy.state, Detail: copy.why})
		if rank(copy.state) < rank(sent.State) {
			sent.State, sent.Detail = copy.state, copy.why
		}
	}
	return sent, nil
}
func (b *topicUndoBatch) commit(a *Agent) error {
	return a.store.addConvOutbox(b.copies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		for i, claim := range b.claims {
			if claim != nil {
				if err := claim(tx, b.ids[i]); err != nil {
					return err
				}
			}
		}
		return nil
	}, "")
}

func undoOperation(request TopicOrganizationUndo, topic string) string {
	h := sha256.Sum256([]byte("agentnet-topic-undo-v1\n" + request.Conv + "\n" + request.LID + "\n" + request.Author + "\n" + request.ID + "\n" + topic))
	return hex.EncodeToString(h[:16])
}

// UndoTopicOrganization prepares every inverse and checks every visible version
// before any effect. Their current stamps/author/recipient checks commit in one
// existing outbox transaction; stale or interrupted batches store nothing.
func (a *Agent) UndoTopicOrganization(ctx context.Context, request TopicOrganizationUndo) ([]ConvSent, error) {
	if request.Peer != "" {
		return a.undoDirectTopicOrganization(request)
	}
	if !protocol.ValidHash(request.Conv) || !protocol.ValidID(request.LID) || !protocol.ValidFingerprint(request.Author) || !protocol.ValidID(request.ID) {
		return nil, ErrNoMessage
	}
	if err := topicOrganizationAuthor(a.store.db, request.Conv, a.Address, a.Self().Fingerprint()); err != nil {
		return nil, err
	}
	msgs, err := a.ConversationMessages(request.Conv)
	if err != nil {
		return nil, err
	}
	var original *ConvMessage
	for i := range msgs {
		key := msgs[i].Key
		if key == "" {
			key = msgs[i].Claimed
		}
		if msgs[i].LID == request.LID && key == request.Author {
			if original != nil {
				return nil, ErrTopicOrganizationChanged
			}
			original = &msgs[i]
		}
	}
	if original == nil || original.TopicEvent == nil || original.TopicEvent.Request == nil || !original.VerifiedAgent || original.TopicEvent.Action != "move" {
		return nil, ErrNoMessage
	}
	byTopic := map[string][]envelope.TopicMove{}
	for _, move := range original.TopicEvent.Moves {
		byTopic[move.Topic] = append(byTopic[move.Topic], move)
	}
	var topics []string
	for topic := range byTopic {
		topics = append(topics, topic)
	}
	sort.Strings(topics)
	var kept []ConvSent
	for _, topic := range topics {
		moves := append([]envelope.TopicMove(nil), byTopic[topic]...)
		for i := range moves {
			moves[i].Topic = original.Topic
		}
		sort.Slice(moves, func(i, j int) bool {
			if moves[i].Author != moves[j].Author {
				return moves[i].Author < moves[j].Author
			}
			return moves[i].LID < moves[j].LID
		})
		operation := undoOperation(request, topic)
		var parent string
		e := a.store.db.QueryRow(`SELECT coalesce(reply_to,'') FROM outbox WHERE conv=? AND lid=? LIMIT 1`, request.Conv, operation).Scan(&parent)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		sent, found, e := a.keptTopicOrganization(TopicOrganizationReview{TopicOrganizationRequest: TopicOrganizationRequest{Conv: request.Conv, Topic: topic, Main: topic == ""}, Operation: operation, Parent: parent}, &envelope.TopicEvent{Action: "move", Moves: moves})
		if e != nil || !found {
			return nil, ErrTopicOrganizationChanged
		}
		kept = append(kept, sent)
	}
	if len(kept) == len(topics) {
		return kept, nil
	}
	if len(kept) != 0 {
		return nil, ErrTopicOrganizationChanged
	}
	selected := map[protocol.GrantRef]bool{}
	for _, move := range original.TopicEvent.Moves {
		selected[protocol.GrantRef{LID: move.LID, Fingerprint: move.Author}] = true
	}
	var events []ConvMessage
	for _, m := range msgs {
		if envelope.TopicOrganization(m.TopicEvent) {
			events = append(events, m)
		}
	}
	sortChatEvents(events)
	after := false
	for _, m := range events {
		if m.LID == original.LID {
			after = true
			continue
		}
		if after {
			for _, move := range m.TopicEvent.Moves {
				if selected[protocol.GrantRef{LID: move.LID, Fingerprint: move.Author}] {
					return nil, ErrTopicOrganizationChanged
				}
			}
		}
	}
	var reviews []TopicOrganizationReview
	for _, topic := range topics {
		var ids []string
		for _, move := range byTopic[topic] {
			ids = append(ids, move.LID)
		}
		review, e := a.PreviewTopicOrganization(TopicOrganizationRequest{Conv: request.Conv, IDs: ids, Topic: topic, Main: topic == ""})
		if e != nil {
			return nil, e
		}
		for _, move := range review.Moves {
			matched := false
			for _, old := range byTopic[topic] {
				if old.LID == move.LID && old.Author == move.Author && old.Hash == move.Hash && move.Topic == original.Topic {
					matched = true
				}
			}
			if !matched {
				return nil, ErrTopicOrganizationChanged
			}
		}
		review.Operation = undoOperation(request, topic)
		reviews = append(reviews, review)
	}
	batch := &topicUndoBatch{}
	var results []ConvSent
	for _, review := range reviews {
		r := review
		claim := func(tx *sql.Tx, _ string) error {
			stamp, e := a.topicOrganizationStamp(tx, r.TopicOrganizationRequest)
			if e != nil || stamp != r.Token {
				return ErrTopicOrganizationChanged
			}
			return nil
		}
		sent, e := a.SendConv(WithQueuedSend(ctx, r.Operation), request.Conv, ConvOutgoing{Body: "Restored selected messages to their previous topic.", Topic: r.Topic, TopicEvent: &envelope.TopicEvent{Action: "move", Moves: r.Moves}, ReplyTo: r.Parent, Origin: envelope.OriginUI, claim: claim, organizationBatch: batch})
		if e != nil {
			return nil, e
		}
		results = append(results, sent)
	}
	if err = batch.commit(a); err != nil {
		return nil, err
	}
	a.NoteChange()
	a.postOutboxBackground()
	return results, nil
}
