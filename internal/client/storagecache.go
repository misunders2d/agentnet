package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

type StorageCacheFile struct {
	ID     string `json:"id"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type StorageCacheSelection struct {
	Files []StorageCacheFile `json:"files"`
	Usage StorageAmount      `json:"usage"`
}
type StorageCacheClear struct {
	Files   []StorageCacheFile `json:"selection"`
	Confirm bool               `json:"confirm"`
}
type StorageCacheResult struct {
	Removed  StorageAmount `json:"removed"`
	Retained StorageAmount `json:"retained"`
}

// A preview selects complete received ciphertext only. Outgoing, failed,
// staged, retained-sent, direct and history-only copies are never candidates.
func (a *Agent) StorageCache(ctx context.Context) (StorageCacheSelection, error) {
	v := StorageCacheSelection{Files: []StorageCacheFile{}}
	rows, err := a.store.db.QueryContext(ctx, `SELECT DISTINCT a.blob_id,a.ct_size,a.ct_sha256 FROM attachments a
 JOIN inbox i ON i.id=a.message_id WHERE i.local=0
 AND NOT EXISTS(SELECT 1 FROM direct_blobs d WHERE d.id=a.blob_id)
 AND NOT EXISTS(SELECT 1 FROM uploads u WHERE u.blob_id=a.blob_id)`)
	if err != nil {
		return v, err
	}
	var candidates []StorageCacheFile
	for rows.Next() {
		var f StorageCacheFile
		if err = rows.Scan(&f.ID, &f.Size, &f.SHA256); err != nil {
			rows.Close()
			return v, err
		}
		if protocol.ValidID(f.ID) && f.Size > 0 && len(f.SHA256) == 64 {
			candidates = append(candidates, f)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	root, err := a.storageDownloads()
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	defer root.Close()
	for _, f := range candidates {
		if err = ctx.Err(); err != nil {
			return v, err
		}
		if _, err := statStorageCache(root, f); err != nil {
			continue
		}
		v.Files = append(v.Files, f)
		v.Usage.Files++
		v.Usage.Bytes += f.Size
		if len(v.Files) == 200 {
			break
		} // an explicit bounded batch, never an unbounded cleanup
	}
	return v, nil
}

func (a *Agent) storageDownloads() (*os.Root, error) {
	home, err := os.OpenRoot(a.home)
	if err != nil {
		return nil, err
	}
	defer home.Close()
	info, err := home.Lstat("downloads")
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("managed downloads must be a plain directory")
	}
	root, err := home.OpenRoot("downloads")
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("managed downloads changed")
	}
	return root, nil
}

func checkStorageCache(root *os.Root, f StorageCacheFile) (os.FileInfo, error) {
	info, err := statStorageCache(root, f)
	if err != nil {
		return nil, err
	}
	src, err := root.Open(f.ID + ".age")
	if err != nil {
		return nil, err
	}
	defer src.Close()
	opened, err := src.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("cached ciphertext changed")
	}
	hash := newCountingHash()
	_, err = io.Copy(hash, io.LimitReader(src, f.Size+1))
	if err != nil {
		return nil, err
	}
	if hash.n != f.Size || hash.hex() != f.SHA256 {
		return nil, errors.New("cached ciphertext digest changed")
	}
	return info, nil
}

func statStorageCache(root *os.Root, f StorageCacheFile) (os.FileInfo, error) {
	info, err := root.Lstat(f.ID + ".age")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != f.Size {
		return nil, errors.New("cached ciphertext changed")
	}
	return info, nil
}

// Delete only the exact reviewed files after a fresh, authorized full relay
// read proves another recoverable copy. Network/checksum failures retain them.
func (a *Agent) ClearStorageCache(ctx context.Context, change StorageCacheClear) (StorageCacheResult, error) {
	v := StorageCacheResult{}
	if !change.Confirm || len(change.Files) > 200 {
		return v, errors.New("confirm the reviewed received-cache selection (up to 200 files)")
	}
	selection, err := a.StorageCache(ctx)
	if err != nil {
		return v, err
	}
	allowed := map[StorageCacheFile]bool{}
	for _, f := range selection.Files {
		allowed[f] = true
	}
	root, err := a.storageDownloads()
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	defer root.Close()
	seen := map[string]bool{}
	for _, f := range change.Files {
		if !allowed[f] || seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		removed := false
		if ctx.Err() == nil {
			release, e := lockfile.Acquire(a.downloadPath(f.ID) + ".lock")
			if e == nil {
				info, e := checkStorageCache(root, f)
				if e == nil {
					hash := newCountingHash()
					for offset := int64(0); offset < f.Size && e == nil; {
						end := min(offset+downloadRange, f.Size)
						e = a.hub.getRange(ctx, "/v1/blobs/"+f.ID+"/data", offset, end, hash)
						offset = end
					}
					if e == nil && hash.n == f.Size && hash.hex() == f.SHA256 {
						now, e := root.Lstat(f.ID + ".age")
						if e == nil && os.SameFile(info, now) {
							// Keep automatic prefetch from undoing this explicit cleanup
							// after restart. Explicit opening/downloading still works.
							e = a.store.setConfig(map[string]string{"cache-cleared/" + f.ID: f.SHA256})
							if e == nil {
								removed = root.Remove(f.ID+".age") == nil
							}
						}
					}
				}
				release()
			}
		}
		if removed {
			v.Removed.Files++
			v.Removed.Bytes += f.Size
		} else {
			v.Retained.Files++
			v.Retained.Bytes += f.Size
		}
	}
	if v.Removed.Files > 0 {
		if err := secfile.SyncDir(filepath.Join(a.home, "downloads")); err != nil {
			return v, err
		}
		a.NoteChange()
	}
	return v, nil
}
