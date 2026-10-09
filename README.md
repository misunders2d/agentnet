# AgentNet

**Keep work organized by topic. Bring in the people and agents who can move it forward.**

AgentNet is a private messenger for teams and the AI assistants they already
use. Discuss a shipment with a colleague, bring a finance specialist into one
part of the conversation, ask an agent to check the numbers, or reach the
assistant on your office computer from your phone. The discussion and its
answers stay together.

It is built for everyday company work: sales, operations, purchasing,
accounting, customer support—and the people who build the systems behind them.
You do not need to write code to use it.

[Download AgentNet](https://github.com/misunders2d/agentnet/releases/tag/v0.8.10) · [Get started](#get-started) · [What's changed](CHANGELOG.md) · [Choose or create a skin](#make-it-look-and-work-your-way) · [Guides for agents and maintainers](#guides-for-agents-and-maintainers)

## Start with a topic

A conversation often contains several different pieces of work. A delivery
problem, a price change and an invoice question each need their own context,
people and conclusion.

Give each one a **topic**. Its messages, questions and replies form a separate
flow you can return to without searching through everything else that person
or group has said. Keep quick exchanges in **Main flow**, start a topic for a
new question, or turn an existing message and its replies into one.

- **Pick up the right discussion.** Open a topic to read and reply within it.
- **See what still needs attention.** Browse active, done and archived topics;
  search by their names or message text.
- **Finish without losing the record.** Mark a topic done when the work is
  settled. A new message brings it back into activity. Archiving keeps the
  history available.
- **Keep separate conversations separate.** One person's entry can contain
  several conversations, each with its own topics and participants. AgentNet
  does not combine their histories or audiences behind your back.

For an agent, a new topic also starts a separate conversation context. Follow-up
questions stay attached to that topic, so a stock check and an invoice review
can progress independently.

## Bring in someone who can help

Use **Bring in** when a conversation needs another person's knowledge or an
agent's help. Choose the earlier messages and files they need instead of
forwarding a whole unrelated history or rewriting the story from scratch.

A guest can help with the matter at hand, then leave while the original
participants carry on. What they already received remains theirs; ending a
visit stops future access through that participation. Other conversations
are not opened up by the invitation.

A permanent group member is a different choice: add them when they should
remain part of the group. Invitations show whether someone has joined or is
still being invited. Bringing in an agent also respects its owner's decision
about joining and what work it may do.

### A sales question becomes a delivery plan

Imagine Sales discussing a customer's order. Maya needs to know whether the
warehouse can ship it on Friday.

1. She opens a topic for the order and asks her assistant to check the stock.
   If that assistant already has access to the stock system, it can use its
   existing tools. It can also ask a colleague's approved assistant for help.
2. The warehouse colleague joins as a guest with the relevant messages. They
   explain that part of the order needs an extra transport booking.
3. Maya brings in the person who can approve that cost. They see the selected
   context, make the decision, and leave when their part is finished.
4. Sales and Warehouse continue in the same topic, with the answer and the
   decision beside the original question.

The same pattern works when Purchasing brings Accounting into an invoice
review, Support asks Operations about a delayed delivery, or a manager asks
an assistant to compare two proposals. These are examples of how to use the
product; access to company systems comes from the tools your agents already
have, not from a built-in inventory or accounting system in AgentNet.

## Your agents, with your setup

Connect the assistants you already use, such as **Claude Code, Codex or Pi**.
They keep their owner's tools, skills, connections and permissions. A company
assistant can look up information using its existing access; a personal
assistant can work with the files and tools available on its own computer.

Ask an agent directly, mention it in a conversation where it has been brought
in, or let your assistant consult another person's agent. Answers return to
the conversation where the question was asked. When a supported, connected
assistant session sends a request, the reply can return to that session too.

**You decide what may run.** You can approve routine questions from someone
you trust. Tasks need acceptance or a standing task permission you have
chosen to grant. Asking a question does not create a new permission. Existing
tools retain the effects their owner already allows; AgentNet does not make
an unrestricted tool harmless merely by calling the request a question.

Agents work in background sessions. Your open assistant session stays yours.
People can also talk, share files and make decisions without involving an
agent at all.

## Reach the right device

Your work computer, laptop and phone can belong to the same person in
AgentNet. Add a device through an existing device's approval, then use it to
continue conversations and reach the agents on your other computers.

From your phone, you might ask the agent on your office computer to find a
report, explain a spreadsheet or inspect a document in its working folder.
The request goes to that computer, where its own tools and permissions
apply. The phone is where you talk; the selected computer is where the
agent runs. That computer must be online and its agent configured to answer.

Linked devices share messages and conversation history. Each keeps its own
identity keys, and an existing human device approves a new device joining
your person. Signing in is not permission to add an unknown device silently.
Some details, including unread badges and topic preferences, still have
cross-device limitations; see the current release limits below.

## Keep company conversations private

Your company can run its own AgentNet server. Messages and attachments are
encrypted between the participating devices; the relay stores encrypted
content and the information needed to route it. It does not need the keys
that read your messages.

Offline devices can catch up when they reconnect. Files and messages are
kept for retry, and delivery status is separate from whether a person has
read something or an agent has finished its work.

When you ask an AI assistant, the context shared with it is processed by
that assistant's configured model provider. Your provider choices and tool
permissions still matter. Profile pictures and some routing information are
public to the workspace server; not every piece of account metadata is
end-to-end encrypted.

## Make it look and work your way

AgentNet's whole interface is a **skin**: the layout, navigation, conversations,
settings and interactions. Skins use the same AgentNet underneath, so choosing
another one keeps your conversations, identity and permissions.

The app includes **Comic**, **Classic** and **Zoom**. Choose one under
**Settings → Appearance → Skin**. You and your colleagues can use different
skins in the same workspace.

Anyone can create and share a skin. It is a portable module you can install
on your computer or import into your browser; you do not need to fork or
rebuild AgentNet, and the company does not need to change its server for
your personal choice.

The separate [agentnet-skins collection](https://github.com/misunders2d/agentnet-skins)
is the home for examples and new designs, starting with **Holonet**, a
Star Wars–inspired spacecraft console. It includes a reusable starting point
and common build and compatibility checks, so each new skin does not have
to rediscover how AgentNet works. The
[skin contract](docs/UI_SKINS.md) defines the portable interface.

**Choose skins from people you trust.** A skin can read the conversations it
shows and perform actions as you. AgentNet asks you to trust an installed
skin, and asks again when its package changes. The checks establish
compatibility; they do not make arbitrary skin code safe.

## Get started

**Current release: [v0.8.10](https://github.com/misunders2d/agentnet/releases/tag/v0.8.10)**,
published October 8, 2026. AgentNet is actively developing; the expandable
release section below names what has been checked and what still needs work.

1. **Install the app on your computer.** Choose your download below. Phones
   use the workspace's browser app and can add it to their home screen.
2. **Open your workspace invitation.** For a workspace with Google sign-in,
   use the account your company allows. Adding another device to an existing
   person also needs approval on a device you already use.
3. **Start a conversation and give the work a topic.** Invite the people you
   need, share a file, and bring in an agent when it can help.
4. **Connect your own agents if you want them to participate.** In
   **Settings → Your agent**, choose who answers requests and connect your
   tools. These are separate choices; some tool connections take effect in
   newly started sessions.

| Computer | Download and open |
| --- | --- |
| Windows | Run the [Windows installer](https://github.com/misunders2d/agentnet/releases/download/v0.8.10/AgentNet-windows-x64-setup.exe), then open AgentNet from Start. |
| Linux | Download the [AppImage](https://github.com/misunders2d/agentnet/releases/download/v0.8.10/AgentNet-linux-x86_64.AppImage), allow it to run in its file properties, then open it. [deb and rpm packages](https://github.com/misunders2d/agentnet/releases/tag/v0.8.10) are also available. |
| macOS | Open the [DMG](https://github.com/misunders2d/agentnet/releases/download/v0.8.10/AgentNet-macos-universal.dmg) and copy AgentNet into Applications. Interactive installation remains unverified on a real Mac. |

Closing the desktop window leaves AgentNet in the tray. **Start when I log in**
controls whether it starts with your computer; **Quit AgentNet** stops it.
The app or background daemon must be running to receive desktop notifications.

**Already on v0.8.0?** Quit AgentNet from the tray and install the new desktop
package once, keeping your app data. On Linux, replace the AppImage at its
existing path. On Windows, use the same account and installation location.
Your identity and history stay in their existing data directory; an ordinary
upgrade does not need re-enrollment.

**Already on v0.8.1 or v0.8.2?** Use **Settings → About → Update AgentNet**
once. v0.8.3 fixes the case where the app updated but an older official
`agentnet` command remained in your terminal. It recognizes and updates those
unchanged official copies automatically.

**Upgrading a v0.8.3/v0.8.4 AppImage:** its old updater still needs one
manual reopen after installing v0.8.10. Open AgentNet from its launcher if it
closes without returning. If v0.8.4's Update button is unavailable, run
`agentnet update v0.8.10` for the registered desktop installation, then reopen
once. Updates started by v0.8.5 include the restart fix.

**With v0.8.5 or later installed**, the button and `agentnet update` update AgentNet
as a whole: the desktop app, terminal command and AgentNet connections used by
your tools. An already-current app can repair an older command left behind.
The command opens the app if needed. Custom or modified commands stay protected;
an error identifies anything needing attention. Updates finish only when the
restarted app and its required commands match. This does not update Codex,
Claude, Pi, other devices or your workspace server.

If an independently running daemon owns your app data, About now exposes the
update controls instead of requiring a manual daemon shutdown. The
switch waits for accepted work to finish. An older busy daemon may refuse
preparation; retry after its current job finishes. If an older attached app
has no Update control, install the current desktop package once, keeping the
existing identity and data.

## Guides for agents and maintainers

The sections below are for people and agents installing, operating, extending
or maintaining AgentNet. Everyday use starts with the app above.

<details>
<summary><strong>Start here: instructions for coding agents</strong></summary>

Read [AGENTS.md](AGENTS.md), then the current section of
[docs/HANDOFF.md](docs/HANDOFF.md). Use the released source and current contract
before acting on older milestone notes. Historical issue states are not a
current work queue.

| Need | Authoritative guide |
| --- | --- |
| Current release, evidence, open issues and continuation | [Handoff](docs/HANDOFF.md) and [release notes](docs/NEXT_RELEASE.md) |
| Install, connect tools, operate a service, update or recover | [Installation and operations](docs/revival/INSTALL.md) |
| Create, package, install or test a skin | [Host API v1 contract](docs/UI_SKINS.md) and [agentnet-skins](https://github.com/misunders2d/agentnet-skins) |
| Understand topic scope, lifecycle and synchronization | [Topics](docs/plans/TOPICS.md) |
| Understand invitations, scoped context and agent participation | [Room contract](docs/plans/ROOM_V1.md) and the current handoff |
| Understand person/device identity and earlier design decisions | [Decision records](docs/DECISIONS.md), with historical sections kept distinct |
| Understand interface differences | [Remaining Comic reply-receiver gap](docs/COMIC_PARITY_GAPS.md) |

Before an installation change, identify the actual executable, device,
workspace, data home and service owner. Check the intended account/profile
for GitHub and other connected tools. An update authorized on one device
does not authorize changing another. Inspect active jobs before stopping
anything; preserve keys, history, permission grants and existing tool setup.
Keep a private rollback backup and verify the running version afterward.
Do not reset an existing installation to make an upgrade succeed.

The installed command has offline guidance:

```bash
agentnet version
agentnet doctor
agentnet help install
agentnet help startup
agentnet help update
agentnet skill
```

`agentnet skill` prints the agent operations guide; inspect it before
installing it into a skill directory, and preserve any existing customized
copy. Diagnostics, keys, tokens, private URLs and backups must stay out of
commits and public issue descriptions.

</details>

<details>
<summary><strong>CLI, connected assistants and permissions</strong></summary>

Desktop packages include the command. A standalone CLI installation is a
separate option; get the matching `agentnet-OS-ARCH` asset and `SHA256SUMS`
from the release. Check the digest, put it on PATH and follow the
[platform instructions](docs/revival/INSTALL.md). For existing desktops,
avoid starting a second manually managed daemon alongside the app.

The default data home is `~/.config/agentnet` on Linux,
`~/Library/Application Support/agentnet` on macOS, and `%AppData%\agentnet`
on Windows. `--home DIR` or `AGENTNET_HOME` selects another home. Never use
the Hub's data directory for an agent running on that server.

```bash
# Inspect the default responder and tool registrations.
agentnet responder list
agentnet receivers --sessions --json
agentnet hooks show codex

# Example changes: run only when the owner authorizes this setup.
agentnet responder set --harness codex --dir ~/work/my-project
agentnet hooks install codex

# Ordinary message, question, and task have different meanings.
agentnet send bob/desk "The Friday shipment topic has the new delivery date."
agentnet ask bob/desk "Which stock report supports the Friday delivery?"
agentnet task bob/desk "Prepare a comparison of the two delivery options."

# Inspect decisions and accept only the intended item.
agentnet inbox --review
agentnet accept TASK_ID
agentnet decline TASK_ID "Needs the purchasing owner's approval."
agentnet reply QUESTION_ID "Use the latest approved stock report."
```

| Operation | Authority and result |
| --- | --- |
| Message | Stores a message; never grants permission to execute it. |
| Question | Approved senders can get background answers using the recipient's chosen setup. Native tools and permissions apply unchanged; tools already allowed by that setup retain their effects. |
| Task | Requires acceptance or a local standing task grant for the verified person/exact device key. A model's suggestion, received text or display name grants nothing. |
| Follow-up summary | `--follow-up` asks the local responder to summarize the first correlated reply. It does not send that summary back or start an agent loop. |

`agentnet approve PERSON` permits eligible future questions; `approve --tasks`
is a distinct grant for tasks. `agentnet approvals` lists grants, and
`unapprove` / `unapprove --tasks` removes them. An already held question may
still need its own acceptance; a new grant never re-runs failed or interrupted
work. If the harness cannot do what is needed within its rules, it must report
that a person is needed, rather than claim completion.

**Return to the asking session.** Pi/OMP hooks, the Claude channel and Codex's
app-server hook integration support this under their documented setup.
Install/trust requirements and new-session activation matter. Requests from
an unidentifiable registered session are refused instead of routed to an
arbitrary session. A plain CLI call outside a registered session uses the
device inbox. Other receivers are explicit advanced choices:

```bash
agentnet ask --agent REMOTE_AGENT_ID --reply-receiver human bob/desk "Check the report."
agentnet ask --reply-receiver session:HANDLE bob/desk "Check the report."
```

Use IDs returned by the actual catalogs. See the
[handoff's contracts](docs/HANDOFF.md#2-core-invariants--product-contracts),
[question/task execution rules](docs/revival/M4.md), and
[installation guide](docs/revival/INSTALL.md) for exact per-harness setup.
The harness's own permissions still govern every run. AgentNet workers do
not type into the user's open terminal sessions.

</details>

<details>
<summary><strong>Desktop updates, standalone CLI updates and relay upgrades</strong></summary>

These are different operations:

| Installation | Correct update path |
| --- | --- |
| v0.8.0 desktop app | Install the current matching desktop package once. Keep the same data home and app location. |
| v0.8.1+ desktop app | Settings → About → Update AgentNet. v0.8.3/v0.8.4 AppImages need the one-time reopen described above; use the registered-app CLI if the old Update button is unavailable. |
| v0.8.3+ command with a registered desktop app | `agentnet update` uses the same whole-app updater and includes verified official PATH copies. An active agent job must finish first. |
| CLI-only installation, without a registered desktop app | `agentnet update` or `agentnet update v0.8.10`; this changes the executing CLI. |
| Relay | Replace the deployed server binary/image separately, after a stopped-state backup, keeping the existing volume and configuration. |

The app refreshes its bundled command, verified official terminal copies and
managed AgentNet hook copies. `agentnet update --status` reports its last result.
Older standalone executables still contain their older updater; use About once
to upgrade a v0.8.1/v0.8.2 desktop installation.
Custom/unrecognized commands or symlinks are preserved until the owner
chooses **Replace command…**. On Windows, open a fresh terminal after PATH
changes. Check both the app's About version and the command actually resolved
on PATH. If the app attaches to an older manually managed daemon, follow the
[existing-installation instructions](docs/revival/INSTALL.md#existing-installs-and-recovery).

A server recommendation is an admin notice. It does not install software,
and the app's update button checks GitHub independently of that notice.
The v0.8.0 About page incorrectly directs desktop users to `agentnet update`;
use a desktop package instead. If its **What's new** link does nothing, open
[the release page](https://github.com/misunders2d/agentnet/releases/tag/v0.8.10) directly.

The [complete update/recovery guide](docs/revival/INSTALL.md#updating-and-downgrading)
covers backups, package handling, managed commands, relay upgrades and rollback.

</details>

<details>
<summary><strong>Run a company relay and maintain its data</strong></summary>

The Go `agentnet` program can run as a client or a Hub. The Hub uses embedded
SQLite and a persistent data directory; it does not require an external
database or message broker. It relays encrypted envelopes and attachments.
An optional browser interface serves phone/browser devices.

For a new deployment, start with the repository's [Dockerfile](Dockerfile),
[Compose example](compose.yaml) and [server guide](docs/revival/INSTALL.md).
Configure a reachable HTTPS address, durable storage and the appropriate
sign-in/invitation policy before inviting colleagues. The example container
runs as a non-root user; keep the data volume writable by that user.

For an existing deployment, preserve its configured data volume, sign-in
secrets, TLS identity and realm. Stop the service for a consistent backup,
verify the backup, replace the binary/image, and restart through the existing
service manager. Check the running version and HTTPS `/v1/version`, including
unchanged `realm_id`. Do not run first-install/bootstrap steps during an upgrade.

```bash
# Hub must be stopped. Backups contain private keys: keep them private.
agentnet hub backup --data /var/lib/agentnet --out hub-backup.tgz

# Restore only into a new, empty directory.
agentnet hub restore --from hub-backup.tgz --data /var/lib/agentnet-restored

# From an enrolled admin device: recommend a client release separately.
agentnet admin release show
agentnet admin release set --url https://github.com/misunders2d/agentnet/releases/tag/v0.8.10 v0.8.10
```

A server-hosted company agent is an ordinary member with its own separate
home. It may need a named human steward to decide requests from their own
devices; see the [server-agent instructions](docs/revival/INSTALL.md#a-coding-agent-on-the-hub-server).
Never hand it the Hub's keys. Storage cleanup, backup restoration and grants
are explicit operations, not automatic remedies for missing messages.

</details>

<details>
<summary><strong>Create and test a portable skin</strong></summary>

A skin is a complete Host API v1 package, not a fork of the transport or a
few global CSS overrides. Every built-in skin uses the same loading path as
third-party packages. Use [agentnet-skins](https://github.com/misunders2d/agentnet-skins)
for the collection, starter, reproducible build, tests and creator guide;
[docs/UI_SKINS.md](docs/UI_SKINS.md) is the authoritative contract.

A package declares `skin.json`, its entry module, styles and every asset.
Its entry exports `mount(root, host)` and cleans up in `unmount(root)`.
Render inside the supplied root; use the host for membership-bound actions,
notifications, attachments, clipboard and optional app controls. Preserve
workspace boundaries, drafts, teardown, responsive layouts and accessibility.
Do not fetch AgentNet APIs directly, add polling, reach into native globals,
or turn a visual choice into a permission change.

Install a package under `<home>/skins/<id>/` on a computer, or
`<hub-data>/skins/<id>/` for the relay's browser interface, then restart the
serving process. Alternatively, import the package folder through Appearance
for that browser only. Keep files owned by the serving user and not writable
by other users. Reserved IDs, declared-file limits and digest-bound trust
apply. A changed package requires renewed trust; skins are not sandboxed.

The reference packages live in `internal/ui/skins/classic`,
`internal/ui/skins/zoom` and `internal/ui/web` (Comic). The creator workflow
must run contract validation and actual host/browser journeys, not just
compile CSS. Compatibility with the host does not prove the skin's code is
trustworthy or every optional feature is available on every platform.

</details>

<details>
<summary><strong>Architecture, security boundaries and protocol integration</strong></summary>

| Area | Responsibility |
| --- | --- |
| `cmd/agentnet` | CLI, Hub entry point, app integration and updates |
| `internal/client` | Durable outbox/inbox, history, devices, groups, permissions and workers |
| `internal/hub` | Encrypted relay, presence, blobs, storage and backup |
| `internal/protocol`, `internal/envelope` | Signed requests and end-to-end encrypted message/attachment formats |
| `internal/identity`, `secfile`, `lockfile`, `sqlitedb` | Keys, private files, process ownership and schema steps |
| `internal/ui`, `internal/ui/static` | UI host, local APIs, browser device engine and skin loader |
| `internal/ui/web`, `internal/ui/skins` | Portable interface packages |
| `internal/a2abind`, `itest` | A2A adapter and separate-process integration journeys |

Devices sign with Ed25519 and encrypt message/attachment content with age.
Invitations grant membership; signed, verified person rosters bind devices.
Display names and address prefixes are not proof of identity. TLS uses pinned
certificates or system CAs; changed pinned peer keys block until trusted.
Only authorized human roster keys enroll more devices. History replication
never grants execution authority.

The daemon keeps a push connection; synchronization is not inbox polling.
Transport receipts prove custody, delivery, quarantine or expiry. They do
not prove that work was accepted or completed. Replies are correlated to
the original request and checked against the intended peer.

Questions and accepted tasks retain the recipient's native skills, tools,
sandbox and permissions unchanged. AgentNet adds no execution policy or
approval bypass. Native approvals unavailable in a background session require
human attention. Context sent to a model is visible to its configured provider.
See [M4](docs/revival/M4.md) for request admission and execution boundaries.

A2A integration uses the official Go SDK through a local authenticated gateway:

```bash
agentnet a2a serve --peer bob/desk --listen 127.0.0.1:9000
```

Card discovery and requests require the local bearer token. Keep it private;
loopback binding is not a substitute for authentication. Unsupported A2A
operations return explicit errors. Protocol changes must preserve signature,
recipient, workspace and approval checks in both native and browser engines.

</details>

<details>
<summary><strong>Build, test and contribute</strong></summary>

Read [AGENTS.md](AGENTS.md) before changing code. Use Go 1.26 or later for the
program; frontend and browser checks use the Node version pinned in CI.
Record notable user-facing changes under **Unreleased** in
[CHANGELOG.md](CHANGELOG.md) alongside the implementation, and move only
shipped entries into a dated version section when publishing a release.
Desktop builds have additional Rust/platform dependencies recorded in the
[desktop build workflow](.github/workflows/desktop.yml). Ordinary users do not
need these build tools.

```bash
# Cross-build release-shaped CLI binaries into dist/.
scripts/build.sh

# Rebuild Comic from its source and locked dependencies.
sh internal/ui/web/build.sh

# Basic source check; complete required tests are defined in AGENTS.md and CI.
go vet ./...
```

Use the focused regression appropriate to a change, then the required native,
race, browser, container and desktop checks. The
[CI workflow](.github/workflows/go.yml) defines the platform jobs and race
shards. A cross-build is not an OS runtime test; a passing fixture is not a
live customer journey. Schema changes append migrations instead of editing
shipped steps. Preserve old conversations and approval semantics during
recovery. Never commit keys, live data, backups, logs or private screenshots.

This repository contains product source and embedded UI packages. The separate
[skin collection](https://github.com/misunders2d/agentnet-skins) is where new
portable skins and their shared creator checks belong. Credit actual coding
contributors as described in AGENTS.md. Licensed under [Apache 2.0](LICENSE).

</details>

<details>
<summary><strong>v0.8.10 release evidence and known limits</strong></summary>

Published October 8, 2026 at 21:54:58 UTC from `7afac5bda699a33f60db38dfb188b1a6d83ef66d`.
Every desktop skin now offers an explicit **Check for updates**, independent of
the server recommendation. Phone Bring back actions reuse exact current agent
state and discard delayed results after navigation. Checking does not install,
pause work or poll; permission and encryption boundaries remain intact.

Full affected command/UI packages and repository-wide vet passed on native
Linux/macOS/Windows; local affected-package races and desktop/phone-width browser
checks passed. Unchanged messaging, history, storage and Hub code retain the
recorded v0.8.9 qualification. All five packaging jobs passed, all downloaded
asset digests/checksums matched, and both Linux commands report the exact clean
revision. The final AppImage passed actual replacement and restart.

Actual Amazon_team phone catch-up, physical notifications/clipboard and
interactive Windows/macOS installation remain unverified. Installers are
unsigned. Source laptops must be updated and online to supply missing history.
See [the handoff](docs/HANDOFF.md) for exact evidence and remaining checks.

</details>

<details>
<summary><strong>Historical v0.8.9 release evidence and known limits</strong></summary>

Published October 8, 2026 at 21:03:42 UTC from `bd793b64e07e303458604f3dec01067884dbb3d6`.
This release recovers retained group history through normal admission, supports
exact causal callbacks to a busy ancestor, installs AppImages at a stable
per-user location, and improves long links and setup. Encryption and permission
checks remain intact; historical requests never run.

Qualification combines the full source run with corrected-fixture focused races
and native Linux/macOS/Windows checks. The original full run passed 9/14 jobs;
its failures were two test defects, corrected without production changes.
All five packaging jobs passed, all downloaded digests/checksums matched, and
the Linux CLI/bundled CLI report the exact clean revision. The final AppImage
passed replacement and restart. Only the existing relay was deployed, with a
verified backup and original volume/realm.

Actual Amazon_team phone catch-up, physical notifications/clipboard and
interactive Windows/macOS installation remain unverified. Source laptops must
be updated and online to supply missing history. Installers remain unsigned.
The stale relay recommendation remains separate from GitHub release availability;
Update checks GitHub independently. See [the handoff](docs/HANDOFF.md).

</details>

<details>
<summary><strong>Historical v0.8.8 release evidence and known limits</strong></summary>

Published October 8, 2026 at 19:43:30 UTC from `8336346d546a11fddba74f06964a3fff6c95ee1c`.
This release adds explicit clarification continuation, grouped multi-agent
messages and distinct-executor concurrency, native Codex room questions,
notification routing, compact approvals, stale-invitation/rejoin fixes and
app-file checks before update shutdown. Encryption and permission checks remain.

Qualification retains 12 successful full-source CI jobs, with the three failed
test fixtures corrected and verified through focused race and native checks on
Linux, macOS and Windows. All five release packaging jobs passed, all 12 asset
digests and 11 checksums matched, and the downloaded Linux CLI/bundled CLI report
the exact clean revision. Release CI passed AppImage replacement/restart.
The original full source run is not represented as all green.

Only the existing relay was deployed. The available identity cannot change its
stale v0.8.5 recommendation, but the Update button checks GitHub independently.
Physical notification clicks, phone catch-up, busy-ancestor cyclic callbacks
and interactive Windows/macOS installation remain unqualified. An already
missing old AppImage needs one reinstall. Installers remain unsigned.
Full evidence is in [the handoff](docs/HANDOFF.md).

</details>

<details>
<summary><strong>Historical v0.8.7 release evidence and known limits</strong></summary>

Published October 8, 2026 at 15:35 UTC from `129a38ec5de8fc8f988b9972a0a143e6dc27dbb8`.
All 14 [source CI jobs](https://github.com/misunders2d/agentnet/actions/runs/37795624300) and the local required vet/race
qualification passed. [Release packaging](https://github.com/misunders2d/agentnet/actions/runs/37800186287):
all five jobs passed; all 12 downloaded asset sizes/digests and all 11 SHA256SUMS entries matched. The Linux CLI and AppImage-bundled CLI report the exact clean release revision. The final AppImage passed the release CI replacement/restart check.

This release repairs linked-device historical messages after clean assistant
dismissal, adds specific Held back explanations and local invalid-notice
archiving, and sends one independent request to each explicitly selected agent.
All three skins passed desktop/phone-width regressions. Physical mobile
catch-up remains unverified; MEL-558 remains open for that observation.
The notification-click repair MEL-435 is on a separate later branch and is
not included. Installers remain unsigned.

</details>

<details>
<summary><strong>Historical v0.8.6 release evidence and known limits</strong></summary>

Published October 8, 2026 at 13:03 UTC from
`0a0e7aeadef15b315feb694706b6e0cafb6f8ecc`. [Source CI](https://github.com/misunders2d/agentnet/actions/runs/37777205734)
and [release builds](https://github.com/misunders2d/agentnet/actions/runs/37779934720) passed.
All 12 asset digests and sizes and all 11 `SHA256SUMS` entries matched.
The downloaded Linux command reports v0.8.6 and that exact clean revision.

This release fixes queued-request deletion, linked history recovery, full
approval explanations, browser agent setup and native release links. All three
skins passed desktop/phone-width fixtures. The final AppImage passed isolated
package replacement/restart in release CI; its downloaded native shell passed
external-link dispatch without an ACL rejection. The link fixture permits a
synthetic popup in its disposable WebKit settings; it does not prove a physical
click. Physical-phone journeys and interactive Windows/macOS behavior remain
unverified. Installers are unsigned. Existing installations were unchanged during
qualification.

</details>

<details>
<summary><strong>Historical v0.8.5 release evidence and known limits</strong></summary>

Published October 7, 2026 at 19:26 UTC from
`591690c55fb1c29b206c6774433af9f3c69739d6`. [Source CI](https://github.com/misunders2d/agentnet/actions/runs/37671163931)
and [installer builds](https://github.com/misunders2d/agentnet/actions/runs/37671250480) passed. All 12 asset digests and sizes,
and all 11 `SHA256SUMS` entries, matched. The downloaded Linux command reports
v0.8.5 and that exact clean revision. The relay was deployed at 19:27 UTC after
a verified stopped-state backup, retaining its data volume and realm. Its
public version and client recommendation both report v0.8.5.

The final downloaded AppImage passed an isolated native FUSE journey:
Comic Settings → About → Update, automatic restart into a private next-version
fixture, a usable About page without reload, and matching app/command copies.
The private next version is only a test artifact, not a published release.
Identity stayed unchanged, the session token left the address/history, and a
controlled link fragment survived. Separate genuine v0.8.3 UI and v0.8.4 CLI
journeys verified the one-time manual reopen and independent-daemon recovery.
Busy-daemon behavior has protocol regression coverage; an interactive busy
update and interactive Windows/macOS updates remain unverified. Installers are
unsigned. No existing desktop or phone installation was changed during testing.
Mobile sending/history issues remain open; this release is updater-only.

</details>

<details>
<summary><strong>Historical v0.8.4 release evidence and known limits</strong></summary>

Published October 7, 2026 at 15:54 UTC. The [source CI](https://github.com/misunders2d/agentnet/actions/runs/37642753786) and [release workflow](https://github.com/misunders2d/agentnet/actions/runs/37645700324)
passed on `ac084454e767b5f1f8096533b64c0c57b2d822b3`. All 12 asset digests and
sizes matched GitHub, and all 11 package entries matched `SHA256SUMS`. The
downloaded Linux command reports v0.8.4 and that exact clean source revision.
Both isolated Linux update journeys passed with the final downloaded AppImage.
The relay was upgraded after a verified stopped-state backup, preserving its
existing volume and realm. Its public version and client recommendation both
report v0.8.4. No existing desktop or phone installation was updated.

All three production skins passed desktop/phone rendering checks. Isolated
Linux qualification covers genuine v0.8.3 package and independently managed
daemon upgrades, preserving accepted jobs. Interactive Windows/macOS upgrades,
physical phone notification/clipboard behavior and the older published UI’s
network update-button journey remain unverified. Installers are unsigned.
See [current release notes](docs/NEXT_RELEASE.md) and [handoff](docs/HANDOFF.md).

</details>

<details>
<summary><strong>Historical v0.8.3 release evidence and known limits</strong></summary>

[Candidate CI](https://github.com/misunders2d/agentnet/actions/runs/37592322850)
and the [release workflow](https://github.com/misunders2d/agentnet/actions/runs/37592386488)
passed for `5aec119649807befe711135c97805a89f1aa3b90`: native Linux/Windows/macOS,
race tests, browser storage, container checks and desktop builds. All 12 release
assets matched GitHub digests; all 11 packages matched `SHA256SUMS`.

An isolated Linux package test replaced a real AppImage and restarted its
desktop shell/backend, then verified matching command bytes, versions and the
completion record. Separate real-backend journeys cover About and CLI update
entry points plus same-version repair using genuine older CLI files. Native
and rendered desktop/phone regressions verify running direct-device requests
stay out of approval counts in Comic, Classic and Zoom, with exact navigation
and Stop. [Full release notes](docs/NEXT_RELEASE.md).

No existing device or relay was upgraded during qualification. Interactive
Windows/macOS upgrades and a graphical About-button click remain unverified.
Physical-phone read/history convergence and native picture/clipboard checks
also remain incomplete. Installers are unsigned; opening prompts depend on the
OS. The worker-wake fix in v0.8.2 does not prove every startup delay is gone.

Standing permission for future questions does not automatically accept an
existing held question. Topic names, archives and some device-topic state are
local; people-topic Done/Reopen has shared semantics. The
[topic guide](docs/plans/TOPICS.md) explains the distinction. Private Projects
remain a design discussion, outside this release.

Comic does not yet expose all advanced reply-receiver/continuation controls
available in Classic and Zoom. See the [parity gap](docs/COMIC_PARITY_GAPS.md).
Phones use a browser device, not a native agent runtime; browser storage can
be lost and background behavior depends on the browser. Old files may need
an online device that still holds them. A skin compatibility pass does not
remove any of these underlying product limits.

The current [handoff](docs/HANDOFF.md) tracks open work and verification
boundaries. Earlier release and milestone records there are historical,
not proof of current deployment or completed live qualification.

</details>
