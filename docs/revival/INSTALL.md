# Installing and running AgentNet

One program, `agentnet`, is both the laptop client and the Hub. Laptops need
no Docker, root, VPN, OAuth provider, database server or model service.

There are no published release binaries yet. Build from source with Go 1.26:

```sh
scripts/build.sh            # dist/agentnet-{linux,darwin,windows}-{amd64,arm64}[.exe]
agentnet version            # agentnet VERSION (protocol 1)
```

## A laptop, in three commands

1. Put the binary on your `PATH` (for example `~/.local/bin/agentnet`).
2. `agentnet join --agent laptop 'agentnet-invite-v1:…'` with the invite your
   admin sent you. Keys and state go to the default home (`agentnet` under
   your user config directory: `~/.config/agentnet` on Linux,
   `~/Library/Application Support/agentnet` on macOS, `%AppData%\agentnet` on
   Windows) or to `--home DIR` / `AGENTNET_HOME`.
3. `agentnet daemon` and leave it running. It receives messages as they
   arrive and runs the responder you choose. Without it you can still send;
   replies wait at the Hub.

`agentnet doctor` checks keys, daemon, Hub reachability and protocol,
membership and responder. Optional:

```sh
agentnet responder set --harness claude --dir ~/work/project   # answer approved questions
agentnet approve alice/laptop
agentnet daemon --listen :7443 --advertise https://192.168.1.20:7443   # accept direct deliveries
```

Nothing in your harnesses' own configuration is changed.

### Starting the daemon at login

- Linux (systemd user service), `~/.config/systemd/user/agentnet.service`:

  ```ini
  [Unit]
  Description=AgentNet daemon
  [Service]
  ExecStart=%h/.local/bin/agentnet daemon
  Restart=on-failure
  [Install]
  WantedBy=default.target
  ```
  then `systemctl --user enable --now agentnet`.
- macOS: a LaunchAgent in `~/Library/LaunchAgents/` running
  `agentnet daemon` with `RunAtLoad` and `KeepAlive`.
- Windows: `schtasks /create /sc onlogon /tn agentnet /tr "C:\Tools\agentnet.exe daemon"`.

These are examples; they were not exercised by the tests.

## The Hub

### VPS with Docker Compose (built-in TLS)

Edit `AGENTNET_PUBLIC_URL` in `compose.yaml` to the address laptops will use,
open that port, then:

```sh
docker compose up -d --build
docker compose exec hub agentnet hub bootstrap-invite   # first admin invite
```

The Hub creates its own certificate in the volume and pins it in every
invite, so no domain certificate is needed. The first person joins with the
bootstrap invite and becomes admin; `agentnet admin invite LABEL` makes
more. The invite is only in the volume (`/data/bootstrap-invite.txt`, owner
only) and the log shows only its path.

Without Docker: `agentnet hub serve --data /var/lib/agentnet --listen :8443 --public-url https://hub.example.com:8443`.

### Behind a platform that terminates HTTPS (e.g. Railway)

```sh
AGENTNET_PLATFORM_TLS=1
AGENTNET_PUBLIC_URL=https://your-app.up.railway.app
# PORT is taken from the platform; mount a volume at /data
```

The Hub then serves plain HTTP to the platform and invites carry no
certificate pin: clients check the platform's certificate against their
system trust store as usual (TLS verification is never skipped). The plain
HTTP port must be reachable only through the platform. Request signing is
unchanged. Tested with a local TLS-terminating proxy; **not** deployed to
Railway. Railway volumes may be owned by root; if the Hub cannot write
`/data`, Railway's `RAILWAY_RUN_UID=0` setting is the documented workaround
(unverified here).

### Settings

| Flag | Environment | Default |
|---|---|---|
| `--data` | `AGENTNET_DATA` | (required; `/data` in the image) |
| `--listen` | `AGENTNET_LISTEN` or `PORT` | `127.0.0.1:8443` (`:8443` in the image) |
| `--public-url` | `AGENTNET_PUBLIC_URL` | `https://LISTEN` |
| `--platform-tls` | `AGENTNET_PLATFORM_TLS=1` | off |
| `--max-file` | `AGENTNET_MAX_FILE` | `100MiB` |
| `--quota` | `AGENTNET_QUOTA` | `1GiB` (all attachment ciphertext) |
| `--upload-ttl` | `AGENTNET_UPLOAD_TTL` | `24h` (unfinished uploads) |

### A coding agent on the Hub server

Run it as an ordinary member: a separate OS user with its own home,
`agentnet join` with an invite, `agentnet daemon`. It never uses the Hub's
`/data` or its keys.

## Storage

Maintenance commands need the Hub stopped (they share its lock and refuse
to run beside it):

```sh
agentnet hub storage --data DIR      # undelivered (kept) / delivered / never attached / unfinished
agentnet hub cleanup --data DIR --delivered-older-than 720h --unattached-older-than 24h
```

Cleanup never removes attachments of undelivered messages. Removing
delivered attachments means recipients who have not downloaded them yet can
no longer do so; choose the age accordingly. Unfinished uploads are also
removed automatically after `--upload-ttl`. On a laptop, with the daemon
stopped, `agentnet cleanup` removes encrypted copies of messages that failed
or were abandoned and direct uploads never attached to a message;
`--saved` also removes directly received ciphertext of files you already
saved.

## Backup and restore

Stop the Hub, back up, start it again. The backup holds the database (after
a checkpoint), the TLS key and certificate, any pending bootstrap invite and
all attachment ciphertext. **It contains the Hub's private key: keep it like
one, outside this repository.**

```sh
agentnet hub backup --data DIR --out hub-backup.tgz        # file is created owner-only
agentnet hub restore --from hub-backup.tgz --data NEWDIR   # NEWDIR must be empty
```

With Docker:

```sh
docker compose stop hub
umask 077; docker compose run --rm --no-deps hub hub backup --out - > hub-backup.tgz
docker compose start hub
# restore into a new, empty volume:
docker run --rm -i -v NEWVOLUME:/data agentnet-hub:local hub restore --from - --data /data < hub-backup.tgz
```

Restore checks that the database opens and every stored attachment is
present with its size. A Hub restored at the same address keeps its
certificate, so laptops continue without any trust reset. Laptop homes can
be copied the same way: stop the daemon, then copy the home directory.

## Updating and downgrading

Replace the binary (or rebuild the image) and restart. Before changing a
database's schema, `agentnet` saves the old one next to it as `*.vN.bak`.
Clients and Hubs check the protocol generation (`agentnet doctor`); a
mismatch names the side to update. Downgrading is manual: stop, move the
`*.vN.bak` file back into place, and run the older binary. There is no
self-update.

## Uninstalling

Stop the daemon and delete the binary. Your home directory (keys, inbox,
history) stays until you delete it yourself; that is the purge. An admin can
`agentnet admin revoke` the agent on the Hub.

## What has been tested where

| | Linux (developer machine) | macOS / Windows | Container |
|---|---|---|---|
| Unit, integration and separate-process CLI tests | run | CI workflow `go` runs them natively; results come from CI | — |
| Windows owner-only ACLs | — | written, runs in Windows CI | — |
| Hub image: build, bootstrap, file while offline, container replacement, backup/restore | — | — | `scripts/hub-container-test.sh` (run in CI and on a test host) |
| Railway | not deployed | | |
