package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
)

// ConversationDeletion deletes a conversation (all of this person's
// devices), one device topic, or a whole device chat (every topic of its
// row) — each on all of this person's devices; others keep their copies.
type ConversationDeletion interface {
	DeleteConversation(DeleteConversationAction) (string, error)
}

// DeleteConversationAction names what to delete: a conversation by id, or
// a device thread by its peer and its earliest message (ThreadSummary.ID).
// With Chat, the whole device chat that thread is in: every topic of its
// chat-list row, archived ones too (one topic is archived, not deleted).
type DeleteConversationAction struct {
	Conv   string `json:"conv,omitempty"`
	Peer   string `json:"peer,omitempty"`
	Thread string `json:"thread,omitempty"`
	Chat   bool   `json:"chat,omitempty"`
}

// deleteConversation implements POST /api/conversation/delete.
func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(ConversationDeletion)
	if !ok {
		writeErr(w, NotFound("deleting conversations is not available here"))
		return
	}
	var v DeleteConversationAction
	if !readJSON(w, r, &v) {
		return
	}
	note, err := p.DeleteConversation(v)
	writeResult(w, map[string]string{"note": note}, err)
}

// DeleteConversation implements ConversationDeletion.
func (l *Live) DeleteConversation(x DeleteConversationAction) (string, error) {
	var done client.ConversationDeleted
	var err error
	switch {
	case x.Conv != "" && x.Peer == "" && x.Thread == "" && !x.Chat:
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
		defer cancel()
		done, err = l.a.DeleteConversation(ctx, x.Conv)
	case x.Conv == "" && x.Peer != "" && x.Thread != "" && x.Chat:
		done, err = l.a.DeleteDeviceChat(x.Peer, x.Thread)
	case x.Conv == "" && x.Peer != "" && x.Thread != "":
		done, err = l.a.DeleteThread(x.Peer, x.Thread)
	default:
		return "", Refuse("Name one conversation, or one thread and its peer.")
	}
	switch {
	case errors.Is(err, client.ErrNoConversation), errors.Is(err, client.ErrNoMessage):
		return "", NotFound("no such conversation here")
	case errors.Is(err, client.ErrNothingToDelete):
		return "", Refuse("There is nothing here to delete.")
	case err != nil:
		return "", Refuse(sentence(err))
	}
	l.a.NoteChange()
	var note string
	switch {
	case x.Conv == "": // a device topic or chat (client DeleteThread, DeleteDeviceChat)
		if done.Devices == 0 {
			note = "Deleted on all your devices: no other device of yours is linked now, and one you link later gets the deletion."
		} else {
			note = fmt.Sprintf("Deleted on all your devices: here now; your %d other device(s) remove it once each is connected and runs an AgentNet version that applies it, and until then still show it.", done.Devices)
		}
		if keeper, kerr := l.a.DeviceChatKeeper(x.Peer); kerr == nil && keeper != "" {
			note += " " + keeper
		}
	case done.Devices == 0:
		note = "Deleted here. No other device of yours was linked; one you link later gets the deletion. Others in it keep their copies."
	default:
		note = fmt.Sprintf("Deleted here. Queued for your %d other device(s): each removes it once it is connected and runs an AgentNet version that applies deletions; until then it still shows it there. Others in it keep their copies.", done.Devices)
	}
	if done.Kept > 0 {
		note += fmt.Sprintf(" %d item(s) still in progress keep running and are removed when they finish.", done.Kept)
	}
	return note, nil
}
