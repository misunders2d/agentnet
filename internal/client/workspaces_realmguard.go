package client

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// WorkspaceRequestGuard returns one gate for this Agent's verified connection.
// Open installs it on hubConn.request; stream reconnect resets its successful
// check. Version requests bypass it, so CheckRealm cannot recurse. Offline
// local DB operations are unaffected and no periodic polling is introduced.
func (a *Agent) WorkspaceRequestGuard() func(context.Context, string) error {
	var mu sync.Mutex
	checked := false
	var terminal error
	return func(ctx context.Context, path string) error {
		endpoint := strings.SplitN(path, "?", 2)[0]
		if endpoint == "/v1/version" {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if terminal != nil {
			return terminal
		}
		if endpoint == "/v1/stream" {
			checked = false
		}
		if checked {
			return nil
		}
		pinned, pinErr := a.RealmID()
		if pinErr != nil && !errors.Is(pinErr, ErrRealmUnknown) {
			terminal = pinErr
			return terminal
		}
		_, err := a.CheckRealm(ctx)
		if errors.Is(err, ErrRealmUnsupported) && pinned == "" && errors.Is(pinErr, ErrRealmUnknown) {
			checked = true
			return nil
		}
		if err != nil {
			var changed *RealmChangedError
			if errors.As(err, &changed) || errors.Is(err, ErrRealmInvalid) || errors.Is(err, ErrRealmUnsupported) && pinned != "" {
				terminal = err
			}
			return err
		}
		checked = true
		return nil
	}
}
