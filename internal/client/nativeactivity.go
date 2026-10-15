package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Only these fixed categories leave an owned native stream. Tool arguments,
// output, prompts and reasoning never become status detail.
type nativeActivity uint32

const (
	activityUnknown nativeActivity = iota
	activityWorking
	activityReading
	activityEditing
	activityCommand
	activitySearch
	activityTool
	activityAnswer
)

func (k nativeActivity) detail() string {
	switch k {
	case activityWorking:
		return "Working"
	case activityReading:
		return "Reading files"
	case activityEditing:
		return "Editing files"
	case activityCommand:
		return "Running a command"
	case activitySearch:
		return "Searching"
	case activityTool:
		return "Using a tool"
	case activityAnswer:
		return "Preparing answer"
	}
	return ""
}

func nativeToolActivity(name string) nativeActivity {
	switch name {
	case "Read", "read", "read_file":
		return activityReading
	case "Edit", "MultiEdit", "Write", "edit", "write":
		return activityEditing
	case "Bash", "bash":
		return activityCommand
	case "Grep", "Glob", "WebSearch", "grep", "find", "ls":
		return activitySearch
	case "":
		return activityUnknown
	}
	return activityTool
}

// The native reader does no I/O and retains one category. A slow store or
// status sender cannot backpressure native tools or the completed answer.
func (a *runActivity) report(k nativeActivity) {
	if k.detail() == "" || a.native.Swap(uint32(k)) == uint32(k) {
		return
	}
	a.touch(time.Now())
}

func publicActivityDetail(detail string) bool {
	label, stamp, timed := strings.Cut(detail, "\nReported at ")
	if timed {
		at, err := time.Parse(time.RFC3339, stamp)
		if err != nil || at.UTC().Format(time.RFC3339) != stamp {
			return false
		}
	}
	for k := activityWorking; k <= activityAnswer; k++ {
		if label == k.detail() {
			return true
		}
	}
	return false
}

const nativeActivityInterval = 2 * time.Second
const nativeInputDetail = "Waiting for native input"

var errNativeActivityDeferred = errors.New("optional native activity deferred until its reader is available")

// This only recognizes optional public activity. Terminal, approval, steering
// and decision statuses retain their ordinary durable delivery semantics.
func nativeActivityStatus(sub, body string) *envelope.Status {
	if sub != envelope.SubStatus {
		return nil
	}
	var st envelope.Status
	if json.Unmarshal([]byte(body), &st) != nil || st.State != stateRunning || st.Decision != "" || st.Refused != "" || !publicActivityDetail(st.Detail) {
		return nil
	}
	return &st
}

// Availability can omit optional telemetry; it never admits a destination.
// The existing send path still checks keys, capabilities and current audience.
// A pending sealed copy stays immutable. Keep newer detail on the request
// until that copy is delivered, rather than sealing a progress backlog.
func (a *Agent) nativeActivityRecipients(ctx context.Context, ref ControlRef, st envelope.Status) func(string) (bool, error) {
	connected := map[string]bool{}
	if view := a.MemberView(); view.Current {
		for _, m := range view.Members.Members {
			connected[m.Address] = m.Presence == protocol.PresenceConnected && !m.Suspended
		}
	}
	type copyState struct {
		latest  envelope.Status
		id      string
		pending bool
	}
	var copies map[string]copyState
	var loadErr error
	load := func() {
		copies = map[string]copyState{}
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		// One indexed pass for this send, shared by every recipient. No sort,
		// per-recipient history scan or persistent projection/cache.
		rows, err := a.store.db.QueryContext(ctx, `SELECT id,recipient,state,body FROM outbox
		 WHERE ref_id=? AND ref_fp=? AND sub='status' AND coalesce(conv,'')=?`, ref.ID, ref.Fingerprint, ref.Conv)
		if err != nil {
			loadErr = err
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id, to, state, body string
			if loadErr = rows.Scan(&id, &to, &state, &body); loadErr != nil {
				return
			}
			if !connected[to] {
				continue
			}
			var previous envelope.Status
			if loadErr = json.Unmarshal([]byte(body), &previous); loadErr != nil {
				return
			}
			if previous.Decision != "" {
				continue
			}
			c := copies[to]
			if previous.N > c.latest.N || previous.N == c.latest.N && id > c.id {
				c.latest, c.id = previous, id
			}
			c.pending = c.pending || state != protocol.StateDelivered && previous.Attempt == st.Attempt && previous.State == stateRunning && publicActivityDetail(previous.Detail)
			copies[to] = c
		}
		loadErr = rows.Err()
	}
	return func(to string) (bool, error) {
		if !connected[to] {
			return false, errNativeActivityDeferred
		}
		if copies == nil {
			load()
		}
		if loadErr != nil {
			return false, errNativeActivityDeferred
		}
		c := copies[to]
		previous := c.latest
		if previous.Attempt > st.Attempt || previous.Attempt == st.Attempt && previous.State != "" && previous.State != "queued" && previous.State != stateRunning {
			return false, nil // a later or settled outcome is already sealed
		}
		if previous.Attempt == st.Attempt && previous.State == st.State && previous.Detail == st.Detail {
			return false, nil // this exact value already has its immutable copy
		}
		if c.pending {
			return false, errNativeActivityDeferred
		}
		return true, nil
	}
}

// status_due coalesces changes before sending; it does not coalesce sealed
// outbox copies. Telemetry can only annotate the exact still-running attempt,
// never change its execution state.
func (s *store) markNativeActivity(ctx context.Context, j job, k nativeActivity) bool {
	if j.nativeAttempt < 1 || k.detail() == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	due := 1
	if j.Receiver != nil {
		due = 0
	}
	label := k.detail()
	detail := label + "\nReported at " + time.Now().UTC().Format(time.RFC3339)
	// A selected local continuation has no status audience at the reply author.
	res, err := s.db.ExecContext(ctx, `UPDATE inbox SET detail=?,status_due=status_due+?
		WHERE id=? AND sender=? AND verified_by=? AND attempts=? AND state='running' AND replica=0
		AND coalesce(conv,'')=? AND coalesce(pid,'')=?
		AND coalesce(detail,'') NOT IN (?,?) AND coalesce(detail,'') NOT LIKE ?
		AND NOT EXISTS(SELECT 1 FROM native_requests n WHERE n.request=inbox.id AND n.attempt=inbox.attempts
		 AND n.state='pending' AND (n.deadline=0 OR n.deadline>?))`, detail, due, j.ID, j.From, j.Key, j.nativeAttempt, j.Conv, j.PID, label, roomWaitDetail, label+"\nReported at %", time.Now().UnixMilli())
	if err != nil {
		return false // optional telemetry has no execution or error-reporting effect
	}
	n, err := res.RowsAffected()
	if err == nil && n > 0 {
		s.changed()
		return true
	}
	return false
}

func (a *Agent) noteNativeWait(request string) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _ = a.store.db.ExecContext(ctx, `UPDATE inbox SET detail=CASE WHEN EXISTS(
		 SELECT 1 FROM native_requests n WHERE n.request=inbox.id AND n.attempt=inbox.attempts
		 AND n.state='pending' AND (n.deadline=0 OR n.deadline>?)) THEN ? ELSE NULL END,
		 status_due=status_due+1 WHERE id=? AND state='running' AND coalesce(detail,'')!=?
		 AND (detail=? OR EXISTS(SELECT 1 FROM native_requests n WHERE n.request=inbox.id
		 AND n.attempt=inbox.attempts AND n.state='pending' AND (n.deadline=0 OR n.deadline>?)))`,
		time.Now().UnixMilli(), nativeInputDetail, request, roomWaitDetail, nativeInputDetail, time.Now().UnixMilli())
	a.wakeStatus()
}
