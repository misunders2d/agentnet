package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

// Message controls from the page (MessageControls): the page names a
// message as it shows it; the client turns that into the exact reference
// (id or logical id plus the sender's verified key) and refuses what the
// keys do not allow.

func (l *Live) controlRef(x ControlAction) (client.ControlRef, error) {
	ref, err := l.a.RefOf(x.Conv, x.ID, x.Dir)
	if errors.Is(err, client.ErrNoMessage) {
		return ref, NotFound("no such message here")
	}
	if err != nil {
		return ref, Refuse(sentence(err))
	}
	return ref, nil
}

func controlNote(sent client.ControlSent, done string) string {
	if len(sent.Skipped) > 0 {
		return fmt.Sprintf("%s Not sent to %d device(s) that cannot read it yet: %s", done, len(sent.Skipped), strings.Join(sent.Skipped, "; "))
	}
	return done
}

// React implements MessageControls.
func (l *Live) React(x ControlAction) (string, error) {
	ref, err := l.controlRef(x)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	sent, err := l.a.React(ctx, ref, x.Emoji, x.Remove)
	if err != nil {
		return "", Refuse(sentence(err))
	}
	l.a.NoteChange()
	if x.Remove {
		return controlNote(sent, "Reaction removed."), nil
	}
	return controlNote(sent, "Reacted."), nil
}

// EditMessage implements MessageControls.
func (l *Live) EditMessage(x ControlAction) (string, error) {
	if strings.TrimSpace(x.Text) == "" {
		return "", Refuse("Write the new text first.")
	}
	ref, err := l.controlRef(x)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	sent, err := l.a.Revise(ctx, ref, x.Text)
	if err != nil {
		return "", Refuse(sentence(err))
	}
	l.a.NoteChange()
	return controlNote(sent, "Edited. A question or task already sent keeps running on what was sent; the edit is shown beside it."), nil
}

// DeleteMessage implements MessageControls.
func (l *Live) DeleteMessage(x ControlAction) (string, error) {
	ref, err := l.controlRef(x)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	sent, err := l.a.Retract(ctx, ref, "")
	if err != nil {
		return "", Refuse(sentence(err))
	}
	l.a.NoteChange()
	return controlNote(sent, "Deleted here and on devices that can read deletions. What was already read, saved or given to an agent stays with them; nothing running was stopped."), nil
}
