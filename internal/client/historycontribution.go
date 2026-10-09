package client

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
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

const HistoryContributionMaxMessages = 64
const HistoryContributionMaxBody = 128 << 10

type HistoryContributionFileRef struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}
type HistoryContributionRequest struct {
	Source      string                       `json:"source"`
	IDs         []string                     `json:"ids"`
	Destination string                       `json:"destination"`
	Topic       string                       `json:"topic,omitempty"`
	NewTopic    bool                         `json:"new_topic,omitempty"`
	Title       string                       `json:"title,omitempty"`
	Files       []HistoryContributionFileRef `json:"files,omitempty"`
}
type HistoryContributionFile struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Available bool   `json:"available"`
	Selected  bool   `json:"selected"`
}
type HistoryContributionItem struct {
	ID             string                    `json:"id"`
	LID            string                    `json:"lid"`
	Author         string                    `json:"author"`
	Hash           string                    `json:"hash"`
	From           string                    `json:"from"`
	Label          string                    `json:"label,omitempty"`
	SentAt         string                    `json:"sent_at"`
	Body           string                    `json:"body"`
	ReplyTo        string                    `json:"reply_to,omitempty"`
	SelectedParent int                       `json:"selected_parent,omitempty"`
	Files          []HistoryContributionFile `json:"files"`
}
type HistoryContributionAudience struct {
	Person      string  `json:"person"`
	Address     string  `json:"address"`
	Fingerprint string  `json:"fingerprint"`
	Label       string  `json:"label"`
	Role        string  `json:"role"`
	Topic       *string `json:"topic,omitempty"`
	PID         string  `json:"pid,omitempty"`
	AgentID     string  `json:"agent_id,omitempty"`
}
type HistoryContributionReview struct {
	HistoryContributionRequest
	Operation       string                        `json:"operation"`
	Token           string                        `json:"token"`
	Body            string                        `json:"body"`
	Items           []HistoryContributionItem     `json:"items"`
	Audience        []HistoryContributionAudience `json:"audience"`
	ImportedAt      string                        `json:"imported_at"`
	AudiencePending bool                          `json:"audience_pending"`
}

var ErrHistoryContributionChanged = errors.New("the selected history or destination audience changed; review the contribution again")

func contributionOperation(r HistoryContributionReview) string {
	r.Operation = ""
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

func historyContributionEvents(q dbq, conv string) ([]protocol.ParticipationEvent, error) {
	rows, e := q.Query(`SELECT event FROM participation_events WHERE conv=? ORDER BY hash`, conv)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []protocol.ParticipationEvent
	for rows.Next() {
		var raw string
		var event protocol.ParticipationEvent
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &event); e != nil {
			return nil, e
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (a *Agent) historyContributionAudience(q dbq, conv, topic string) ([]HistoryContributionAudience, bool, error) {
	if err := topicOrganizationAuthor(q, conv, a.Address, a.Self().Fingerprint()); err != nil {
		return nil, false, err
	}
	m, err := membersIn(q, conv)
	if err != nil {
		return nil, false, err
	}
	if m.root.Kind != protocol.ConvKindGroup {
		return nil, false, errors.New("choose an existing group in this workspace")
	}
	events, err := historyContributionEvents(q, conv)
	if err != nil {
		return nil, false, err
	}
	if err = m.loadHosts(q, events); err != nil {
		return nil, false, err
	}
	out := []HistoryContributionAudience{}
	for _, p := range m.persons {
		for _, d := range p.roster.Devices {
			out = append(out, HistoryContributionAudience{Person: p.info.Person, Address: d.Address, Fingerprint: d.Fingerprint(), Label: p.info.Label, Role: "member"})
		}
	}
	pids := map[string]bool{}
	pending := false
	for _, e := range events {
		pids[e.PID] = true
	}
	for pid := range pids {
		var selected []protocol.ParticipationEvent
		for _, e := range events {
			if e.PID == pid {
				selected = append(selected, e)
			}
		}
		p := resolve(conv, pid, selected, m)
		if p.State == PartInvited {
			pending = true
		}
		if p.State == PartActive && p.follows() && p.Held != 0 {
			return nil, false, errors.New("destination audience proof is pending; review it after recovery")
		}
		if !p.Following() || p.Topic != nil && *p.Topic != topic {
			continue
		}
		role := "agent"
		if p.Role == protocol.RoleHuman {
			role = "guest"
		}
		label := p.Host.Label
		if role == "agent" {
			id := p.AgentID
			if id == "" {
				id = p.PID
			}
			if len(id) > 12 {
				id = id[:12]
			}
			label = fmt.Sprintf("Agent %s at %s", id, p.Host.Label)
		}
		out = append(out, HistoryContributionAudience{Person: p.Host.Person, Address: p.Host.Address, Fingerprint: p.Host.Fingerprint, Label: label, Role: role, Topic: p.Topic, PID: p.PID, AgentID: p.AgentID})
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		return x.Person+"/"+x.Address+"/"+x.Role+"/"+x.PID < y.Person+"/"+y.Address+"/"+y.Role+"/"+y.PID
	})
	for _, p := range out {
		if p.Address == a.Address {
			continue
		}
		key, ok, e := pinnedKey(q, p.Address)
		if e != nil {
			return nil, false, e
		}
		var pending int
		if e = q.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, p.Address).Scan(&pending); e != nil {
			return nil, false, e
		}
		if !ok || pending != 0 || key.Fingerprint() != p.Fingerprint {
			return nil, false, errors.New("destination recipient key changed")
		}
	}
	var invited int
	if err = q.QueryRow(`SELECT count(*) FROM group_invitations WHERE conv=? AND state IN ('pending','accepted')`, conv).Scan(&invited); err != nil {
		return nil, false, err
	}
	return out, pending || invited > 0, nil
}

func (a *Agent) historyContributionStamp(q dbq, r HistoryContributionRequest) (string, error) {
	if err := topicOrganizationAuthor(q, r.Source, a.Address, a.Self().Fingerprint()); err != nil {
		return "", err
	}
	source, e := a.topicOrganizationStamp(q, TopicOrganizationRequest{Conv: r.Source, IDs: r.IDs})
	if e != nil {
		return "", e
	}
	destination, e := a.topicOrganizationStamp(q, TopicOrganizationRequest{Conv: r.Destination})
	if e != nil {
		return "", e
	}
	audience, pending, e := a.historyContributionAudience(q, r.Destination, r.Topic)
	if e != nil {
		return "", e
	}
	events, e := historyContributionEvents(q, r.Destination)
	if e != nil {
		return "", e
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, v := range []any{r, source, destination, audience, pending, events} {
		if e = enc.Encode(v); e != nil {
			return "", e
		}
	}
	for _, p := range audience {
		if p.Address == a.Address {
			continue
		}
		var public, pending string
		if e = q.QueryRow(`SELECT public,coalesce(pending,'') FROM peers WHERE address=?`, p.Address).Scan(&public, &pending); e != nil {
			return "", e
		}
		if e = enc.Encode([3]string{p.Address, public, pending}); e != nil {
			return "", e
		}
	}
	if r.Topic == "" {
		var title, mark string
		var at, count int64
		e = q.QueryRow(`SELECT coalesce(title,''),coalesce(mark,''),coalesce(mark_at,0),coalesce(mark_count,0) FROM topic_state WHERE peer=? AND topic=?`, mainTopicScope(r.Destination), mainTopicKey).Scan(&title, &mark, &at, &count)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return "", e
		}
		if e = enc.Encode([]any{title, mark, at, count}); e != nil {
			return "", e
		}
	}
	var inCount, outCount int64
	if e = q.QueryRow(`SELECT (SELECT count(*) FROM inbox WHERE conv=?),(SELECT count(*) FROM outbox WHERE conv=?)`, r.Destination, r.Destination).Scan(&inCount, &outCount); e != nil {
		return "", e
	}
	if e = enc.Encode([2]int64{inCount, outCount}); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Bound the selected reply DAG only. Clock skew cannot place a chosen child
// before its chosen parent; omitted parents never enter this traversal.
func contributionOrder(messages []ConvMessage) ([]ConvMessage, error) {
	aliases := map[string]string{}
	for _, m := range messages {
		aliases[m.ID] = m.LID
		aliases[m.LID] = m.LID
		for _, c := range m.Copies {
			aliases[c.ID] = m.LID
		}
	}
	less := func(i, j int) bool {
		x, y := messages[i], messages[j]
		if x.Sent != y.Sent {
			return x.Sent < y.Sent
		}
		if x.Key != y.Key {
			return x.Key < y.Key
		}
		return x.LID < y.LID
	}
	sort.Slice(messages, less)
	out := make([]ConvMessage, 0, len(messages))
	done := map[string]bool{}
	for len(out) < len(messages) {
		progress := false
		for _, m := range messages {
			if done[m.LID] {
				continue
			}
			parent := aliases[m.ReplyTo]
			if parent != "" && !done[parent] {
				continue
			}
			out = append(out, m)
			done[m.LID] = true
			progress = true
			break
		}
		if !progress {
			return nil, errors.New("selected reply relationships form a cycle; choose fewer messages")
		}
	}
	return out, nil
}

func contributionQuote(body string) string {
	return "> " + strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\n> ")
}
func contributionText(items []HistoryContributionItem, importer, at, source string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Shared by %s at %s · quoted context from another chat.\n\nOriginal speakers and times below are attributed by the person sharing this snapshot. Source tasks and permissions stay in the source chat.\n", importer, at)
	for i, item := range items {
		label := item.Label
		if label == "" {
			label = item.From
		}
		fmt.Fprintf(&b, "\n%d. %s — %s\n\n%s\n", i+1, label, item.SentAt, contributionQuote(item.Body))
		fmt.Fprintf(&b, "\n[Open source message](agentnet:message/%s?conv=%s)\n", item.ID, source)
		if item.SelectedParent > 0 {
			fmt.Fprintf(&b, "\nReply to selected item %d.\n", item.SelectedParent)
		} else if item.ReplyTo != "" {
			b.WriteString("\nReply outside the selected history; parent not shared.\n")
		}
		for _, f := range item.Files {
			if f.Selected {
				fmt.Fprintf(&b, "\nAttached selected file: %s (%d bytes).\n", SafeName(f.Name), f.Size)
			}
		}
		omitted := 0
		for _, f := range item.Files {
			if !f.Selected {
				omitted++
			}
		}
		if omitted > 0 {
			fmt.Fprintf(&b, "\n%d source file(s) omitted.\n", omitted)
		}
	}
	return b.String()
}

func (a *Agent) PreviewHistoryContribution(r HistoryContributionRequest) (HistoryContributionReview, error) {
	var out HistoryContributionReview
	if !protocol.ValidHash(r.Source) || !protocol.ValidHash(r.Destination) || r.Source == r.Destination || len(r.IDs) == 0 || len(r.IDs) > HistoryContributionMaxMessages || r.Topic != "" && !protocol.ValidID(r.Topic) || r.NewTopic && r.Topic == "" || len(r.Files) > envelope.MaxAttachments {
		return out, errors.New("choose at most 64 messages, 8 files and a different existing group in this workspace")
	}
	r.Title = strings.Join(strings.Fields(r.Title), " ")
	if utf8.RuneCountInString(r.Title) > TopicTitleMax || r.NewTopic && r.Title == "" {
		return out, ErrTopicTitle
	}
	seen := map[string]bool{}
	for _, id := range r.IDs {
		if !protocol.ValidID(id) || seen[id] {
			return out, ErrNoMessage
		}
		seen[id] = true
	}
	before, e := a.historyContributionStamp(a.store.db, r)
	if e != nil {
		return out, e
	}
	audience, pending, e := a.historyContributionAudience(a.store.db, r.Destination, r.Topic)
	if e != nil {
		return out, e
	}
	destinationMessages, e := a.ConversationMessages(r.Destination)
	if e != nil {
		return out, e
	}
	if r.Topic == "" {
		main, e := a.ChatMainTopicForMessages(r.Destination, destinationMessages)
		if e != nil {
			return out, e
		}
		if main != nil && main.State != TopicActive {
			return out, errors.New("choose an active destination topic, or reopen it first")
		}
	}
	topics, e := a.ChatTopicsForMessages(r.Destination, destinationMessages)
	if e != nil {
		return out, e
	}
	found := r.Topic == ""
	for _, t := range topics {
		if t.ID == r.Topic {
			found = true
			if r.NewTopic || t.State != TopicActive {
				return out, errors.New("choose an active destination topic, or reopen it first")
			}
		}
	}
	if !r.NewTopic && !found {
		return out, ErrNoMessage
	}
	msgs, e := a.ConversationMessages(r.Source)
	if e != nil {
		return out, e
	}
	selected := []ConvMessage{}
	for _, id := range r.IDs {
		var candidates []ConvMessage
		for _, m := range msgs {
			if m.ID == id || m.LID == id {
				candidates = append(candidates, m)
			}
		}
		if len(candidates) != 1 {
			return out, ErrNoMessage
		}
		m := candidates[0]
		if m.Sub != "" || m.TopicEvent != nil || m.ExcerptPID != "" || m.Deleted {
			return out, ErrNoMessage
		}
		selected = append(selected, m)
	}
	selected, e = contributionOrder(selected)
	if e != nil {
		return out, e
	}
	used := map[string]bool{}
	selectedFiles := 0
	for _, m := range selected {
		author := m.Key
		if author == "" {
			author = m.Claimed
		}
		if !protocol.ValidFingerprint(author) {
			return out, ErrNoMessage
		}
		rows, e := a.historySourceRows(a.store.db, "conv=? AND (id=? OR lid=?)", "dir,id", 1, r.Source, m.ID, m.LID)
		if e != nil {
			return out, e
		}
		if len(rows) != 1 {
			return out, ErrNoMessage
		}
		original, e := a.historySourceItem(a.store.db, rows[0])
		if e != nil {
			return out, e
		}
		inner := original.inner(r.Source)
		inner.Body = m.Controls.Shown(m.Body)
		item := HistoryContributionItem{ID: m.ID, LID: m.LID, Author: author, Hash: contentHash(inner), From: m.From, SentAt: time.Unix(m.Sent, 0).UTC().Format(time.RFC3339), Body: inner.Body, ReplyTo: m.ReplyTo, Files: []HistoryContributionFile{}}
		var label string
		_ = a.store.db.QueryRow(`SELECT p.label FROM persons p JOIN person_devices d ON d.person=p.person WHERE d.address=? AND d.fingerprint=? AND p.state IN ('self','pinned')`, m.From, author).Scan(&label)
		if label == "" {
			label = m.From
		}
		speaker := m
		if len(speaker.AgentID) > 12 {
			speaker.AgentID = speaker.AgentID[:12]
		}
		item.Label = contextSpeaker(speaker, map[string]string{m.From + "|" + m.Key: label}, map[string]string{m.From + "|" + m.Claimed: label})
		if item.From == "" {
			item.From = a.Address
		}
		for index, f := range m.Attachments {
			chosen := false
			for j, ref := range r.Files {
				if (ref.ID == m.ID || ref.ID == m.LID) && ref.Index == index {
					if chosen {
						return out, errors.New("duplicate selected file")
					}
					chosen = true
					used[fmt.Sprint(j)] = true
					selectedFiles++
				}
			}
			available := false
			if m.Dir == "out" && m.Via == "" {
				available = a.keptHere(f.SHA256)
			} else if !strings.HasPrefix(f.BlobID, historyBlob) {
				_, e := os.Stat(a.downloadPath(f.BlobID))
				available = e == nil
			}
			if chosen && (!available || f.Size > MaxFileSize) {
				return out, errors.New("selected file bytes are unavailable or exceed the file limit; open or request that exact file first")
			}
			item.Files = append(item.Files, HistoryContributionFile{Index: index, Name: SafeName(f.Name), Size: f.Size, SHA256: f.SHA256, Available: available, Selected: chosen})
		}
		out.Items = append(out.Items, item)
	}
	if selectedFiles != len(r.Files) || len(used) != len(r.Files) {
		return out, errors.New("selected file is outside the reviewed messages")
	}
	for i := range out.Items {
		for j, p := range out.Items {
			if p.LID == out.Items[i].ReplyTo || p.ID == out.Items[i].ReplyTo {
				out.Items[i].SelectedParent = j + 1
				break
			}
		}
	}
	out.HistoryContributionRequest = r
	out.Audience = audience
	out.AudiencePending = pending
	out.ImportedAt = time.Now().UTC().Format(time.RFC3339Nano)
	out.Body = contributionText(out.Items, a.Address, out.ImportedAt, r.Source)
	if len(out.Body) > HistoryContributionMaxBody {
		return out, errors.New("selected quoted history is larger than 128 KiB; select fewer messages")
	}
	after, e := a.historyContributionStamp(a.store.db, r)
	if e != nil {
		return out, e
	}
	if before != after {
		return out, ErrHistoryContributionChanged
	}
	out.Token = after
	out.Operation = contributionOperation(out)
	if encoded, e := json.Marshal(out); e != nil || len(encoded) > 48<<10 {
		return HistoryContributionReview{}, errors.New("selected history review is larger than 48 KiB; select fewer messages or files")
	}
	return out, nil
}

func contributionStoredFilesMatch(r HistoryContributionReview, files []FileInfo) bool {
	var chosen []HistoryContributionFile
	for _, item := range r.Items {
		for _, f := range item.Files {
			if f.Selected {
				chosen = append(chosen, f)
			}
		}
	}
	if len(chosen) != len(files) {
		return false
	}
	for i, f := range chosen {
		actual := files[i]
		if actual.SHA256 != f.SHA256 || actual.Size != f.Size || SafeName(actual.Name) != f.Name {
			return false
		}
	}
	return true
}

func (a *Agent) keptHistoryContribution(r HistoryContributionReview) (ConvSent, bool, error) {
	var id, body, topic, kind, pid, target, sub, reply, agent string
	cols := `id,body,coalesce(topic,''),kind,coalesce(pid,''),coalesce(target,''),coalesce(sub,''),coalesce(reply_to,''),coalesce(agent_id,'')`
	e := a.store.db.QueryRow(`SELECT `+cols+` FROM outbox WHERE conv=? AND lid=? LIMIT 1`, r.Destination, r.Operation).Scan(&id, &body, &topic, &kind, &pid, &target, &sub, &reply, &agent)
	received := false
	if errors.Is(e, sql.ErrNoRows) {
		received = true
		e = a.store.db.QueryRow(`SELECT `+cols+` FROM inbox WHERE conv=? AND lid=? AND coalesce(verified_by,claimed_fp) IN (SELECT fingerprint FROM person_devices WHERE person=(SELECT person FROM persons WHERE state='self')) LIMIT 1`, r.Destination, r.Operation).Scan(&id, &body, &topic, &kind, &pid, &target, &sub, &reply, &agent)
		if errors.Is(e, sql.ErrNoRows) {
			return ConvSent{}, false, nil
		}
	}
	if e != nil {
		return ConvSent{}, false, e
	}
	if body != r.Body || topic != r.Topic || kind != envelope.KindMessage || pid != "" || target != "" || sub != "" || reply != "" || agent != "" {
		return ConvSent{}, true, ErrHistoryContributionChanged
	}
	var files []FileInfo
	if received {
		files, e = a.store.attachments(id)
	} else {
		files, e = a.store.sentAttachments(id)
	}
	if e != nil {
		return ConvSent{}, true, e
	}
	if !contributionStoredFilesMatch(r, files) {
		return ConvSent{}, true, ErrHistoryContributionChanged
	}
	if received {
		return ConvSent{ID: id, LID: r.Operation, State: "stored"}, true, nil
	}
	copies, e := a.SentCopies(r.Operation)
	if e != nil {
		return ConvSent{}, true, e
	}
	if len(copies) == 0 {
		return ConvSent{}, true, ErrNoMessage
	}
	return ConvSent{ID: copies[0].ID, LID: r.Operation, State: copies[0].State, Copies: copies}, true, nil
}

// A review never authorizes fetching an unkept attachment implicitly. Reuse
// the existing checked decryptors, reading only the cached exact selected file.
func (a *Agent) openHistoryContributionFile(m ConvMessage, index int) (io.ReadCloser, FileInfo, error) {
	if index < 0 || index >= len(m.Attachments) {
		return nil, FileInfo{}, ErrNoMessage
	}
	if m.Dir == "out" && m.Via == "" {
		return a.OpenSentAttachment(context.Background(), m.ID, index)
	}
	f := m.Attachments[index]
	if strings.HasPrefix(f.BlobID, historyBlob) {
		return nil, f, errNotKept
	}
	dir := filepath.Join(a.home, "opened")
	if e := secfile.EnsureDir(dir); e != nil {
		return nil, f, e
	}
	tmp, e := a.decryptTo(dir, f)
	if e != nil {
		return nil, f, e
	}
	reader, e := os.Open(tmp)
	if e != nil {
		os.Remove(tmp)
		return nil, f, e
	}
	return &removeOnClose{File: reader}, f, nil
}

func (a *Agent) ApplyHistoryContribution(ctx context.Context, r HistoryContributionReview) (ConvSent, error) {
	if !protocol.ValidHash(r.Token) || !protocol.ValidID(r.Operation) || r.Operation != contributionOperation(r) {
		return ConvSent{}, ErrHistoryContributionChanged
	}
	if sent, found, e := a.keptHistoryContribution(r); found || e != nil {
		return sent, e
	}
	current, e := a.PreviewHistoryContribution(r.HistoryContributionRequest)
	if e != nil {
		return ConvSent{}, ErrHistoryContributionChanged
	}
	current.ImportedAt = r.ImportedAt
	current.Body = contributionText(current.Items, a.Address, r.ImportedAt, r.Source)
	current.Operation = contributionOperation(current)
	if current.Operation != r.Operation {
		return ConvSent{}, ErrHistoryContributionChanged
	}
	sourceMessages, e := a.ConversationMessages(r.Source)
	if e != nil {
		return ConvSent{}, ErrHistoryContributionChanged
	}
	var files []OutgoingFile
	var cleanup []func()
	defer func() {
		for _, f := range cleanup {
			f()
		}
	}()
	for _, item := range r.Items {
		for _, f := range item.Files {
			if !f.Selected {
				continue
			}
			var source *ConvMessage
			for i := range sourceMessages {
				if sourceMessages[i].ID == item.ID {
					source = &sourceMessages[i]
					break
				}
			}
			if source == nil {
				return ConvSent{}, ErrHistoryContributionChanged
			}
			reader, info, e := a.openHistoryContributionFile(*source, f.Index)
			if e != nil {
				if errors.Is(e, os.ErrNotExist) || errors.Is(e, errNotKept) || errors.Is(e, ErrNoMessage) {
					return ConvSent{}, ErrHistoryContributionChanged
				}
				return ConvSent{}, e // Preserve unknown local storage errors for an exact retry.
			}
			if info.Size != f.Size || info.SHA256 != f.SHA256 {
				reader.Close()
				return ConvSent{}, ErrHistoryContributionChanged
			}
			path, remove, e := a.StageUpload(f.Name, io.LimitReader(reader, f.Size+1))
			reader.Close()
			if e != nil {
				return ConvSent{}, e
			}
			cleanup = append(cleanup, remove)
			size, hash, e := fileDigest(path)
			if e != nil || size != f.Size || hash != f.SHA256 {
				return ConvSent{}, ErrHistoryContributionChanged
			}
			files = append(files, OutgoingFile{Name: f.Name, Path: path})
		}
	}
	claim := func(tx *sql.Tx, _ string) error {
		stamp, e := a.historyContributionStamp(tx, r.HistoryContributionRequest)
		if e != nil {
			return ErrHistoryContributionChanged
		}
		if stamp != r.Token {
			return ErrHistoryContributionChanged
		}
		if r.NewTopic {
			return a.recordTopicTitle(tx, r.Destination, r.Topic, r.Title)
		}
		return nil
	}
	sent, e := a.SendConv(WithQueuedSend(ctx, r.Operation), r.Destination, ConvOutgoing{Kind: envelope.KindMessage, Origin: envelope.OriginUI, Body: r.Body, Topic: r.Topic, Files: files, claim: claim, contribution: true})
	if e != nil {
		if kept, found, x := a.keptHistoryContribution(r); found || x != nil {
			return kept, x
		}
	}
	if e == nil && r.NewTopic {
		a.topicTitlesChanged()
	}
	return sent, e
}
