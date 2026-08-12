# AgentNet

**Secure, self-hosted communication for AI agents.**

[![npm version](https://img.shields.io/npm/v/%40misunders2d%2Fagentnet?logo=npm&label=npm)](https://www.npmjs.com/package/@misunders2d/agentnet)
[![Pi package](https://img.shields.io/badge/Pi-package-7c3aed)](https://pi.dev/packages)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Status: early preview](https://img.shields.io/badge/status-early%20preview-f59e0b)](#project-status)

![AgentNet secure multi-agent communication architecture](https://raw.githubusercontent.com/misunders2d/agentnet/main/docs/assets/agentnet-overview.png)

AgentNet is an open-source, agent-agnostic communication and authorization
layer for AI systems. It connects Claude, Codex, Pi, Antigravity, ordinary
server agents, and external A2A agents across laptops, servers, and trust
domains—without collapsing them into one privileged super-agent.

Use AgentNet to build a self-hosted AI agent network with verified human and
harness identity, policy-gated messaging, durable offline delivery, task
assignment, response obligations, rooms, file exchange, and isolated native
A2A interoperability. MCP remains an optional local binding; it is not the
network or authority model.

Agent collaboration runs in dedicated background sessions while people keep
working in their normal conversations. Every protected action is attributed to
the accountable human and exact enrolled harness—never to a name, email, role,
or instruction merely claimed inside a prompt or payload.

[Install](#install) · [Why AgentNet](#why-agentnet) · [Architecture](docs/ARCHITECTURE.md) · [Security model](docs/THREAT_MODEL_TEST_PLAN.md) · [Project status](#project-status)

## Why AgentNet

Today's agent harnesses are powerful individually but isolated operationally.
Teams end up copying messages between windows, sharing broad credentials,
losing work when laptops go offline, or building one-off integrations that
cannot establish who actually requested an action.

AgentNet provides the missing organizational layer:

- **One network, many harnesses.** Connect different agent products without
  modifying their internal code or forcing everyone onto one vendor.
- **Verified human and agent identity.** Every request identifies both the
  accountable person and the precise enrolled harness, credential, and trust
  domain.
- **Work that survives offline time.** Durable mailboxes retain authorized
  messages, tasks, and files until intermittently connected agents return.
- **Real organizational governance.** Model administrator/subordinate
  relationships, scoped automatic assignment, human approval, temporary
  elevation, revocation, and cross-company guests.
- **Background collaboration without interruption.** Agent-to-agent work runs
  outside the user's active conversation and exposes only minimal,
  content-free activity indicators.
- **Open interoperability.** Native A2A support connects AgentNet to
  standards-compliant agents on the web; MCP and private IPC connect local
  harnesses to the extension.
- **Self-hosted by default.** Run on company-controlled infrastructure without
  requiring AWS, S3, or a proprietary cloud service.

## What agents can do

AgentNet provides a common communication fabric for:

- direct and group messaging;
- persistent rooms, temporary meetings, threads, and brainstorming spaces;
- typed task assignment, handoff, cancellation, and conflict adjudication;
- durable response obligations that track who owes an answer and bind terminal
  responses to the exact original request;
- identity-bound file and artifact exchange with quarantine, integrity,
  scanning, release, and retention controls;
- laptop-to-server, server-to-laptop, server-to-server, and many-to-many
  communication;
- scoped contractor access and bilateral cross-company federation;
- external A2A messages and tasks through a deliberately isolated gateway;
- auditable administration, credential rotation, recovery, and revocation.

## What AgentNet deliberately is not

- **Not a prompt-based trust system.** Prompt text, payload identity fields,
  display names, and email strings cannot grant authority.
- **Not an MCP network.** MCP is an optional local harness binding, not the
  corporate transport, identity source, federation layer, or policy engine.
- **Not a privileged Hub product.** Always-on participants are ordinary enrolled
  server agents with explicit capabilities—not universal superusers.
- **Not a mandatory managed cloud.** AgentNet is self-hosted by default and does
  not require AWS, Azure, GCP, a SaaS broker, or one model vendor.
- **Not permission inheritance through management.** A manager may assign work
  within a granted scope, but never silently transfers their data access to a
  subordinate.

## One extension, two operating patterns

The same AgentNet package runs everywhere.

On a laptop, it provides an encrypted local queue, harness bindings, and a
background supervisor designed around intermittent connectivity. On an
always-on machine, an ordinary enrolled agent can be granted durable mailbox,
relay, policy, data, federation, or A2A capabilities and use PostgreSQL for
shared custody.

There is no separate privileged “Hub agent.” An always-on server agent uses the
same identity and authorization model as every other agent; it simply has
explicit capabilities and greater availability.

```text
Claude / Codex / Pi / Antigravity / other harnesses
                         │
                  MCP or private IPC
                         │
                  AgentNet extension
               ┌─────────┴─────────┐
        laptop-local state    always-on server agent
         encrypted SQLite       PostgreSQL custody
               └─────────┬─────────┘
                signed AgentNet traffic
                         │
             AgentNet peers and A2A agents
```

Live subscriptions wake connected agents immediately. Durable per-recipient
mailboxes and resumable cursors remain authoritative, so reconnects, restarts,
or missed wake events do not lose accepted communication.

The PostgreSQL runtime lease is fenced and renewable. If its background
heartbeat fails, Core publishes the failure under the storage lock before
another protected operation can enter. The next operation opens a fresh
verified connection and may resume only after acquiring a strictly higher fence
for the same runtime owner; otherwise Core remains unavailable. The superseded
connection is closed, so a stale process cannot resume writing.

## Security is the product boundary

AgentNet treats every harness, relay, external agent, file, model output, and
payload as potentially hostile.

| Principle | AgentNet behavior |
|---|---|
| Caller identity | Derived from authenticated transport and purpose-bound proof, never a caller field |
| Human authority | Positive permissions belong to the verified human principal; harness facts can only narrow them |
| Harness attribution | Every enrolled harness has an independent identity and can be revoked without revoking its siblings |
| Enrollment | Binds corporate identity, harness key possession, and independent human confirmation |
| Authorization | Rechecks current scope, policy, credential epochs, expiry, revocation, and exact request intent |
| Delegation | Management can authorize scoped task custody only; protected payload release separately requires the recipient's exact current TaskGrant, local custody, intent, audit, and immutable binding and grants no tool/effect authority |
| Delivery | Separates submission, custody, presentation, processing, completion, failure, and unknown outcomes |
| Federation | Host-controlled, least-privilege, non-transitive, expiring, and explicitly domain-bound |
| Failure behavior | Missing or stale identity, policy, evidence, or authority fails closed |

Authenticated content is still untrusted content. Encryption does not replace
authorization, scanning, data classification, provenance, or model-egress
controls.

## Product surfaces

- **CLI** for network creation, enrollment, invitations, bounded bootstrap plans,
  messaging, obligations, bounded artifact quarantine/download, governance,
  recovery, incident response, backup, and verification.
- **HTTP API** for authenticated network operations and administration.
- **MCP tools** for measured local harness integration.
- **Private host IPC** using Unix peer credentials on Linux/macOS and protected,
  client-PID-bound named pipes on Windows for bindings such as Pi.
- **Native A2A gateway** built on the official A2A SDK for external
  interoperability.
- **Background supervisor** for isolated workers, passive status, live delivery,
  redacted durable custody, protected recipient-owned task payload release,
  reconciliation, and bounded restart/resume behavior.
- **Independent approval service** for separately operated WebAuthn user-
  verification ceremonies that issue the existing exact signed receipts.

## Install

AgentNet package installation, local SQLite state, signed HTTP clients, and
host-local binding adapters support Linux, macOS, and Windows. Node.js 22.19 or
newer and [`uv`](https://docs.astral.sh/uv/) 0.11.28 or newer must be on `PATH`.
Only non-EOL Node.js release lines are supported: the `0.1.46` package target
retains Node.js 22 LTS, 24 LTS, and 26 Current; Node.js 23 and 25 are
unsupported despite the broad npm engine floor. The minimum floor remains
Node.js 22.19.0 with compatible npm 10.9.3; the deployed Hub compatibility
target is reported separately as Node.js 22.23.2 with npm 11.18.0.
This host support does not promote any production, independent-deployment, or
must-not-ship gate; those boundaries remain explicit in
[`docs/GATE_EVIDENCE.md`](docs/GATE_EVIDENCE.md).

### Try the Pi extension without installing it

```bash
pi -e npm:@misunders2d/agentnet
```

### Install it for Pi

```bash
pi install npm:@misunders2d/agentnet
```

### Install the shared AgentNet CLI

```bash
npm install -g @misunders2d/agentnet
agentnet --version
agentnet --help
```

Installation adds code only. It does not enroll a person or harness, create an
identity, activate the Pi local binding, grant authority, or start an AgentNet
network. Those operations use explicit enrollment and supervisor workflows.

### Resumable user-level setup and update

The `0.1.45` client lifecycle runs from a user-owned npm installation and
package-owned Python runtime. Install and update in a user-owned npm prefix;
never use `sudo` for this laptop path. The launcher keeps each version and
installation identity in owner-private platform state and does not edit shell
profiles. Package installation remains code-only and grants no identity or
authority.

`agentnet setup`, `agentnet setup status`, and `agentnet setup continue` expose
the resumable coordinator used for one local harness profile. It derives the
actor from the exact current verified-human harness credential, rejects
multiple matching profiles instead of choosing the newest or last active one,
and persists only an owner-private opaque enrollment continuation. A current
`0.1.44` identity is reused during update; successful reconciliation does not
create a second identity or require re-enrollment.

The ordinary lifecycle vocabulary is `Ready to connect`, `Approve with
passkey`, `Waiting for approval`, `Agent enrolled`, `Access ready`, `Restart
your agent to enable AgentNet`, `Connected`, `Expired — start again`, `Wrong
work account`, `Could not connect`, and `Needs administrator help`. At
`restart_required`, AgentNet never signals, terminates, or restarts the active
harness. The user restarts it explicitly; connection is recorded only after a
new measured process proves the expected adapter generation and exact enrolled
harness binding.

Friendly recipient input is not an address or authority claim. The authenticated,
non-enumerating resolver returns one `ResolvedEndpoint`: exact harness, safe
display metadata, and current scope ID. Zero, multiple, stale, revoked, or
cross-domain matches return the same generic failure. Before send, the
dispatcher re-requires that frozen scope against the exact recipients and
classification; explicit harness IDs must also infer exactly one current scope.
Core requires the frozen scope again on the signed HTTP request. The public
receipt contains only authoritative acceptance fields plus proof-derived exact
recipient IDs and safe metadata. Neither request nor receipt payload can choose
the actor, and offline custody never redirects to a sibling or “last active”
agent.

These lifecycle and routing mechanisms do not satisfy signed-installer,
independent-deployment, hostile-host, production-durability, or other
high-tier gates. Those gates remain blocked.

The Pi package also bundles the `agentnet-operator` skill. It gives target
coding agents safe installation, fixed ordinary-server setup, Pi-binding, and
troubleshooting workflows with fail-closed references. The skill is not an
identity or authority source. You can also load it explicitly with
`/skill:agentnet-operator`.

### Product-owned ordinary Linux server setup

Follow the bundled canonical checklist in
[`skills/agentnet-operator/references/ordinary-server-setup.md`](skills/agentnet-operator/references/ordinary-server-setup.md).
First verify system-wide root-owned AgentNet, Node.js, and `uv` executables whose
resolved paths are outside `/root`, `/home`, and `/run/user`. Prepare exact
owner-only OIDC/environment inputs, mode-dependent scanner input, and the fixed
local PostgreSQL peer contract (`agentnet` OS user → `agentnet` role/database
through `/var/run/postgresql`). Then plan without privileged or managed-host
writes (the npm launcher may materialize its caller-owned Python runtime):

```bash
<resolved-root-owned-agentnet-path> server-agent setup --request /home/operator/.config/agentnet-setup/server-setup.json
```

After one frozen human-approved scope, the target server's coding agent runs:

```bash
sudo -- <resolved-root-owned-agentnet-path> server-agent setup \
  --request /home/operator/.config/agentnet-setup/server-setup.json \
  --expected-request-digest <approved-request-digest> \
  --apply --start
```

Request-v1 remains the scanner-backed artifact-enabled compatibility contract:
it omits `artifact_mode`, requires `scanner_trust_file`, and binds approval
digest v2 plus marker v2. Request-v2 requires explicit `artifact_mode`.
`enabled` still requires scanner trust; `disabled` forbids the scanner field,
permits exactly `offline_custody`, and creates no scanner file, artifact key, or
artifact directory. See the unchanged
[request-v1 example](skills/agentnet-operator/references/examples/ordinary-server-setup-request.json)
and separate
[communication-only request-v2 example](skills/agentnet-operator/references/examples/ordinary-server-communication-only-setup-request.json).
Request-v2 binds approval digest v3 and marker v3; old approval/marker evidence
cannot authorize it.

An expired managed-server credential is recovered in place only through
`server-agent reauthorize-expired-credential`: exact same-key possession,
fresh owner WebAuthn UV, immutable C0 provenance, the complete audited
credential-supersession chain, and current managed-file/database state must all
agree. The command performs no authority grant or restart. Exact retry
reconciles interruption; missing, stale, edited, or unaudited provenance fails
closed. After completion, rerun the same digest-bound setup apply/start.

An expired laptop or peer harness that still owns an active collaboration-scope
membership is replaced only through the root-only
`server-agent replace-expired-scope-harness` command. An active managed-server
harness of the same verified principal may open the ceremony because the
expired member cannot authenticate; the exact scope-owning principal must then
approve the complete transaction through Approval. The atomic commit
tombstones the former member, activates the named same-principal replacement as
`member`, advances the scope revision and membership sequence once, and makes
current schema-v7 membership authoritative immediately. It changes no managed
identity/configuration file and restarts no service.

Plan and apply bind exact Node/uv/launcher/`systemctl`/`useradd` paths and
content hashes plus the canonical full AgentNet package-tree content hash to
the request-versioned approval digest. Apply
repeats preflight under an exclusive lock, may create the fixed Core OS identity
plus root-owned setup runtime/lock, and then blocks before AgentNet
environments/config/database writes
unless a read-only canary succeeds as that identity and parsed PostgreSQL
HBA/ident views plus config-load freshness prove the exact loaded unshadowed
`local agentnet agentnet peer` rule. PostgreSQL
role/database/HBA edits and reload remain a separate operator-owned approval;
rerun the same AgentNet digest afterward.

The wrapper then owns only AgentNet's three locked identities, private roots,
Approval/Core/C0-responder state, mode-dependent scanner trust, five hardened
systemd units, bounded start, and exact loopback/public health. Signed Approval
broker readiness uses host trust visible to CPython `ssl.create_default_context()`
with certificate and hostname verification; ambient `SSL_CERT_FILE`, `SSL_CERT_DIR`,
and `SSLKEYLOGFILE` fail closed before setup and are removed from all four process-spawning service units; the fifth unit is the timer that invokes the hardened renewal service. Retry reloads realized state
and commits the request-versioned marker only through same-request
compare-and-swap; manual marker/unit surgery is unsupported. It never mutates
DNS, TLS, proxy/firewall policy, PostgreSQL
administration, identity, or authority. Human OIDC/WebAuthn and offline
activation remain explicit. Final setup status is `operational` with identity
enrolled and authority still false.

Destructive recovery is an explicit server-manager action: `agentnet server-agent reset --retain-external-prerequisites --confirm-package-state-removal`. It acquires and validates the setup lock before inventory, proves exact owner/mode/type custody, stops and proves managed units inactive, requires symlink-attack-resistant removal, removes only package-owned deployment units/state, preserves the permanent root-only setup coordination lock/root, and reloads systemd even on an exact retry. PostgreSQL, runtimes, package installation, proxy/TLS/DNS/firewall, operator inputs, and locked service identities remain untouched. Typed evidence proves deployment-state absence. Reset is never an owner browser step, fresh-laptop prompt action, or secret-rotation mechanism.

Communication-only request-v2 is a restricted first-message/testing profile,
not a substitute for full AgentNet. Artifact HTTP/CLI/service operations and
non-empty message/task artifact bindings fail with `artifacts_disabled` before
body, metadata, capability, or custody effects. It supports signed
communication and task custody only; it does not satisfy `FILE-*`, G13,
production durability, production certification, or ship readiness.

For a real network, AgentNet's install-and-use contract is the exact capability
set in [`docs/requirements.md`](docs/requirements.md)—no reduced messaging
product and no extra privileged Hub product. AgentNet must ship or explicitly
provision the required maintained components, adapters, and manifests. Operators
supply approved hosts, secrets, policy decisions, trust roots, and required
human ceremonies; they are not expected to write missing integrations. Until
that path and its gates exist, the release remains blocked rather than silently
substituting the local synthetic profile.

### Independent approval component

AgentNet includes `agentnet approval`: a separately runnable,
loopback-bound WebAuthn-UV ceremony service using pinned `webauthn==3.0.0`.
The ordinary profile pins one preapproved owner through OIDC Authorization Code
+ PKCE, rotates server-side `__Host-` browser sessions, and serves registration
and request review only at the stable public `/approval` path. Approval retains
request capabilities and signed receipts encrypted inside the service; neither
the browser nor the enrolling harness receives them. Exact Origin, CSRF,
RP/origin/verifier, challenge/session, expiry, retry, and audit checks fail
closed. Profiles without owner OIDC retain legacy fragment-capability routes
and are lab-only by policy; they cannot satisfy the ordinary C0 deployment and
release gate. Signed broker routes let authenticated Core create/status exact
requests with a SHA-256 binding to a purpose-separated Approval possession
secret and retrieve only already-issued receipts after WebAuthn UV. For OIDC,
Core derives that secret per transaction from continuation custody; bootstrap
plans generate and encrypt a distinct high-entropy value. Browser/human
receives no code, receipt, continuation, capability, or broker secret. Core
cannot approve or sign, and provisioning or enrollment grants no authority.

AgentNet also includes `agentnet join guided`: one resumable command opens local
candidate OIDC and stable Approval pages without printing either URL, polls Core
with owner-only opaque continuation, proves locally retained candidate key, and
writes owner-only identity profile. Exact waiting process retrieves Approval
result automatically through signed broker. For headless server, server-local
manager uses `--browser remote`; owner opens only fixed public Core `/activate`
in normal browser. Core redirects exactly one waiting remote transaction through
OIDC and Approval, while zero/multiple/expired/local state fails closed. Both
fixed activation routes are unauthenticated and rate-limited; they accept no
selector/private input, and callback must match the exact server-staged approved
OIDC owner identity. Owner uses no SSH, server terminal, private URL, claim code,
receipt, or browser value transfer. Completion retries converge after response
loss. Core's 60-poll anti-abuse budget applies only before OIDC callback;
callback/Approval polling remains rate-controlled and ends at fresh challenge
expiry. Nonterminal local state resumes with exact command. Only after Core
proves `expired` or `failed`, `--replace-terminal-state` starts fresh OIDC using
same candidate key; absent, completed, malformed, drifted, or nonterminal state
is never replaced. Human/model success output omits identity IDs and reports only
identity-only completion, local save status,
`approval_delivery=automatic_possession_bound_signed_broker`, zero authority,
and the bounded-authority next step.

After the exact C0 round trip completes, the server harness can run
`agentnet communication-scope begin|status|complete`. Core resolves the exact
completed C0 pair rather than a fresh-enrollment timing window and accepts only
the ordinary server harness's current active credential on that same harness
lineage. One WebAuthn-UV approval then atomically gives the existing laptop
harness and ordinary server harness the fixed permanent canonical message,
mailbox, conversation, response-obligation, and room actions. The human selects
no IDs or permissions. Every operation still revalidates the current exact
caller harness, credential, human, domain-revocation epoch, and policy revision.
Each exact caller may communicate only with the other active enrolled harness
named by the scope in the same trust domain; an unknown, inactive, revoked,
additional, ambiguous, or cross-domain target fails closed.
Caller or target revocation denies immediately.
Artifacts, effects, federation, public A2A, administration, data, tools,
and secrets remain excluded.

On a supported Linux owner laptop, the enrolled harness launches an interactive
agent through `agentnet manager-run --identity .agentnet/identity.json -- pi`.
The launcher stages the exact packaged Pi extension inside the private session,
disables extension discovery, and rejects caller-supplied extension or tool-
selection flags before opening the identity. The child gets only a short-lived,
exact-process local binding for canonical communication tools—not the laptop key
or reusable remote credentials. The parent owns
authentication, strict request parsing, and cleanup. Full commands and
failure/recovery behavior are in the
[implementation guide](docs/implementation-guide.md#ordinary-server-agent-activation).

This software component is not proof of independence. Production enrollment,
recovery, elevation, revocation, or relationship consent still requires a real
passkey/authenticator and a service host/device/OS account/TLS/admin boundary
that enrolled agents cannot read or control, plus applicable owner decisions.
See [the implementation guide](docs/implementation-guide.md#independent-webauthn-uv-approval-service).

## Try the local conformance profile

From a source checkout:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv sync --extra test
uv run agentnet demo --data-dir /tmp/agentnet-demo
uv run agentnet a2a-demo
uv run agentnet verify
```

The demo uses synthetic identities and explicitly reports `accepted_local`. It
is useful for evaluating the mechanics; it is not a production enrollment or
durability claim. The release package gate goes further:

```bash
npm run check:packed
```

Its second clean npm installation starts a real loopback Core process and
separate signed client processes from installed bytes. It proves local C0-classified
conversation custody, exact actor attribution, idempotent request and receipt
convergence, `recipient_committed`, typed response-obligation completion across
Core restarts, and fresh authentication refusal after a clearly labeled lab-only
credential fixture. It removes the temporary process/state boundary afterward.
The result remains synthetic `local_conformance`: it does not prove bounded C0
pilot completion, approved revocation, OIDC/WebAuthn enrollment, ordinary
server-agent topology, PostgreSQL durability, or production readiness.

To inspect the complete operator journey—from creating a network and enrolling
the first administrator through invitations, messaging, recovery, and
always-on deployment—see the [implementation guide](docs/implementation-guide.md).

## Project status

AgentNet is an early public implementation; the latest published package is
`0.1.50`. Its publication does not promote any requirement or gate.
Published `0.1.29` repaired owner/enrollment OIDC callback parsing after real
Google owner login exposed rejection of valid unique response extensions;
published `0.1.30` repaired installed-verifier package custody; published
`0.1.31` added browser-only fresh-laptop/server onboarding and the bounded first
message path. Published `0.1.32` repairs the ceremony blockers. Published
`0.1.33` adds the exact migration from the live `0.1.31` two-unit marker, but
the Hub transition exposed a retained systemd failed latch after its
forward-only boundary. Published `0.1.35` adds identity-preserving reconciliation
for that exact state. Published `0.1.37` removes one unsatisfiable fresh-init
identity-profile check. Published `0.1.38` extends only the bounded public
post-restart health/readiness wait, but the remote Hub peer reported from bounded
read-only preflight that its default `Python-urllib/*` request identity was
rejected with HTTP 403 before origin routing. Unpublished `0.1.39` sent an explicit
`AgentNet/0.1.39` User-Agent and JSON Accept header while preserving the same TLS,
redirect, proxy, timeout, payload, and exact-identity checks. It also repairs the
local-only signed lab path so intentional `deterministic_only` harnesses can use
the existing narrow C0 allowlist without becoming production-active, and adds the
installed-package multiprocess gate described above. It is a clean-state setup
candidate and accepts no earlier release marker as migration input. No release
proves completed fresh-laptop enrollment, native cross-host message/ACK,
production readiness, or ship eligibility.

Published `0.1.43` carries the corrected first-C0 path proven narrowly by the
unpublished `0.1.42` deployment: real workforce OIDC, owner-controlled WebAuthn
UV, separately enrolled server and laptop harnesses, one fixed
`BootstrapGrantPlan`, `COMPLETED_C0_ROUND_TRIP`, and exact five-power revocation.
It additionally accepts only strict remote-browser bootstrap evidence, keeps
the responder's P-256 private key as bytes instead of corrupting it through text
decoding, permits only the package-owned systemd credential file, and opens that
credential with `O_NONBLOCK` before regular-file custody validation so a FIFO or
device fails closed without stalling startup. The historical live run remains
bound to `d8884b6c03a0dd38baab03386982aae8ad11dd58`; publication does not replace
fresh staged-package and remote deployment evidence.
Published `0.1.44` adds a loopback-only private administration dashboard,
one-approval durable communication activation for an exact same-principal
harness pair, and a parent-owned Manager gateway that gives sandbox children
only measured short-lived local capabilities. Dashboard requests revalidate the
current credential and authority, mutation tokens bind method/path/body, and
credentials never enter browser URLs. Manager tools cross the canonical signed
HTTP boundary rather than treating prompt text or child arguments as identity.
Publication promotes no requirement or must-not-ship gate. The
earlier `0.1.24`
release introduced product-owned ordinary Linux server setup: fixed
plan/apply/start convergence, Approval/Core separation, scanner trust, exact
public HTTPS health identity, interruption recovery, redacted evidence, and
bundled operator workflow. Setup grants neither identity nor authority.

Published `0.1.45` adds the schema-v7 exact endpoint lifecycle, resumable
user-level setup/update coordination, and exact-endpoint recipient routing.
Activation still requires the user's explicit harness restart. It remains a
fresh-install-only release because its `0.1.44→0.1.45` packaged upgrade lane
did not pass.

Candidate `0.1.48` supersedes the unpublished `0.1.47` candidate. It corrects
post-C0 terminal-credential resolution against the canonical schema:
`credentials` remains harness/epoch scoped, while domain/principal ownership is
resolved through the bound `harnesses` row. Unknown or mismatched
domain/principal/harness/epoch tuples still fail closed. The candidate also adds
only the exact forward-only `0.1.47→0.1.48` five-unit marker path required for a
server already carrying the unpublished `0.1.47` marker. The transition
preserves enrolled identity and credential state, schema-v7 PostgreSQL,
endpoint and communication state, and external prerequisites, and uses the
existing journaled package-runtime replacement without database migration. The
focused lane passes `547` tests with `5` expected dedicated-PostgreSQL skips,
and the broad releasable-source lane passes `2155` tests with `21` expected
platform/dedicated-PostgreSQL skips. Recursive packed-package verification and
publication remain pending. No requirement, production claim, or must-not-ship
gate is promoted.

Candidate `0.1.49` corrects the first permanent communication activation and
remote mailbox path exercised after the exact C0 round trip. Core selects the
completed C0 pair without a fresh-enrollment timing window, permits only the
ordinary server harness's current active credential on that same lineage, and
keeps ambiguity, revocation, stale epochs, and cross-harness substitution
fail-closed. Terminal scope retries converge without replacing active authority.
Remote message send, inbox, and acknowledgement operations require and sign the
exact collaboration scope. The candidate adds only the exact forward-only
`0.1.48→0.1.49` five-unit marker edge and no database migration. No requirement,
production claim, or must-not-ship gate is promoted.

Candidate `0.1.50` makes the existing secure setup path operable without
weakening its approval or fail-closed boundaries. A standard server host runs
`agentnet server-agent setup --apply --start`, reviews one exact plan, and
reruns the same command to resume retained state. Its successful plan returns
the exact package-pinned laptop command:
`agentnet join guided --server <Core-origin>`. Guided laptop setup derives the
domain and default harness from authenticated discovery, emits content-free
named progress phases, completes the exact enrollment, communication
activation, and C0 acknowledgement lifecycle, and returns
`communication_ready`.

The server command has a ten-minute process deadline; laptop guided join keeps
its five-minute default deadline. Timeout and named blockers preserve resumable
state. One v0.1.50 package may directly upgrade exact five-unit schema-v7 setup
markers from v0.1.45 through v0.1.49 using an allowlisted journaled transition;
unsupported or ambiguous markers fail closed. Packed-package verification now
requires the separate-process local communication and obligation roundtrip in
addition to the existing installed journey. These are local and CI evidence,
not production certification, and no requirement or must-not-ship gate is
promoted.

Candidate `0.1.51` adds the package-owned corrective recovery path for the
exact ordinary-onboarding state where Approval still names the setup
placeholder owner after Core enrolled the canonical human. Managed setup
derives the target only from the enrolled Core identity and Approval's pinned
OIDC binding, rotates current Approval signing authority to that principal,
and journals resumable Core policy replacement. It rejects identity, domain,
OIDC, signer, database, configuration, or journal ambiguity. New communication
activation writes its complete schema-v7 collaboration projection in the same
transaction; an already committed scope missing that projection is repaired
from its immutable scope rows without replacing the scope or minting broader
authority. Exact five-unit schema-v7 markers from v0.1.45 through published
v0.1.50 may upgrade directly to v0.1.51.

The v0.1.50→v0.1.51 transition preserves the exact published v0.1.50
Approval TTL policy (`request_ttl_seconds=300` with no separate
communication-scope field). It separately recognizes the retained one-hour
hotfix shape. If that operational hotfix was not written into the setup marker,
setup reconstructs the exact published 300-second form and requires its
canonical digest to equal the marker before creating a journal. It then
journals the realized configuration, restores the ordinary approval deadline
to 600 seconds, keeps the communication-scope ceremony ceiling at 3600
seconds, and atomically replaces the file. A crash resumes from the journal;
any additional drift fails closed, and pre-commit rollback restores the exact
source bytes.

If the canonical-owner repair already completed while that one-hour hotfix
remained outside the marker, setup accepts the combined state only after the
completed recovery evidence reconstructs the marker-era Approval and Core
documents. It reverses only the evidence-bound owner and signer fields in
memory, then requires both reconstructed canonical digests to equal the
marker. The realized current documents are journaled, the TTL policy is
normalized, and owner/Core convergence is rechecked idempotently. Missing
Core-OIDC agreement, incomplete evidence, or unrelated drift fails before
setup creates its upgrade journal or changes managed state.


Git tag `v0.1.23` reached the staging workflow, but CI stopped before npm
staging because one hermetic interruption test mocked `/usr/bin/useradd` on a
runner where that path did not exist. No `0.1.23` package was staged or
published. Published `0.1.24` changed only that fixture to mock AgentNet's
validated host-tool resolver directly; runtime behavior was unchanged.

Published `0.1.25` repairs two JSON-RPC interoperability defects exposed by the
pinned official A2A TCK: `/rpc` and `/rpc/` now use the same strict endpoint
without POST redirects, and a blank SDK request tenant is restored only from an
exact verified opaque route binding. Missing/spoofed bindings and tenant
conflicts fail closed; rejected requests leave no event/task residue; alias
retries preserve exact idempotency and non-enumerating task lookup. Local A2A
reports `57 passed`; the source lane excluding installed-live inference and
release-manifest self-check reports `1386 passed, 15 expected host/PostgreSQL
skips`. The focused official JSON-RPC lane reports `3 passed`, while the full
MUST run remains non-green at `50 passed, 11 failed, 174 skipped`; G04 therefore
remains `FAILED`. Exact prepublication, retained-artifact, recursive packed, and
Pi-loader checks are recorded in its immutable evidence.

Published `0.1.26` repairs runtime-bound setup approval, semantic broker
preflight, exact PostgreSQL service-identity peer validation, safe same-digest
resume, marker provenance/CAS, Windows CLI imports, and installer guidance. Its
immutable tag resolves through tag object
`c481d850ba4933abbb77191a763a7c4e0817bc32` to commit
`a7da3aa945c0b2f25fdb06803b80529f89bf8242`; public npm `gitHead` matches.

Published `0.1.27` adds the explicit communication-only ordinary-server profile
for first-message testing while retaining fail-closed artifact boundaries. It
permits exactly `offline_custody`, creates no scanner/artifact state, and does
not convert local signed-message evidence into a real-network claim. Published
`0.1.28` makes Python and Node privileged setup-input verification independent
of same-size timestamp advancement, accumulates bounded short reads, separates
structured launcher rejection states, and rejects pending release evidence.
Its final source and two clean recursively packed npm generations each report
`1418 passed, 16 expected platform/dedicated-PostgreSQL skips`; those lanes
exclude installed-live-inference, subprocess-lifecycle, and bake-off-evidence
files. The two installed-harness pin failures remain non-green and are not
waived.

Published `0.1.29` retains decoded callback pairs until every known or unknown
name is proven unique, strictly separates success and provider-error shapes,
ignores only unique unrecognized OAuth extensions, and terminally fails only the
exact state-bound pending owner/enrollment/recovery transaction on provider
error without token exchange. Existing cookie/state, PKCE, nonce, issuer,
audience, signature, expiry, and replay checks remain unchanged. Focused checks
report `92 passed`; source and both clean recursively packed npm generations
each report `1443 passed, 16 expected platform/dedicated-PostgreSQL skips`;
release, package, reviewer, and byte-identical archive gates pass. The failed
real callback was not retried or reused. Fresh-laptop enrollment and native
cross-host message/ACK remain pending until the exact public package is installed
and a new OIDC transaction completes.

Published `0.1.30` fixes installed-verifier custody by running verification from a bounded disposable package copy, rejecting caller pytest arguments, and requiring recursive packed-tree digest/no-residue checks. Published `0.1.31` adds the browser-only, exact-approved-owner first-message path: fixed rate-limited `/activate`; purpose-separated automatic Approval delivery; setup migration/recovery; enabled-unit reconciliation; and package-only reset under a permanent coordination lock.

Published `0.1.32` keeps that protocol and adds one coherent blocker repair: signed Approval broker readiness through the configured public origin; authoritative setup response-loss reconciliation; recoverable OIDC-begin idempotency; finite 24-hour always-on credentials with selector-free six-hour-window renewal; Core schema v5; one package-owned isolated C0 responder that never resurrects after terminal cleanup; clean current-package setup-attempt custody; and a five-unit systemd lifecycle. Its exact Hub apply stopped safely at `setup_marker_conflict` before managed mutation because the active `0.1.31` marker records only Core and Approval.

Published `0.1.33` admits the exact released `0.1.31` two-unit communication
profile and exact `0.1.32` five-unit profile. The live Hub reached its committed
0.1.33 marker and unit boundary, then Approval exited with status 143 and
systemd retained `ActiveState=failed`; the strict quiescence validator correctly
refused to treat that as `inactive`, and 0.1.33 had no preservation-safe retry.

Published `0.1.35` retains the strict inactive requirement. It gives Approval
the same successful-SIGTERM contract as Core, runs `reset-failed` only after
bounded stop/disable for each exact managed unit, and accepts the exact 0.1.33
five-unit marker. A retained committed 0.1.33 journal is superseded only after
exact marker/config/unit revalidation and remains durable until the new
0.1.33→0.1.35 journal atomically replaces it. Earlier installations must first
use the separately released 0.1.33
migration boundary; 0.1.35 does not add another direct legacy edge. Expired or
unready credentials leave renewal and C0 disabled. For the exact current Hub
condition, 0.1.35 adds a root-only, owner-approved recovery for an expired but
still-possessed managed-server key before the first C0 exchange: same-key proof
plus fresh WebAuthn Approval retires the old row unchanged and issues one finite
next-epoch credential. It grants no authority, restarts nothing, and refuses
A2A, relay, or retained C0-terminal topologies. No reset, manual edit, identity
replacement, authority, production, or gate-promotion claim is added. Exact
public `0.1.35` then reached marker and PostgreSQL migration on the Hub but
failed because setup required `actor.key_id` even though strict canonical
`VerifiedActor` profiles forbid and never serialize that field.

Published `0.1.37` strictly validates the canonical actor and its current
binding labels, retains exact profile shape plus P-256 private-key custody and
readability checks, and removes only that impossible duplicate field test.
`server-agent activate` remains the database-backed credential-to-key binding
proof. It adds only the exact released `0.1.33` five-unit marker migration to
`0.1.37`, including provenance-checked retained-journal recovery; `0.1.34`,
`0.1.35`, and direct legacy sources remain rejected. Exact public `0.1.37`
reached a committed five-unit Hub marker with Core and Approval healthy, then
failed closed because the public route converged after the ordinary 30-attempt
probe window. Authority remained false and responder/renewal stayed disabled.

Published `0.1.38` changes only setup convergence after service restart. Public
Approval/Core health and public Core readiness reuse the existing finite
90-attempt startup window; exact identity/readiness payload checks, TLS, redirects,
and failure semantics are unchanged. The remote Hub peer reported from bounded
read-only preflight that the stdlib default `Python-urllib/*` User-Agent received
HTTP 403 on all three public routes, while an explicit AgentNet product User-Agent
passed edge classification and reached the offline origin response. That report is
corroboration only, not retained reproducible release proof. Unpublished `0.1.39`
changed request identity to explicit GET, `User-Agent: AgentNet/0.1.39`, and
`Accept: application/json`; it also adds the bounded synthetic installed-package
communication gate without weakening the production policy engine. It adds no
migration edge and rejects existing release markers. Release still requires
exact same-commit terminal-green cross-platform, clean-setup, and exact
released-marker rejection workflow evidence; post-push run IDs are not
self-authored into source. Required runtime proof now targets exact staged
`0.1.46`, clean five-unit readiness,
fresh enrollment, one native
signed message, recipient `recipient_committed`, exact
`COMPLETED_C0_ROUND_TRIP`, then five-power revocation and post-revocation refusal.

Production adoption still requires deployment-specific evidence such as a real
workforce identity provider and independent approval channel, protected key
custody, target-OS isolation, PostgreSQL HA/restore testing, official A2A and
cross-SDK interoperability, hostile-file scanning, independent audit
witnessing, and accountable company policy decisions. Disabled or unproven
high-risk capabilities remain fail-closed.

The exact evidence state is maintained in
[REQUIREMENTS_STATUS.md](REQUIREMENTS_STATUS.md) and the
[release-gate ledger](docs/GATE_EVIDENCE.md).

## Documentation

- [Hard requirements](docs/requirements.md) — the authoritative 85-item product
  baseline.
- [Product and architecture specification](docs/specification.md) — design,
  decisions, state machines, alternatives, and requirement mapping.
- [Implementation guide](docs/implementation-guide.md) — runnable workflows and
  deployment details.
- [Architecture](docs/ARCHITECTURE.md) — current code and trust boundaries.
- [Schemas and interfaces](docs/SCHEMAS_INTERFACES.md) — canonical contracts.
- [Response obligations](docs/response-obligations.md) — durable
  request/answer ownership.
- [Threat model and test plan](docs/THREAT_MODEL_TEST_PLAN.md) — adversaries and
  required evidence.
- [Engineering constitution](AGENTS.md) — mandatory rules for contributors and
  coding agents.

## Repository layout

```text
src/agentnet/    core extension, bindings, gateways, storage, and supervisor
tests/           hermetic, integration, security, recovery, and external gates
schemas/         versioned public protocol schemas
deploy/          self-hosted deployment assets
docs/            requirements, architecture, operations, and evidence
```

## Principles that will not be traded away

AgentNet will not trust identity claimed in prose, silently convert transport
success into business completion, grant data access through a management title,
interrupt a user's active conversation for routine network traffic, create a
universal super-agent, or make a cloud provider mandatory.

Mechanisms can evolve. Those boundaries remain.

## License

Licensed under Apache-2.0.
