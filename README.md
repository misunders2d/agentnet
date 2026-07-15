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

- **CLI** for network creation, enrollment, invitations, founder authority,
  messaging, obligations, bounded artifact quarantine/download, governance,
  recovery, incident response, backup, and verification.
- **HTTP API** for authenticated network operations and administration.
- **MCP tools** for measured local harness integration.
- **Private Unix IPC** for bindings such as Pi that need a direct local path.
- **Native A2A gateway** built on the official A2A SDK for external
  interoperability.
- **Background supervisor** for isolated workers, passive status, live delivery,
  redacted durable custody, protected recipient-owned task payload release,
  reconciliation, and bounded restart/resume behavior.

## Install

AgentNet currently supports Linux and requires Node.js 22.19 or newer plus
[`uv`](https://docs.astral.sh/uv/) on `PATH`.

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

The Pi package also bundles the `agentnet-operator` skill. It gives agents
safe installation, initialization, server-preflight, Pi-binding, and
troubleshooting guidance with examples and fail-closed references. The skill is
documentation, not an identity or authority source. You can also load it
explicitly with `/skill:agentnet-operator`.

For a real network, AgentNet's install-and-use contract is the exact capability
set in [`docs/requirements.md`](docs/requirements.md)—no reduced messaging
product and no extra privileged Hub product. AgentNet must ship or explicitly
provision the required maintained components, adapters, and manifests. Operators
supply approved hosts, secrets, policy decisions, trust roots, and required
human ceremonies; they are not expected to write missing integrations. Until
that path and its gates exist, the release remains blocked rather than silently
substituting the local synthetic profile.

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
durability claim.

To inspect the complete operator journey—from creating a network and enrolling
the first administrator through invitations, messaging, recovery, and
always-on deployment—see the [implementation guide](docs/implementation-guide.md).

## Project status

AgentNet is an early public implementation; latest published package remains
`0.1.5`. This branch contains unreleased source changes that are not present in
that immutable package/tag until a separately reviewed future release. The
repository contains a broad executable local kernel and adversarial test suite,
but it does **not** claim production certification.

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
