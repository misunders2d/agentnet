package client

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// InboxPageOptions selects a local, read-only inspection. Before is the
// returned keyset cursor, never an offset into a changing unread/review list.
type InboxPageOptions struct {
	Unread, Review bool
	Limit          int
	Before, ID     string
}

type InboxPageItem struct {
	Message
	Conv   string `json:"conv,omitempty"`
	PID    string `json:"pid,omitempty"`
	Cursor string `json:"-"`
}

type InboxPage struct {
	Items []InboxPageItem
	Next  string
}

type inboxPosition struct {
	Section string
	At      int64
	Row     int64
	Key     string
}

func (p inboxPosition) cursor() string {
	raw, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func inboxBefore(section string, o InboxPageOptions) (*inboxPosition, error) {
	if o.Limit < 1 || o.Limit > 100 || len(o.Before) > 512 || o.ID != "" && o.Before != "" {
		return nil, errors.New("inbox: limit must be 1..100; choose before or id")
	}
	if o.Before == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(o.Before)
	var p inboxPosition
	if err != nil || json.Unmarshal(raw, &p) != nil || p.Section != section || p.At < 0 || p.Row < 0 || len(p.Key) > 160 {
		return nil, errors.New("inbox: invalid cursor for this section")
	}
	return &p, nil
}

// InspectInbox loads only one bounded page, newest first. The existing Inbox
// and Review APIs keep their behavior; this method never marks or resolves.
func (a *Agent) InspectInbox(o InboxPageOptions) (InboxPage, error) {
	var page InboxPage
	section := fmt.Sprintf("messages/%t/%t", o.Unread, o.Review)
	before, err := inboxBefore(section, o)
	if err != nil {
		return page, err
	}
	where := `local=0 AND ref_id IS NULL AND coalesce(sub,'') NOT IN ` + recordSubs
	args := []any{}
	if o.Review {
		where = `((` + inReview + `) OR (state=? AND (` + receivedNotice + `)))`
		args = append(args, reviewStates...)
		args = append(args, stateNeedHuman, envelope.KindMessage, envelope.StatusReviewNotice)
	}
	if o.Unread {
		where += ` AND read_at IS NULL`
	}
	if o.ID != "" {
		if !protocol.ValidID(o.ID) {
			return page, errors.New("inbox: id must be an exact received message id")
		}
		where += ` AND id=?`
		args = append(args, o.ID)
	}
	if before != nil {
		where += ` AND (coalesce(received_ms,received_at*1000),rowid)<(?,?)`
		args = append(args, before.At, before.Row)
	}
	args = append(args, o.Limit+1)
	rows, err := a.store.db.Query(`SELECT id,coalesce(conv,''),coalesce(pid,''),coalesce(received_ms,received_at*1000),rowid FROM inbox WHERE `+where+` ORDER BY coalesce(received_ms,received_at*1000) DESC,rowid DESC LIMIT ?`, args...)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var item InboxPageItem
		p := inboxPosition{Section: section}
		if err = rows.Scan(&item.ID, &item.Conv, &item.PID, &p.At, &p.Row); err != nil {
			break
		}
		item.Cursor = p.cursor()
		page.Items = append(page.Items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Items) > o.Limit {
		page.Items = page.Items[:o.Limit]
		page.Next = page.Items[len(page.Items)-1].Cursor
	}
	for i := range page.Items {
		m, err := a.store.inboxMessage(page.Items[i].ID)
		if err != nil {
			return InboxPage{}, err
		}
		if m == nil {
			return InboxPage{}, ErrNoMessage
		}
		page.Items[i].Message = *m
	}
	if o.ID != "" && len(page.Items) == 0 {
		return page, ErrNoMessage
	}
	return page, nil
}

// InboxNotice is actionable metadata from one of review's existing auxiliary
// sections. It does not grant a decision or substitute for that action's checks.
type InboxNotice struct {
	ID              string `json:"id"`
	From            string `json:"from,omitempty"`
	Conv            string `json:"conv,omitempty"`
	PID             string `json:"pid,omitempty"`
	Kind            string `json:"kind,omitempty"`
	Detail          string `json:"detail,omitempty"`
	DetailTruncated bool   `json:"detail_truncated,omitempty"`
	At              int64  `json:"at,omitempty"`
	Content         any    `json:"content,omitempty"`
	Cursor          string `json:"-"`
}

type InboxNoticePage struct {
	Items []InboxNotice
	Next  string
}

// InspectInboxNotices pages each existing review section independently, so
// an old invitation or held envelope cannot make the default lookup unbounded.
func (a *Agent) InspectInboxNotices(section string, o InboxPageOptions) (InboxNoticePage, error) {
	var page InboxNoticePage
	before, err := inboxBefore(section, o)
	if err != nil {
		return page, err
	}
	if o.ID != "" && !protocol.ValidID(o.ID) && !protocol.ValidHash(o.ID) {
		return page, errors.New("inbox: invalid exact notice id")
	}
	if section == "joined" {
		notices, err := a.SelfConsentNotices() // storage already caps this at 64
		if err != nil {
			return page, err
		}
		slices.SortFunc(notices, func(a, b SelfConsentNotice) int {
			if a.At > b.At {
				return -1
			}
			if a.At < b.At {
				return 1
			}
			return strings.Compare(b.PID, a.PID)
		})
		for _, n := range notices {
			if o.ID != "" && n.PID != o.ID || before != nil && (n.At > before.At || n.At == before.At && n.PID >= before.Key) {
				continue
			}
			page.Items = append(page.Items, InboxNotice{ID: n.PID, PID: n.PID, Conv: n.Conv, From: n.Inviter, At: n.At, Kind: n.AgentID, Content: n, Cursor: (inboxPosition{Section: section, At: n.At, Key: n.PID}).cursor()})
			if len(page.Items) > o.Limit {
				break
			}
		}
	} else {
		// Normalize only metadata, not message bodies or signed proposal blobs.
		var query string
		var args []any
		switch section {
		case "requests":
			query = `SELECT id,sender AS sender,coalesce(conv,'') AS conv,coalesce(pid,'') AS pid,kind,received_at AS at,coalesce(json_extract(target,'$.agent_id'),'') AS detail FROM inbox WHERE state=? AND pid IS NOT NULL AND replica=0 AND NOT EXISTS(SELECT 1 FROM reply_receiver_inputs ri WHERE ri.inbox_id=inbox.id)`
			args = []any{stateAgentWaiting}
		case "invites":
			query = `SELECT pid AS id,'' AS sender,conv,pid,'' AS kind,min(received_at) AS at,'' AS detail FROM participation_events WHERE type IN (?,?) AND json_extract(event,'$.host.address')=? GROUP BY conv,pid`
			args = []any{protocol.EventInvite, protocol.EventScope, a.Address}
		case "groups":
			query = `SELECT id,peer_address AS sender,conv,'' AS pid,'' AS kind,rowid AS at,'' AS detail FROM group_invitations WHERE direction='in' AND state='pending'`
		case "links":
			query = `SELECT offer AS id,address AS sender,'' AS conv,'' AS pid,'' AS kind,requested_at AS at,public AS detail FROM device_links WHERE state=? AND expires>?`
			args = []any{LinkPending, time.Now().Unix()}
		case "held":
			query = `SELECT id,sender,'' AS conv,'' AS pid,reason AS kind,received_at AS at,detail_code AS detail FROM quarantine q WHERE notice_archived=0 AND reason<>?`
			args = []any{reasonProof}
			if o.ID == "" { // a re-sent copy is one notice with its record; its exact id still finds it
				query += ` AND ` + heldOwnRow
			}
		default:
			return page, errors.New("inbox: section must be requests, invites, groups, links, held or joined")
		}
		query = `SELECT id,sender,conv,pid,kind,at,detail FROM (` + query + `) WHERE 1=1`
		if o.ID != "" {
			query += ` AND id=?`
			args = append(args, o.ID)
		}
		if before != nil {
			query += ` AND (at,conv||'/'||id)<(?,?)`
			args = append(args, before.At, before.Key)
		}
		query += ` ORDER BY at DESC,conv DESC,id DESC LIMIT ?`
		args = append(args, o.Limit+1)
		rows, err := a.store.db.Query(query, args...)
		if err != nil {
			return page, err
		}
		for rows.Next() {
			var n InboxNotice
			if err = rows.Scan(&n.ID, &n.From, &n.Conv, &n.PID, &n.Kind, &n.At, &n.Detail); err != nil {
				break
			}
			n.Cursor = (inboxPosition{Section: section, At: n.At, Key: n.Conv + "/" + n.ID}).cursor()
			page.Items = append(page.Items, n)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return page, err
		}
	}
	if len(page.Items) > o.Limit {
		page.Items = page.Items[:o.Limit]
		page.Next = page.Items[len(page.Items)-1].Cursor
	}
	visible := page.Items[:0]
	for _, n := range page.Items {
		switch section {
		case "links":
			var pub identity.Public
			if err := json.Unmarshal([]byte(n.Detail), &pub); err != nil {
				return InboxNoticePage{}, err
			}
			n.Detail = "key " + pub.Fingerprint()
		case "requests":
			n.Detail = a.NothingRuns(n.Detail)
			if n.Detail == "" {
				n.Detail = "waiting for participation evidence or acceptance"
			}
		case "invites":
			info, err := a.participation(n.Conv, n.PID)
			if errors.Is(err, ErrNoParticipation) || errors.Is(err, ErrGroupContextPending) {
				continue
			}
			if err != nil {
				if o.ID != "" {
					return InboxNoticePage{}, err
				}
				continue // hostInvites likewise omits context that cannot resolve
			}
			if !info.HostHere || info.State != PartInvited || info.Role == protocol.RoleHuman && info.Held != 0 {
				continue
			}
			n.From, n.Detail, n.Content = info.Inviter.Address, info.Note, info
		case "groups":
			r, err := groupInvitationIn(a.store.db, n.ID, "in")
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return InboxNoticePage{}, err
			}
			n.At = 0 // the ordering key is insertion order, not a fabricated date
			n.Content = r.GroupInvitationInfo
		}
		visible = append(visible, n)
	}
	page.Items = visible
	if o.ID != "" && len(page.Items) == 0 {
		return page, ErrNoMessage
	}
	return page, nil
}
