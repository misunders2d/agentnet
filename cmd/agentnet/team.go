package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const teamHelp = `team list | create NAME | join ID | leave ID | rename ID NAME | archive ID | restore ID | remove ID PERSON | manager-add ID PERSON | manager-remove ID PERSON | snapshot ID...
Teams are workspace directory collections. Self-join/leave several active teams; membership grants no conversation history or execution permission.
Creators are managers and members. Managers rename/archive/restore, remove non-manager members, and explicitly add/remove manager roles. Add another current member as manager before removing the last manager; archived teams retain a manager for restore. Remove a manager role explicitly before leaving/removing membership. Removing membership is not a ban.
snapshot returns a current deduplicated invitation list with source versions and verified roster references. Review/remove people and choose earlier history before a separate conversation invitation; this command invites nobody.`

func runTeam(ctx context.Context, a *client.Agent, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: " + teamHelp)
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("usage: team list")
		}
		v, err := a.Teams(ctx)
		for _, t := range v.Teams {
			role := ""
			if t.Member {
				role = " member"
			}
			if t.Manager {
				role = " manager"
			}
			if t.Archived {
				role += " archived"
			}
			if t.Conflict {
				role += " conflict"
			}
			fmt.Fprintf(stdout, "%s  %s  %d members%s\n", t.ID, termText(t.Name, ""), len(t.Members), role)
		}
		if !v.Current {
			fmt.Fprintln(stderr, v.Reason)
		}
		if v.Truncated {
			fmt.Fprintln(stderr, "team directory is incomplete")
		}
		return err
	case "snapshot":
		if len(args) < 2 {
			return errors.New("usage: team snapshot ID...")
		}
		v, err := a.TeamSnapshot(ctx, args[1:])
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(v)
	}
	c, err := parseTeamChange(args)
	if err != nil {
		return err
	}
	v, err := a.ChangeTeam(ctx, c)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s  %s  %d members  step %d\n", v.ID, termText(v.Name, ""), len(v.Members), v.Seq)
	return nil
}

func parseTeamChange(args []string) (client.TeamChange, error) {
	var c client.TeamChange
	if len(args) == 0 {
		return c, errors.New("usage: " + teamHelp)
	}
	c.Op = args[0]
	switch c.Op {
	case protocol.TeamCreate:
		if len(args) < 2 {
			return c, errors.New("usage: team create NAME")
		}
		c.Name = strings.Join(args[1:], " ")
	case protocol.TeamRename:
		if len(args) < 3 {
			return c, errors.New("usage: team rename ID NAME")
		}
		c.Team = args[1]
		c.Name = strings.Join(args[2:], " ")
	case protocol.TeamJoin, protocol.TeamLeave, protocol.TeamArchive, protocol.TeamRestore:
		if len(args) != 2 {
			return c, errors.New("usage: team " + c.Op + " ID")
		}
		c.Team = args[1]
	case protocol.TeamRemove, protocol.TeamManagerAdd, protocol.TeamManagerRemove:
		if len(args) != 3 {
			return c, errors.New("usage: team " + c.Op + " ID PERSON")
		}
		c.Team, c.Target = args[1], args[2]
		if !protocol.ValidID(c.Target) {
			return c, errors.New("target must be a stable person ID, not a display name")
		}
	default:
		return c, errors.New("usage: " + teamHelp)
	}
	if c.Team != "" && !protocol.ValidID(c.Team) {
		return c, errors.New("team must be a stable ID, not a display name")
	}
	return c, nil
}
