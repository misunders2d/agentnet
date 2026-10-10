package envelope

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// HistoryArchiveChunk retains each carrier's existing signed encryption.
// Parsing checks bounds and embedded ciphertext, never sender authority.
// Importers must still Open every child, bind its endpoints to the descriptor,
// validate its inert subtype and use the existing history admission checks.
type HistoryArchiveChunk struct {
	V       int                   `json:"v"`
	Entries []HistoryArchiveEntry `json:"entries"`
}

type HistoryArchiveEntry struct {
	Envelope Envelope             `json:"envelope"`
	Blobs    []HistoryArchiveBlob `json:"blobs"`
}

// HistoryArchiveBlob embeds only ciphertext needed for structural admission.
// Ordinary historical files remain lazy under the existing file protocol.
type HistoryArchiveBlob struct {
	ID string `json:"id"`
	CT []byte `json:"ct"`
}

func ParseHistoryArchiveChunk(data []byte) (HistoryArchiveChunk, error) {
	var r HistoryArchiveChunk
	if len(data) > protocol.MaxHistoryArchivePlaintext || !utf8.Valid(data) {
		return r, errors.New("history archive: oversized or invalid UTF-8 chunk")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, errors.New("history archive: invalid chunk")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return r, errors.New("history archive: trailing data")
	}
	for _, entry := range r.Entries {
		if entry.Blobs == nil {
			return r, errors.New("history archive: embedded blobs array required")
		}
	}
	return r, validateHistoryArchiveChunk(r)
}

func MarshalHistoryArchiveChunk(r HistoryArchiveChunk) ([]byte, error) {
	if err := validateHistoryArchiveChunk(r); err != nil {
		return nil, err
	}
	// Keep the shared wire representation an array even when there are no
	// structural files. Work on a copy so serialization does not mutate jobs.
	r.Entries = append([]HistoryArchiveEntry(nil), r.Entries...)
	for i := range r.Entries {
		if r.Entries[i].Blobs == nil {
			r.Entries[i].Blobs = []HistoryArchiveBlob{}
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if len(data) > protocol.MaxHistoryArchivePlaintext {
		return nil, errors.New("history archive: oversized chunk")
	}
	return data, nil
}

func validateHistoryArchiveChunk(r HistoryArchiveChunk) error {
	if r.V != 1 || len(r.Entries) < 1 || len(r.Entries) > protocol.MaxHistoryArchiveEntries {
		return errors.New("history archive: invalid version or count")
	}
	seen := map[string]bool{}
	bytes := 0
	for _, entry := range r.Entries {
		e := entry.Envelope
		if e.V != Version2 || !protocol.ValidID(e.ID) || seen[e.ID] || e.Kind != KindMessage || e.TS <= 0 ||
			len(e.CT) == 0 || len(e.CT) > MaxCiphertext || len(e.Sig) != ed25519.SignatureSize ||
			e.Attn || e.Chan != "" || e.Session != "" || e.Fallback || len(e.Blobs) > MaxAttachments || len(entry.Blobs) > MaxAttachments {
			return errors.New("history archive: invalid or duplicate child")
		}
		if _, _, err := protocol.SplitAddress(e.From); err != nil {
			return errors.New("history archive: invalid child sender")
		}
		if _, _, err := protocol.SplitAddress(e.To); err != nil {
			return errors.New("history archive: invalid child recipient")
		}
		seen[e.ID] = true
		bytes += len(e.CT)
		refs := map[string]Blob{}
		for _, blob := range e.Blobs {
			if !protocol.ValidID(blob.ID) || blob.Size < 1 || !protocol.ValidHash(blob.SHA256) {
				return errors.New("history archive: invalid child blob reference")
			}
			if _, exists := refs[blob.ID]; exists {
				return errors.New("history archive: duplicate child blob reference")
			}
			refs[blob.ID] = blob
		}
		embedded := map[string]bool{}
		for _, blob := range entry.Blobs {
			ref, exists := refs[blob.ID]
			if !exists || embedded[blob.ID] || int64(len(blob.CT)) != ref.Size {
				return errors.New("history archive: unexpected or mismatched embedded blob")
			}
			sum := sha256.Sum256(blob.CT)
			if hex.EncodeToString(sum[:]) != ref.SHA256 {
				return errors.New("history archive: embedded blob digest mismatch")
			}
			embedded[blob.ID] = true
			bytes += len(blob.CT)
		}
		if bytes > protocol.MaxHistoryArchivePlaintext {
			return errors.New("history archive: oversized child ciphertext")
		}
	}
	return nil
}

// ValidateHistoryArchiveChild is an explicit inert allowlist, applied only
// after ordinary signature/decryption checks. It never admits a live turn,
// request, consent or authority-changing control through archive import.
func ValidateHistoryArchiveChild(in Inner) error {
	if in.V != Version2 || in.Kind != KindMessage || in.Target != nil || in.Session != "" || in.Fallback ||
		in.Sub == SubHistory && !in.Replica || in.PID != "" && in.Sub != SubGroupProof && in.Sub != SubGroupContext {
		return errors.New("history archive: inert version 2 history child required")
	}
	switch in.Sub {
	case SubHistory, SubDeviceHistory, SubRootSync, SubGroupProof, SubGroupContext:
		return checkVersion2(in)
	default:
		return errors.New("history archive: non-history child")
	}
}
