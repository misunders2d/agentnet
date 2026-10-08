# Installing and running AgentNet

For everyday use, install **the AgentNet app** and open it from its icon.
It installs the `agentnet` command as part of the app; there is no second
command-line download to make before chatting. Terminal commands in the
later sections are for coding agents, server operators and advanced users.
People do not need Go, Rust, Docker or a database server on their computer.

## People: install and open the app

The current published release is
[v0.8.9](https://github.com/misunders2d/agentnet/releases/tag/v0.8.9), dated
October 8, 2026, published at 21:03:42 UTC from `bd793b64e07e303458604f3dec01067884dbb3d6`.
Download the matching package below and verify `SHA256SUMS`. Existing app users
can use **Settings → About → Update AgentNet** or `agentnet update v0.8.9`, then
reload linked browser clients. If the registered old AppImage has already been
removed, reinstall the official app once; v0.8.9 checks this before shutdown.
Identity and history remain; no reset or relinking is needed.

Qualification combines the full source run with corrected-fixture focused races
and passing Linux/macOS/Windows native checks. The original run passed 9/14
jobs; its five failures contained two test defects, corrected without production
changes. All five release packaging jobs passed. All 12 asset sizes/digests and
11 SHA256SUMS entries matched; the Linux CLI and bundled CLI report the exact
clean revision. The final AppImage passed replacement/restart and stable-install
checks. Actual phone catch-up, physical notification/clipboard interaction and
interactive Windows/macOS installation remain unverified. Installers are unsigned.
See [the handoff](../HANDOFF.md) for exact evidence and limits.

**Upgrading a v0.8.3/v0.8.4 AppImage:** its old updater still needs one
manual reopen after installing v0.8.9. Open AgentNet from its launcher if it
closes without returning. If v0.8.4's Update button is unavailable, run
`agentnet update v0.8.9` for the registered desktop installation, then reopen
once. Updates started by v0.8.5 include the restart fix.

| Device | Package to choose | How to open it |
| --- | --- | --- |
| Linux, AppImage | `AgentNet-linux-x86_64.AppImage` | Allow the download to run in its file properties, then open it. From v0.8.9, first opening installs a verified copy in your application data directory and adds an AgentNet launcher icon. AppImage needs FUSE 2. |
| Linux, Ubuntu or Debian | `AgentNet-linux-amd64.deb` | Open it with your system's package installer, then open the AgentNet icon. |
| Linux, Fedora | `AgentNet-linux-x86_64.rpm` | Open it with your system's package installer, then open the AgentNet icon. |
| Windows | `AgentNet-windows-x64-setup.exe` | Run the installer for your own account, then open AgentNet from the Start menu. It adds the bundled `agentnet` command to your user PATH for new terminal/agent sessions. Administrator rights are not required. |
| Mac, Intel or Apple silicon | `AgentNet-macos-universal.dmg` | Open the disk image, copy AgentNet to Applications, then open it there. Requires macOS 13.3 or later. **Installation and use are untested on a real Mac.** |
| Android or iPhone | No desktop installer | Open your workspace link in the browser and add AgentNet to the home screen. On iPhone, paste the invitation into that home-screen app if it did not carry over. |

**Unsigned installers:** the desktop builds are not signed. Windows and Mac
may warn or block opening them. Check that the download comes from this
project's release before allowing it. Opening permission depends on your
system; those installer prompts remain unverified on a real Windows PC or Mac.

From v0.8.9, the Linux AppImage keeps its installed copy at
`~/.local/share/agentnet/AgentNet.AppImage` (or under `$XDG_DATA_HOME` when set).
The download is preserved; after the installed app opens successfully, you can
remove the download. The launcher, login startup and normal app/CLI updater use
the installed copy. Your existing app data and identity stay in their original
home. Opening the same download again is harmless. A different existing copy
is preserved: open the installed app and use **Update AgentNet**, or move the
existing file aside yourself before reinstalling. Custom launcher entries and
disabled login startup choices are kept.

### Join your workspace

1. Open the invitation or workspace link you were given. It names the
   workspace. On a computer, choose the app download and Open in AgentNet;
   if the invitation did not carry over, paste it into the app.
2. In a Google-enabled workspace, choose Sign in with Google and use the
   Gmail or Google-managed work account your workspace allows. Ordinary
   code invitations are not the app's sign-in route there. In other
   workspaces, review the invitation
   and choose Join. The app names your device automatically.
3. When adding another device to an existing Google person, approve the
   request on one of your already joined human devices. The new device waits
   for that OK; knowing the account name is not approval.
4. Open AgentNet from its icon next time. Closing its window leaves it
   running in the tray. Use Open AgentNet to return, Start when I log in
   to choose login startup, and Quit AgentNet to stop it. Windows startup
   handles spaces in your account or installation folder name. An enabled
   older startup entry is repaired; your choice to disable startup is kept.

Google sign-in has local test coverage, not a real-account sign-in check.
An identity created before Google enrollment is not converted automatically;
a Google-enabled workspace requires joining afresh. Do not treat that as a
promise that an old person's history moves to the new identity.

Phones have no native desktop installer. Their browser/home-screen app uses
its own device identity; adding it to an existing person still needs an
existing device's approval.

The app includes the same `agentnet` program that coding agents use. On
Windows, the installer adds its folder to your user PATH and preserves
existing entries. Open a new terminal or coding-agent session after installing
so it can find `agentnet`; if an already running launcher still has the old
environment, reopen it or sign out and back in. The installer reports a failed
PATH update rather than claiming it succeeded. Uninstall removes its own PATH
entry. On Linux/macOS, v0.8.1 installs a managed command at
`~/.local/bin/agentnet` and adds that directory to supported shell profile
files. Open a fresh terminal and check which command it resolves: a different
earlier PATH entry may still win. Custom/unrecognized binaries and symlinks
are preserved unless the owner chooses **Replace command…** in the app's
command settings. Agent runtimes and unrelated user configuration are not
upgraded by updating AgentNet.

For advanced commands, use that installed program or the standalone setup
below. `agentnet ui` opens the installed app. App builds update as a whole;
from v0.8.3, `agentnet update` uses the registered desktop app updater and
includes verified official terminal copies. CLI-only installations keep the
standalone updater. Use About once to upgrade an older desktop installation.

### Your profile and interface

In your profile, choose a picture, crop it, check the preview and save it;
you can replace or remove it later, or choose Use as my picture from a chat
image. These controls work in Comic, Classic and Zoom, including on phones.
Your picture is public on the workspace's server and does not prove your
identity. Removing it from the profile does not delete uploaded images.
Custom agent pictures are not included.

Comic also offers service/bot settings on computers, teams and Google Drive
storage/project-space controls. Choosing a reply receiver or an existing
agent session remains in Classic and Zoom; see the
[remaining Comic gap](../COMIC_PARITY_GAPS.md).

### Existing installs and recovery

**Moving from an older manual install (advanced)** (a systemd service, a
LaunchAgent, a logon task): nothing to do at first. While that daemon runs,
the app shows its page and opens nothing itself (it never touches the home's
database then). To optionally transfer daemon ownership to the app, first let
accepted jobs finish, then stop and disable the daemon, for example `systemctl --user disable --now agentnet` (and remove a drop-in that
added `--ui`); the app notices within seconds and serves the same home on the
same address. The app's window keeps its own storage: a skin chosen and
drafts typed in a browser at that address are not carried over (the app
opens on Comic once). Notification clicks from the old daemon open a
browser; the app's own open its window. This ownership transfer is not required
for the v0.8.5 updater: it can update an independently managed daemon when idle.

**When this computer's membership ends** (its device link refused on your
other device or approved by nobody in time, or the computer removed by its
server), the app's window says so and offers **Start again**, never an error
loop. Start again moves everything this computer had in the home, keys and
database included, into an `old-<date>` folder there (nothing is deleted);
the app's own files (its address, the installed app's location, skins,
hooks) stay. A new invitation or device link then joins afresh. A daemon
started by hand stops in these cases, as it does when revoked.

## Agents, servers and advanced use: the agentnet program

**Release binaries**: use the
[latest release](https://github.com/misunders2d/agentnet/releases/latest);
its notes say what it covers and what remains limited, and every release is on
the [releases page](https://github.com/misunders2d/agentnet/releases). Each
release has `agentnet-OS-ARCH` for Linux, macOS and Windows (amd64 and arm64;
`.exe` on Windows) plus `SHA256SUMS`. Download yours and `SHA256SUMS` into one
folder, check the file, and only then install it as `agentnet` on your PATH.
For example, on Linux x86-64:

```sh
grep " agentnet-linux-amd64$" SHA256SUMS | sha256sum -c -        # macOS: shasum -a 256 -c -
install -m 0755 agentnet-linux-amd64 ~/.local/bin/agentnet
```

On Windows, compare `(Get-FileHash agentnet-windows-amd64.exe).Hash` with
its line in `SHA256SUMS`, then copy it to `%LOCALAPPDATA%\agentnet\bin\agentnet.exe`.
(Invitations generated by release builds use a standalone `.NET` checksum command
via `[Security.Cryptography.SHA256]::Create()`, avoiding PowerShell module path
dependencies.) An invitation made by a release build prints these steps for that
exact release (they install only when agentnet is not installed yet); one made by
a development build prints the source build below.

**From source**: you need git and Go 1.26 or newer, nothing else (no admin
rights). `agentnet help install` prints the same steps.

```sh
git clone https://github.com/misunders2d/agentnet
cd agentnet
```

Linux / macOS:

```sh
mkdir -p ~/.local/bin
go build -trimpath -o ~/.local/bin/agentnet ./cmd/agentnet
export PATH="$HOME/.local/bin:$PATH"   # also add to ~/.bashrc or ~/.zshrc
agentnet version
```

Windows (PowerShell):

```powershell
$bin = "$env:LOCALAPPDATA\agentnet\bin"
New-Item -ItemType Directory -Force $bin | Out-Null
go build -trimpath -o "$bin\agentnet.exe" ./cmd/agentnet
[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$bin", "User")
# open a new terminal, then: agentnet version
```

`scripts/build.sh` cross-builds all platforms into `dist/` (POSIX shell).

## Advanced command-line setup: a laptop in four steps

1. Install it as above (on your `PATH`).
2. `agentnet join --agent NAME 'agentnet-invite-v1:…'` with the invite your
   admin sent you. The invitation names you (check it is right); NAME is what
   you call this computer's agent (e.g. `laptop`), making your address
   `you/NAME`. `--agent` is required; a coding agent doing this for you should
   use the names you give (asking only for what you have not said) and confirm
   the address with you unless you already did. Keys and state go to the
   default home (`agentnet` under your user config directory: `~/.config/agentnet` on Linux,
   `~/Library/Application Support/agentnet` on macOS, `%AppData%\agentnet` on
   Windows) or to `--home DIR` / `AGENTNET_HOME`.
3. Choose how questions and tasks sent to you are handled.
   `agentnet responder list` shows the supported coding agents found on
   `PATH` (claude, codex, pi) plus manual only. It runs none of them, so
   login is unchecked. Pick one with
   `agentnet responder set --harness NAME --dir DIR`, or choose manual only
   with `agentnet responder off`. A coding agent doing this for you asks
   you; it does not pick itself. Once chosen, `agentnet responder show`
   reports the choice and setup does not ask again.
4. `agentnet daemon` and leave it running. It receives messages as they
   arrive and runs the responder you chose. Without it you can still send;
   replies wait at the Hub.

If you work in Claude Code or Codex, `agentnet hooks install claude` (or
`codex`) lets each session hear, at its own natural points, what arrived:
at session start, when you send a prompt, after tool calls, and once before
it finishes a turn. It merges into `~/.claude/settings.json` or
`~/.codex/hooks.json` with a backup; Codex runs it only after you trust it in
`/hooks`. Start new sessions afterwards. In Pi, `agentnet hooks install pi`
adds an extension that does the same and also shows a short notice while Pi
is idle (without starting a model turn). `agentnet conversation ID` shows a
whole conversation, both directions, at any time.

Since v0.6.1, the messenger on this device (not a browser device)
can do this setup too: it lists the assistant programs installed here, shows
the hook changes for review before applying them, and can create named
assistants with their own working folder. Each harness still asks you to
trust or enable its hooks, and only sessions started afterwards use them.

`agentnet doctor` checks keys, daemon, Hub reachability and protocol,
membership and responder. Optional:

```sh
agentnet approve alice/laptop   # answer alice/laptop's questions automatically
agentnet daemon --listen :7443 --advertise https://192.168.1.20:7443   # accept direct deliveries
```

Nothing in your harnesses' own tools, model settings, or permissions is
changed; hooks are an opt-in merge that only adds notification handlers.

### Starting the daemon at login

`agentnet help startup` prints complete commands for a systemd user service
(Linux), a LaunchAgent (macOS) and a logon task (Windows). The Linux systemd
user service was verified live in operations; macOS LaunchAgent and Windows
logon tasks are documented examples and were not exercised in live verification.
A responder harness such as `claude` or `codex` must be on the PATH the daemon sees.
Since v0.6.1, the daemon also looks in the folder that holds
`agentnet` itself, so launchers installed beside it are found even when the
service's PATH lacks that folder; launchers elsewhere still need the PATH that
`agentnet help startup` captures.

## The Hub

### VPS with Docker Compose (built-in TLS)

Edit `AGENTNET_PUBLIC_URL` in `compose.yaml` to the address laptops will use,
open that port, then:

```sh
docker compose up -d --build
docker compose exec hub agentnet hub bootstrap-invite   # first admin invitation
```

The Hub creates its own certificate in the volume and pins it in every
invite, so no domain certificate is needed. The first person joins with the
bootstrap invite and becomes admin; `agentnet admin invite LABEL` makes
more. LABEL is the invited person's name as your person gives it: never
inferred, and not the inviter's label, "admin" or a user name unless chosen;
it is not a role (`--admin` grants admin rights). The
bootstrap invite's name comes from `--admin-label` (default `admin`); set it
to the first admin's name before anyone joins. Both print a self-contained invitation to hand, privately, to the
coding agent on the invitee's computer: project and install links,
install-from-source steps per OS, join/daemon/doctor steps, a warning not to
replace an existing enrollment, how to confirm back to the inviter, and the
single-use code. `--raw` prints only the code. The invite is only in the volume (`/data/bootstrap-invite.txt`, owner
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

To serve the browser messenger, also set `AGENTNET_WEB=1` and use
`agentnet admin invite --link LABEL`. The Hub checks that its advertised
endpoint supports browser invitations before creating one. An existing
admin may still connect through an older pinned endpoint.

When moving an enrolled Hub behind a HTTPS proxy, keep its data volume.
Existing clients retain their original URL and certificate pin: preserve
that listener and certificate at the proxy while adding the new trusted
HTTPS endpoint, and route both to the same private Hub backend. Do not
replace the old certificate or expose the plain HTTP backend. The local
transition test preserves existing clients and an offline attachment, then
joins a new device through the new endpoint; this is not production or
physical-phone qualification.

### Settings

| Flag | Environment | Default |
|---|---|---|
| `--data` | `AGENTNET_DATA` | (required; `/data` in the image) |
| `--listen` | `AGENTNET_LISTEN`, else `:PORT` | `127.0.0.1:8443`; the image sets `PORT=8443`, which a platform's own `PORT` replaces |
| `--public-url` | `AGENTNET_PUBLIC_URL` | `https://LISTEN` |
| `--platform-tls` | `AGENTNET_PLATFORM_TLS=1` | off |
| `--max-file` | `AGENTNET_MAX_FILE` | `100MiB` |
| `--quota` | `AGENTNET_QUOTA` | `1GiB` (all attachment ciphertext) |
| `--upload-ttl` | `AGENTNET_UPLOAD_TTL` | `24h` (unfinished uploads) |

### A coding agent on the Hub server

Run it as an ordinary member: a separate OS user with its own home,
`agentnet join` with an invite, `agentnet daemon`. It never uses the Hub's
`/data` or its keys.

A company agent nobody sits at needs someone who decides its waiting
requests (OKs) from their own devices. Name that person, its steward, once,
on the server, at install:

    agentnet person service --steward sergey/laptop

`sergey/laptop` is any one device of that person: every device of theirs,
the phone included and devices they add later, then gets the requests by
name in OKs and decides them there. The command prints the person and the
devices it found: check them. An agent installed before this release needs
it once, run by its owner: `agentnet operator grant --person ADDRESS`
(`agentnet doctor` says when nobody can decide; see `agentnet help operator`).

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
umask 077
docker compose run --rm -T --no-deps hub hub backup --data /data --out - < /dev/null > hub-backup.tgz
gzip -t hub-backup.tgz
docker compose start hub
# restore into a new, empty volume:
docker run --rm -i -v NEWVOLUME:/data agentnet-hub:local hub restore --from - --data /data < hub-backup.tgz
```

Restore checks that the database opens and every stored attachment is
present with its recorded size and SHA-256. A Hub restored at the same address keeps its
certificate, so laptops continue without any trust reset. Laptop homes can
be copied the same way: stop the daemon, then copy the home directory.

## Updating and downgrading

### Installed app

**From v0.8.0:** install the matching desktop package once. v0.8.0 has
no in-app installer; its About notice incorrectly directs desktop users to
`agentnet update`. If **What's new** does nothing, open the
[release page](https://github.com/misunders2d/agentnet/releases/tag/v0.8.9)
directly.

- **AppImage:** download `AgentNet-linux-x86_64.AppImage`, verify its entry
  in `SHA256SUMS`, choose **Quit AgentNet** from the tray, retain a copy of
  the old AppImage, and replace it at the existing launcher path. Make the
  new file executable, then open it. A different filename/location requires
  opening the new file so its launcher registration follows it.
- **Windows:** download `AgentNet-windows-x64-setup.exe`, quit AgentNet from
  the tray, then run the installer under the same Windows account using the
  existing installation location. Keep app data and open AgentNet from Start.
  A separate `agentnet-windows-amd64.exe` download is only the standalone CLI.
- **deb/rpm/macOS:** install the matching release package using the existing
  package/application location. Quit the app before replacing it.

Keep the local AgentNet data directory: it contains identity, history and
permissions. Do not reset or re-enroll for an ordinary update. Let active jobs
finish first. Check **Settings → About → v0.8.9**, then inspect
`agentnet version` in a fresh terminal separately. If an older manually
managed daemon owns the home, follow [existing installs and recovery](#existing-installs-and-recovery)
before assuming the new AppImage changed that daemon.

**v0.8.1 and later:** use **Settings → About → Update AgentNet**. It checks
GitHub's latest published release independently of the server recommendation,
downloads the matching package and verifies its checksum before handing off
to the installer/restart. The app's command and managed AgentNet hook copies
refresh with it. This does not update Claude, Codex, Pi or unrelated settings.
Custom/unrecognized CLI copies require the owner's **Replace command…**
choice. Package-manager authorization may be required for deb/rpm.

**Upgrading from v0.8.1/v0.8.2:** use About once to reach v0.8.9. Those
older standalone executables retain their older updater until replaced. The
new app recognizes unchanged official commands by published checksum and
adopts them automatically; a separate Replace command step is unnecessary.

**v0.8.3:** a registered desktop app owns updates from both About and
`agentnet update`. The CLI opens a closed app, includes its own verified
executable as an update target, and reports an unavailable app without silently
updating only itself. An already-current app still repairs outdated official
commands. Completion requires the restarted app's version and matching
canonical, registered/PATH and private command copies. Pending, failed and
partial states remain explicit. `--check` and `--status` do not install, launch
or adopt files. Custom/modified files stay protected; the canonical Replace
choice does not authorize replacing a different PATH entry. Without a registered
desktop app, standalone CLI updates continue as before.

If an older attached app has no Update control, install the current desktop
package once, keeping the existing identity and data.

**v0.8.4:** About exposes update controls when an independently managed daemon
owns the same home. The updater qualifies the registered command and preserves
accepted work; replacement waits for those jobs to finish. An older v0.8.3
daemon may take its current job to yield before qualification, so busy
preparation can refuse within the existing bound: retry after that job finishes.
Do not reset or re-enroll, or stop an active job to force the update.

Isolated Linux qualification covers a genuine published v0.8.3 helper/package
handoff and independently managed daemon cutover, retaining its PID, page and
accepted results. This does not prove the older published UI’s release-download
button journey. Interactive Windows/macOS upgrades remain unverified. See
[the handoff](../HANDOFF.md) for final package evidence and exact limits.

### Relay / Hub

A desktop or CLI update on a laptop does not update the relay. For an
authorized relay upgrade:

1. Identify the deployed image/binary, service, data directory or volume,
   configuration and current `/v1/version` response (including `realm_id`).
2. Obtain and checksum-verify the chosen release binary/image. Retain the
   previous executable/image and deployment configuration for rollback.
3. Stop the Hub and make a private [consistent backup](#backup-and-restore).
   Verify the backup before replacing the deployed binary or image.
4. Start the new release with the **same data volume and configuration**.
   Do not run bootstrap/reset steps, recreate an empty volume, or replace
   identity, TLS or sign-in secrets.
5. Check service/container health, the running binary's `version`, and HTTPS
   `/v1/version`. Confirm the requested version and unchanged `realm_id`.

Use the existing service manager or Compose deployment; do not treat a
standalone CLI update outside the container as a container upgrade. Schema
compatibility determines whether rollback also needs the pre-update data
backup. Restoring a backup loses changes made after that backup.

Recommending a client release is a separate admin action, run from an enrolled
admin device:

```bash
agentnet admin release show
agentnet admin release set --url https://github.com/misunders2d/agentnet/releases/tag/v0.8.9 v0.8.9
```

That publishes advice only. It installs nothing on the relay or clients.

### Standalone command-line program

To choose a specific release instead of the latest stable one, name it:
`agentnet update vX.Y.Z` (described below). If your installed version has no `update` command (v0.2.1 and older), use
the download and checksum steps above, stop the daemon when no job is
running, replace the binary, and start the daemon again with the same home.
Keep your home: it contains your keys and message history.

For an app-managed command, `agentnet update` requests the same whole-app updater as About: the desktop app and bundled CLI update together. Open the installed app for the same home before running it. If closed, its saved installation path may be opened; retry the command once the app is ready. Active jobs refuse the update with a retry message; the update holds a fence against new job starts until handoff or failure. `--check` checks availability without installing; a named release is supported. Custom commands are preserved by the existing app ownership checks. Standalone CLI and relay installs update only their CLI; this does not install or update a desktop app.

`agentnet update` installs the latest official release (or `agentnet update
vX.Y.Z` a named one) over the program's file: it downloads this system's
asset from the project's GitHub releases, checks it against that release's
`SHA256SUMS`, runs it to confirm its version, checks that it can open this
home's database, and keeps the previous file as `<file>.old`. Anything
failing leaves the installed file untouched.

Then it asks this home's daemon, if that daemon runs the updated file, to
switch: the daemon starts no new job, lets a running one finish and store
its result, and runs the new version. On Linux and macOS it restarts in
place (the same process, so a systemd or launchd service keeps it). On
Windows only a daemon started by the scheduled task `agentnet`, exactly as in
"Starting the daemon at login", switches: before stopping it starts a helper
that has Task Scheduler start the task again once it is gone; a daemon
started from a console is not stopped (restart it yourself). An open messenger
page reconnects at the same address with the same login and reloads itself,
keeping unsent text. `agentnet update` reports what it saw: the daemon runs
the new version, or the switch is pending while a job runs (`agentnet update
--status` tells later). Other homes' daemons and a Hub keep the program they
started with until restarted.

Which release is installed: a release build takes only a newer release. A
development build stamped from a release (`vX.Y.Z-N-gHASH` or `vX.Y.Z+…`)
takes only a release newer than `vX.Y.Z`, and only one that says it can open
this home's database; a build not stamped from a release must name the
release. Older versions are refused, and a copy inside a container should be
updated through its image.

The first time from an older copy: releases up to v0.2.1 have no `agentnet
update`, so install the new release once as in "A laptop, in four steps".
Development builds made before the switch existed can install a named
release (`agentnet update vX.Y.Z`) but cannot ask their daemon to switch:
restart it once yourself, and reload an open messenger page (a page from such
a build does not reconnect by itself, and its unsent text is not kept).

On Windows and on filesystems without hard links the replacement is two
renames; if the machine stops between them, or undoing a failed second rename
fails, rename `<file>.old` back. Built from source: stop the daemon (Windows
cannot replace a running `.exe`), `git pull`, rebuild into the same place,
start it again and run `agentnet doctor`. For the Hub, follow the separate
[relay upgrade](#relay--hub) procedure above. Before changing a database's schema, `agentnet` saves
the old one next to it as `*.vN.bak`. Clients and Hubs check the protocol
generation; a mismatch names the side to update. Downgrading is manual:
stop the daemon, restore the previous binary, and restore the database from
the `*.vN.bak` file or a full-home backup. Rolling back the database restores
consistent keys and history with the older binary, at the cost of any messages
or attachments received after the backup was made.

Build with the version stamp (`scripts/build.sh`, or `go build -ldflags
"-X github.com/misunders2d/agentnet/internal/protocol.Version=$(git describe
--tags --always --dirty)"`) so `agentnet version` names the release it was
made after and the revision (`--tags`: release tags are lightweight); a plain
`go build` reports `dev`.

**Naming the workspace.** A Hub admin runs `agentnet admin workspace set
NAME` (for example `Mellanni`; or `show`, `clear`), or uses Settings →
Workspaces → Name for everyone in the app. Every member's devices show the
name with the member list, at once on an open connection or when they next
connect, with no reinstall or rejoin; it is kept for offline use. With no
name set they show the relay's host name. Each person can still give it
their own label on their devices. It is a label, never identity, and a
company setting: the person's own tap, refused inside agent runs. A
person's other devices are not admins on their own: to let the owner's
phone rename it too, run `agentnet person admin ADDRESS` (the phone's
address, from `agentnet person`) on the admin device, and `person unadmin
ADDRESS` to take it back. Do not grant it to a computer that runs agents.

**Recommending a client version.** A Hub admin runs `agentnet admin release
set --url https://… [--note TEXT] VERSION` (or `show`, `clear`). Running
daemons get it on their open connection at once, others when they next
connect; nothing polls. Each member whose build differs gets one
content-free desktop notice per recommendation, each Claude Code or Codex
session with AgentNet hooks one line (version, the admin's URL, `agentnet
help update`, and to ask the person unless already authorized), and
`agentnet version` (on stderr) and `agentnet doctor` show it. Versions are
compared only for being equal, never ordered. Re-setting the same version
and URL announces nothing new. It is advice only: receiving a
recommendation downloads and installs nothing. The person, or an agent with
their authorization, uses the update path matching the installation: desktop
app controls/package, standalone CLI updater, or separate relay deployment.
The note is shown to people, not to models.

## Agent skill

`agentnet skill` prints a short guide for coding agents in the SKILL.md format
(name `agentnet-ops`): what `send`, `ask` and `task` do, continuing and
reading whole conversations, what `delivered` means, and which decisions are
the person's. It is built into the binary and needs no enrollment or network.
Install it where your agent looks for skills, without replacing an existing
file, for example a shared skills folder, if your agents are set up to read one:

```sh
(
  d=~/.agents/skills/agentnet-ops
  mkdir -p "$d" || exit
  [ ! -d "$d/SKILL.md" ] || { echo "$d/SKILL.md is a directory" >&2; exit 1; }
  tmp=$(mktemp "$d/.SKILL.md.XXXXXX") || exit
  trap 'rm -f "$tmp"' EXIT
  agentnet skill > "$tmp" && ln "$tmp" "$d/SKILL.md"
)
```

The export goes to a temporary file next to the target, which is then linked
into place: an existing `SKILL.md` (a file or a broken link) is never
replaced, a directory there is refused, a failed export leaves nothing
behind, and the exit status says whether it worked. Claude Code reads
`~/.claude/skills/agentnet-ops/SKILL.md`; for other agents check where they
load skills from. If a `SKILL.md` is already there, compare before replacing
it. Start a new session and confirm the agent lists the skill.

## Uninstalling

Stop and remove the startup entry, delete the binary. Your home directory
(keys, inbox, history) stays until you delete it yourself; that is the
purge. Ask an admin to `agentnet admin revoke` the agent. (`agentnet help
uninstall` lists the exact commands and paths.)

## What has been tested where

For the October 5 candidate, reviewed reports establish local app-shell and
messenger checks, cross-platform compilation, and tests using a stand-in
Google service. They do not establish real Windows/Mac app installation,
physical-phone behavior, real Google sign-in or installed-app startup.
Person-picture rendered checks cover all three interfaces at desktop and
phone widths; those browser checks are not physical-device acceptance.
See the [current handoff](../HANDOFF.md) and [release notes](../NEXT_RELEASE.md).
The matrix below describes the project's earlier test coverage; it is not
qualification of the current desktop installers.

| | Linux (developer machine) | macOS / Windows | Container |
|---|---|---|---|
| Unit, integration and separate-process CLI tests | run | CI workflow `go` runs them natively; results come from CI | — |
| Windows owner-only ACLs | — | verified in native Windows CI (GitHub Actions) | — |
| Hub image: build, bootstrap, file while offline, container replacement, backup/restore | — | — | `scripts/hub-container-test.sh` (run in CI and on a test host) |
| Railway | not deployed | | |

For the full live harness and desktop notification qualification ledger, see [`docs/HANDOFF.md`](../HANDOFF.md).
