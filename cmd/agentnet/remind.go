package main

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// runRemind sets, moves, lists and ends reminders (agentnet help remind).
func runRemind(a *client.Agent, args []string, out io.Writer, now time.Time) error {
	usage := errors.New("usage: remind ID WHEN | remind list [--all] | remind done ID | remind cancel ID (see agentnet help remind)")
	switch {
	case len(args) >= 1 && args[0] == "list":
		all := len(args) == 2 && args[1] == "--all"
		if len(args) > 2 || (len(args) == 2 && !all) {
			return usage
		}
		rs, err := a.Reminders(all)
		if err != nil {
			return err
		}
		if len(rs) == 0 {
			fmt.Fprintln(out, "no reminders")
		}
		for _, r := range rs {
			fmt.Fprintln(out, reminderLine(r, now))
		}
		return nil
	case len(args) == 2 && (args[0] == "done" || args[0] == "cancel"):
		end := a.DoneReminder
		if args[0] == "cancel" {
			end = a.CancelReminder
		}
		if err := end(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %s\n", args[1], map[string]string{"done": client.ReminderDone, "cancel": client.ReminderCancelled}[args[0]])
		return nil
	case len(args) >= 2 && !strings.HasPrefix(args[0], "-"):
		due, err := parseWhen(strings.Join(args[1:], " "), now)
		if err != nil {
			return err
		}
		r, err := a.SetReminder(args[0], due)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, reminderLine(r, now))
		return nil
	}
	return usage
}

func reminderLine(r client.Reminder, now time.Time) string {
	due := time.Unix(r.Due, 0)
	state := r.State
	switch {
	case r.Overdue:
		state = "overdue since " + due.Format("2006-01-02 15:04")
	case r.State == client.ReminderPending:
		state = "due " + due.Format("2006-01-02 15:04") + " (in " + due.Sub(now).Round(time.Minute).String() + ")"
	}
	where := "from " + r.From
	if r.Conv != "" {
		where += " in DM " + r.Conv
	}
	return fmt.Sprintf("%s  %s  %s", r.Message, state, where)
}

var clockTime = regexp.MustCompile(`^([01]?[0-9]|2[0-3]):([0-5][0-9])$`)

// parseWhen reads a reminder time, in local time: a duration from now
// (30m, 2h, 1h30m, 2d), a clock time today or else tomorrow (15:00), or a
// date and time (2026-09-30 09:00).
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if n, ok := strings.CutSuffix(s, "d"); ok {
		if days, err := strconv.Atoi(n); err == nil && days > 0 && days <= 366 {
			return now.AddDate(0, 0, days), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Time{}, errors.New("choose a time in the future")
		}
		return now.Add(d), nil
	}
	if m := clockTime.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		t := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, now.Location())
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, now.Location()); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("when? %q: use a duration (30m, 2h, 2d), a time (15:00) or a date and time (2026-09-30 09:00)", s)
}
