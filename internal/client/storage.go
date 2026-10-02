package client

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// StorageSummary is one explicit read, not a cache or a daemon scan. Local
// counts exclude keys, databases and files saved to a person's chosen folder.
type StorageSummary struct {
	Local  LocalStorage  `json:"local"`
	Remote RemoteStorage `json:"remote"`
}

type StorageAmount struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

type StorageArea struct {
	Directory string         `json:"directory"` // one relative managed directory
	Label     string         `json:"label"`
	Kind      string         `json:"kind"`             // plaintext or ciphertext
	Status    string         `json:"status"`           // available or unavailable
	Usage     *StorageAmount `json:"usage,omitempty"`  // absent when scan failed
	Reason    string         `json:"reason,omitempty"` // path-free
	Lifetime  string         `json:"lifetime"`
}

type LocalStorage struct {
	Scope      string        `json:"scope"`
	Location   string        `json:"location"`
	Areas      []StorageArea `json:"areas"`
	Known      StorageAmount `json:"known"` // successful areas only; never an unknown total
	Complete   bool          `json:"complete"`
	Exclusions string        `json:"exclusions"`
}

type RemoteStorage struct {
	Status string               `json:"status"` // available, unsupported, unavailable
	Reason string               `json:"reason,omitempty"`
	Usage  *protocol.HubStorage `json:"usage,omitempty"` // unknown when absent
}

// Storage reads only AgentNet's five flat managed directories and asks the
// authenticated Hub endpoint once. Remote failure never hides local counts.
// Authentication has its normal nonce effects, but no storage is cleaned.
func (a *Agent) Storage(ctx context.Context) (StorageSummary, error) {
	view := StorageSummary{Local: a.localStorage(), Remote: RemoteStorage{Status: "unavailable"}}
	var remote protocol.HubStorage
	err := a.hub.do(ctx, http.MethodGet, "/v1/storage", nil, &remote)
	var he *HubError
	switch {
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		view.Remote.Status, view.Remote.Reason = "unsupported", "This Hub does not report storage usage."
	case err != nil:
		view.Remote.Reason = "Storage usage could not be verified with the Hub."
	case remote.Scope != "caller-owned-ciphertext" || remote.QuotaScope != "hub-global" || remote.QuotaBytes <= 0 || remote.MaxFileBytes <= 0 || remote.UploadIdleTTLSeconds <= 0:
		view.Remote.Reason = "The Hub's storage summary was not understood; its usage and policy remain unknown."
	default:
		view.Remote.Status, view.Remote.Usage = "available", &remote
	}
	return view, nil
}

func (a *Agent) localStorage() LocalStorage {
	view := LocalStorage{
		Scope: "agent-home-managed-files", Location: "This installation's AgentNet home", Complete: true,
		Exclusions: "Keys, databases, backups, nested directories, symlinks and files saved to user-selected folders are not counted. Counts are file lengths, not allocated disk blocks.",
		Areas: []StorageArea{
			{Directory: "staging", Label: "Files waiting to be sent", Kind: "plaintext", Lifetime: "Removed after send/discard and at page/daemon startup; a page also checks its one-hour staging expiry when another file is staged, not on a timer."},
			{Directory: "spool", Label: "Outgoing transfer spool", Kind: "ciphertext", Lifetime: "Released after custody/delivery; queued transfers stay for retry. Failed or abandoned copies can be removed by explicit cleanup."},
			{Directory: "kept", Label: "Retained sent files", Kind: "ciphertext", Lifetime: "Retained for history and other devices with no automatic expiry. Explicit message deletion removes eligible retained copies; copies shared by another sent message are preserved."},
			{Directory: "downloads", Label: "Retained received files and incoming transfers", Kind: "ciphertext", Lifetime: "Fetched ciphertext stays after saving with no general automatic expiry. Verified message deletion removes eligible cached copies; files saved to user-selected folders remain. Unfinished direct transfers have opportunistic reclaim, and explicit cleanup can remove eligible direct copies."},
			{Directory: "opened", Label: "Temporary opened files", Kind: "plaintext", Lifetime: "Removed when the opened file closes; leftovers are removed at daemon startup."},
		},
	}
	root, err := os.OpenRoot(a.home)
	if err == nil {
		defer root.Close()
	}
	for i := range view.Areas {
		area := &view.Areas[i]
		if err != nil {
			area.Status, area.Reason = "unavailable", "The managed storage home could not be inspected."
		} else {
			area.Usage, area.Reason = scanStorageArea(root, area.Directory)
			area.Status = "available"
			if area.Usage == nil {
				area.Status = "unavailable"
			}
		}
		if area.Usage == nil {
			view.Complete = false
		} else {
			view.Known.Files += area.Usage.Files
			view.Known.Bytes += area.Usage.Bytes
		}
	}
	return view
}

// The rooted handle confines lookup to the managed home. Anchoring each plain
// directory before listing also prevents a rename/symlink swap from causing
// entry lookups in a different directory. No contents or symlink targets read.
func scanStorageArea(home *os.Root, name string) (*StorageAmount, string) {
	info, err := home.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return &StorageAmount{}, ""
	}
	if err != nil {
		return nil, "The managed directory could not be inspected."
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "The managed directory is not a plain directory; it was not followed."
	}
	root, err := home.OpenRoot(name)
	if err != nil {
		return nil, "The managed directory could not be opened."
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, "The managed directory changed during inspection."
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, "The managed directory could not be listed."
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, "The managed directory could not be listed."
	}
	var usage StorageAmount
	for _, entry := range entries {
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return nil, "A managed entry could not be inspected; this directory's total is unknown."
		}
		if info.Mode().IsRegular() {
			usage.Files++
			usage.Bytes += info.Size()
		}
	}
	return &usage, ""
}
