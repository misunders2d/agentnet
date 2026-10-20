package client

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Personal presentation only. Original rows, reply links, native sessions and
// execution records are never changed. Missing originals leave inert facts.
const directTopicSchema = `
CREATE TABLE direct_topic_placements(owner TEXT NOT NULL,peer TEXT NOT NULL,id TEXT NOT NULL,author TEXT NOT NULL,topic TEXT NOT NULL,revision INTEGER NOT NULL,writer TEXT NOT NULL,synthetic INTEGER NOT NULL,host TEXT NOT NULL,host_key TEXT NOT NULL,agent_id TEXT NOT NULL,PRIMARY KEY(owner,author,id));
CREATE TABLE direct_topic_copies(owner TEXT NOT NULL,recipient_fp TEXT NOT NULL,peer TEXT NOT NULL,id TEXT NOT NULL,author TEXT NOT NULL,topic TEXT NOT NULL,revision INTEGER NOT NULL,writer TEXT NOT NULL,synthetic INTEGER NOT NULL,host TEXT NOT NULL,host_key TEXT NOT NULL,agent_id TEXT NOT NULL,carrier TEXT NOT NULL,PRIMARY KEY(owner,recipient_fp,author,id));
CREATE INDEX direct_topic_original ON direct_topic_placements(owner,id);
CREATE TABLE direct_topic_operations(id TEXT PRIMARY KEY,review TEXT NOT NULL,revision INTEGER NOT NULL);
`

func saveDirectTopicPlacement(tx *sql.Tx, owner string, p protocol.DirectTopicPlacement) error {
	_, err := tx.Exec(`INSERT INTO direct_topic_placements(owner,peer,id,author,topic,revision,writer,synthetic,host,host_key,agent_id) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(owner,author,id) DO UPDATE SET peer=excluded.peer,topic=excluded.topic,revision=excluded.revision,writer=excluded.writer,synthetic=excluded.synthetic,host=excluded.host,host_key=excluded.host_key,agent_id=excluded.agent_id WHERE (excluded.revision,excluded.writer,excluded.topic,excluded.peer,excluded.synthetic,excluded.host,excluded.host_key,excluded.agent_id)>(direct_topic_placements.revision,direct_topic_placements.writer,direct_topic_placements.topic,direct_topic_placements.peer,direct_topic_placements.synthetic,direct_topic_placements.host,direct_topic_placements.host_key,direct_topic_placements.agent_id)`, owner, p.Peer, p.ID, p.Author, p.Topic, p.Revision, p.Writer, p.Synthetic, p.Destination.Host, p.Destination.Fingerprint, p.Destination.AgentID)
	return err
}

func directTopicID(g []string, rows map[string]threadRow) string {
	if id := rows[g[0]].topic; id != "" {
		return id
	}
	return g[0]
}

func directTopicLocal(locals map[string]topicLocal, g []string, rows map[string]threadRow) (topicLocal, string) {
	id := directTopicID(g, rows)
	if rows[g[0]].organized {
		return locals[id], id
	}
	return localOf(locals, g)
}

func (a *Agent) organizeDirectGroups(peer string, groups [][]string, rows map[string]threadRow) ([][]string, error) {
	if len(groups) == 0 {
		return groups, nil
	}
	for _, g := range groups {
		for _, id := range g {
			r := rows[id]
			r.topic = g[0]
			rows[id] = r
		}
	}
	owner, err := a.ownTopicWriter(a.store.db)
	if err != nil {
		return nil, err
	}
	if owner == "" {
		return groups, nil
	}
	placements, err := a.store.db.Query(`SELECT peer,id,author,topic,revision,writer,synthetic,host,host_key,agent_id FROM direct_topic_placements WHERE owner=? AND id IN (SELECT id FROM device_thread_links WHERE peer=?)`, owner, peer)
	if err != nil {
		return nil, err
	}
	var facts []protocol.DirectTopicPlacement
	synthetic := map[string]protocol.DirectTopicDestination{}
	conflicts := map[string]bool{}
	for placements.Next() {
		var p protocol.DirectTopicPlacement
		if err = placements.Scan(&p.Peer, &p.ID, &p.Author, &p.Topic, &p.Revision, &p.Writer, &p.Synthetic, &p.Destination.Host, &p.Destination.Fingerprint, &p.Destination.AgentID); err != nil {
			break
		}
		facts = append(facts, p)
		if p.Synthetic {
			if old, ok := synthetic[p.Topic]; ok && old != p.Destination {
				conflicts[p.Topic] = true
			}
			synthetic[p.Topic] = p.Destination
		}
	}
	if err == nil {
		err = placements.Err()
	}
	placements.Close()
	if err != nil {
		return nil, err
	}
	dirty := map[string]bool{}
	for _, p := range facts {
		r, held := rows[p.ID]
		destination, destinationHeld := rows[p.Topic]
		if !held || r.key != p.Author || r.destination != p.Destination || !p.Destination.Valid() || conflicts[p.Topic] {
			continue
		}
		if p.Synthetic {
			if destinationHeld {
				continue
			} // a fresh topic never impersonates an original
		} else if !destinationHeld || destination.destination != p.Destination {
			continue
		}
		dirty[r.topic], dirty[p.Topic] = true, true
		r.topic, r.organized = p.Topic, true
		rows[p.ID] = r
	}
	byTopic := map[string][]string{}
	links := map[string]link{}
	for id, r := range rows {
		byTopic[r.topic] = append(byTopic[r.topic], id)
		links[id] = r.link
	}
	result := make([][]string, 0, len(byTopic))
	for topic, g := range byTopic {
		sortThread(g, links)
		// A partially moved root remains a message link to its new location.
		// Give the unselected remainder its earliest retained message as usual,
		// so every topic still has an unambiguous navigable message anchor.
		if _, virtual := synthetic[topic]; !virtual && rows[topic].topic != topic {
			topic = g[0]
		}
		for _, id := range g {
			r := rows[id]
			r.organized = dirty[r.topic]
			r.topic = topic
			rows[id] = r
		}
		result = append(result, g)
	}
	return result, nil
}

// Capture the complete direct-chat presentation inputs, not execution state.
// An incoming reply or an edit during review requires fresh selection.
func (a *Agent) directTopicStamp(q dbq, request TopicOrganizationRequest) (string, error) {
	h := sha256.New()
	enc := json.NewEncoder(h)
	if err := enc.Encode(request); err != nil {
		return "", err
	}
	owner, err := a.ownTopicWriter(q)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return "", errors.New("only a current own human device may organize direct topics")
	}
	me, _, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return "", err
	}
	if err = enc.Encode(me.info.Roster); err != nil {
		return "", err
	}
	queries := []struct {
		sql  string
		args []any
	}{
		{`SELECT l.id,l.author_fp,l.direction,coalesce(i.body,o.body,''),coalesce(i.reply_to,o.reply_to,''),json_array(coalesce(i.sub,o.sub,''),coalesce(i.target,o.target,''),coalesce(i.agent_id,o.agent_id,'')) FROM device_thread_links l LEFT JOIN inbox i ON l.storage='in' AND i.id=l.id LEFT JOIN outbox o ON l.storage='out' AND o.id=l.id WHERE l.peer=? ORDER BY l.id,l.storage`, []any{request.Peer}},
		{`SELECT id,author,topic,cast(revision AS TEXT),writer,peer||':'||synthetic||':'||host||':'||host_key||':'||agent_id FROM direct_topic_placements WHERE owner=? AND id IN (SELECT id FROM device_thread_links WHERE peer=?) ORDER BY author,id`, []any{owner, request.Peer}},
		{`SELECT topic,coalesce(title,''),coalesce(mark,''),coalesce(cast(mark_at AS TEXT),''),'','' FROM topic_state WHERE peer=? ORDER BY topic`, []any{request.Peer}},
		{`SELECT key,lid,'','','','' FROM conv_erased WHERE conv='' ORDER BY key,lid`, nil},
		{`SELECT address,public,coalesce(pending,''),'','','' FROM peers WHERE address=? ORDER BY address`, []any{request.Peer}},
	}
	for _, query := range queries {
		rs, e := q.Query(query.sql, query.args...)
		if e != nil {
			return "", e
		}
		for rs.Next() {
			var v [6]string
			if e = rs.Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5]); e != nil {
				break
			}
			if e = enc.Encode(v); e != nil {
				break
			}
		}
		if e == nil {
			e = rs.Err()
		}
		rs.Close()
		if e != nil {
			return "", e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *Agent) previewDirectTopicOrganization(request TopicOrganizationRequest) (TopicOrganizationReview, error) {
	var review TopicOrganizationReview
	_, _, e := protocol.SplitAddress(request.Peer)
	request.Title = strings.Join(strings.Fields(request.Title), " ")
	if e != nil || request.Conv != "" || request.Main || len(request.IDs) == 0 || len(request.IDs) > envelope.MaxTopicMoves || request.New && request.Title == "" || !request.New && request.Title != "" || request.Topic != "" && !protocol.ValidID(request.Topic) || !request.New && request.Topic == "" || request.Merge != "" && (!protocol.ValidID(request.Merge) || request.Merge == request.Topic) {
		return review, errors.New("choose at most 200 messages and an existing or named new topic in this agent chat")
	}
	if utf8.RuneCountInString(request.Title) > TopicTitleMax {
		return review, ErrTopicTitle
	}
	operation := protocol.NewID()
	if request.New {
		if request.Topic != "" {
			operation = request.Topic
		}
		request.Topic = operation
	}
	before, err := a.directTopicStamp(a.store.db, request)
	if err != nil {
		return review, err
	}
	groups, rows, err := a.peerThreadGroups(request.Peer)
	if err != nil {
		return review, err
	}
	virtual, err := a.directSyntheticTopics(request.Peer)
	if err != nil {
		return review, err
	}
	var destination protocol.DirectTopicDestination
	known := false
	for _, g := range groups {
		if directTopicID(g, rows) == request.Topic {
			if request.New {
				return review, ErrTopicOrganizationChanged
			}
			destination, err = directGroupDestination(g, rows)
			if err != nil {
				return review, ErrTopicOrganizationChanged
			}
			known = true
		}
	}
	if !request.New && !known {
		return review, ErrNoMessage
	}
	if request.New {
		if _, held := rows[request.Topic]; held {
			return review, ErrTopicOrganizationChanged
		}
		virtual[request.Topic] = true
	}
	seen := map[string]bool{}
	for _, id := range request.IDs {
		r, found := rows[id]
		if !found || !protocol.ValidID(id) || seen[id] || r.notice || r.topic == request.Topic || request.Merge != "" && r.topic != request.Merge {
			return review, ErrTopicOrganizationChanged
		}
		if !r.destination.Valid() {
			return review, ErrTopicOrganizationChanged
		}
		if !known {
			destination, known = r.destination, true
		}
		if r.destination != destination {
			return review, ErrTopicOrganizationChanged
		}
		seen[id] = true
		original, err := a.deviceHistoryOriginal(a.store.db, id)
		if err != nil {
			return review, err
		}
		if original.item.FromKey != r.key {
			return review, ErrTopicOrganizationChanged
		}
		review.Moves = append(review.Moves, envelope.TopicMove{LID: id, Author: r.key, Hash: deviceHistoryHash(original), Topic: r.topic})
	}
	review.Destination = &destination
	selectedVirtual := map[string]bool{}
	if virtual[request.Topic] {
		selectedVirtual[request.Topic] = true
	}
	for _, m := range review.Moves {
		if virtual[m.Topic] {
			selectedVirtual[m.Topic] = true
		}
	}
	for id := range selectedVirtual {
		review.SyntheticTopics = append(review.SyntheticTopics, id)
	}
	sort.Strings(review.SyntheticTopics)
	sort.Slice(review.Moves, func(i, j int) bool { return review.Moves[i].LID < review.Moves[j].LID })
	after, err := a.directTopicStamp(a.store.db, request)
	if err != nil {
		return review, err
	}
	if after != before {
		return review, ErrTopicOrganizationChanged
	}
	review.TopicOrganizationRequest, review.Operation, review.Token = request, operation, after
	return review, nil
}

func (a *Agent) directSyntheticTopics(peer string) (map[string]bool, error) {
	owner, err := a.ownTopicWriter(a.store.db)
	if err != nil {
		return nil, err
	}
	rows, err := a.store.db.Query(`SELECT DISTINCT topic FROM direct_topic_placements WHERE owner=? AND synthetic=1 AND id IN (SELECT id FROM device_thread_links WHERE peer=?)`, owner, peer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// A synthetic topic has no message of its own. Resolve its earliest held
// original for navigation only; the topic ID and original ancestry stay intact.
func (a *Agent) directTopicAnchor(id string) (string, error) {
	if _, err := a.store.peerOf(id); err == nil {
		return id, nil
	} else if !errors.Is(err, ErrNoMessage) {
		return "", err
	}
	owner, err := a.ownTopicWriter(a.store.db)
	if err != nil {
		return "", err
	}
	var peer string
	err = a.store.db.QueryRow(`SELECT l.peer FROM direct_topic_placements p JOIN device_thread_links l ON l.id=p.id AND (l.author_fp=p.author OR l.storage='out' AND l.author_fp='' AND p.author=?) WHERE p.owner=? AND p.topic=? AND p.synthetic=1 ORDER BY l.peer,p.id LIMIT 1`, a.Self().Fingerprint(), owner, id).Scan(&peer)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoMessage
	}
	if err != nil {
		return "", err
	}
	groups, rows, err := a.peerThreadGroups(peer)
	if err != nil {
		return "", err
	}
	for _, g := range groups {
		if directTopicID(g, rows) == id {
			return g[0], nil
		}
	}
	return "", ErrNoMessage
}

func (a *Agent) applyDirectTopicOrganization(review TopicOrganizationReview) (ConvSent, error) {
	raw, err := json.Marshal(review)
	if err != nil {
		return ConvSent{}, err
	}
	if !protocol.ValidID(review.Operation) || !protocol.ValidHash(review.Token) || review.Destination == nil || !review.Destination.Valid() || review.New && review.Topic != review.Operation {
		return ConvSent{}, ErrTopicOrganizationChanged
	}
	kept := func(q dbq) (bool, error) {
		var old string
		err := q.QueryRow(`SELECT review FROM direct_topic_operations WHERE id=?`, review.Operation).Scan(&old)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if old != string(raw) {
			return false, ErrTopicOrganizationChanged
		}
		return true, nil
	}
	result := ConvSent{ID: review.Operation, LID: review.Operation, State: "stored"}
	if found, err := kept(a.store.db); found || err != nil {
		return result, err
	}
	current, err := a.previewDirectTopicOrganization(review.TopicOrganizationRequest)
	if err != nil {
		return ConvSent{}, err
	}
	left, _ := json.Marshal(current.Moves)
	right, _ := json.Marshal(review.Moves)
	if current.Token != review.Token || string(left) != string(right) || *current.Destination != *review.Destination || !slices.Equal(current.SyntheticTopics, review.SyntheticTopics) {
		return ConvSent{}, ErrTopicOrganizationChanged
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return ConvSent{}, err
	}
	defer tx.Rollback()
	if found, err := kept(tx); found || err != nil {
		return result, err
	}
	stamp, err := a.directTopicStamp(tx, review.TopicOrganizationRequest)
	if err != nil {
		return ConvSent{}, err
	}
	if stamp != review.Token {
		return ConvSent{}, ErrTopicOrganizationChanged
	}
	owner, err := a.ownTopicWriter(tx)
	if err != nil {
		return ConvSent{}, err
	}
	var rev int64
	if err = tx.QueryRow(`SELECT coalesce(max(revision),0) FROM direct_topic_placements WHERE owner=?`, owner).Scan(&rev); err != nil {
		return ConvSent{}, err
	}
	if rev >= protocol.MaxTopicTitleRevision {
		return ConvSent{}, errors.New("direct topic revision limit reached")
	}
	for _, move := range review.Moves {
		p := protocol.DirectTopicPlacement{Peer: review.Peer, ID: move.LID, Author: move.Author, Topic: review.Topic, Revision: rev + 1, Writer: a.Self().Fingerprint(), Synthetic: slices.Contains(review.SyntheticTopics, review.Topic), Destination: *review.Destination}
		if !p.Valid() {
			return ConvSent{}, ErrTopicOrganizationChanged
		}
		if err = saveDirectTopicPlacement(tx, owner, p); err != nil {
			return ConvSent{}, err
		}
	}
	if review.New {
		if err = a.recordTopicTitle(tx, review.Peer, review.Topic, review.Title); err != nil {
			return ConvSent{}, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO direct_topic_operations(id,review,revision) VALUES(?,?,?)`, review.Operation, string(raw), rev+1); err != nil {
		return ConvSent{}, err
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.topicTitlesChanged()
	}
	return result, err
}
