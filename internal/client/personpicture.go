package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// SetPersonPicture publishes bytes before the signed reference. Empty removes it.
func (a *Agent) SetPersonPicture(ctx context.Context, b []byte) (PersonInfo, error) {
	hash := ""
	if len(b) > 0 {
		if err := protocol.ValidatePicture(b); err != nil {
			return PersonInfo{}, err
		}
		hash = protocol.PictureHash(b)
		if err := a.hub.doBytes(ctx, "PUT", "/v1/pictures/"+hash, b, nil); err != nil {
			return PersonInfo{}, err
		}
	}
	return a.changePersonProfile(ctx, nil, &hash)
}

// PersonPicture checks the content hash even for the on-disk cache.
func (a *Agent) PersonPicture(ctx context.Context, hash string) ([]byte, error) {
	if !protocol.ValidHash(hash) {
		return nil, errors.New("invalid picture hash")
	}
	dir := filepath.Join(a.home, "pictures")
	path := filepath.Join(dir, hash+".png")
	good := func(b []byte) bool { return protocol.ValidatePicture(b) == nil && protocol.PictureHash(b) == hash }
	if b, err := os.ReadFile(path); err == nil && good(b) {
		return b, nil
	}
	ctx, cancel := context.WithTimeout(ctx, a.hub.timeout)
	defer cancel()
	req, err := a.hub.request(ctx, "GET", "/v1/pictures/"+hash, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.hub.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err = checkStatus(resp); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxPictureBytes+1))
	if err != nil {
		return nil, err
	}
	if !good(b) {
		return nil, errors.New("picture bytes do not match the signed hash")
	}
	if err = secfile.EnsureDir(dir); err == nil {
		err = secfile.Write(path, b)
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}
