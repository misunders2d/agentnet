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
  2. agentnet join --agent NAME 'agentnet-invite-v1:...'     the invitation your admin sent;
     NAME is what the person calls this computer's agent: use theirs, or ask; never infer it
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
  conversation  show a whole conversation, sent and received
  download   save a message's attachments
  status     what is known about a message you sent
  sessions   list another agent's running sessions

Questions and tasks sent to you:
  accept     let your responder run a task or answer a held question
             (accept --always ID: also let this sender's future tasks run)
  decline    refuse a task or question
  cancel     stop your responder's current work on a message
  approve    answer an agent's questions automatically (unapprove to stop);
             approve --tasks: run its tasks without asking, for its exact key
  approvals  list who is approved, with task keys and whether they still hold
  resolve    close an item your responder marked needs_human
  open       review an item (or --review, all waiting) with your coding agent
  review-to  tell another agent of yours, without content, when items wait here
  responder  choose the local harness that answers and runs tasks

Identity and trust:
  join, whoami, fingerprint, trust

Awareness in Claude Code and Codex sessions:
  hooks      install hooks that tell each session what arrived
  hook       (run by those hooks)

Running and checking:
  daemon     stay connected: receive, answer, retry (optionally accept direct deliveries)
  doctor     check keys, daemon, Hub, membership and responder
  version    print the version
  update     install the latest official release over this program
  cleanup    free local space from failed or abandoned sends

Admin (from an admin agent):
  admin invite, admin revoke
  The invitee's name (LABEL) comes from your person: use what they said, or ask; never infer it.

Hub (on the server):
  hub serve, hub bootstrap-invite, hub storage, hub cleanup, hub backup, hub restore

Local A2A clients:
  a2a serve  let an A2A client on this machine talk to one peer

Messenger page (preview):
  ui --demo  try the messenger page in a browser with invented data

Home directory: --home DIR, or AGENTNET_HOME, or "agentnet" in your user
config directory. It holds your keys, inbox and history; keep it private.
Something wrong? Run: agentnet doctor
Tested on Linux; macOS and Windows run in CI. Also: docs/revival/INSTALL.md
`

// topics maps a command (or "command subcommand") to its help text.
var topics = map[string]string{
	"join": `Usage: agentnet [--home DIR] join --agent NAME CODE

Enroll this computer as an agent with an invite code from your admin. Creates
keys in the home directory, registers them with the Hub, and prints your
address (LABEL/NAME) and key fingerprint. LABEL comes from the invitation;
NAME is required and chosen by the person: use the name they gave for this
agent, or ask (lowercase letters, digits, hyphens), and unless they already
confirmed it, confirm the full address before joining. Nothing is created until NAME is given. If the answer is lost, run
the same command again; it reuses the same keys. An existing enrollment in
the home is never replaced.

If the Hub says the address is taken (another device already has it, or had
it and was revoked), nothing was enrolled and the invitation and keys stay
valid. The Hub names a free address, which is not reserved: show it to the
person or ask for another NAME, and join again only with the one they confirm.

Example (after the person chose "laptop"):
  agentnet join --agent laptop 'agentnet-invite-v1:eyJodWIiOi...'`,

	"whoami": `Usage: agentnet whoami

Print this agent's address and key fingerprint. Give the fingerprint to
coworkers who want to confirm they trust the right key.`,

	"send": `Usage: agentnet send [--file PATH]... [--fallback] [--wait 5s] ADDRESS[#SESSION] TEXT

Send an end-to-end encrypted message. It goes straight to the recipient when
they advertise a reachable address, otherwise through the Hub, which keeps it
until they are online. Prints: ID STATE PATH, and on stderr what that means:
  delivered   stored in the recipient's inbox (not necessarily read or answered)
  custody     the Hub holds it; it is delivered when the recipient connects
  queued      the Hub was unreachable; the daemon retries it
If the Hub has it, send waits up to --wait for the recipient's receipt, woken
by the receipt itself (one request, no polling). Answers and task results
arrive later in your inbox.

  --wait D      how long to wait for the receipt (default 5s; 0 returns at once)
  --file PATH   attach a file (repeatable, up to 8, 100 MiB each by default)
  --fallback    for ADDRESS#SESSION: if that session has ended, deliver to
                the agent's inbox instead of failing

Examples:
  agentnet send bob/desk "build is green"
  agentnet send --file report.pdf bob/desk "numbers attached"`,

	"ask": `Usage: agentnet ask [--file PATH]... [--wait 5s] [--follow-up TEXT] [--reply-to ID] ADDRESS TEXT

Send a question. If the recipient approved you and chose a responder, their
harness answers automatically in the background; otherwise it waits for them.
The answer arrives in your inbox as kind "answer" replying to this message.
Output and --wait as for send: "delivered" means it reached their inbox,
not that it was answered.
` + followUpHelp + `

Example:
  agentnet ask --follow-up "tell me if staging needs a migration" bob/desk "what is the deploy command for staging?"`,

	"task": `Usage: agentnet task [--file PATH]... [--wait 5s] [--follow-up TEXT] [--reply-to ID] ADDRESS TEXT

Send a task. It waits until the recipient accepts it, unless they granted
your agent's exact key standing permission to run tasks (accept --always,
approve --tasks); either way their responder runs it with their normal
permissions. The outcome arrives as
kind "result" with a status (done, failed, timeout, cancelled, declined).
Output and --wait as for send: "delivered" means it reached their inbox, not
that it was accepted or done.
` + followUpHelp + `

Example:
  agentnet task bob/desk "update CHANGELOG.md for release 1.4"`,

	"reply": `Usage: agentnet reply [--file PATH]... [--wait 5s] ID TEXT

Reply to a received message. Replying to a question or task by hand takes it
over, so your responder will not also answer it; this is refused while the
responder is working on it (use agentnet cancel ID first).

Example:
  agentnet reply 3f9c... "use make deploy-staging"`,

	"inbox": `Usage: agentnet inbox [--unread | --review] [--json]

List received messages (and mark them read). Questions and tasks show their
state: pending, held, awaiting, accepted, running, answered, manual, declined,
failed, cancelled, interrupted, needs_human, resolved. Replies you asked to
follow up show summarized with the summary. Reading never makes anything run
and never accepts or clears anything.

Items waiting for your decision (--review):
  held         a question from an agent you have not approved:
               accept ID, reply ID TEXT, or decline ID
  awaiting     a task: accept ID (or accept --always ID) or decline ID
               (tasks run without asking only under a task grant)
  needs_human  your responder stopped and asked you to decide (reason shown),
               or a review notice: an agent says requests wait for a
               person on its machine (agentnet help review-to); any agent
               can send one, it proves nothing: resolve ID once seen;
               for your responder's items:
               reply ID TEXT or decline ID answers a question or task;
               accept ID reruns it afresh (e.g. after you add context);
               resolve ID closes it without sending anything
While the daemon runs, a desktop notification with only a count (no content)
tells you when new items wait (Linux: notify-send; macOS: osascript;
Windows: a notification-area balloon, whose icon stays while the daemon
runs; AGENTNET_NOTIFY=off turns it off). If none can be shown, the daemon
log says so and the items still wait. On Linux with xdg-terminal-exec,
clicking the notification opens a review in a new terminal (agentnet help
open): on Omarchy also from the notification history; on other desktops only
while the notification is shown. Clicks are not handled on macOS or Windows
yet.

  --unread   only unread messages
  --review   only items waiting for your decision; does not mark them read
  --json     machine-readable output`,

	"conversation": `Usage: agentnet conversation [--json] [--offset N] [--limit N] ID

Show the whole conversation containing message ID, oldest first: every
message you sent and received that is linked to it through replies, with the
same agent. Links to messages with anyone else, or to unknown ids, are not
followed. Each message shows its direction, kind, status, delivery or
response state, body, files, and any local note or follow-up summary
(separate from what the other agent wrote). Nothing is changed, not even
read state. Everything stays in the local database after sessions and the
daemon exit.

  --limit N   at most N messages (default 50; 0: all); the header says which
  --offset N  skip the first N`,

	"hooks": `Usage: agentnet hooks show|install|remove claude|codex [--file PATH]

Hooks let a running Claude Code or Codex session learn what arrived for
AgentNet at its own natural points: when it starts, when you send a prompt,
after each tool call, and before it finishes a turn. Nothing wakes a session
that is idle, and nothing is typed into it.

  show     print the hook configuration AgentNet would add
  install  add it to the user-level file, keeping everything else there:
           Claude Code ~/.claude/settings.json, Codex ~/.codex/hooks.json
           (or $CODEX_HOME/hooks.json); a backup of the old file is written
           first, and running it again changes nothing
  remove   take AgentNet's hooks out again

Codex runs a hook only after you review and trust it: open /hooks in Codex.
Start new sessions after installing; running ones may not pick it up.
Each session is told only message ids, kinds, senders and states (never the
text, which comes from other people's agents), and each session keeps its
own place, so reading or answering in one session never hides anything from
another. If new messages arrive during a turn, the session is asked once to
check them before it finishes. Pi and Antigravity hooks, and hooks on
Windows, are not supported; use agentnet inbox and agentnet conversation
there. Sessions the background worker starts are not told anything.`,

	"hook": `Usage: agentnet hook claude|codex

Run by the hooks that agentnet hooks install configures: reads the hook
event (JSON) on stdin and prints what to tell the session. It only reads
the local database, never contacts the Hub or starts a model, and prints
nothing (exit 0) if AgentNet is not set up here.`,

	"download": `Usage: agentnet download [--dir DIR] [--force] ID

Save all attachments of message ID into DIR (default: current directory).
Each file is checked against the sender's signature before it gets its name.
Existing files are kept unless --force; names are made safe and unique.
Running it again after an interruption continues where it stopped.`,

	"status": `Usage: agentnet status [--wait D] ID

With --wait, a message the Hub still holds is waited on for up to D (one
request, woken by the recipient's receipt). Show what is known about a
message you sent: queued (not yet at the Hub),
custody (the Hub has it), delivered (the recipient stored it), quarantined
(the recipient could not verify it), expired (its session ended), and the
path (relay or direct).`,

	"sessions": `Usage: agentnet sessions ADDRESS

List an agent's running daemons ("sessions"), whether each is connected or
reconnecting, and whether it accepts direct deliveries. Address one session
with ADDRESS#SESSION.`,

	"accept": `Usage: agentnet accept ID
       agentnet accept --always ID

Let your responder run a task that awaits acceptance, answer a held question,
or rerun one that was interrupted, failed, cancelled or marked needs_human.
A rerun starts afresh; it does not resume the earlier run. Only you can do
this; nothing a sender does can.

--always (tasks only) also lets future tasks from the same sender run without
asking, in one step: only for the exact key that signed this task, and only
while that key is still the one you trust. Like approve --tasks; stop with
agentnet unapprove --tasks ADDRESS.`,

	"decline": `Usage: agentnet decline ID [REASON]

Refuse a task or question; the sender receives a result/answer with status
"declined" and your reason.`,

	"cancel": `Usage: agentnet cancel ID

Stop your responder while it is running ID. On Linux and macOS the harness and
the processes it started are stopped; on Windows only the harness itself.`,

	"open": `Usage: agentnet open ID
       agentnet open --review

What clicking an AgentNet review notification runs, in a new terminal (Linux,
with xdg-terminal-exec). It starts your chosen coding agent (agentnet
responder show) interactively, in a new session in the responder directory,
with a prompt to show and summarize item ID (its whole conversation) or
everything waiting for your decision. For a review notice from another
machine it explains that the requests can only be decided there. Without a
coding agent it prints the conversation or list. Opening never accepts,
declines, replies or runs anything; the prompt asks the agent not to act
unless you ask, but in that session your own tool permissions still apply.`,

	"resolve": `Usage: agentnet resolve ID

Close an item your responder marked needs_human after you have dealt with it.
It sends nothing; to answer the sender, use reply or decline instead.`,

	"review-to": `Usage: agentnet review-to               (show)
       agentnet review-to ADDRESS
       agentnet review-to --off

For a machine where nobody sees desktop notifications (a server): when a
held question, a task awaiting acceptance or a needs_human item waits here,
the daemon sends ADDRESS (another agent of the same person, e.g. their
laptop) one plain message with only a count and this agent's address: no
text, senders or ids of the requests. Each item is reported once. Deciding
still happens on this machine: nothing received there can accept, decline
or approve anything here, and the senders are told nothing.

ADDRESS files the notice (from any agent; it grants and proves nothing) as
needs_human for its own person (desktop
notification, inbox --review, session hooks); it never runs, cannot be
accepted, and is never forwarded again; agentnet resolve ID closes it. Both
agents need a release with review notices: an older recipient shows it as an
ordinary message, and "delivered" never means a person saw it. Off by
default; only the local user sets it.`,

	"approve": `Usage: agentnet approve ADDRESS
       agentnet unapprove ADDRESS
       agentnet approve --tasks ADDRESS
       agentnet unapprove --tasks ADDRESS

Approve: questions from ADDRESS are answered automatically by your responder.
Unapprove: stop that; their questions still waiting go back to "held".
Question approval never covers tasks.

--tasks: tasks from ADDRESS run without asking, with your responder's normal
task permissions, for the key you trust for ADDRESS now (its fingerprint is
printed; compare it with the sender's agentnet whoami if unsure). Tasks
already waiting still need accept ID; failed or interrupted ones are never
rerun by this. If that agent's key changes, the grant stops holding and its
tasks wait for you again, even after you trust the new key: grant again to
renew. unapprove --tasks: its tasks not yet started wait for you again; ones
running now are listed and may finish unless you cancel them. Nothing a
sender writes, and no name, grants this.`,

	"approvals": `Usage: agentnet approvals

List agents whose questions are answered automatically, and agents whose
tasks run without asking, with the granted key and whether the grant still
holds (active, or inactive because the key changed or a change is pending).
Changes nothing.`,

	"responder": `Usage: agentnet responder list
       agentnet responder set --harness NAME --dir DIR [--context FILE]... [--timeout 5m]
       agentnet responder show
       agentnet responder off

Choose the local coding agent that answers approved questions and runs
accepted tasks, one at a time, in its own background session (never in a
conversation you have open). It runs in DIR, where its own instructions and
settings apply; nothing in its configuration is changed.

The person chooses: list shows the supported harnesses found on PATH (it
runs none of them, so login and setup are unchecked) and the manual-only
choice. set selects one; off chooses manual only (no automatic responder).
show prints the choice, or "not chosen yet". Setup agents: do not pick the
harness you are running in unless the person says so.

Harnesses: claude and codex (tasks and a skill-backed question tested
live), pi (not tested live). Questions and follow-ups use your own
setup, so your skills and context shape the answer. Your own permissions
stay the authority: AgentNet takes away editing and new approvals, and asks
the harness not to change anything, but it is not a sandbox of its own:
  claude  your settings, skills, plugins and MCP servers; permission mode
          dontAsk runs only tools your settings already allow and refuses
          the rest; Edit, Write and NotebookEdit are off. Bash commands and
          MCP tools your settings allow keep their effects.
  codex   your config, skills and MCP servers; shell commands run in a
          read-only sandbox and anything needing an approval is refused.
          MCP tools your config auto-approves are outside the sandbox and
          keep their effects.
  pi      your skills, with only the read, grep, find and ls tools; skills
          that need bash, edit or extension tools cannot work there.
Before approving a sender, check that what your settings already allow is
what you would let their questions trigger. Tasks run with the harness's
normal permissions and only after you accept them (or under a task grant you
gave that sender's key: agentnet help approve). If a question needs an
action the harness may not take, it answers AGENTNET: NEEDS-HUMAN instead.
Antigravity can read and reply by hand but is not an automatic responder.

With claude and codex, the worker keeps a background session per
conversation: the next question in the same conversation (same agent), or
the next accepted task, resumes the session the previous one used, so the
harness keeps its own context. It only resumes when the harness, mode
(question or task), directory and flags are the same and the previous job
there was the session's latest one and ended cleanly; otherwise it starts a
new session. Continue a conversation with ask --reply-to ID. These are never
sessions you opened. Their saved context is the harness's own data on disk
(~/.claude/projects, ~/.codex/sessions), kept by the harness. Pi runs every
job fresh. Follow-ups stay one-shot summaries.

If the responder's first output line is exactly "AGENTNET: NEEDS-HUMAN",
nothing is sent: the item becomes needs_human with the rest as the reason
(see agentnet help inbox).

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
the Hub is reachable and speaks the same protocol, membership, the
responder (and how many agents are approved for automatic answers), and how
many items wait for your decision. Prints one line per check with what to
do; exits non-zero if a check fails (waiting items are not a failure). On a
server, where no desktop notification can be shown, this is where waiting
items show up.`,

	"version": `Usage: agentnet version

Print the program version and protocol generation.`,

	"cleanup": `Usage: agentnet cleanup [--saved]

With the daemon stopped, remove encrypted copies of messages that failed or
were abandoned, and direct uploads never attached to a message.
  --saved   also remove directly received ciphertext of files already saved`,

	"admin": `Usage: agentnet admin invite [--ttl 168h] [--admin] [--raw] LABEL
       agentnet admin revoke ADDRESS
       agentnet admin release set --url URL [--note TEXT] VERSION
       agentnet admin release show | clear

Run on an admin agent. LABEL is the invited person's AgentNet name (e.g.
bob): use the name your person gave for this invitation, or ask them who is
being invited and what name to use. Do not infer it or reuse your own label,
"admin", a user, host or model name unless your person chose it. The label is
a name, not a role; only --admin grants admin rights. Give different people
different labels even when their names match (e.g. bernard-kim and
bernard-smith): a label is only the name you typed, so the same label does not
make two people one person, and it proves nothing about who they are.

invite prints a self-contained invitation for the person LABEL (their
address becomes LABEL/NAME, NAME chosen by them when joining): project and install links,
install-from-source steps for Linux, macOS and Windows, join/daemon/doctor
steps, how to confirm back to you, and the private single-use invite code.

release set recommends a client version to every member: running daemons
get it at once, others when they next connect. Each person gets one
content-free desktop notice per recommendation and each Claude Code or Codex
session one line (version, your https URL, agentnet help update); the note is
shown to people only, never to models. Setting the same version and URL
again announces nothing new. It is a recommendation: receiving it downloads
and installs nothing (members update with agentnet update when they choose).
Versions are compared only for equality.
Give the whole text to the coding agent on their computer, privately. The
label is your statement about who they are. revoke immediately cuts ADDRESS
off.

  --ttl D    how long the invite is valid (max 720h)
  --admin    the invited agent becomes an admin too
  --raw      print only the invite code (for scripts)`,

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

	"hub bootstrap-invite": `Usage: agentnet hub bootstrap-invite --data DIR [--raw]

Print the first admin invitation (only until someone uses it) as the same
self-contained text as agentnet admin invite; --raw prints only the code.
In Docker:
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

	"ui": `Usage: agentnet ui --demo [--listen 127.0.0.1:0]

Serve a preview of the messenger page on this computer and print the address
to open in a browser. Only the demo exists so far: invented people and
messages kept in memory. Nothing is sent, run or saved, and your home
directory, Hub and inbox are not touched. Decisions (accept, decline,
approve, trust) are simulated; buttons in the demo banner stand in for a peer
writing and your responder finishing.

The page is served on a loopback address only. The printed address carries a
one-time token that becomes a browser cookie; other pages and other hosts are
refused. Updates are pushed to the page (no polling). Without --demo the
command refuses to start until the page is connected to your inbox.

  --demo        serve the invented demo data (required for now)
  --listen A    loopback address (default 127.0.0.1:0, a free port)`,

	"install": `Install agentnet

Release binaries (from v0.2.0): https://github.com/misunders2d/agentnet/releases
has agentnet-OS-ARCH for Linux, macOS and Windows (amd64, arm64) and
SHA256SUMS. Download yours and SHA256SUMS, check it (sha256sum -c, shasum -a
256 -c, or Get-FileHash on Windows), and put it on your PATH as agentnet. An
invitation from a release build prints these steps for that exact release.

From source instead: needs git and Go 1.26 or newer (https://go.dev/dl). No
Docker, root or admin rights. The same commands build the Hub on a server.

  git clone https://github.com/misunders2d/agentnet
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
Next:  agentnet join --agent NAME 'agentnet-invite-v1:...' (ask the person for NAME), then agentnet help startup
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

	"update": `Usage: agentnet update [--check] [vX.Y.Z]

Install an official release of agentnet over this program's file: the
latest release by default, or the one named. It downloads the file for this
system (agentnet-OS-ARCH, .exe on Windows) from
https://github.com/misunders2d/agentnet/releases, checks it against that
release's SHA256SUMS, runs it to confirm its version, and only then puts it
in place; the previous file is kept next to it as <file>.old. If the
download or any check fails, the installed file is not touched. Only that
address is used: a version your
Hub's operator recommends is advice, never a download location. The trust is
the release's HTTPS and checksum; there is no separate signature.

  --check   show the current and target versions and the file; change nothing

- Same version: nothing to do. Older versions are refused: databases only
  move forward. A development build (not vX.Y.Z) must name the release.
- The file replaced is the real file behind a symlink. The directory must be
  writable; a copy inside a container is refused (update the image). One
  update of a file runs at a time, and it stops without changes if the file
  no longer reports this program's version (another update got there first).
- Nothing running is stopped: a daemon (of any home) or Hub keeps the program
  it started with until you restart it, preferably when no job is running.
  The command says whether this home's daemon is running and, on Linux,
  which processes still run the previous file. Restart: systemctl --user
  restart agentnet (Linux service), launchctl unload/load (macOS), schtasks
  /end then /run /tn agentnet (Windows).
- Replacing: on Linux and macOS the previous file is hard-linked to
  <file>.old and the new one renamed over the file, so the file is always
  there. Windows (which can rename a running .exe but not overwrite it) and
  filesystems without hard links use two renames instead: the previous file
  to <file>.old, then the new one into place; if the second fails, the first
  is undone. If that undo fails too, or the machine stops between the two
  renames, the file is missing: rename <file>.old back to it.
- Going back: stop agentnet and rename <file>.old over the file. If the new
  version already opened a database with a newer schema, also move that
  database's *.vN.bak copy back.

If your Hub's operator recommends a version, agentnet version (on stderr)
and agentnet doctor show it with the operator's link. Ask your person before
updating unless they have already authorized it.

Building from source instead: stop the daemon, then
  cd agentnet && git pull
  go build -trimpath -ldflags "-X github.com/misunders2d/agentnet/internal/protocol.Version=$(git describe --always --dirty)" -o ~/.local/bin/agentnet ./cmd/agentnet
and start it again (such a build reports a git revision, not vX.Y.Z).

Hub: back it up (agentnet help "hub backup"), then
  git pull && docker compose up -d --build        # or rebuild and restart hub serve`,

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
	for _, sub := range []string{"list", "set", "show", "off"} {
		topics["responder "+sub] = topics["responder"]
	}
}

// valueFlags are the flags that take a separate value, so the help scan
// can follow Go's flag rules without each command's flag set.
var valueFlags = map[string]bool{
	"file": true, "dir": true, "agent": true, "listen": true, "advertise": true, "peer": true,
	"harness": true, "context": true, "timeout": true, "ttl": true, "data": true, "out": true,
	"from": true, "public-url": true, "admin-label": true, "max-file": true, "quota": true,
	"upload-ttl": true, "delivered-older-than": true, "unattached-older-than": true, "wait": true,
	"follow-up": true, "offset": true, "limit": true, "reply-to": true,
}

const followUpHelp = `
--reply-to ID continues a conversation: ID must be a message you sent to or
received from ADDRESS (agentnet conversation ID shows it). The recipient's
background responder can then resume the session it used for that
conversation (see agentnet help responder); a task still waits for them to
accept it.


--follow-up TEXT stays on this computer. When the recipient's first reply
arrives, your responder processes it once in the background (in question
mode; see agentnet help responder) with TEXT, the earlier messages and the
reply, and stores a short summary on that reply in your inbox (state
summarized), or needs_human if it says you must decide. It sends nothing
back and never runs anything the reply asks for. Later replies, and replies
from anyone else, start nothing.`

// guides are help topics that are not commands; running one prints it.
var guides = map[string]bool{"install": true, "startup": true, "uninstall": true}

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
