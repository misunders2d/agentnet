package ui

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Schedules is optional. These records belong to this local workspace/device.
// Sent means queued by the ordinary sender, never a delivery receipt.
type Schedules interface {
	ScheduledMessages(conv string, all bool) ([]client.ScheduledSend, error)
	ScheduledMessage(id string) (client.ScheduledSend, error)
	ScheduleAudience(conv, topic string) (client.ScheduledAudience, error)
	ChangeSchedule(context.Context, string, ScheduleDraft) (client.ScheduledSend, error)
}
type ScheduleDraft struct {
	ID       string                    `json:"id,omitempty"`
	Rev      int64                     `json:"rev,omitempty"`
	Conv     string                    `json:"conv"`
	Topic    string                    `json:"topic,omitempty"`
	Body     string                    `json:"body"`
	ReplyTo  string                    `json:"reply_to,omitempty"`
	Quote    string                    `json:"quote,omitempty"`
	Due      time.Time                 `json:"due"`
	Timezone string                    `json:"timezone"`
	Files    []string                  `json:"files,omitempty"`
	Audience *client.ScheduledAudience `json:"audience,omitempty"`
}
type ScheduleAction struct {
	ID  string `json:"id"`
	Rev int64  `json:"rev"`
}

func scheduleRefusal(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{client.ErrScheduledIDConflict, client.ErrScheduledStale, client.ErrScheduledAudience} {
		if errors.Is(err, known) {
			return Refuse(sentence(known))
		}
	}
	// File errors may contain private staging paths; never expose them or log them.
	return Refuse("The scheduled message is unavailable or could not be saved. Check its chat, time and attachments.")
}
func (l *Live) ScheduledMessages(conv string, all bool) ([]client.ScheduledSend, error) {
	rows, err := l.a.ScheduledSends(all)
	if err != nil {
		return nil, scheduleRefusal(err)
	}
	if conv == "" {
		return rows, nil
	}
	out := []client.ScheduledSend{}
	for _, row := range rows {
		if row.Conv == conv {
			out = append(out, row)
		}
	}
	return out, nil
}
func (l *Live) ScheduledMessage(id string) (client.ScheduledSend, error) {
	row, ok, err := l.a.ScheduledSend(id)
	if err != nil {
		return row, scheduleRefusal(err)
	}
	if !ok {
		return row, NotFound("That scheduled message is not on this device.")
	}
	return row, nil
}
func (l *Live) ScheduleAudience(conv, topic string) (client.ScheduledAudience, error) {
	row, err := l.a.ScheduleAudience(conv, topic)
	return row, scheduleRefusal(err)
}
func (l *Live) ChangeSchedule(ctx context.Context, what string, d ScheduleDraft) (client.ScheduledSend, error) {
	files, cleanup, err := l.takeStaged(d.Files)
	if err != nil {
		return client.ScheduledSend{}, err
	}
	defer cleanup()
	if d.Files != nil && files == nil {
		files = []client.OutgoingFile{}
	}
	if d.ID != "" && !protocol.ValidID(d.ID) || what != "create" && (!protocol.ValidID(d.ID) || d.Rev < 1) || what == "create" && d.Rev != 0 {
		return client.ScheduledSend{}, Refuse("Choose the exact scheduled message and current revision.")
	}
	in := client.ScheduledSendInput{ID: d.ID, Conv: d.Conv, Topic: d.Topic, Body: d.Body, ReplyTo: d.ReplyTo, Quote: d.Quote, Due: d.Due, Timezone: d.Timezone, Files: files, Audience: d.Audience}
	var row client.ScheduledSend
	switch what {
	case "create":
		row, err = l.a.ScheduleSend(in)
	case "edit":
		row, err = l.a.EditScheduledSend(d.ID, d.Rev, in)
	case "cancel":
		row, err = l.a.CancelScheduledSend(d.ID, d.Rev)
	case "send_now":
		row, err = l.a.SendScheduledNow(ctx, d.ID, d.Rev)
	default:
		return row, NotFound("That schedule action is not available.")
	}
	return row, scheduleRefusal(err)
}
func (s *Server) schedules(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Schedules)
	if !ok {
		writeErr(w, NotFound("Scheduled sending is not available here."))
		return
	}
	q := r.URL.Query()
	for k := range q {
		if k != "id" && k != "conv" && k != "all" || len(q[k]) != 1 {
			writeErr(w, Refuse("Invalid schedule query."))
			return
		}
	}
	id, conv, all := q.Get("id"), q.Get("conv"), q.Get("all")
	if id != "" {
		if !protocol.ValidID(id) || conv != "" || all != "" {
			writeErr(w, Refuse("Choose an exact scheduled message."))
			return
		}
		row, err := p.ScheduledMessage(id)
		writeResult(w, row, err)
		return
	}
	if conv != "" && !protocol.ValidHash(conv) || all != "" && all != "0" && all != "1" {
		writeErr(w, Refuse("Invalid schedule query."))
		return
	}
	rows, err := p.ScheduledMessages(conv, all == "1")
	writeResult(w, rows, err)
}
func (s *Server) scheduleAudience(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Schedules)
	if !ok {
		writeErr(w, NotFound("Scheduled sending is not available here."))
		return
	}
	q := r.URL.Query()
	for k := range q {
		if k != "conv" && k != "topic" || len(q[k]) != 1 {
			writeErr(w, Refuse("Invalid schedule audience query."))
			return
		}
	}
	conv, topic := q.Get("conv"), q.Get("topic")
	if !protocol.ValidHash(conv) || topic != "" && !protocol.ValidID(topic) {
		writeErr(w, Refuse("Choose an existing chat and topic."))
		return
	}
	row, err := p.ScheduleAudience(conv, topic)
	writeResult(w, row, err)
}
func (s *Server) changeSchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Schedules)
	if !ok {
		writeErr(w, NotFound("Scheduled sending is not available here."))
		return
	}
	what := r.PathValue("what")
	var d ScheduleDraft
	switch what {
	case "create", "edit":
		if !readJSON(w, r, &d) {
			return
		}
	case "cancel", "send_now":
		var a ScheduleAction
		if !readJSON(w, r, &a) {
			return
		}
		d.ID, d.Rev = a.ID, a.Rev
	default:
		writeErr(w, NotFound("That schedule action is not available."))
		return
	}
	row, err := p.ChangeSchedule(r.Context(), what, d)
	writeResult(w, row, err)
}
