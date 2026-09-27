package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Help never needs an enrolled agent and never touches the home directory:
// `agentnet help [COMMAND]`, `agentnet --help` and `agentnet COMMAND --help`
// all print text and exit successfully.

const rootHelp = `agentnet — encrypted messages, questions, tasks and files between coding agents

Usage:
  agentnet [--home DIR] COMMAND [flags] [args]
  agentnet help [COMMAND]        or  agentnet COMMAND --help

One program is both the laptop client and the Hub (server). Laptops need no
Docker, root, VPN, OAuth provider, database or model service.

Get started on a laptop:
  1. Install agentnet on your PATH: agentnet help install (Go 1.26 build; no release binaries yet)
  2. agentnet join --agent laptop 'agentnet-invite-v1:...'   use the invite your admin sent
  3. agentnet daemon                                         leave running; it receives messages
  Then: agentnet send bob/desk "hello"   (addresses are person/agent)

Setup and upkeep (help topics):
  install    build from source and put agentnet on your PATH (Linux, macOS, Windows)
  startup    start the daemon at login (systemd, launchd, Windows task)
  update     update the binary or the Hub; downgrade
  uninstall  remove agentnet, keeping or deleting your history

Messages and files:
  send       send an encrypted message, with --file attachments
  ask        send a question (approved peers may get an automatic answer)
  task       send a task (runs only if the recipient accepts it)
  reply      reply to a received message (takes over a question or task)
  inbox      list received messages
  download   save a message's attachments
  status     what is known about a message you sent
  sessions   list another agent's running sessions

Questions and tasks sent to you:
  accept     let your responder run a task or answer a held question
  decline    refuse a task or question
  cancel     stop your responder's current work on a message
  approve    answer an agent's questions automatically (unapprove to stop)
  responder  choose the local harness that answers and runs tasks

Identity and trust:
  join, whoami, fingerprint, trust

Running and checking:
  daemon     stay connected: receive, answer, retry (optionally accept direct deliveries)
  doctor     check keys, daemon, Hub, membership and responder
  version    print the version
  cleanup    free local space from failed or abandoned sends

Admin (from an admin agent):
  admin invite, admin revoke

Hub (on the server):
  hub serve, hub bootstrap-invite, hub storage, hub cleanup, hub backup, hub restore

Local A2A clients:
  a2a serve  let an A2A client on this machine talk to one peer

Home directory: --home DIR, or AGENTNET_HOME, or "agentnet" in your user
config directory. It holds your keys, inbox and history; keep it private.
Something wrong? Run: agentnet doctor
Tested on Linux; macOS and Windows run in CI. Also: docs/revival/INSTALL.md
`

// topics maps a command (or "command subcommand") to its help text.
var topics = map[string]string{
	"join": `Usage: agentnet [--home DIR] join [--agent NAME] CODE

Enroll this computer as an agent with an invite code from your admin. Creates
keys in the home directory, registers them with the Hub, and prints your
address (person/agent) and key fingerprint. NAME defaults to the host name.
If the answer is lost, run the same command again; it reuses the same keys.

Example:
  agentnet join --agent laptop 'agentnet-invite-v1:eyJodWIiOi...'`,

	"whoami": `Usage: agentnet whoami

Print this agent's address and key fingerprint. Give the fingerprint to
coworkers who want to confirm they trust the right key.`,

	"send": `Usage: agentnet send [--file PATH]... [--fallback] ADDRESS[#SESSION] TEXT

Send an end-to-end encrypted message. It goes straight to the recipient when
they advertise a reachable address, otherwise through the Hub, which keeps it
until they are online. Prints: ID STATE PATH (custody/delivered, relay/direct).
If the Hub is unreachable the message is queued and the daemon retries it.

  --file PATH   attach a file (repeatable, up to 8, 100 MiB each by default)
  --fallback    for ADDRESS#SESSION: if that session has ended, deliver to
                the agent's inbox instead of failing

Examples:
  agentnet send bob/desk "build is green"
  agentnet send --file report.pdf bob/desk "numbers attached"`,

	"ask": `Usage: agentnet ask [--file PATH]... ADDRESS TEXT

Send a question. If the recipient approved you and chose a responder, their
harness answers automatically in the background; otherwise it waits for them.
The answer arrives in your inbox as kind "answer" replying to this message.

Example:
  agentnet ask bob/desk "what is the deploy command for staging?"`,

	"task": `Usage: agentnet task [--file PATH]... ADDRESS TEXT

Send a task. It never runs by itself: the recipient must accept it, then
their responder runs it with their normal permissions. The outcome arrives as
kind "result" with a status (done, failed, timeout, cancelled, declined).

Example:
  agentnet task bob/desk "update CHANGELOG.md for release 1.4"`,

	"reply": `Usage: agentnet reply [--file PATH]... ID TEXT

Reply to a received message. Replying to a question or task by hand takes it
over, so your responder will not also answer it; this is refused while the
responder is working on it (use agentnet cancel ID first).

Example:
  agentnet reply 3f9c... "use make deploy-staging"`,

	"inbox": `Usage: agentnet inbox [--unread] [--json]

List received messages (and mark them read). Questions and tasks show their
state: pending, held, awaiting, accepted, running, answered, manual, declined,
failed, cancelled, interrupted. Reading never makes anything run.

  --unread   only unread messages
  --json     machine-readable output`,

	"download": `Usage: agentnet download [--dir DIR] [--force] ID

Save all attachments of message ID into DIR (default: current directory).
Each file is checked against the sender's signature before it gets its name.
Existing files are kept unless --force; names are made safe and unique.
Running it again after an interruption continues where it stopped.`,

	"status": `Usage: agentnet status ID

Show what is known about a message you sent: queued (not yet at the Hub),
custody (the Hub has it), delivered (the recipient stored it), quarantined
(the recipient could not verify it), expired (its session ended), and the
path (relay or direct).`,

	"sessions": `Usage: agentnet sessions ADDRESS

List an agent's running daemons ("sessions"), whether each is connected or
reconnecting, and whether it accepts direct deliveries. Address one session
with ADDRESS#SESSION.`,

	"accept": `Usage: agentnet accept ID

Let your responder run a task that awaits acceptance, answer a held question,
or retry one that was interrupted, failed or cancelled. Only you can do this;
nothing a sender does can.`,

	"decline": `Usage: agentnet decline ID [REASON]

Refuse a task or question; the sender receives a result/answer with status
"declined" and your reason.`,

	"cancel": `Usage: agentnet cancel ID

Stop your responder while it is running ID. On Linux and macOS the harness and
the processes it started are stopped; on Windows only the harness itself.`,

	"approve": `Usage: agentnet approve ADDRESS
       agentnet unapprove ADDRESS

Approve: questions from ADDRESS are answered automatically by your responder.
Unapprove: stop that; their questions still waiting go back to "held".
Approval never covers tasks.`,

	"responder": `Usage: agentnet responder set --harness NAME --dir DIR [--context FILE]... [--timeout 5m]
       agentnet responder show
       agentnet responder off

Choose the local coding agent that answers approved questions and runs
accepted tasks, one at a time, in its own background session (never in a
conversation you have open). It runs in DIR, where its own instructions and
settings apply; nothing in its configuration is changed.

Harnesses: claude (tested live), pi (not tested live). Questions run with all
tools disabled; tasks run with the harness's normal permissions. Codex and
Antigravity can read and reply by hand but are not automatic responders.

  --context FILE   text given with every question (repeatable)
  --timeout D      limit per question or task (default 5m)

Example:
  agentnet responder set --harness claude --dir ~/work/project --context ~/notes/team.md`,

	"fingerprint": `Usage: agentnet fingerprint ADDRESS

Show the key fingerprint you trust for ADDRESS and the one the Hub directory
offers now. Compare it with the owner (e.g. their agentnet whoami) when you
first talk, or when they differ.`,

	"trust": `Usage: agentnet trust ADDRESS

Trust ADDRESS's current keys after you verified the fingerprint with its
owner. Needed after a key change: sending is blocked and their messages are
held until you do.`,

	"daemon": `Usage: agentnet daemon [--listen ADDR] [--advertise https://HOST:PORT]

Stay connected to the Hub: receive messages as they arrive, run your
responder, and retry queued sends. Each run is one session. There is no
polling; the Hub pings every 90 s and the daemon answers. Only one daemon
runs per home.

  --listen ADDR       also accept direct deliveries on ADDR (e.g. :7443)
  --advertise URL     the https://host:port peers can reach (nothing is guessed;
                      no NAT traversal). Defaults to https://LISTEN.

Start it at login: agentnet help startup.`,

	"doctor": `Usage: agentnet doctor

Check version, keys and their permissions, whether the daemon runs, whether
the Hub is reachable and speaks the same protocol, membership, and the
responder. Prints one line per check with what to do; exits non-zero if a
check fails.`,

	"version": `Usage: agentnet version

Print the program version and protocol generation.`,

	"cleanup": `Usage: agentnet cleanup [--saved]

With the daemon stopped, remove encrypted copies of messages that failed or
were abandoned, and direct uploads never attached to a message.
  --saved   also remove directly received ciphertext of files already saved`,

	"admin": `Usage: agentnet admin invite [--ttl 168h] [--admin] LABEL
       agentnet admin revoke ADDRESS

Run on an admin agent. invite prints a single-use code for the person LABEL
(their address becomes LABEL/agent); send it to them privately. The label is
your statement about who they are. revoke immediately cuts ADDRESS off.

  --ttl D    how long the invite is valid (max 720h)
  --admin    the invited agent becomes an admin too`,

	"hub": `Usage: agentnet hub serve|bootstrap-invite|storage|cleanup|backup|restore [flags]

Run and maintain a Hub. See agentnet help "hub serve" etc. Maintenance
commands need the Hub stopped.

Environment variables (how the container image is configured):
  all hub commands:  AGENTNET_DATA (--data)
  hub serve only:    AGENTNET_LISTEN or PORT (--listen), AGENTNET_PUBLIC_URL,
                     AGENTNET_ADMIN_LABEL, AGENTNET_PLATFORM_TLS=1,
                     AGENTNET_MAX_FILE, AGENTNET_QUOTA, AGENTNET_UPLOAD_TTL
Other flags (--out, --from, cleanup ages) have no variable.`,

	"hub serve": `Usage: agentnet hub serve --data DIR [--listen ADDR] [--public-url URL]
                         [--platform-tls] [--max-file 100MiB] [--quota 1GiB]
                         [--upload-ttl 24h] [--admin-label admin]

Serve a Hub. On first start it writes a one-time admin invite to
DIR/bootstrap-invite.txt (read it with agentnet hub bootstrap-invite).

  --data DIR        state: database, TLS key, ciphertext (env AGENTNET_DATA)
  --listen ADDR     listen address (env AGENTNET_LISTEN or PORT)
  --public-url URL  https URL laptops use (env AGENTNET_PUBLIC_URL)
  --platform-tls    plain HTTP behind a platform that terminates HTTPS for
                    --public-url (Railway, load balancer); keep the port private
  --max-file SIZE   largest attachment        --quota SIZE  total attachments
  --upload-ttl D    remove unfinished uploads after D idle

Default TLS: the Hub makes its own certificate and pins it in invites.

Examples:
  agentnet hub serve --data /var/lib/agentnet --listen :8443 --public-url https://hub.example.com:8443
  docker compose up -d --build     (compose.yaml in the repository)`,

	"hub bootstrap-invite": `Usage: agentnet hub bootstrap-invite --data DIR

Print the first admin invite (only until someone uses it). In Docker:
  docker compose exec hub agentnet hub bootstrap-invite`,

	"hub storage": `Usage: agentnet hub storage --data DIR

With the Hub stopped, show attachment storage: undelivered (always kept),
delivered, never attached, unfinished uploads.`,

	"hub cleanup": `Usage: agentnet hub cleanup --data DIR [--delivered-older-than 720h] [--unattached-older-than 24h]

With the Hub stopped, remove attachments of messages delivered longer ago
(recipients who have not downloaded them yet lose them) and completed
uploads never attached to a message. Undelivered attachments are never
removed.`,

	"hub backup": `Usage: agentnet hub backup --data DIR --out FILE|-

With the Hub stopped, write a consistent backup: database, TLS key and
certificate, pending bootstrap invite, attachment ciphertext. It contains
the Hub's private key: keep it private and out of repositories.

Examples:
  agentnet hub backup --data /var/lib/agentnet --out hub-backup.tgz
  docker compose stop hub && umask 077 && docker compose run --rm --no-deps hub hub backup --out - > hub-backup.tgz`,

	"hub restore": `Usage: agentnet hub restore --from FILE|- --data NEWDIR

Unpack a backup into an empty directory and check every attachment's size
and SHA-256. A Hub restored at the same address keeps its certificate, so
laptops just continue.`,

	"a2a": `Usage: agentnet a2a serve --peer PERSON/AGENT [--listen 127.0.0.1:0]

Let an A2A client on this machine talk to one peer through AgentNet (official
A2A Go SDK, HTTP+JSON). Loopback only; clients need the bearer token in
HOME/a2a-token. A2A messages become encrypted AgentNet questions, tasks
(metadata agentnet.kind=task) or messages; file:// parts become attachments.
Tasks show SUBMITTED until the peer replies. Cancel, streaming, push and task
listing are not supported. Keep agentnet daemon running for replies.`,

	"install": `Install agentnet from source (no release binaries yet)

Needs git and Go 1.26 or newer (https://go.dev/dl). No Docker, root or admin
rights. The same commands build the Hub on a server.

  git clone -b revival/mvp https://github.com/misunders2d/agentnet
  cd agentnet

Linux / macOS:
  mkdir -p ~/.local/bin
  go build -trimpath -o ~/.local/bin/agentnet ./cmd/agentnet
  export PATH="$HOME/.local/bin:$PATH"      # also add this line to ~/.bashrc or ~/.zshrc

Windows (PowerShell):
  $bin = "$env:LOCALAPPDATA\agentnet\bin"
  New-Item -ItemType Directory -Force $bin | Out-Null
  go build -trimpath -o "$bin\agentnet.exe" ./cmd/agentnet
  [Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$bin", "User")
  # open a new terminal so PATH updates

Check: agentnet version
Next:  agentnet join --agent laptop 'agentnet-invite-v1:...'  then  agentnet help startup
Cross-building for other systems: scripts/build.sh (POSIX shell).`,

	"startup": `Start agentnet daemon at login

The commands assume agentnet is installed as in agentnet help install and
uses the default home. These are examples; the tests do not exercise them.

Linux (systemd user service):
  mkdir -p ~/.config/systemd/user
  cat > ~/.config/systemd/user/agentnet.service <<'UNIT'
  [Unit]
  Description=AgentNet daemon
  [Service]
  ExecStart=%h/.local/bin/agentnet daemon
  Restart=on-failure
  [Install]
  WantedBy=default.target
  UNIT
  systemctl --user daemon-reload && systemctl --user enable --now agentnet
  journalctl --user -u agentnet        # logs

macOS (LaunchAgent):
  cat > ~/Library/LaunchAgents/net.agentnet.daemon.plist <<PLIST
  <?xml version="1.0" encoding="UTF-8"?>
  <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
  <plist version="1.0"><dict>
    <key>Label</key><string>net.agentnet.daemon</string>
    <key>ProgramArguments</key><array><string>$HOME/.local/bin/agentnet</string><string>daemon</string></array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardErrorPath</key><string>$HOME/Library/Logs/agentnet.log</string>
  </dict></plist>
  PLIST
  launchctl load ~/Library/LaunchAgents/net.agentnet.daemon.plist

Windows (runs at logon, PowerShell):
  schtasks /create /sc onlogon /tn agentnet /tr "$env:LOCALAPPDATA\agentnet\bin\agentnet.exe daemon"
  schtasks /run /tn agentnet

A responder harness (e.g. claude) must be on the PATH the daemon sees.`,

	"update": `Update agentnet

Laptop: stop the daemon (Windows cannot replace a running .exe), rebuild into
the same place, start it again, then check:
  cd agentnet && git pull
  systemctl --user stop agentnet          # or: launchctl unload ... / schtasks /end /tn agentnet
  go build -trimpath -o ~/.local/bin/agentnet ./cmd/agentnet
  systemctl --user start agentnet
  agentnet doctor

Hub: back it up (agentnet help "hub backup"), then
  git pull && docker compose up -d --build        # or rebuild and restart hub serve

Before changing a database's schema, agentnet saves the old one next to it
as *.vN.bak. Clients and Hubs compare protocol generations; doctor names the
side to update. Downgrade: stop, move the *.vN.bak file back over the
database, run the older build. There is no self-update.`,

	"uninstall": `Uninstall agentnet

1. Stop and remove the startup entry:
     systemctl --user disable --now agentnet && rm ~/.config/systemd/user/agentnet.service
     launchctl unload ~/Library/LaunchAgents/net.agentnet.daemon.plist && rm ~/Library/LaunchAgents/net.agentnet.daemon.plist
     schtasks /delete /tn agentnet /f
2. Delete the binary (~/.local/bin/agentnet or %LOCALAPPDATA%\agentnet\bin\agentnet.exe).
3. Your home directory (keys, inbox, history) is kept. To erase it too,
   delete it: ~/.config/agentnet (Linux), ~/Library/Application Support/agentnet
   (macOS), %AppData%\agentnet (Windows), or your --home / AGENTNET_HOME.
4. Ask your admin to run agentnet admin revoke YOUR/ADDRESS.`,
}

func init() {
	topics["unapprove"] = topics["approve"]
	topics["a2a serve"] = topics["a2a"]
	for _, sub := range []string{"invite", "revoke"} {
		topics["admin "+sub] = topics["admin"]
	}
	for _, sub := range []string{"set", "show", "off"} {
		topics["responder "+sub] = topics["responder"]
	}
}

// valueFlags are the flags that take a separate value, so the help scan
// can follow Go's flag rules without each command's flag set.
var valueFlags = map[string]bool{
	"file": true, "dir": true, "agent": true, "listen": true, "advertise": true, "peer": true,
	"harness": true, "context": true, "timeout": true, "ttl": true, "data": true, "out": true,
	"from": true, "public-url": true, "admin-label": true, "max-file": true, "quota": true,
	"upload-ttl": true, "delivered-older-than": true, "unattached-older-than": true,
}

// guides are help topics that are not commands; running one prints it.
var guides = map[string]bool{"install": true, "startup": true, "update": true, "uninstall": true}

// wantsHelp reports whether args ask for help: "help", or -h/--help among
// a command's flags. Like Go's flag parsing, it stops at the first
// positional argument or "--", so message text such as "--help" after the
// recipient is just text.
func wantsHelp(args []string) bool {
	if len(args) == 0 || args[0] == "help" || guides[args[0]] {
		return true
	}
	rest := args[1:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		if _, ok := topics[args[0]+" "+rest[0]]; ok {
			rest = rest[1:] // subcommand, e.g. hub serve
		}
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--":
			return false
		case a == "-h" || a == "-help" || a == "--help":
			return true
		case strings.HasPrefix(a, "-"):
			name := strings.TrimLeft(a, "-")
			if valueFlags[name] { // "--flag=value" is not in the table, so it takes no extra token
				i++ // its value
			}
		default:
			return false
		}
	}
	return false
}

// printHelp writes help for the command named by args (words before any
// flag); it fails only for unknown topics.
func printHelp(w io.Writer, args []string) error {
	if len(args) > 0 && args[0] == "help" {
		args = args[1:]
	}
	var words []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		words = append(words, strings.Fields(a)...)
	}
	if len(words) == 0 {
		fmt.Fprint(w, rootHelp)
		return nil
	}
	if len(words) > 1 {
		if t, ok := topics[words[0]+" "+words[1]]; ok {
			fmt.Fprintln(w, t)
			return nil
		}
	}
	if t, ok := topics[words[0]]; ok {
		fmt.Fprintln(w, t)
		return nil
	}
	var names []string
	for k := range topics {
		if !strings.Contains(k, " ") {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return fmt.Errorf("no help for %q; commands: %s", strings.Join(words, " "), strings.Join(names, ", "))
}
