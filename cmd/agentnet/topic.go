package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const topicHelp = `topic list --peer ADDRESS [--state active|done|archived] [--before CURSOR]
topic show ID
topic preview|move --peer ADDRESS (--to TOPIC | --new TITLE) MESSAGE_ID...
topic merge --peer ADDRESS --to TOPIC SOURCE_TOPIC
topic apply REVIEW.json
topic undo --peer ADDRESS [--id RETRY_ID] OPERATION_ID

These commands return JSON. Direct agent-chat organization moves original
messages into an existing or named new topic without rewriting their reply links, files,
audiences or execution state. Only the selected messages move; later replies
keep their original placement. Organization follows your current linked human
devices after they update to a reader that supports it.

preview stores nothing. apply consumes its exact review; retain its operation
ID when retrying an uncertain result. move and merge preview and apply at once
for an already authorized request. merge selects the currently held source
messages, never later arrivals. undo refuses to overwrite a subsequent move.
No command accepts or reruns old work, or reads private native sessions.`

func runTopic(ctx context.Context, a *client.Agent, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(topicHelp)
	}
	encode := func(v any) error { enc := json.NewEncoder(out); enc.SetIndent("", "  "); return enc.Encode(v) }
	command := args[0]
	fs := flag.NewFlagSet("topic "+command, flag.ContinueOnError)
	peer := fs.String("peer", "", "exact agent chat address")
	to := fs.String("to", "", "existing destination topic")
	newTitle := fs.String("new", "", "name of a new destination topic")
	state := fs.String("state", "", "topic state")
	before := fs.String("before", "", "opaque topic page cursor")
	retryID := fs.String("id", "", "durable undo retry identity")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch command {
	case "list":
		if *peer == "" || fs.NArg() != 0 {
			return errors.New(topicHelp)
		}
		p, err := a.Topics(client.TopicQuery{Peer: *peer, State: *state, Before: *before, Limit: client.TopicPageMax})
		if err != nil {
			return err
		}
		return encode(p)
	case "show":
		if fs.NArg() != 1 {
			return errors.New(topicHelp)
		}
		v, err := a.DisplayConversation(fs.Arg(0), 0, 0)
		if err != nil {
			return err
		}
		return encode(v)
	case "preview", "move", "merge":
		if *peer == "" || (*to == "") == (strings.TrimSpace(*newTitle) == "") || fs.NArg() == 0 {
			return errors.New(topicHelp)
		}
		request := client.TopicOrganizationRequest{Peer: *peer, Topic: *to, IDs: fs.Args()}
		if *newTitle != "" {
			request.New, request.Title = true, *newTitle
		}
		if command == "merge" {
			if fs.NArg() != 1 {
				return errors.New(topicHelp)
			}
			source, err := a.TopicOf(fs.Arg(0))
			if err != nil {
				return err
			}
			if source.Peer != *peer || source.ID != fs.Arg(0) {
				return errors.New("choose the source topic in this exact agent chat")
			}
			v, err := a.DisplayConversation(source.ID, 0, 0)
			if err != nil {
				return err
			}
			request.Merge = source.ID
			request.IDs = nil
			for _, m := range v.Messages {
				request.IDs = append(request.IDs, m.ID)
			}
		}
		review, err := a.PreviewTopicOrganization(request)
		if err != nil {
			return err
		}
		if command == "preview" {
			return encode(review)
		}
		v, err := a.ApplyTopicOrganization(ctx, review)
		if err != nil {
			return err
		}
		return encode(v)
	case "apply":
		if fs.NArg() != 1 {
			return errors.New(topicHelp)
		}
		raw, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		if len(raw) > 65536 {
			return errors.New("topic review is too large")
		}
		var review client.TopicOrganizationReview
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&review); err != nil {
			return err
		}
		if review.Peer == "" {
			return errors.New("topic apply requires a direct agent-chat review")
		}
		var extra any
		if err = dec.Decode(&extra); err != io.EOF {
			return errors.New("topic review contains trailing data")
		}
		v, err := a.ApplyTopicOrganization(ctx, review)
		if err != nil {
			return err
		}
		return encode(v)
	case "undo":
		if *peer == "" || fs.NArg() != 1 {
			return errors.New(topicHelp)
		}
		id := *retryID
		if id == "" {
			id = protocol.NewID()
		}
		v, err := a.UndoTopicOrganization(ctx, client.TopicOrganizationUndo{Peer: *peer, LID: fs.Arg(0), ID: id})
		if err != nil {
			return err
		}
		return encode(v)
	}
	return errors.New(topicHelp)
}
