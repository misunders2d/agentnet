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
  1. Put agentnet on your PATH (build: scripts/build.sh; no release binaries yet).
  2. agentnet join --agent laptop 'agentnet-invite-v1:...'   use the invite your admin sent
  3. agentnet daemon                                         leave running; it receives messages
  Then: agentnet send bob/desk "hello"   (addresses are person/agent)

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
Tested on Linux; macOS and Windows run in CI. More: docs/revival/INSTALL.md
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

Start it at login with a systemd user service, a macOS LaunchAgent or a
Windows logon task (see docs/revival/INSTALL.md).`,

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
commands need the Hub stopped. Every flag can also come from an AGENTNET_*
variable (e.g. AGENTNET_DATA), which is how the container image is configured.`,

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

Unpack a backup into an empty directory and check every attachment. A Hub
restored at the same address keeps its certificate, so laptops just continue.`,

	"a2a": `Usage: agentnet a2a serve --peer PERSON/AGENT [--listen 127.0.0.1:0]

Let an A2A client on this machine talk to one peer through AgentNet (official
A2A Go SDK, HTTP+JSON). Loopback only; clients need the bearer token in
HOME/a2a-token. A2A messages become encrypted AgentNet questions, tasks
(metadata agentnet.kind=task) or messages; file:// parts become attachments.
Tasks show SUBMITTED until the peer replies. Cancel, streaming, push and task
listing are not supported. Keep agentnet daemon running for replies.`,
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

// wantsHelp reports whether args ask for help anywhere: help, -h, --help.
func wantsHelp(args []string) bool {
	if len(args) > 0 && args[0] == "help" {
		return true
	}
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--help" {
			return true
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
