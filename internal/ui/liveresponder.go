package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

// The person's responder, seen and changed from the messenger page
// (MEL-428, MEL-498): the daemon's own view, since the daemon is the
// process that runs jobs, with the PATH it actually has. Nothing here runs
// a harness to find out anything.

// ResponderStatus implements ResponderControl.
func (l *Live) ResponderStatus() (ResponderView, error) {
	v := ResponderView{Harnesses: []HarnessView{}}
	for _, h := range client.ListHarnesses() {
		v.Harnesses = append(v.Harnesses, HarnessView{Name: h.Name, Found: h.Path != "", Path: h.Path, TestedLive: h.Tested, QuestionMode: h.Limits})
	}
	r, err := l.a.Responder()
	if err != nil {
		return v, Refuse(sentence(err))
	}
	if r == nil {
		chosen, err := l.a.ResponderChosen()
		if err != nil {
			return v, Refuse(sentence(err))
		}
		v.Chosen, v.Manual = chosen, chosen
		if !chosen {
			v.Problem = "Not chosen yet: questions and tasks wait for you until you choose a harness or manual handling."
		}
		return v, nil
	}
	v.Chosen, v.Harness, v.Dir, v.Context, v.Timeout = true, r.Harness, r.Dir, r.Context, int(r.Timeout.Seconds())
	v.Ready, v.Problem = responderReady(v.Harnesses, r)
	return v, nil
}

// responderReady says whether r can be started here: its harness is on the
// daemon's PATH and its directory exists. It never says it works.
func responderReady(hs []HarnessView, r *client.Responder) (bool, string) {
	found := false
	for _, h := range hs {
		if h.Name == r.Harness && h.Found {
			found = true
		}
	}
	if !found {
		return false, fmt.Sprintf("%s is not on the daemon's PATH: questions and tasks fail until it is installed where the daemon can find it.", r.Harness)
	}
	if info, err := os.Stat(r.Dir); err != nil || !info.IsDir() {
		return false, fmt.Sprintf("The responder directory %s does not exist.", r.Dir)
	}
	return true, ""
}

// SetResponder implements ResponderControl: manual handling, or the
// harness and directory, keeping the timeout and context files already
// chosen (the CLI edits those). It applies to the next job.
func (l *Live) SetResponder(c ResponderChange) (string, error) {
	if c.Manual {
		if err := l.a.SetResponder(nil); err != nil {
			return "", Refuse(sentence(err))
		}
		return "Manual handling: questions and tasks wait for you.", nil
	}
	c.Harness = strings.TrimSpace(c.Harness)
	if c.Harness == "" {
		return "", Refuse("Choose a harness, or manual handling.")
	}
	// Only a harness the daemon can find may be chosen: the choice is
	// refused with the reason, and the previous setting stays as it was.
	// Found on PATH is all this checks; it says nothing about login.
	found := false
	for _, h := range client.ListHarnesses() {
		if h.Name == c.Harness {
			found = h.Path != ""
		}
	}
	if !found {
		if _, ok := client.Harnesses[c.Harness]; !ok {
			return "", Refuse(fmt.Sprintf("%q is not a supported responder (supported: %s).", c.Harness, strings.Join(client.HarnessNames(), ", ")))
		}
		return "", Refuse(fmt.Sprintf("%s is not on this daemon's PATH, so it cannot be chosen here; install it where the daemon can find it, or choose another harness. Your current setting is unchanged.", c.Harness))
	}
	current, err := l.a.Responder()
	if err != nil {
		return "", Refuse(sentence(err))
	}
	next := client.Responder{Harness: c.Harness, Dir: strings.TrimSpace(c.Dir)}
	if current != nil {
		next.Timeout, next.Context = current.Timeout, current.Context
		if next.Dir == "" {
			next.Dir = current.Dir
		}
	}
	if next.Dir == "" {
		return "", Refuse("Choose the directory the responder works in.")
	}
	if err := l.a.SetResponder(&next); err != nil {
		return "", Refuse(sentence(err))
	}
	return fmt.Sprintf("%s answers approved questions and runs accepted tasks from now on, in %s.", next.Harness, next.Dir), nil
}
