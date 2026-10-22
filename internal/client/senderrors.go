package client

import (
	"errors"
	"net"
	"syscall"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Keep the producer code in the existing durable detail field, like the
// peer_update/server_unavailable causes. Readers never infer a cause from prose.
const SendErrorPrefix = "send_error:"

func sendErrorDetail(err error) string {
	var he *HubError
	if errors.As(err, &he) {
		switch he.Code {
		case protocol.CodeRelayQuota:
			return SendErrorPrefix + he.Code + ": Relay attachment quota is full. Ask a server admin to raise the limit or clean up eligible files, then retry this message."
		case protocol.CodeFileTooLarge:
			return SendErrorPrefix + he.Code + ": This file exceeds the relay's file-size limit. Choose a smaller file."
		case protocol.CodeRelayDiskFull:
			return SendErrorPrefix + he.Code + ": The relay disk is full. Ask a server admin to free disk space, then retry this message."
		}
	}
	if errors.Is(err, syscall.ENOSPC) {
		return SendErrorPrefix + "local_disk_full: This device is out of storage. Free disk space, then retry this message."
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return SendErrorPrefix + "network_unavailable: Cannot reach the server. Your message and files are kept here for retry."
	}
	return err.Error()
}
