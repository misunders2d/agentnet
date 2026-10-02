package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Hub settings may also come from AGENTNET_* environment variables, so a
// container needs no command-line edits.
func env(name, def string) string {
	if v := os.Getenv("AGENTNET_" + name); v != "" {
		return v
	}
	return def
}

// parseSize reads sizes such as 100MiB, 1GiB, 512KiB or plain bytes.
func parseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}
	mult := int64(1)
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSuffix(s, u.suffix), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q (use e.g. 100MiB or 1GiB)", s)
	}
	return n * mult, nil
}

func dataFlag(fs *flag.FlagSet) *string {
	return fs.String("data", env("DATA", ""), "Hub data directory (env AGENTNET_DATA)")
}

func runHub(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hub serve|bootstrap-invite|storage|cleanup|backup|restore ...")
	}
	fs := flag.NewFlagSet("hub "+args[0], flag.ContinueOnError)
	data := dataFlag(fs)
	need := func() error {
		if *data == "" {
			return errors.New("--data is required")
		}
		return nil
	}
	switch args[0] {
	case "serve":
		return hubServe(ctx, fs, data, args[1:])
	case "bootstrap-invite":
		raw := fs.Bool("raw", false, "print only the invite code (for scripts)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := need(); err != nil {
			return err
		}
		path := filepath.Join(*data, hub.BootstrapFile)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no pending bootstrap invite: an admin has already enrolled (use `agentnet admin invite`)")
		}
		if err != nil {
			return err
		}
		code := strings.TrimSpace(string(data))
		if *raw {
			fmt.Println(code)
			return nil
		}
		packet, err := invitePacket(code, "")
		if err != nil {
			return err
		}
		fmt.Print(packet)
		return nil
	case "storage":
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := need(); err != nil {
			return err
		}
		m, err := hub.OpenMaintenance(*data)
		if err != nil {
			return err
		}
		defer m.Close()
		u, err := m.Usage()
		if err != nil {
			return err
		}
		for _, row := range []struct {
			name string
			a    hub.Amount
		}{{"undelivered (kept)", u.Undelivered}, {"delivered", u.Delivered}, {"never attached", u.Unattached}, {"unfinished uploads", u.Uploading}} {
			fmt.Printf("%-20s %6d files %12d bytes\n", row.name, row.a.Files, row.a.Bytes)
		}
		return nil
	case "cleanup":
		delivered := fs.Duration("delivered-older-than", 30*24*time.Hour, "remove attachments of messages delivered longer ago (recipients must have downloaded them)")
		unattached := fs.Duration("unattached-older-than", 24*time.Hour, "remove completed uploads never attached to a message")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := need(); err != nil {
			return err
		}
		m, err := hub.OpenMaintenance(*data)
		if err != nil {
			return err
		}
		defer m.Close()
		r, err := m.Cleanup(*delivered, *unattached, 24*time.Hour)
		if err == nil {
			fmt.Printf("removed %d files, %d bytes; undelivered attachments are never removed\n", r.Files, r.Bytes)
		}
		return err
	case "backup":
		out := fs.String("out", "", "backup file to create, or - for stdout (contains the Hub's TLS key: keep it private)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := need(); err != nil {
			return err
		}
		if *out == "" {
			return errors.New("--out is required")
		}
		m, err := hub.OpenMaintenance(*data)
		if err != nil {
			return err
		}
		defer m.Close()
		if *out == "-" { // for containers: docker run ... hub backup --out - > backup.tgz
			return m.Backup(os.Stdout)
		}
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		err = m.Backup(f)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(*out)
			return err
		}
		fmt.Printf("backup written to %s\n", *out)
		return nil
	case "restore":
		from := fs.String("from", "", "backup file, or - for stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := need(); err != nil {
			return err
		}
		in := os.Stdin
		if *from != "-" {
			f, err := os.Open(*from)
			if err != nil {
				return err
			}
			defer f.Close()
			in = f
		}
		sum, err := hub.Restore(in, *data)
		if err == nil {
			fmt.Printf("restored %d agents, %d messages, %d attachments into %s\n", sum.Agents, sum.Messages, sum.Blobs, *data)
		}
		return err
	}
	return fmt.Errorf("unknown hub command %q", args[0])
}

func hubServe(ctx context.Context, fs *flag.FlagSet, data *string, args []string) error {
	listen := fs.String("listen", defaultListen(), "listen address (env AGENTNET_LISTEN, or PORT)")
	public := fs.String("public-url", env("PUBLIC_URL", ""), "https URL clients use (default https://LISTEN; env AGENTNET_PUBLIC_URL)")
	adminLabel := fs.String("admin-label", env("ADMIN_LABEL", "admin"), "person label for the bootstrap admin invite")
	platformTLS := fs.Bool("platform-tls", env("PLATFORM_TLS", "") == "1", "serve plain HTTP behind a platform that terminates HTTPS for --public-url (env AGENTNET_PLATFORM_TLS=1)")
	web := fs.Bool("web", env("WEB", "") == "1", "serve the browser messenger at this Hub's URL (env AGENTNET_WEB=1)")
	maxFile := fs.String("max-file", env("MAX_FILE", "100MiB"), "largest attachment (plaintext)")
	quota := fs.String("quota", env("QUOTA", "1GiB"), "total attachment storage")
	uploadTTL := fs.Duration("upload-ttl", mustDuration(env("UPLOAD_TTL", "24h")), "idle time before an unfinished upload is removed")
	pushHosts := fs.String("push-hosts", env("PUSH_HOSTS", ""), "comma-separated push services to send Web Push to, besides Apple, Google, Mozilla and Microsoft (env AGENTNET_PUSH_HOSTS)")
	var browserOrigins repeatedBrowserOrigins
	fs.Var(&browserOrigins, "browser-origin", "explicit HTTPS browser workspace origin (repeatable; no wildcard)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *data == "" {
		return errors.New("--data is required")
	}
	maxBytes, err := parseSize(*maxFile)
	if err != nil {
		return fmt.Errorf("--max-file: %w", err)
	}
	quotaBytes, err := parseSize(*quota)
	if err != nil {
		return fmt.Errorf("--quota: %w", err)
	}
	if quotaBytes < protocol.CiphertextBound(maxBytes) {
		return errors.New("--quota must be at least --max-file")
	}
	if *uploadTTL < time.Minute {
		return errors.New("--upload-ttl must be at least 1m")
	}
	if *public == "" {
		if *platformTLS {
			return errors.New("--platform-tls needs --public-url (the platform's https address)")
		}
		*public = "https://" + *listen
	}
	var extra []string
	for _, h := range strings.Split(*pushHosts, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			if !protocol.ValidPushHost(h) {
				return fmt.Errorf("--push-hosts: %q is not a host name (such as push.example.com)", h)
			}
			extra = append(extra, h)
		}
	}
	h, err := hub.Open(hub.Config{DataDir: *data, PublicURL: *public, AdminLabel: *adminLabel, PlatformTLS: *platformTLS, Web: *web,
		MaxFileSize: maxBytes, StorageQuota: quotaBytes, UploadTTL: *uploadTTL, PushHosts: extra, BrowserOrigins: []string(browserOrigins)})
	if err != nil {
		return err
	}
	defer h.Close()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	mode := "TLS with its own certificate (pinned in invites)"
	if *platformTLS {
		mode = "plain HTTP behind platform TLS: keep this port private to the platform"
	}
	log.Printf("agentnet %s hub listening on %s, public %s, %s", protocol.Version, ln.Addr(), *public, mode)
	return h.Serve(ctx, ln)
}

func mustDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Fatalf("AGENTNET_UPLOAD_TTL: %v", err)
	}
	return d
}

// defaultListen is AGENTNET_LISTEN if set, else :PORT when a platform (such
// as Railway, or the container image's PORT=8443) assigns the port, else
// loopback 8443 for a local run.
func defaultListen() string {
	if v := os.Getenv("AGENTNET_LISTEN"); v != "" {
		return v
	}
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return "127.0.0.1:8443"
}

// repeatedBrowserOrigins preserves explicit origins for Hub.Open validation.
type repeatedBrowserOrigins []string

func (v *repeatedBrowserOrigins) String() string          { return strings.Join(*v, ",") }
func (v *repeatedBrowserOrigins) Set(origin string) error { *v = append(*v, origin); return nil }
