//go:build !windows

package main

import (
	"context"
	"errors"
)

func runNSISInstaller(_ context.Context, _, _ string) error {
	return errors.New("Windows installer is unavailable on this computer")
}
