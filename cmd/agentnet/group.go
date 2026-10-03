package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const groupHelp = `group create TITLE | invite [--history-last N | --history-since RFC3339 | --history-refs FILE] CONV PERSON | invitations | accept ID | decline ID | retry ID | request-file CONV MESSAGE_ID INDEX | rename CONV TITLE | promote CONV PERSON | demote CONV PERSON | remove CONV PERSON | leave CONV
create starts a group with only this person as member and administrator.
invite records a local intent; its verified proposal grants no membership or execution. Earlier history defaults to nothing. Select at most64 actual visible turns with --history-last, --history-since, or exact {lid,author,hash} --history-refs. Their text and file manifests are delivered only after consent and membership publication; selected files are requested from the forwarding device. Changed/deleted selection refuses fresh publication; already committed membership can report history unavailable. Nothing substitutes other content.
invitations shows verified pending proposals and their exact history refs. accept/decline is an explicit human decision. Accepted exact consent is published automatically by the inviter; stale membership requires a fresh invitation and fresh consent. When the group changed before an invitation was published (for example someone else joined first), the inviter's daemon sends it once more at the current state, to the same person with the same history (never after a decline): the older one shows stale, and the newer one needs a fresh accept. retry recovers an already accepted local intent without changing its signature.
request-file explicitly queues one selected history file (zero-based INDEX) from its forwarding device; it does not claim download. After its offer arrives, agentnet download saves and checks the returned bytes. A file no source holds is unavailable.
rename/promote/demote/remove publish one exact current administrator CAS. PERSON is an exact person ID. Last administrator must promote a successor before demoting, removing, or leaving. Administrator leave uses explicit self-removal CAS when a successor already exists. Ordinary leave records local departure plus encrypted quiet fanout atomically, including while offline; queued does not mean peers received it. No automatic transfer, election or dissolution.`

func runGroup(ctx context.Context, a *client.Agent, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: " + groupHelp)
	}
	encode := func(value any, err error) error {
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(value)
	}
	switch args[0] {
	case "rename", "promote", "demote", "remove":
		if len(args) < 3 || args[0] != "rename" && len(args) != 3 {
			return fmt.Errorf("usage: group %s CONV %s", args[0], map[bool]string{true: "TITLE", false: "PERSON"}[args[0] == "rename"])
		}
		var packet client.GroupContext
		var err error
		switch args[0] {
		case "rename":
			packet, err = a.RenameGroup(ctx, args[1], strings.Join(args[2:], " "))
		case "promote":
			packet, err = a.PromoteGroupMember(ctx, args[1], args[2])
		case "demote":
			packet, err = a.DemoteGroupMember(ctx, args[1], args[2])
		case "remove":
			packet, err = a.RemoveGroupMember(ctx, args[1], args[2])
		}
		return encode(packet, err)
	case "leave":
		if len(args) != 2 {
			return errors.New("usage: group leave CONV")
		}
		result, err := a.LeaveGroup(ctx, args[1])
		return encode(result, err)
	case "request-file":
		if len(args) != 4 {
			return errors.New("usage: group request-file CONV MESSAGE_ID INDEX")
		}
		index, err := strconv.Atoi(args[3])
		if err != nil || index < 0 || !protocol.ValidID(args[2]) {
			return errors.New("group: invalid message/index")
		}
		if _, err = a.GroupContext(args[1]); err != nil {
			return err
		}
		messages, err := a.ConversationMessages(args[1])
		if err != nil {
			return err
		}
		var selected *client.ConvMessage
		matches := 0
		for i := range messages {
			if messages[i].ID == args[2] {
				matches++
				selected = &messages[i]
			}
		}
		if matches != 1 || selected == nil || !selected.History || selected.SyncedFrom == "" || index >= len(selected.Attachments) {
			return errors.New("group: no exact scoped received history file")
		}
		if !strings.HasPrefix(selected.Attachments[index].BlobID, "history-") {
			fmt.Fprintln(stdout, "file offer available; use agentnet download to save and verify bytes")
			return nil
		}
		if err = a.RequestFile(ctx, args[2], index); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "requested file %d of %s; queued until the forwarding device returns an offer\n", index, args[2])
		return nil
	case "create":
		fs := flag.NewFlagSet("group create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return errors.New("usage: group create TITLE")
		}
		packet, err := a.CreateGroup(ctx, strings.Join(fs.Args(), " "))
		return encode(packet, err)
	case "invite":
		fs := flag.NewFlagSet("group invite", flag.ContinueOnError)
		fs.SetOutput(stderr)
		file := fs.String("history-refs", "", "JSON array of explicitly selected history refs (max64)")
		last := fs.Int("history-last", 0, "last N visible group turns (max64)")
		since := fs.String("history-since", "", "visible group turns since RFC3339 (max64)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 2 {
			return errors.New("usage: group invite [--history-last N | --history-since RFC3339 | --history-refs FILE] CONV PERSON")
		}
		modes := 0
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "history-refs" || f.Name == "history-last" || f.Name == "history-since" {
				modes++
			}
		})
		if modes > 1 {
			return errors.New("group: choose exactly one history selector")
		}
		var refs []protocol.GroupHistoryRef
		if *file != "" {
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
			if err != nil {
				return err
			}
			if len(data) > 64<<10 {
				return errors.New("group: history refs file exceeds bound")
			}
			dec := json.NewDecoder(strings.NewReader(string(data)))
			dec.DisallowUnknownFields()
			if err = dec.Decode(&refs); err != nil {
				return err
			}
			if dec.Decode(new(any)) != io.EOF {
				return errors.New("group: extra history refs data")
			}
		}
		selection := client.GroupHistorySelection{Last: *last, Refs: refs}
		if *since != "" {
			at, err := time.Parse(time.RFC3339Nano, *since)
			if err != nil {
				return errors.New("group: --history-since requires RFC3339")
			}
			selection.Since = at.UnixMilli()
		}
		refs, err := a.SelectGroupHistory(ctx, fs.Arg(0), selection)
		if err != nil {
			return err
		}
		proposal, err := a.InviteGroup(ctx, fs.Arg(0), fs.Arg(1), refs)
		return encode(proposal, err)
	case "invitations":
		if len(args) != 1 {
			return errors.New("usage: group invitations")
		}
		list, err := a.GroupInvitations()
		return encode(list, err)
	case "accept", "decline", "retry":
		if len(args) != 2 || !protocol.ValidHash(args[1]) {
			return fmt.Errorf("usage: group %s ID", args[0])
		}
		var err error
		if args[0] == "retry" {
			err = a.PublishGroupInvitation(ctx, args[1])
		} else {
			err = a.DecideGroupInvitation(ctx, args[1], args[0] == "accept")
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %s\n", args[0], args[1])
		return nil
	default:
		return errors.New("usage: " + groupHelp)
	}
}
