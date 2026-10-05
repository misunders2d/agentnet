package client

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
)

// A harness's own session file (a Claude transcript, a Codex rollout, a Pi
// session) is read locally, one JSONL record at a time, to prove which
// session it is and whether a delivered input reached it. Nothing of it is
// copied into AgentNet or sent anywhere. Its size never decides whether a
// question may be asked or answered (MEL-537): there is no cap on the whole
// file or on how many records it holds. The only bound is per record: one
// longer than nativeRecordMax (answerwait.go) is read through and skipped,
// for matching and for validation alike, never a refusal.

// nativeRecord is one record of a native session file.
type nativeRecord struct {
	Line     []byte // the record without its newline; nil when Oversize; valid only during the call
	Offset   int64  // where the record starts in the file
	Oversize bool   // longer than nativeRecordMax: read through, not kept
	Complete bool   // ended by a newline (false only for the file's last record)
}

// errNativeStop ends a scan early without an error.
var errNativeStop = errors.New("native scan stopped")

// nativeRecords calls each for every record of file from offset on, in
// order. An offset inside a record (the file was being written when it was
// taken) starts at the next whole record: what began before the offset is
// never read. A file shorter than offset is an error, so a caller that
// recorded the size it saw fails closed when the file was replaced. A
// missing file is os.ErrNotExist, for the caller to judge. each returning
// errNativeStop ends the scan with no error.
func nativeRecords(file string, offset int64, each func(nativeRecord) error) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	if offset < 0 {
		return errors.New("native offset is negative")
	}
	if offset > 0 {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if info.Size() < offset {
			return errors.New("native session file is shorter than when its input was claimed")
		}
		var prev [1]byte
		if _, err := f.ReadAt(prev[:], offset-1); err != nil {
			return err
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		if prev[0] != '\n' {
			// The record in progress at offset began before it: skip to its end.
			r := bufio.NewReader(f)
			n, err := skipLine(r)
			offset += n
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			return readRecords(r, offset, each)
		}
	}
	return readRecords(bufio.NewReaderSize(f, 64<<10), offset, each)
}

// skipLine reads through the next newline, returning the bytes read.
func skipLine(r *bufio.Reader) (int64, error) {
	var n int64
	for {
		chunk, err := r.ReadSlice('\n')
		n += int64(len(chunk))
		if err == bufio.ErrBufferFull {
			continue
		}
		return n, err
	}
}

func readRecords(r *bufio.Reader, offset int64, each func(nativeRecord) error) error {
	var buf bytes.Buffer
	for {
		start := offset
		buf.Reset()
		oversize := false
		complete := false
		var readErr error
		for {
			chunk, err := r.ReadSlice('\n')
			offset += int64(len(chunk))
			if !oversize {
				if buf.Len()+len(chunk) > nativeRecordMax+1 { // +1: the newline
					oversize = true
					buf.Reset()
				} else {
					buf.Write(chunk)
				}
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			if err == nil {
				complete = true
			} else {
				readErr = err
			}
			break
		}
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if offset == start { // nothing more
			return nil
		}
		rec := nativeRecord{Offset: start, Oversize: oversize, Complete: complete}
		if !oversize {
			rec.Line = bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
		}
		if err := each(rec); err != nil {
			if err == errNativeStop {
				return nil
			}
			return err
		}
		if readErr == io.EOF {
			return nil
		}
	}
}
