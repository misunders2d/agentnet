package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/misunders2d/agentnet/internal/client"
)

// runOperator manages who may decide this machine's waiting requests from
// their own messenger: a device named by its address, under its exact
// pinned key, or a person (a steward, --person): every current and future
// device of that person, as its signed device list names it. Local only;
// nothing received can grant it. Operators also receive this machine's
// review reports with the requests named.
func runOperator(ctx context.Context, a *client.Agent, args []string, stdout io.Writer) error {
	usage := errors.New("usage: operator grant [--person] ADDRESS | list | revoke [--person] ADDRESS|PERSON")
	if len(args) == 0 {
		return usage
	}
	person := len(args) == 3 && args[1] == "--person"
	switch {
	case args[0] == "grant" && person:
		g, err := a.GrantOperatorPerson(ctx, args[2])
		if err != nil {
			return err
		}
		printSteward(stdout, g, "is a steward here: each of their devices decides requests waiting here from its messenger (accept, decline, reply, resolve, stop) and receives this machine's reports with the requests named, including devices they add later")
		fmt.Fprintf(stdout, "check that this is the person you mean; revoke: agentnet operator revoke --person %s\n", g.Person)
		return nil
	case args[0] == "grant" && len(args) == 2:
		fp, err := a.GrantOperator(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s may decide requests waiting here from its messenger (accept, decline, reply, resolve, stop), under key %s; it receives this machine's reports with the requests named. Revoke: agentnet operator revoke %s\n", args[1], fp, args[1])
		return nil
	case args[0] == "revoke" && person:
		g, err := a.RevokeOperatorPerson(args[2])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%q (%s) is no longer a steward here; decisions already applied stay applied\n", g.Label, g.Person)
		return nil
	case args[0] == "revoke" && len(args) == 2:
		if err := a.RevokeOperator(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s no longer decides here; decisions already applied stay applied\n", args[1])
		return nil
	case args[0] == "list" && len(args) == 1:
		grants, err := a.Operators()
		if err != nil {
			return err
		}
		stewards, err := a.Stewards()
		if err != nil {
			return err
		}
		if len(grants) == 0 && len(stewards) == 0 {
			fmt.Fprintln(stdout, "no operators: nobody decides requests waiting here from their own devices; name a steward: agentnet operator grant --person ADDRESS")
			return nil
		}
		for _, g := range stewards {
			status := "active"
			if g.State != "pinned" {
				status = "inactive: " + g.State
			}
			printSteward(stdout, g, "steward, "+status)
		}
		for _, g := range grants {
			fmt.Fprintf(stdout, "%s  key %s  %s\n", g.Address, g.Fingerprint, g.Status)
		}
		return nil
	}
	return usage
}

// printSteward prints a steward grant: the person, its label (its own
// claim) and the devices its roster lists now.
func printSteward(w io.Writer, g client.PersonGrant, what string) {
	fmt.Fprintf(w, "%q (person %s) %s\n", g.Label, g.Person, what)
	var devices []string
	for _, d := range g.Devices {
		devices = append(devices, d.Address+" "+d.Fingerprint)
	}
	if len(devices) > 0 {
		fmt.Fprintf(w, "  devices now: %s\n", strings.Join(devices, ", "))
	}
}
