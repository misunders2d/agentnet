//go:build windows

package client

import "os"

// outboxSupported: a file's owner and link count are not read here, so a
// task run gets no outbox on Windows.
const outboxSupported = false

const openOutFlags = os.O_RDONLY

func statKey(info os.FileInfo) (fileKey, bool) { return fileKey{}, false }
