package hub

import (
	"archive/tar"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/gdrive"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func (m *Maintenance) attachmentOAuthPath() (string, error) {
	var path string
	err := m.store.db.QueryRow(`SELECT oauth_path FROM relay_attachment_drive WHERE id=1`).Scan(&path)
	if path == "" {
		path = filepath.Join(m.dir, AttachmentDriveOAuthFile)
	}
	if err == nil {
		path, err = filepath.Abs(path)
	}
	return path, err
}
func (m *Maintenance) attachmentClient() (*gdrive.Client, string, error) {
	path, err := m.attachmentOAuthPath()
	if err != nil {
		return nil, "", err
	}
	oauth, configured, err := readAttachmentOAuth(path, m.attachmentHTTP)
	if err != nil || !configured {
		return nil, "", errors.New("relay attachment OAuth configuration is required to read or clean up Drive ciphertext")
	}
	var realm string
	if err = m.store.db.QueryRow(`SELECT realm_id FROM realm WHERE id=1`).Scan(&realm); err != nil {
		return nil, "", err
	}
	var mu sync.Mutex
	client := &gdrive.Client{HTTP: m.attachmentHTTP}
	client.Token = func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		c, err := m.store.relayDriveConnection(ctx)
		if err != nil || c.Token.Refresh == "" || c.ClientID != oauth.ClientID {
			return "", gdrive.ErrConsent
		}
		if c.Token.Expiry.After(time.Now().Add(time.Minute)) && c.Token.Access != "" {
			return c.Token.Access, nil
		}
		c.Token, err = oauth.Refresh(ctx, c.Token)
		if err == nil {
			err = m.store.saveRelayDriveConnection(ctx, c)
		}
		if err != nil {
			return "", err
		}
		return c.Token.Access, nil
	}
	return client, realm, nil
}

type remoteBackupBlob struct {
	id, fileID, sha string
	size            int64
}

func (m *Maintenance) remoteBackupPage(after string) ([]remoteBackupBlob, error) {
	rows, err := m.store.db.Query(`SELECT b.id,d.file_id,b.sha256,b.size FROM blobs b JOIN relay_drive_blobs d ON d.blob_id=b.id WHERE b.state=? AND d.state='remote' AND b.id>? ORDER BY b.id LIMIT 100`, protocol.BlobStored, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []remoteBackupBlob
	for rows.Next() {
		var b remoteBackupBlob
		if err := rows.Scan(&b.id, &b.fileID, &b.sha, &b.size); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// backupRemoteAttachments adds full verified ciphertext in the existing tar
// layout. Restoring the archive needs no network, OAuth configuration or Drive.
func (m *Maintenance) backupRemoteAttachments(tw *tar.Writer) error {
	var client *gdrive.Client
	var realm, after string
	for {
		page, err := m.remoteBackupPage(after)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, b := range page {
			after = b.id
			if _, err := os.Stat(filepath.Join(m.dir, "blobs", b.id+".blob")); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if client == nil {
				client, realm, err = m.attachmentClient()
				if err != nil {
					return err
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			err = func() error {
				remote, err := client.OpenCiphertext(ctx, b.fileID, realm, b.id, b.sha, b.size)
				if err != nil {
					return err
				}
				defer remote.Close()
				if err := tw.WriteHeader(&tar.Header{Name: "blobs/" + b.id + ".blob", Mode: 0o600, Size: b.size}); err != nil {
					return err
				}
				return copyVerifiedCiphertext(tw, remote, b.size, b.sha)
			}()
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

func (m *Maintenance) cleanupRemoteAttachment(id string) error {
	var remoteID string
	err := m.store.db.QueryRow(`SELECT file_id FROM relay_drive_blobs WHERE blob_id=?`, id).Scan(&remoteID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && remoteID == "" {
		return nil
	}
	if err != nil {
		return err
	}
	b, err := m.store.blob(id)
	if err != nil {
		return err
	}
	client, realm, err := m.attachmentClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return client.DeleteCiphertext(ctx, remoteID, realm, id, b.SHA256, b.Size)
}
