package ui

import (
	"context"
	"github.com/misunders2d/agentnet/internal/client"
	"strings"
)

// Deciding for another machine (OperatorDecisions): the page names the
// reported request exactly; the host applies the decision once and answers
// with a status the page shows on the report item (ReviewItem.Report).

// Decide implements OperatorDecisions.
func (l *Live) Decide(x DecisionAction) (string, error) {
	x.Action = strings.TrimSpace(x.Action)
	if (x.Action == "reply" || x.Action == "decline" || x.Action == "continue") && strings.TrimSpace(x.Text) == "" {
		return "", Refuse("Write the text first.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	if x.Action == "continue" || x.Action == "resolve" && x.Report == "" {
		ctx = client.WithQueuedSend(ctx, x.SendID)
	}
	sent, err := l.a.Decide(ctx, x.Host, x.ID, x.Key, x.Action, x.Expect, x.Attempt, x.Text, x.Report)
	if err != nil {
		return "", Refuse(sentence(err))
	}
	l.a.NoteChange()
	return "Sent to " + x.Host + " (" + sent.State + "). It applies your decision once, if the request is still as you saw it, and answers here.", nil
}
