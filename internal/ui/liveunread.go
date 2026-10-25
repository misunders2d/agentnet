package ui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// UnreadProvider is optional: absent support means unknown, not zero unread.
type UnreadProvider interface {
	UnreadPage(UnreadQuery) (UnreadPage, error)
}
type UnreadQuery struct {
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// UnreadRow is display/navigation only. Author is the original fingerprint;
// ID is the selected local row and LID is its logical identity. No actions.
type UnreadRow struct {
	Key        string    `json:"key"`
	Kind       string    `json:"kind"`
	Conv       string    `json:"conv,omitempty"`
	Peer       string    `json:"peer,omitempty"`
	Thread     string    `json:"thread,omitempty"`
	Topic      string    `json:"topic,omitempty"`
	TopicTitle string    `json:"topic_title,omitempty"`
	ID         string    `json:"id"`
	LID        string    `json:"lid"`
	Author     string    `json:"author"`
	Dir        string    `json:"dir"`
	At         time.Time `json:"at"`
	Label      string    `json:"label"`
	Preview    string    `json:"preview"`
	ready      bool
}
type UnreadPage struct {
	Rows    []UnreadRow `json:"rows"`
	Total   int         `json:"total"`
	Next    string      `json:"next,omitempty"`
	Content uint64      `json:"content"`
	Seq     uint64      `json:"seq"`
}
type unreadIndex struct {
	content uint64
	seq     uint64
	rows    []UnreadRow
	sorted  bool
}
type unreadCursor struct {
	V   int    `json:"v"`
	At  int64  `json:"at"`
	Key string `json:"key"`
}

// UnreadNotificationRefs checks only currently displayed native notice targets
// against the overview's compact index. It neither builds a view nor hydrates
// previews. The caller retries on its existing content event if the view moved.
func (l *Live) UnreadNotificationRefs(content uint64, refs []protocol.ReadRef) (map[protocol.ReadRef]bool, error) {
	if len(refs) > protocol.MaxNotifySeenIDs {
		return nil, Refuse("Too many notification targets.")
	}
	wanted := make(map[protocol.ReadRef]bool, len(refs))
	for _, ref := range refs {
		if ref.Conv != "" && !protocol.ValidHash(ref.Conv) || !protocol.ValidFingerprint(ref.Fingerprint) || !protocol.ValidID(ref.LID) {
			return nil, Refuse("That notification target is not valid.")
		}
		wanted[ref] = false
	}
	l.overviewMu.Lock()
	defer l.overviewMu.Unlock()
	revision, _ := l.a.ViewChanges()
	if revision.Content != content || l.unread == nil || l.unread.content != content {
		return nil, Refuse("Notification state changed.")
	}
	for _, row := range l.unread.rows {
		ref := protocol.ReadRef{Conv: row.Conv, Fingerprint: row.Author, LID: row.LID}
		if _, selected := wanted[ref]; selected {
			wanted[ref] = true
		}
	}
	return wanted, nil
}

func unreadKey(r UnreadRow) string {
	scope := r.Conv
	if r.Kind == "device" {
		scope = r.Peer
	}
	lid := r.LID
	if lid == "" {
		lid = r.ID
	}
	return strings.Join([]string{r.Kind, scope, r.Author, lid}, ":")
}
func unreadAfter(r UnreadRow, c unreadCursor) bool {
	return r.At.UnixMilli() < c.At || r.At.UnixMilli() == c.At && r.Key > c.Key
}
func encodeUnreadCursor(r UnreadRow) string {
	b, _ := json.Marshal(unreadCursor{2, r.At.UnixMilli(), r.Key})
	return base64.RawURLEncoding.EncodeToString(b)
}
func parseUnreadCursor(s string) (unreadCursor, error) {
	var c unreadCursor
	var fields struct {
		V   *int    `json:"v"`
		At  *int64  `json:"at"`
		Key *string `json:"key"`
	}
	bad := Refuse("That unread list request is not valid.")
	if len(s) > 2048 {
		return c, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, bad
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&fields) != nil || fields.V == nil || fields.At == nil || fields.Key == nil {
		return c, bad
	}
	c = unreadCursor{*fields.V, *fields.At, *fields.Key}
	if c.V != 2 || c.At < 0 || c.At > 9007199254740991 || c.Key == "" || len(c.Key) > 512 {
		return c, bad
	}
	if d.Decode(new(any)) != io.EOF {
		return c, bad
	}
	return c, nil
}

func (l *Live) UnreadPage(q UnreadQuery) (UnreadPage, error) {
	limit := q.Limit
	if limit == 0 {
		limit = client.TopicPageDefault
	}
	if limit < 1 || limit > client.TopicPageMax {
		return UnreadPage{}, Refuse("That unread list request is not valid.")
	}
	var cursor unreadCursor
	var err error
	if q.After != "" {
		if cursor, err = parseUnreadCursor(q.After); err != nil {
			return UnreadPage{}, err
		}
	}
	l.overviewMu.Lock()
	defer l.overviewMu.Unlock()
	revision, _ := l.a.ViewChanges()
	if l.unread == nil || l.unread.content != revision.Content {
		if _, err = l.overviewLocked(false); err != nil {
			return UnreadPage{}, err
		}
	}
	index := l.unread
	if !index.sorted {
		sort.Slice(index.rows, func(i, j int) bool {
			a, b := index.rows[i], index.rows[j]
			if a.At.UnixMilli() != b.At.UnixMilli() {
				return a.At.UnixMilli() > b.At.UnixMilli()
			}
			return a.Key < b.Key
		})
		index.sorted = true
	}
	start := 0
	if q.After != "" {
		start = sort.Search(len(index.rows), func(i int) bool { return unreadAfter(index.rows[i], cursor) })
	}
	end := min(start+limit, len(index.rows))
	page := UnreadPage{Rows: append([]UnreadRow{}, index.rows[start:end]...), Total: len(index.rows), Content: index.content, Seq: index.seq}
	if revision.Content == index.content {
		page.Seq = revision.Seq
	}
	if end < len(index.rows) {
		page.Next = encodeUnreadCursor(index.rows[end-1])
	}
	var refs []client.DeviceUnreadRef
	for _, r := range page.Rows {
		if r.Kind == "device" && !r.ready {
			refs = append(refs, client.DeviceUnreadRef{Peer: r.Peer, ID: r.ID, Author: r.Author})
		}
	}
	if len(refs) > 0 {
		lines, err := l.a.UnreadDevicePreviews(refs)
		if err != nil {
			return UnreadPage{}, err
		}
		for i := range page.Rows {
			r := &page.Rows[i]
			if r.Kind == "device" && !r.ready {
				r.Preview = lines[r.Peer+"\x00"+r.ID]
			}
		}
	}
	return page, nil
}

func (s *Server) unread(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(UnreadProvider)
	if !ok {
		writeErr(w, NotFound("unread messages are not listed here"))
		return
	}
	q := UnreadQuery{After: r.URL.Query().Get("after")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		for _, c := range raw {
			if c < '0' || c > '9' {
				err = strconv.ErrSyntax
				break
			}
		}
		if err != nil {
			writeErr(w, Refuse("That unread list request is not valid."))
			return
		}
		q.Limit = n
	}
	page, err := p.UnreadPage(q)
	writeResult(w, page, err)
}
