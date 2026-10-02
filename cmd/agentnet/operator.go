package main

import (
	"errors"
	"fmt"

	"github.com/misunders2d/agentnet/internal/client"
)

// runOperator manages who may decide this machine's waiting requests from
// their own messenger: a device named by its address, under its exact
// pinned key. Local only; nothing received can grant it. Granted operators
// also receive this machine's review reports with the requests named.
func runOperator(a *client.Agent, args []string) error {
	usage := errors.New("usage: operator grant ADDRESS | list | revoke ADDRESS")
	if len(args) == 0 {
		return usage
	}
	switch args[0] {
	case "grant":
		if len(args) != 2 {
			return usage
		}
		fp, err := a.GrantOperator(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("%s may decide requests waiting here from its messenger (accept, decline, reply, resolve, stop), under key %s; it receives this machine's reports with the requests named. Revoke: agentnet operator revoke %s\n", args[1], fp, args[1])
		return nil
	case "revoke":
		if len(args) != 2 {
			return usage
		}
		if err := a.RevokeOperator(args[1]); err != nil {
			return err
		}
		fmt.Printf("%s no longer decides here; decisions already applied stay applied\n", args[1])
		return nil
	case "list":
		grants, err := a.Operators()
		if err != nil {
			return err
		}
		if len(grants) == 0 {
			fmt.Println("no operators: requests waiting here are decided on this machine (agentnet inbox --review)")
			return nil
		}
		for _, g := range grants {
			fmt.Printf("%s  key %s  %s\n", g.Address, g.Fingerprint, g.Status)
		}
		return nil
	}
	return usage
}
