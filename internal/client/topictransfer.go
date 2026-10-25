package client

// A transfer is an explicitly reviewed, finite quotation. Its manifest keeps
// references and commitments only; the ordinary encrypted outbox owns delivery.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const topicTransferSchema = `
CREATE TABLE topic_transfer_plans(id TEXT PRIMARY KEY,header TEXT NOT NULL,cursor INTEGER NOT NULL,state TEXT NOT NULL);
CREATE TABLE topic_transfer_manifest(plan TEXT NOT NULL,seq INTEGER NOT NULL,reference TEXT NOT NULL,PRIMARY KEY(plan,seq));
CREATE TABLE topic_locations(owner TEXT NOT NULL,scope TEXT NOT NULL,topic TEXT NOT NULL,fact TEXT NOT NULL,PRIMARY KEY(owner,scope,topic));
CREATE TABLE topic_location_copies(owner TEXT NOT NULL,recipient_fp TEXT NOT NULL,scope TEXT NOT NULL,topic TEXT NOT NULL,fact TEXT NOT NULL,carrier TEXT NOT NULL,PRIMARY KEY(owner,recipient_fp,scope,topic));
`

var ErrTopicTransferChanged = errors.New("the topic snapshot or destination audience changed; review the whole topic again")

type TopicTransferRequest struct {
	Source           string `json:"source,omitempty"`
	Topic            string `json:"topic,omitempty"`
	Peer             string `json:"peer,omitempty"`
	Thread           string `json:"thread,omitempty"`
	Destination      string `json:"destination"`
	DestinationTopic string `json:"destination_topic,omitempty"`
	NewTopic         bool   `json:"new_topic,omitempty"`
	Title            string `json:"title,omitempty"`
}
type TopicTransferReview struct {
	TopicTransferRequest
	ID              string                        `json:"id"`
	Token           string                        `json:"token"`
	CreatedAt       string                        `json:"created_at"`
	Cutoff          int64                         `json:"cutoff"`
	TotalItems      int                           `json:"total_items"`
	TotalBatches    int                           `json:"total_batches"`
	TotalFiles      int                           `json:"total_files"`
	TotalBytes      int64                         `json:"total_bytes"`
	Cursor          int                           `json:"cursor"`
	State           string                        `json:"state"`
	Audience        []HistoryContributionAudience `json:"audience"`
	AudiencePending bool                          `json:"audience_pending"`
	AudienceTotal   int                           `json:"audience_total"`
	AudienceNext    int                           `json:"audience_next"`
	Sent            *ConvSent                     `json:"sent,omitempty"`
}
type TopicTransferAction struct {
	ID            string `json:"id"`
	Token         string `json:"token,omitempty"`
	Cursor        int    `json:"cursor"`
	After         int    `json:"after"`
	Source        string `json:"source,omitempty"`
	Topic         string `json:"topic,omitempty"`
	Peer          string `json:"peer,omitempty"`
	Thread        string `json:"thread,omitempty"`
	AudienceAfter *int   `json:"audience_after,omitempty"`
}
type TopicTransferPage struct {
	ID            string                        `json:"id"`
	Token         string                        `json:"token"`
	After         int                           `json:"after"`
	Next          int                           `json:"next"`
	Done          bool                          `json:"done"`
	Items         []HistoryContributionItem     `json:"items"`
	Body          string                        `json:"body"`
	Audience      []HistoryContributionAudience `json:"audience,omitempty"`
	AudienceTotal int                           `json:"audience_total,omitempty"`
	AudienceNext  int                           `json:"audience_next,omitempty"`
}

func publicTransferReview(r TopicTransferReview) TopicTransferReview {
	r.Sent = nil
	r.AudienceTotal = len(r.Audience)
	n := min(16, len(r.Audience))
	r.Audience = r.Audience[:n]
	if n < r.AudienceTotal {
		r.AudienceNext = n
	}
	return r
}

type transferHeader struct {
	Review    TopicTransferReview `json:"review"`
	Authority string              `json:"authority"`
	Owner     string              `json:"owner"`
}
type transferRef struct {
	ID      string `json:"id"`
	LID     string `json:"lid"`
	Author  string `json:"author"`
	Hash    string `json:"hash"`
	Guard   string `json:"guard"`
	ReplyTo string `json:"reply_to,omitempty"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Files   []int  `json:"files"`
	LIDOut  string `json:"lid_out"`
}

func transferHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func transferScope(r TopicTransferRequest) (string, string) {
	if r.Source != "" {
		if r.Topic == "" {
			return r.Source, mainTopicKey
		}
		return r.Source, r.Topic
	}
	return r.Peer, r.Thread
}
func (a *Agent) transferAuthority(q dbq, r TopicTransferRequest) (string, []HistoryContributionAudience, bool, error) {
	if r.Source != "" {
		if e := topicOrganizationAuthor(q, r.Source, a.Address, a.Self().Fingerprint()); e != nil {
			return "", nil, false, e
		}
	} else if owner, e := a.ownTopicWriter(q); e != nil || owner == "" {
		if e == nil {
			e = errors.New("only a current own human device may share this topic")
		}
		return "", nil, false, e
	}
	audience, pending, e := a.historyContributionAudience(q, r.Destination, r.DestinationTopic, true)
	if e != nil {
		return "", nil, false, e
	}
	dest, e := a.topicOrganizationStamp(q, TopicOrganizationRequest{Conv: r.Destination})
	if e != nil {
		return "", nil, false, e
	}
	var source any
	if r.Source != "" {
		source, e = a.topicOrganizationStamp(q, TopicOrganizationRequest{Conv: r.Source})
	} else {
		me, _, x := scanPersonIn(q, "state = ?", personSelf)
		e = x
		var public, pending string
		if e == nil {
			e = q.QueryRow(`SELECT public,coalesce(pending,'') FROM peers WHERE address=?`, r.Peer).Scan(&public, &pending)
		}
		h := sha256.New()
		enc := json.NewEncoder(h)
		if e == nil {
			rows, x := q.Query(`SELECT id,author,topic,revision,writer,synthetic,host,host_key,agent_id FROM direct_topic_placements WHERE owner=? AND peer=? ORDER BY author,id`, me.info.Person, r.Peer)
			if x != nil {
				return "", nil, false, x
			}
			for rows.Next() {
				var id, author, topic, writer, host, key, agent string
				var revision int64
				var synthetic bool
				if x = rows.Scan(&id, &author, &topic, &revision, &writer, &synthetic, &host, &key, &agent); x != nil {
					break
				}
				_ = enc.Encode([]any{id, author, topic, revision, writer, synthetic, host, key, agent})
			}
			if x == nil {
				x = rows.Err()
			}
			rows.Close()
			e = x
		}
		source = []string{me.info.Person, me.info.Roster, public, pending, hex.EncodeToString(h.Sum(nil))}
	}
	if e != nil {
		return "", nil, false, e
	}
	return transferHash([]any{r, source, dest, audience, pending}), audience, pending, nil
}

// The source list is resolved by the server, never by a caller's loaded page.
func (a *Agent) transferMessages(r TopicTransferRequest) ([]ConvMessage, error) {
	if r.Source != "" {
		all, e := a.ConversationMessages(r.Source)
		if e != nil {
			return nil, e
		}
		assigned := ChatTopicAssignments(all)
		var out []ConvMessage
		for _, m := range all {
			if m.Sub == "" && m.TopicEvent == nil && m.ExcerptPID == "" && !m.Deleted && assigned[m.LID] == r.Topic {
				out = append(out, m)
			}
		}
		return out, nil
	}
	groups, rows, e := a.peerThreadGroups(r.Peer)
	if e != nil {
		return nil, e
	}
	var ids []string
	for _, g := range groups {
		if directTopicID(g, rows) == r.Thread {
			if _, e = directGroupDestination(g, rows); e != nil {
				return nil, e
			}
			ids = g
			break
		}
	}
	if len(ids) == 0 {
		return nil, ErrNoMessage
	}
	conversation, e := a.DisplayConversation(ids[0], 0, 0)
	if e != nil {
		return nil, e
	}
	if conversation.Peer != r.Peer {
		return nil, ErrNoMessage
	}
	var out []ConvMessage
	for _, m := range conversation.Messages {
		if m.Deleted || m.attentionSub != "" {
			continue
		}
		original, e := a.deviceHistoryOriginal(a.store.db, m.ID)
		if e != nil {
			return nil, e
		}
		out = append(out, ConvMessage{ID: m.ID, LID: m.ID, Dir: m.Dir, From: original.item.From, Kind: original.item.Kind, Body: m.Body, ReplyTo: original.item.ReplyTo, Sent: original.item.TS, Key: original.item.FromKey, AgentID: original.item.AgentID, Origin: original.item.Origin, Attachments: m.Attachments, AtMS: original.item.At, Controls: m.Controls, Via: m.SyncedFrom})
	}
	return out, nil
}
func (a *Agent) transferGuard(q dbq, r TopicTransferRequest, m ConvMessage) (string, error) {
	var erased int
	if e := q.QueryRow(`SELECT count(*) FROM conv_erased WHERE conv=? AND key=? AND lid=?`, r.Source, m.Key, m.LID).Scan(&erased); e != nil {
		return "", e
	}
	if erased != 0 {
		return "", ErrTopicTransferChanged
	}
	if r.Source != "" {
		return a.topicOrganizationStamp(q, TopicOrganizationRequest{Conv: r.Source, IDs: []string{m.ID}})
	}
	original, e := a.deviceHistoryOriginal(q, m.ID)
	if e != nil {
		return "", e
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(original.item)
	_ = enc.Encode(original.to)
	_ = enc.Encode(original.toKey)
	rows, e := q.Query(`SELECT id,sub,body FROM inbox WHERE conv IS NULL AND ref_id=? AND ref_fp=? AND sub IN ('revision','retraction') UNION ALL SELECT id,sub,body FROM outbox WHERE conv IS NULL AND ref_id=? AND ref_fp=? AND sub IN ('revision','retraction') ORDER BY id`, m.ID, m.Key, m.ID, m.Key)
	if e != nil {
		return "", e
	}
	for rows.Next() {
		var v [3]string
		if e = rows.Scan(&v[0], &v[1], &v[2]); e != nil {
			break
		}
		_ = enc.Encode(v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return "", e
	}
	owner, e := a.ownTopicWriter(q)
	if e != nil {
		return "", e
	}
	var placement string
	e = q.QueryRow(`SELECT json_array(peer,topic,revision,writer,synthetic,host,host_key,agent_id) FROM direct_topic_placements WHERE owner=? AND id=? AND author=?`, owner, m.ID, m.Key).Scan(&placement)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}
	_ = enc.Encode(placement)
	return hex.EncodeToString(h.Sum(nil)), nil
}
func transferItem(m ConvMessage) HistoryContributionItem {
	author := m.Key
	if author == "" {
		author = m.Claimed
	}
	body := m.Controls.Shown(m.Body)
	label := m.From
	if m.AgentID != "" || envelope.AgentOrigin(m.Origin) {
		id := m.AgentID
		if id == "" {
			id = strings.TrimPrefix(m.Origin, envelope.OriginAgentPrefix)
		}
		if len(id) > 12 {
			id = id[:12]
		}
		label = fmt.Sprintf("Claimed agent %s, %s; host: %s", id, m.Kind, m.From)
	}
	return HistoryContributionItem{ID: m.ID, LID: m.LID, Author: author, Hash: transferContentHash(body, m.ReplyTo, m.Attachments), From: m.From, Label: label, SentAt: time.Unix(m.Sent, 0).UTC().Format(time.RFC3339), Body: body, ReplyTo: m.ReplyTo, Files: []HistoryContributionFile{}}
}
func transferContentHash(body, reply string, files []FileInfo) string {
	var refs []any
	for _, f := range files {
		refs = append(refs, []any{SafeName(f.Name), f.Size, f.SHA256})
	}
	return transferHash([]any{body, reply, refs})
}
func transferQuoteText(items []HistoryContributionItem, r TopicTransferReview, importer string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Shared by %s at %s · quoted topic snapshot from another chat.\n\nOriginal speakers and times below are attributed by the person sharing this snapshot. Source tasks and permissions stay in the source chat. Mention links in quoted text are inert. Later posts and running work remain there.\n", importer, r.CreatedAt)
	for _, item := range items {
		body := item.Body
		for _, kind := range []string{"agent", "person", "guest", "collective"} {
			body = strings.ReplaceAll(body, "](agentnet:"+kind+"/", "](agentnet&#58;"+kind+"/")
		}
		fmt.Fprintf(&b, "\n%s — %s\n\n%s\n", item.Label, item.SentAt, contributionQuote(body))
		source := ""
		if r.Source != "" {
			source = "?conv=" + r.Source
		}
		fmt.Fprintf(&b, "\n[Open source message](agentnet:message/%s%s)\n", item.ID, source)
		if item.ReplyTo != "" {
			b.WriteString("\nOriginal reply relationship retained in the source chat.\n")
		}
		for _, f := range item.Files {
			fmt.Fprintf(&b, "\nAttached source file: %s (%d bytes).\n", SafeName(f.Name), f.Size)
		}
	}
	return b.String()
}

// Hydrate one exact original, not the source timeline again. The committed
// guard also covers topic organization, original controls and erasure.
func (a *Agent) transferMessage(r TopicTransferRequest, ref transferRef) (ConvMessage, error) {
	var m ConvMessage
	var item HistoryItem
	if r.Source != "" {
		rows, e := a.historySourceRows(a.store.db, "conv=? AND id=?", "dir,id", 2, r.Source, ref.ID)
		if e != nil {
			return m, e
		}
		if len(rows) != 1 {
			return m, ErrTopicTransferChanged
		}
		item, e = a.historySourceItem(a.store.db, rows[0])
		if e != nil {
			return m, e
		}
		m.Dir = rows[0].dir
	} else {
		original, e := a.deviceHistoryOriginal(a.store.db, ref.ID)
		if e != nil {
			return m, e
		}
		var peer, dir string
		if e = a.store.db.QueryRow(`SELECT peer,direction FROM device_thread_links WHERE id=? AND storage=?`, ref.ID, original.storage).Scan(&peer, &dir); e != nil || peer != r.Peer {
			return m, ErrTopicTransferChanged
		}
		item = original.item
		m.Dir = dir
		if original.replica {
			m.Via = "linked history"
		}
	}
	m.ID, m.LID, m.Key, m.From, m.Sent, m.Body, m.ReplyTo = item.ID, item.LID, item.FromKey, item.From, item.TS, item.Body, item.ReplyTo
	m.AtMS, m.At, m.Kind = item.At, item.At/1000, item.Kind
	m.AgentID, m.Origin = item.AgentID, item.Origin
	if r.Source == "" {
		m.LID = m.ID
	}
	if m.ID != ref.ID || m.LID != ref.LID || m.Key != ref.Author {
		return m, ErrTopicTransferChanged
	}
	var e error
	if m.Dir == "out" && m.Via == "" {
		m.Attachments, e = a.store.sentAttachments(m.ID)
	} else {
		m.Attachments, e = a.store.attachments(m.ID)
	}
	if e != nil {
		return m, e
	}
	rows, e := a.store.db.Query(`SELECT id,sub,sender,coalesce(verified_by,claimed_fp,''),ref_id,ref_fp,body,coalesce(received_ms,received_at*1000),coalesce(pid,''),coalesce(agent_id,''),coalesce(origin,''),coalesce(human,'') FROM inbox WHERE coalesce(conv,'')=? AND ref_id=? AND ref_fp=? AND sub IN ('revision','retraction') UNION ALL SELECT id,sub,?,?,ref_id,ref_fp,body,coalesce(created_ms,created_at*1000),coalesce(pid,''),coalesce(agent_id,''),coalesce(origin,''),coalesce(human,'') FROM outbox WHERE coalesce(conv,'')=? AND ref_id=? AND ref_fp=? AND sub IN ('revision','retraction')`, r.Source, m.LID, m.Key, a.Address, a.Self().Fingerprint(), r.Source, m.LID, m.Key)
	if e != nil {
		return m, e
	}
	controls, e := scanControls(rows)
	if e != nil {
		return m, e
	}
	var auth authority
	if r.Source == "" {
		auth = legacyAuthority(r.Peer, a.Address, a.Self().Fingerprint())
	} else {
		members, x := controlMembers(a.store.db, r.Source)
		if x != nil {
			return m, x
		}
		me, _, x := a.store.selfPerson(a.Address)
		if x != nil {
			return m, x
		}
		auth, _ = a.convAuthority(r.Source, members, me)
	}
	m.Controls = resolveControls(controls, auth)[ControlRef{ID: m.LID, Fingerprint: m.Key}]
	if m.Deleted {
		return m, ErrTopicTransferChanged
	}
	return m, nil
}
func (a *Agent) transferPage(h transferHeader, ref transferRef, after int) (TopicTransferPage, ConvMessage, error) {
	m, e := a.transferMessage(h.Review.TopicTransferRequest, ref)
	if e != nil {
		return TopicTransferPage{}, m, e
	}
	item := transferItem(m)
	if item.Author != ref.Author || item.Hash != ref.Hash || ref.End > len(item.Body) || ref.Start > ref.End {
		return TopicTransferPage{}, m, ErrTopicTransferChanged
	}
	guard, e := a.transferGuard(a.store.db, h.Review.TopicTransferRequest, m)
	if e != nil || guard != ref.Guard {
		return TopicTransferPage{}, m, ErrTopicTransferChanged
	}
	item.Body = item.Body[ref.Start:ref.End]
	for _, index := range ref.Files {
		if index < 0 || index >= len(m.Attachments) {
			return TopicTransferPage{}, m, ErrTopicTransferChanged
		}
		f := m.Attachments[index]
		available := a.keptHere(f.SHA256)
		if m.Dir != "out" || m.Via != "" {
			_, x := os.Stat(a.downloadPath(f.BlobID))
			available = x == nil && !strings.HasPrefix(f.BlobID, historyBlob)
		}
		item.Files = append(item.Files, HistoryContributionFile{Index: index, Name: SafeName(f.Name), Size: f.Size, SHA256: f.SHA256, Selected: true, Available: available})
	}
	p := TopicTransferPage{ID: h.Review.ID, Token: h.Review.Token, After: after, Next: after + 1, Done: after+1 >= h.Review.TotalBatches, Items: []HistoryContributionItem{item}}
	p.Body = transferQuoteText(p.Items, h.Review, a.Address)
	if b, e := json.Marshal(p); e != nil || len(b) > 48<<10 {
		return p, m, errors.New("this snapshot preview exceeds the bounded page limit")
	}
	return p, m, nil
}
func (a *Agent) PreviewTopicTransfer(ctx context.Context, r TopicTransferRequest) (TopicTransferReview, error) {
	r.Title = strings.Join(strings.Fields(r.Title), " ")
	if !validTransferScope(r) || !protocol.ValidHash(r.Destination) || r.Source == r.Destination || r.DestinationTopic != "" && !protocol.ValidID(r.DestinationTopic) || utf8.RuneCountInString(r.Title) > TopicTitleMax || r.NewTopic && r.Title == "" {
		return TopicTransferReview{}, errors.New("choose one whole source topic and a different existing chat or group")
	}
	if r.NewTopic && r.DestinationTopic == "" {
		r.DestinationTopic = protocol.NewID()
	}
	var stamp string
	var audience []HistoryContributionAudience
	var pending bool
	var e error
	for {
		stamp, audience, pending, e = a.transferAuthority(a.store.db, r)
		var missing *historyRecipientMissing
		if !errors.As(e, &missing) {
			break
		}
		key, x := a.sendKeyExpected(ctx, missing.Address, missing.Fingerprint)
		if x != nil {
			return TopicTransferReview{}, x
		}
		if key.Fingerprint() != missing.Fingerprint {
			return TopicTransferReview{}, ErrTopicTransferChanged
		}
	}
	if e != nil {
		return TopicTransferReview{}, e
	}
	if e = a.transferDestinationActive(r, false); e != nil {
		return TopicTransferReview{}, e
	}
	msgs, e := a.transferMessages(r)
	if e != nil {
		return TopicTransferReview{}, e
	}
	if len(msgs) == 0 {
		return TopicTransferReview{}, ErrNoMessage
	}
	// Presentation may choose another physical fanout copy than the history
	// source's earliest native copy. Freeze the existing canonical original,
	// keeping its independently bound logical ID and author.
	if r.Source != "" {
		for i := range msgs {
			rows, x := a.historySourceRows(a.store.db, "conv=? AND lid=?", "dir,id", 2, r.Source, msgs[i].LID)
			if x != nil {
				return TopicTransferReview{}, x
			}
			if len(rows) != 1 || rows[0].key != transferItem(msgs[i]).Author {
				return TopicTransferReview{}, ErrTopicTransferChanged
			}
			canonical, x := a.transferMessage(r, transferRef{ID: rows[0].in.ID, LID: msgs[i].LID, Author: rows[0].key})
			if x != nil {
				return TopicTransferReview{}, x
			}
			if transferItem(canonical).Hash != transferItem(msgs[i]).Hash {
				return TopicTransferReview{}, ErrTopicTransferChanged
			}
			msgs[i] = canonical
		}
	}
	msgs, e = contributionOrder(msgs)
	if e != nil {
		return TopicTransferReview{}, e
	}
	h := transferHeader{Review: TopicTransferReview{TopicTransferRequest: r, ID: protocol.NewID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), State: "review", Audience: audience, AudiencePending: pending}, Authority: stamp}
	h.Owner, e = a.ownTopicWriter(a.store.db)
	if e != nil || h.Owner == "" {
		return TopicTransferReview{}, errors.New("only a current own human device may transfer this topic")
	}
	h.Review.Cutoff = time.Now().UnixMilli()
	var refs []transferRef
	for _, m := range msgs {
		item := transferItem(m)
		m.Key = item.Author
		guard, e := a.transferGuard(a.store.db, r, m)
		if e != nil {
			return TopicTransferReview{}, e
		}
		h.Review.TotalItems++
		h.Review.TotalBytes += int64(len(item.Body))
		h.Review.TotalFiles += len(m.Attachments)
		for _, f := range m.Attachments {
			h.Review.TotalBytes += f.Size
		}
		start, fi := 0, 0
		for start < len(item.Body) || fi < len(m.Attachments) || start == 0 && len(item.Body) == 0 && fi == 0 {
			end := min(start+8192, len(item.Body))
			for end < len(item.Body) && !utf8.RuneStart(item.Body[end]) {
				end--
			}
			files := []int{}
			for fi < len(m.Attachments) && len(files) < envelope.MaxAttachments {
				files = append(files, fi)
				fi++
			}
			ref := transferRef{ID: m.ID, LID: m.LID, Author: item.Author, Hash: item.Hash, Guard: guard, ReplyTo: m.ReplyTo, Start: start, End: end, Files: files, LIDOut: transferHash([]any{h.Review.ID, len(refs)})[:32]}
			// JSON controls can expand sixfold; bind boundaries to the encoded
			// preview, not an assumed character count. No preview is retained.
			for {
				fragment := item
				fragment.Body = item.Body[start:end]
				fragment.Files = nil
				for _, i := range files {
					f := m.Attachments[i]
					fragment.Files = append(fragment.Files, HistoryContributionFile{Index: i, Name: SafeName(f.Name), Size: f.Size, SHA256: f.SHA256, Selected: true, Available: true})
				}
				p := TopicTransferPage{ID: h.Review.ID, Token: strings.Repeat("a", 64), Items: []HistoryContributionItem{fragment}, Body: transferQuoteText([]HistoryContributionItem{fragment}, h.Review, a.Address)}
				b, _ := json.Marshal(p)
				if len(b) < 47<<10 {
					break
				}
				if end-start < 2 {
					return TopicTransferReview{}, errors.New("source file metadata exceeds the preview limit")
				}
				end = start + (end-start)/2
				for end > start && !utf8.RuneStart(item.Body[end]) {
					end--
				}
			}
			ref.End = end
			refs = append(refs, ref)
			start = end
			if start == len(item.Body) && fi == len(m.Attachments) {
				break
			}
		}
	}
	h.Review.TotalBatches = len(refs)
	h.Review.Token = transferHash([]any{h.Review, h.Authority, refs})
	tx, e := a.store.db.Begin()
	if e != nil {
		return TopicTransferReview{}, e
	}
	defer tx.Rollback()
	if current, _, _, x := a.transferAuthority(tx, r); x != nil || current != stamp {
		return TopicTransferReview{}, ErrTopicTransferChanged
	}
	for i, ref := range refs {
		var m ConvMessage
		for _, v := range msgs {
			if v.ID == ref.ID {
				m = v
				break
			}
		}
		if guard, x := a.transferGuard(tx, r, m); x != nil || guard != ref.Guard {
			return TopicTransferReview{}, ErrTopicTransferChanged
		}
		raw, _ := json.Marshal(ref)
		if _, e = tx.Exec(`INSERT INTO topic_transfer_manifest(plan,seq,reference) VALUES(?,?,?)`, h.Review.ID, i, string(raw)); e != nil {
			return TopicTransferReview{}, e
		}
	}
	raw, _ := json.Marshal(h)
	_, e = tx.Exec(`INSERT INTO topic_transfer_plans(id,header,cursor,state) VALUES(?,?,0,'review')`, h.Review.ID, string(raw))
	if e != nil {
		return TopicTransferReview{}, e
	}
	if e = a.store.done(tx.Commit()); e != nil {
		return TopicTransferReview{}, e
	}
	return publicTransferReview(h.Review), nil
}
func validTransferPeer(p string) bool { _, _, e := protocol.SplitAddress(p); return e == nil }
func validTransferScope(r TopicTransferRequest) bool {
	if r.Source != "" {
		return protocol.ValidHash(r.Source) && r.Peer == "" && r.Thread == "" && (r.Topic == "" || protocol.ValidID(r.Topic))
	}
	return validTransferPeer(r.Peer) && protocol.ValidID(r.Thread) && r.Topic == ""
}
func (a *Agent) transferDestinationActive(r TopicTransferRequest, started bool) error {
	msgs, e := a.ConversationMessages(r.Destination)
	if e != nil {
		return e
	}
	if r.DestinationTopic == "" {
		main, e := a.ChatMainTopicForMessages(r.Destination, msgs)
		if e != nil {
			return e
		}
		if main != nil && main.State != TopicActive {
			return errors.New("reopen the destination topic first")
		}
		return nil
	}
	topics, e := a.ChatTopicsForMessages(r.Destination, msgs)
	if e != nil {
		return e
	}
	for _, t := range topics {
		if t.ID == r.DestinationTopic {
			if r.NewTopic && !started || t.State != TopicActive {
				return errors.New("choose an active destination topic")
			}
			return nil
		}
	}
	if r.NewTopic {
		return nil
	}
	return ErrNoMessage
}
func transferHeaderIn(q dbq, id string) (transferHeader, error) {
	var h transferHeader
	var raw string
	if !protocol.ValidID(id) {
		return h, ErrNoMessage
	}
	e := q.QueryRow(`SELECT header,cursor,state FROM topic_transfer_plans WHERE id=?`, id).Scan(&raw, &h.Review.Cursor, &h.Review.State)
	if e != nil {
		return h, e
	}
	cursor, state := h.Review.Cursor, h.Review.State
	e = json.Unmarshal([]byte(raw), &h)
	h.Review.Cursor, h.Review.State = cursor, state
	return h, e
}
func transferRefIn(q dbq, id string, n int) (transferRef, error) {
	var r transferRef
	var raw string
	e := q.QueryRow(`SELECT reference FROM topic_transfer_manifest WHERE plan=? AND seq=?`, id, n).Scan(&raw)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &r)
	}
	return r, e
}
func (a *Agent) loadTopicTransfer(q dbq, id string) (transferHeader, error) {
	h, e := transferHeaderIn(q, id)
	if e != nil {
		return h, e
	}
	owner, e := a.ownTopicWriter(q)
	if e != nil {
		return h, e
	}
	if owner == "" || owner != h.Owner {
		return h, errors.New("topic snapshot belongs to another person or inactive device")
	}
	return h, nil
}
func (a *Agent) TopicTransferStatus(d TopicTransferAction) (*TopicTransferReview, error) {
	if d.ID == "" {
		if !validTransferScope(TopicTransferRequest{Source: d.Source, Topic: d.Topic, Peer: d.Peer, Thread: d.Thread}) {
			return nil, ErrTopicQuery
		}
		owner, e := a.ownTopicWriter(a.store.db)
		if e != nil {
			return nil, e
		}
		if owner == "" {
			return nil, errors.New("only a current own human device may read topic snapshots")
		}
		var id string
		e = a.store.db.QueryRow(`SELECT id FROM topic_transfer_plans WHERE json_extract(header,'$.owner')=? AND coalesce(json_extract(header,'$.review.source'),'')=? AND coalesce(json_extract(header,'$.review.topic'),'')=? AND coalesce(json_extract(header,'$.review.peer'),'')=? AND coalesce(json_extract(header,'$.review.thread'),'')=? ORDER BY json_extract(header,'$.review.created_at') DESC LIMIT 1`, owner, d.Source, d.Topic, d.Peer, d.Thread).Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, nil
		}
		if e != nil {
			return nil, e
		}
		d.ID = id
	}
	h, e := a.loadTopicTransfer(a.store.db, d.ID)
	r := publicTransferReview(h.Review)
	return &r, e
}
func (a *Agent) TopicTransferPage(d TopicTransferAction) (TopicTransferPage, error) {
	h, e := a.loadTopicTransfer(a.store.db, d.ID)
	if e != nil {
		return TopicTransferPage{}, e
	}
	if h.Review.Token != d.Token || d.After < 0 || d.After >= h.Review.TotalBatches {
		return TopicTransferPage{}, ErrTopicTransferChanged
	}
	if stamp, _, _, x := a.transferAuthority(a.store.db, h.Review.TopicTransferRequest); x != nil || stamp != h.Authority {
		return TopicTransferPage{}, ErrTopicTransferChanged
	}
	if d.AudienceAfter != nil {
		after := *d.AudienceAfter
		if after < 0 || after >= len(h.Review.Audience) {
			return TopicTransferPage{}, ErrTopicTransferChanged
		}
		end := min(after+16, len(h.Review.Audience))
		next := 0
		if end < len(h.Review.Audience) {
			next = end
		}
		return TopicTransferPage{ID: d.ID, Token: d.Token, Items: []HistoryContributionItem{}, Audience: h.Review.Audience[after:end], AudienceTotal: len(h.Review.Audience), AudienceNext: next, Done: next == 0}, nil
	}
	ref, e := transferRefIn(a.store.db, d.ID, d.After)
	if e != nil {
		return TopicTransferPage{}, e
	}
	p, _, e := a.transferPage(h, ref, d.After)
	return p, e
}
func (a *Agent) CancelTopicTransfer(d TopicTransferAction) (TopicTransferReview, error) {
	tx, e := a.store.db.Begin()
	if e != nil {
		return TopicTransferReview{}, e
	}
	defer tx.Rollback()
	h, e := a.loadTopicTransfer(tx, d.ID)
	if e != nil {
		return h.Review, e
	}
	if h.Review.Token != d.Token {
		return h.Review, ErrTopicTransferChanged
	}
	if h.Review.State != "queued" {
		_, e = tx.Exec(`UPDATE topic_transfer_plans SET state='canceled' WHERE id=?`, d.ID)
		h.Review.State = "canceled"
	}
	if e == nil {
		e = a.store.done(tx.Commit())
	}
	return publicTransferReview(h.Review), e
}
func (a *Agent) ApplyTopicTransfer(ctx context.Context, d TopicTransferAction) (TopicTransferReview, error) {
	h, e := a.loadTopicTransfer(a.store.db, d.ID)
	if e != nil {
		return h.Review, e
	}
	if d.Token != h.Review.Token || d.Cursor < 0 || d.Cursor > h.Review.Cursor {
		return h.Review, ErrTopicTransferChanged
	}
	if d.Cursor < h.Review.Cursor || h.Review.State == "queued" {
		return publicTransferReview(h.Review), nil
	}
	if h.Review.State == "canceled" {
		return h.Review, errors.New("topic transfer canceled; already queued quotations keep their delivery status")
	}
	stamp, _, _, e := a.transferAuthority(a.store.db, h.Review.TopicTransferRequest)
	if e != nil || stamp != h.Authority {
		return h.Review, ErrTopicTransferChanged
	}
	ref, e := transferRefIn(a.store.db, d.ID, d.Cursor)
	if e != nil {
		return h.Review, e
	}
	page, m, e := a.transferPage(h, ref, d.Cursor)
	if e != nil {
		return h.Review, e
	}
	var files []OutgoingFile
	var clean []func()
	defer func() {
		for _, f := range clean {
			f()
		}
	}()
	for _, f := range page.Items[0].Files {
		reader, info, x := a.openHistoryContributionFile(m, f.Index)
		if x != nil {
			return h.Review, fmt.Errorf("source file %s is unavailable; acquire that exact source file before continuing: %w", f.Name, x)
		}
		if info.Size != f.Size || info.SHA256 != f.SHA256 {
			reader.Close()
			return h.Review, ErrTopicTransferChanged
		}
		path, remove, x := a.StageUpload(f.Name, io.LimitReader(reader, f.Size+1))
		reader.Close()
		if x != nil {
			return h.Review, x
		}
		clean = append(clean, remove)
		size, hash, x := fileDigest(path)
		if x != nil || size != f.Size || hash != f.SHA256 {
			return h.Review, ErrTopicTransferChanged
		}
		files = append(files, OutgoingFile{Name: f.Name, Path: path})
	}
	claim := func(tx *sql.Tx, _ string) error {
		current, x := a.loadTopicTransfer(tx, d.ID)
		if x != nil {
			return x
		}
		if current.Review.Cursor != d.Cursor || current.Review.State == "canceled" {
			return ErrTopicTransferChanged
		}
		stamp, _, _, x := a.transferAuthority(tx, h.Review.TopicTransferRequest)
		if x != nil || stamp != h.Authority {
			return ErrTopicTransferChanged
		}
		guard, x := a.transferGuard(tx, h.Review.TopicTransferRequest, m)
		if x != nil || guard != ref.Guard {
			return ErrTopicTransferChanged
		}
		if h.Review.NewTopic && d.Cursor == 0 {
			var exists bool
			if x = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM inbox WHERE conv=? AND topic=? UNION ALL SELECT 1 FROM outbox WHERE conv=? AND topic=?)`, h.Review.Destination, h.Review.DestinationTopic, h.Review.Destination, h.Review.DestinationTopic).Scan(&exists); x != nil {
				return x
			}
			if exists {
				return ErrTopicTransferChanged
			}
			if x = a.recordTopicTitle(tx, h.Review.Destination, h.Review.DestinationTopic, h.Review.Title); x != nil {
				return x
			}
		}
		state := "partial"
		if d.Cursor+1 == h.Review.TotalBatches {
			state = "queued"
			if x = a.recordTopicLocation(tx, h.Review); x != nil {
				return x
			}
		}
		next, _, _, x := a.transferAuthority(tx, h.Review.TopicTransferRequest)
		if x != nil {
			return x
		}
		h.Authority = next
		raw, _ := json.Marshal(h)
		_, x = tx.Exec(`UPDATE topic_transfer_plans SET cursor=?,state=?,header=? WHERE id=?`, d.Cursor+1, state, string(raw), d.ID)
		return x
	}
	sent, e := a.SendConv(WithQueuedSend(ctx, ref.LIDOut), h.Review.Destination, ConvOutgoing{Kind: envelope.KindMessage, Origin: envelope.OriginUI, Body: page.Body, Topic: h.Review.DestinationTopic, Files: files, claim: claim, contribution: true})
	if e != nil {
		current, x := a.loadTopicTransfer(a.store.db, d.ID)
		if x == nil && current.Review.Cursor > d.Cursor {
			return publicTransferReview(current.Review), nil
		}
		return h.Review, e
	}
	h, e = a.loadTopicTransfer(a.store.db, d.ID)
	h.Review.Sent = &sent
	if e == nil {
		a.topicTitlesChanged()
	}
	return publicTransferReview(h.Review), e
}
