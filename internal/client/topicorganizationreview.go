package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

var ErrTopicOrganizationChanged = errors.New("the selected messages, topic or audience changed; review the move again")

// A review is local and inert. Apply echoes its exact selection and token;
// the operation ID makes a retry of the same reviewed action idempotent.
type TopicOrganizationRequest struct {
	Conv  string   `json:"conv"`
	IDs   []string `json:"ids"`
	Topic string   `json:"topic"`
	Merge string   `json:"merge,omitempty"`
	New   bool     `json:"new,omitempty"`
	Title string   `json:"title,omitempty"`
}
type TopicOrganizationReview struct {
	TopicOrganizationRequest
	Operation string               `json:"operation"`
	Token     string               `json:"token"`
	Moves     []envelope.TopicMove `json:"moves"`
	Parent    string               `json:"parent,omitempty"`
}

func (a *Agent) PreviewTopicOrganization(request TopicOrganizationRequest) (TopicOrganizationReview, error) {
	var out TopicOrganizationReview
	if !protocol.ValidHash(request.Conv) || len(request.IDs) == 0 || len(request.IDs) > envelope.MaxTopicMoves || !protocol.ValidID(request.Topic) || request.Merge != "" && (!protocol.ValidID(request.Merge) || request.Merge == request.Topic) {
		return out, errors.New("choose at most 200 messages and a different topic in this chat")
	}
	request.Title = strings.Join(strings.Fields(request.Title), " ")
	if utf8.RuneCountInString(request.Title) > TopicTitleMax {
		return out, ErrTopicTitle
	}
	if err := topicOrganizationAuthor(a.store.db, request.Conv, a.Address, a.Self().Fingerprint()); err != nil {
		return out, err
	}
	before, err := a.topicOrganizationStamp(a.store.db, request)
	if err != nil {
		return out, err
	}
	msgs, err := a.ConversationMessages(request.Conv)
	if err != nil {
		return out, err
	}
	assigned := ChatTopicAssignments(msgs)
	if request.Merge != "" {
		redirects := ChatTopicRedirects(msgs)
		for next, seen := request.Topic, map[string]bool{}; next != "" && !seen[next]; next = redirects[next] {
			if next == request.Merge {
				return out, errors.New("this merge would create a topic cycle")
			}
			seen[next] = true
		}
	}
	topics, err := a.ChatTopicsForMessages(request.Conv, msgs)
	if err != nil {
		return out, err
	}
	known := false
	for _, topic := range topics {
		if topic.ID == request.Topic {
			known = true
			if request.New || topic.State != TopicActive {
				return out, errors.New("choose an active destination topic, or reopen it first")
			}
		}
	}
	if !request.New && !known {
		return out, ErrNoMessage
	}
	seen := map[string]bool{}
	for _, id := range request.IDs {
		if !protocol.ValidID(id) || seen[id] {
			return out, errors.New("invalid or duplicate selected message")
		}
		seen[id] = true
		var selected *ConvMessage
		for i := range msgs {
			m := &msgs[i]
			if m.ID != id && m.LID != id {
				continue
			}
			if selected != nil {
				return out, errors.New("selected message identity is ambiguous")
			}
			selected = m
		}
		if selected == nil || selected.Sub != "" || selected.TopicEvent != nil || selected.ExcerptPID != "" || selected.Deleted {
			return out, ErrNoMessage
		}
		m := *selected
		author := m.Key
		if author == "" {
			author = m.Claimed
		}
		if !protocol.ValidFingerprint(author) || assigned[m.LID] == request.Topic || request.Merge != "" && assigned[m.LID] != request.Merge {
			return out, ErrTopicOrganizationChanged
		}
		rows, e := a.historySourceRows(a.store.db, "conv=? AND (id=? OR lid=?)", "dir,id", 1, request.Conv, m.ID, m.LID)
		if e != nil || len(rows) != 1 {
			if e != nil {
				return out, e
			}
			return out, ErrNoMessage
		}
		item, e := a.historySourceItem(a.store.db, rows[0])
		if e != nil {
			return out, e
		}
		inner := item.inner(request.Conv)
		inner.Body = m.Controls.Shown(m.Body)
		out.Moves = append(out.Moves, envelope.TopicMove{LID: m.LID, Author: author, Hash: contentHash(inner), Topic: assigned[m.LID]})
	}
	sort.Slice(out.Moves, func(i, j int) bool {
		if out.Moves[i].Author != out.Moves[j].Author {
			return out.Moves[i].Author < out.Moves[j].Author
		}
		return out.Moves[i].LID < out.Moves[j].LID
	})
	var events []ConvMessage
	for _, m := range msgs {
		if envelope.TopicOrganization(m.TopicEvent) {
			events = append(events, m)
		}
	}
	sortChatEvents(events)
	if len(events) > 0 {
		out.Parent = events[len(events)-1].LID
	}
	after, err := a.topicOrganizationStamp(a.store.db, request)
	if err != nil {
		return out, err
	}
	if before != after {
		return out, ErrTopicOrganizationChanged
	}
	out.TopicOrganizationRequest = request
	out.Token = after
	out.Operation = protocol.NewID()
	return out, nil
}

func (a *Agent) ApplyTopicOrganization(ctx context.Context, review TopicOrganizationReview) (ConvSent, error) {
	if !protocol.ValidID(review.Operation) || !protocol.ValidHash(review.Token) {
		return ConvSent{}, ErrTopicOrganizationChanged
	}
	event := &envelope.TopicEvent{Action: "move", Moves: review.Moves, Merge: review.Merge}
	if review.Merge != "" {
		event.Action = "merge"
	}
	if sent, found, err := a.keptTopicOrganization(review, event); found || err != nil {
		return sent, err
	}
	current, err := a.PreviewTopicOrganization(review.TopicOrganizationRequest)
	if err != nil {
		return ConvSent{}, ErrTopicOrganizationChanged
	}
	left, _ := json.Marshal(current.Moves)
	right, _ := json.Marshal(review.Moves)
	if current.Token != review.Token || current.Parent != review.Parent || string(left) != string(right) {
		return ConvSent{}, ErrTopicOrganizationChanged
	}
	claim := func(tx *sql.Tx, _ string) error {
		stamp, e := a.topicOrganizationStamp(tx, review.TopicOrganizationRequest)
		if e != nil {
			return ErrTopicOrganizationChanged
		}
		if stamp != review.Token {
			return ErrTopicOrganizationChanged
		}
		if review.New && review.Title != "" {
			return a.recordTopicTitle(tx, review.Conv, review.Topic, review.Title)
		}
		return nil
	}
	sent, err := a.SendConv(WithQueuedSend(ctx, review.Operation), review.Conv, ConvOutgoing{Body: "Moved selected messages to this topic.", Topic: review.Topic, ReplyTo: review.Parent, TopicEvent: event, claim: claim})
	if err != nil {
		// A concurrent identical click may have committed between preview
		// and the existing queued-send claim. Return only that exact action.
		if kept, found, e := a.keptTopicOrganization(review, event); found || e != nil {
			return kept, e
		}
	}
	if err == nil && review.New && review.Title != "" {
		a.topicTitlesChanged()
	}
	return sent, err
}

func (a *Agent) keptTopicOrganization(review TopicOrganizationReview, event *envelope.TopicEvent) (ConvSent, bool, error) {
	var topic, raw, parent string
	err := a.store.db.QueryRow(`SELECT coalesce(topic,''),coalesce(topic_event,''),coalesce(reply_to,'') FROM outbox WHERE conv=? AND lid=? LIMIT 1`, review.Conv, review.Operation).Scan(&topic, &raw, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return ConvSent{}, false, nil
	}
	if err != nil {
		return ConvSent{}, false, err
	}
	if topic != review.Topic || raw != topicEventJSON(event) || parent != review.Parent {
		return ConvSent{}, true, ErrTopicOrganizationChanged
	}
	copies, err := a.SentCopies(review.Operation)
	if err != nil {
		return ConvSent{}, true, err
	}
	if len(copies) == 0 {
		return ConvSent{}, true, ErrNoMessage
	}
	return ConvSent{ID: copies[0].ID, LID: review.Operation, State: copies[0].State, Copies: copies}, true, nil
}

// Read only the selected originals and controls plus small organization and
// membership metadata. No full conversation rescan is added to rendering or
// the transaction that commits a reviewed action.
func (a *Agent) topicOrganizationStamp(q dbq, request TopicOrganizationRequest) (string, error) {
	h := sha256.New()
	enc := json.NewEncoder(h)
	if err := enc.Encode(request); err != nil {
		return "", err
	}
	m, err := membersIn(q, request.Conv)
	if err != nil {
		return "", err
	}
	var people []string
	for id, p := range m.persons {
		people = append(people, id+":"+p.roster.Hash()+":"+p.info.State)
	}
	sort.Strings(people)
	if err = enc.Encode(people); err != nil {
		return "", err
	}
	var addresses []string
	for _, p := range m.persons {
		for _, d := range p.roster.Devices {
			if d.Address != a.Address {
				addresses = append(addresses, d.Address)
			}
		}
	}
	sort.Strings(addresses)
	for _, address := range addresses {
		var public, pending string
		e := q.QueryRow(`SELECT public,coalesce(pending,'') FROM peers WHERE address=?`, address).Scan(&public, &pending)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return "", e
		}
		if err = enc.Encode([3]string{address, public, pending}); err != nil {
			return "", err
		}
	}
	if m.group != nil {
		if err = enc.Encode(m.group); err != nil {
			return "", err
		}
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(request.IDs)), ",")
	args := []any{request.Conv}
	for _, id := range request.IDs {
		args = append(args, id)
	}
	for _, id := range request.IDs {
		args = append(args, id)
	}
	rows, err := a.historySourceRows(q, "conv=? AND (id IN ("+marks+") OR lid IN ("+marks+"))", "dir,id", len(request.IDs)*3+1, args...)
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		item, e := a.historySourceItem(q, r)
		if e != nil {
			return "", e
		}
		if e = enc.Encode(item); e != nil {
			return "", e
		}
		controls, e := q.Query(`SELECT id,sub,body FROM inbox WHERE conv=? AND ref_id=? AND ref_fp=? AND sub IN ('revision','retraction') UNION ALL SELECT id,sub,body FROM outbox WHERE conv=? AND ref_id=? AND ref_fp=? AND sub IN ('revision','retraction') ORDER BY id`, request.Conv, item.LID, item.FromKey, request.Conv, item.LID, item.FromKey)
		if e != nil {
			return "", e
		}
		for controls.Next() {
			var v [3]string
			if e = controls.Scan(&v[0], &v[1], &v[2]); e != nil {
				break
			}
			e = enc.Encode(v)
			if e != nil {
				break
			}
		}
		if e == nil {
			e = controls.Err()
		}
		controls.Close()
		if e != nil {
			return "", e
		}
	}
	metadata, err := q.Query(`SELECT id,coalesce(topic,''),topic_event,'' FROM inbox WHERE conv=? AND topic_event IS NOT NULL UNION ALL SELECT id,coalesce(topic,''),topic_event,'' FROM outbox WHERE conv=? AND topic_event IS NOT NULL UNION ALL SELECT topic,coalesce(mark,''),coalesce(cast(mark_at AS TEXT),''),coalesce(title,'') FROM topic_state WHERE peer=? ORDER BY 1`, request.Conv, request.Conv, request.Conv)
	if err != nil {
		return "", err
	}
	defer metadata.Close()
	for metadata.Next() {
		var v [4]string
		if err = metadata.Scan(&v[0], &v[1], &v[2], &v[3]); err != nil {
			return "", err
		}
		if err = enc.Encode(v); err != nil {
			return "", err
		}
	}
	if err = metadata.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
