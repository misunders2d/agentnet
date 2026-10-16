package client

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"golang.org/x/text/unicode/norm"
)

// Categories are the fallback when owned native thinking is absent or unsafe.
// Pi/OMP expose provider thinking, which is filtered here rather than assumed
// to be a provider-generated summary. Only a concise safe line becomes detail.
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

type nativeProgress struct {
	kind    nativeActivity
	summary string
	at      time.Time // local observation, not the later store/delivery time
}

func (p nativeProgress) label() string {
	if p.summary != "" {
		return "Thinking: " + p.summary
	}
	return p.kind.detail()
}

func (p nativeProgress) same(other *nativeProgress) bool {
	return other != nil && p.kind == other.kind && p.summary == other.summary
}

const thinkingBufferMax = 2000

var unsafeThinking = []*regexp.Regexp{
	regexp.MustCompile(`[@/\\]`), // includes relative paths and plain user handles
	regexp.MustCompile(`(?i)https?://|www\.|\b[\w.+-]+@[\w.-]+\.\w{2,}\b|<[@#!]|@(?:channel|everyone|here)\b`),
	regexp.MustCompile(`(?i)(?:^|\s)(?:/(?:[\w.-]+/)*[\w.-]+|~/|[A-Za-z]:\\)|\b[0-9a-f]{8}-[0-9a-f-]{27,}\b|\b[A-Za-z0-9_+/=.-]{32,}\b`),
	regexp.MustCompile(`\b[UCDGW][A-Z0-9]{8,}\b`),
	regexp.MustCompile("(?i)\\b(?:api[ _-]?key|authorization|bearer|client[ _-]?secret|password|refresh[ _-]?token|secret|token|ssn|social security|medical|diagnos\\w*|patient|salary|phone number|account number)\\b|[`{}\\[\\]]|=>|\\w\\s*=\\s*\\S"),
}

// Sanitize only a complete native line/block or a public canonical value.
// The full newest line is checked before clipping; never invent a safe suffix
// by dropping the beginning of an oversized or secret-bearing line.
func safeThinkingSummary(value string) (summary string) {
	defer func() {
		if recover() != nil {
			summary = ""
		}
	}()
	if !utf8.ValidString(value) {
		return ""
	}
	var line string
	for value != "" {
		value = strings.TrimRight(value, "\r\n\u2028\u2029")
		at := strings.LastIndexAny(value, "\r\n\u2028\u2029")
		if at < 0 {
			line, value = value, ""
		} else {
			_, size := utf8.DecodeRuneInString(value[at:])
			line, value = value[at+size:], value[:at]
		}
		if strings.TrimSpace(line) != "" {
			break
		}
	}
	if len(line) > thinkingBufferMax*utf8.UTFMax || utf8.RuneCountInString(line) > 500 {
		return ""
	}
	// Preserve the display ellipsis under repeated canonical validation.
	parts := strings.Split(line, "…")
	for i := range parts {
		parts[i] = norm.NFKC.String(parts[i])
	}
	line = strings.TrimSpace(strings.Join(parts, "…"))
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, line)
	line = strings.TrimLeft(line, "-#*_~ \t")
	line = strings.Map(func(r rune) rune {
		if strings.ContainsRune("*_~", r) {
			return -1
		}
		return r
	}, line)
	line = strings.Join(strings.Fields(line), " ")
	line = strings.TrimRight(line, ".!?:;, ")
	if line == "" || strings.Contains(line, "[REDACTED]") {
		return ""
	}
	unsafe := false
	for _, re := range unsafeThinking {
		unsafe = unsafe || re.MatchString(line)
	}
	if unsafe || mixedThinkingToken(line) {
		return ""
	}
	runes := []rune(line)
	if len(runes) > 120 {
		line = string(runes[:117])
		if at := strings.LastIndexByte(line, ' '); at >= 0 {
			line = line[:at]
		}
		line = strings.TrimRight(line, " ") + "…"
	}

	return line
}

// A conservative heuristic for short identifiers/credentials. It does not
// claim to recognize all secrets; normalization runs before this scan.
func mixedThinkingToken(line string) bool {
	for _, token := range strings.FieldsFunc(line, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if utf8.RuneCountInString(token) < 16 {
			continue
		}
		letter, digit := false, false
		for _, r := range token {
			letter = letter || unicode.IsLetter(r)
			digit = digit || unicode.IsNumber(r)
		}
		if letter && digit {
			return true
		}
	}
	return false
}

func thinkingTail(value string) string {
	if len(value) > thinkingBufferMax*utf8.UTFMax {
		value = value[len(value)-thinkingBufferMax*utf8.UTFMax:]
		for len(value) > 0 && !utf8.RuneStart(value[0]) {
			value = value[1:]
		}
	}
	runes := []rune(value)
	if len(runes) > thinkingBufferMax {
		value = string(runes[len(runes)-thinkingBufferMax:])
	}
	return value
}

func canonicalThinkingSummary(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= 120 && safeThinkingSummary(value) == value
}

// Each bridge retains one bounded incomplete line. Overflow discards the
// entire line until its boundary, so a truncated suffix can never be published.
type nativeThinkingBuffer struct {
	key                     string
	index                   int64
	text                    string
	open, overflow, started bool
}

func (b *nativeThinkingBuffer) start(key string, index int64) {
	*b = nativeThinkingBuffer{}
	if len(key) <= 256 && index >= 0 {
		b.key, b.index, b.open = key, index, true
	}
}

func (b *nativeThinkingBuffer) add(key string, index int64, value string, callback func(string), fallback func(nativeActivity)) bool {
	if !b.open || b.key != key || b.index != index || !utf8.ValidString(value) {
		return false
	}
	b.started = b.started || value != ""
	for value != "" {
		at := strings.IndexAny(value, "\r\n\u2028\u2029")
		part := value
		if at >= 0 {
			part = value[:at]
		}
		if !b.overflow {
			if len(b.text)+len(part) > thinkingBufferMax*utf8.UTFMax || utf8.RuneCountInString(b.text)+utf8.RuneCountInString(part) > thinkingBufferMax {
				b.text, b.overflow = "", true
			} else {
				b.text += part
			}
		}
		if at < 0 {
			break
		}
		if b.overflow {
			emitNativeThinking(callback, fallback, "")
		} else if strings.TrimSpace(b.text) != "" {
			emitNativeThinking(callback, fallback, b.text)
		}
		b.text, b.overflow = "", false
		_, size := utf8.DecodeRuneInString(value[at:])
		value = value[at+size:]
	}
	return true
}

func (b *nativeThinkingBuffer) finish(callback func(string), fallback func(nativeActivity)) {
	if b.open {
		if b.overflow {
			emitNativeThinking(callback, fallback, "")
		} else if strings.TrimSpace(b.text) != "" {
			emitNativeThinking(callback, fallback, b.text)
		}
	}
	*b = nativeThinkingBuffer{}
}

func emitNativeActivity(callback func(nativeActivity), kind nativeActivity) {
	defer func() { _ = recover() }() // optional callback cannot unwind a bridge lock
	if callback != nil {
		callback(kind)
	}
}

func emitNativeThinking(callback func(string), fallback func(nativeActivity), value string) {
	defer func() {
		if recover() != nil {
			emitNativeActivity(fallback, activityWorking)
		}
	}()
	if callback != nil {
		callback(safeThinkingSummary(value))
	}
}

// The native reader does no I/O and retains one snapshot. A slow store or
// status sender cannot backpressure native tools or the completed answer.
func (a *runActivity) report(k nativeActivity) {
	a.reportProgress(nativeProgress{kind: k, at: time.Now()})
}

func (a *runActivity) reportThinking(summary string) {
	a.reportProgress(nativeProgress{kind: activityWorking, summary: summary, at: time.Now()})
}

func (a *runActivity) reportProgress(p nativeProgress) {
	if p.kind.detail() == "" {
		return
	}
	for old := a.native.Load(); ; old = a.native.Load() {
		if p.same(old) || old != nil && p.at.Before(old.at) {
			return
		}
		if a.native.CompareAndSwap(old, &p) {
			break
		}
	}
	a.touch(p.at)
}

func publicActivityDetail(detail string) bool {
	label, stamp, timed := strings.Cut(detail, "\nReported at ")
	if timed {
		at, err := time.Parse(time.RFC3339, stamp)
		if err != nil || at.UTC().Format(time.RFC3339) != stamp {
			return false
		}
	}
	if summary, ok := strings.CutPrefix(label, "Thinking: "); ok {
		return canonicalThinkingSummary(summary)
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
		if previous.Attempt == st.Attempt && previous.State == st.State {
			same := previous.Detail == st.Detail
			if !same && publicActivityDetail(previous.Detail) && publicActivityDetail(st.Detail) {
				previousLabel, _, _ := strings.Cut(previous.Detail, "\nReported at ")
				label, _, _ := strings.Cut(st.Detail, "\nReported at ")
				same = previousLabel == label
			}
			if same {
				return false, nil // this public category already has its immutable copy
			}
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
	return s.markNativeProgress(ctx, j, nativeProgress{kind: k, at: time.Now()})
}

func (s *store) markNativeProgress(ctx context.Context, j job, p nativeProgress) bool {
	label := p.label()
	if j.nativeAttempt < 1 || p.kind.detail() == "" || !publicActivityDetail(label) || p.at.IsZero() {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	due := 1
	if j.Receiver != nil {
		due = 0
	}
	detail := label + "\nReported at " + p.at.UTC().Format(time.RFC3339)
	prefix := label + "\nReported at "
	// A selected local continuation has no status audience at the reply author.
	res, err := s.db.ExecContext(ctx, `UPDATE inbox SET detail=?,status_due=status_due+?
		WHERE id=? AND sender=? AND verified_by=? AND attempts=? AND state='running' AND replica=0
		AND coalesce(conv,'')=? AND coalesce(pid,'')=?
		AND coalesce(detail,'') NOT IN (?,?) AND substr(coalesce(detail,''),1,length(?))!=?
		AND NOT EXISTS(SELECT 1 FROM native_requests n WHERE n.request=inbox.id AND n.attempt=inbox.attempts
		 AND n.state='pending' AND (n.deadline=0 OR n.deadline>?))`, detail, due, j.ID, j.From, j.Key, j.nativeAttempt, j.Conv, j.PID, label, roomWaitDetail, prefix, prefix, time.Now().UnixMilli())
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
