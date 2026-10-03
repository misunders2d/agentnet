package ui

import (
	"time"
)

// selfConsentReview adds to the overview's review items the notices of
// this person's own agents that joined without their accept (owner
// decision D3; client/selfconsent.go): notices, never decisions; resolve
// with the item's ID (the participation's id) dismisses one.
func (l *Live) selfConsentReview(o *Overview) error {
	notices, err := l.a.SelfConsentNotices()
	if err != nil || len(notices) == 0 {
		return err
	}
	labels := map[string]string{}
	if agents, err := l.a.LocalAgents(); err == nil {
		for _, e := range agents {
			labels[e.Record.ID] = e.Record.Label
		}
	}
	for _, n := range notices {
		agent := "Your default agent"
		if n.AgentID != "" {
			agent = "Your agent " + n.AgentID
			if label := labels[n.AgentID]; label != "" {
				agent = "Your agent " + label
			}
		}
		o.Review = append(o.Review, ReviewItem{ID: n.PID, Peer: n.Inviter, Why: "Joined without your accept: your own device " + n.Inviter + " invited it",
			Excerpt: agent, At: time.Unix(n.At, 0), Notice: true, Reason: ReasonSelfConsented, Conv: n.Conv, AgentID: n.AgentID})
	}
	return nil
}
