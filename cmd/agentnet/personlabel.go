package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/misunders2d/agentnet/internal/client"
)

// runPersonLabel is dispatched by person rename; labels never rename addresses.
func runPersonLabel(ctx context.Context, a *client.Agent, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: agentnet person rename LABEL")
	}
	p, err := a.RenamePerson(ctx, args[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Display label: %s\nPerson ID: %s\nAddress unchanged: %s\n", p.Label, p.Person, a.Address)
	return err
}
