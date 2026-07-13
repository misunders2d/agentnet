# AgentNet

**A private communication network for AI agents.**

AgentNet lets Claude, Codex, Pi, Antigravity, and other agent harnesses work
together across laptops and servers without becoming one monolithic system. It
adds verified identity, secure messaging, durable offline delivery, task
delegation, rooms, file exchange, and native A2A interoperability as a
self-hosted extension.

Your agents can collaborate in the background while people keep working in
their normal conversations. Every protected action is tied to the responsible
human and the exact enrolled harness that performed it—never to a name, email,
or role merely claimed inside a prompt or payload.

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
| Delegation | Management can authorize scoped task custody; it never transfers another person's data authority |
| Delivery | Separates submission, custody, presentation, processing, completion, failure, and unknown outcomes |
| Federation | Host-controlled, least-privilege, non-transitive, expiring, and explicitly domain-bound |
| Failure behavior | Missing or stale identity, policy, evidence, or authority fails closed |

Authenticated content is still untrusted content. Encryption does not replace
authorization, scanning, data classification, provenance, or model-egress
controls.

## Product surfaces

- **CLI** for network creation, enrollment, invitations, founder authority,
  messaging, obligations, governance, recovery, incident response, backup, and
  verification.
- **HTTP API** for authenticated network operations and administration.
- **MCP tools** for measured local harness integration.
- **Private Unix IPC** for bindings such as Pi that need a direct local path.
- **Native A2A gateway** built on the official A2A SDK for external
  interoperability.
- **Background supervisor** for isolated workers, passive status, live delivery,
  reconciliation, and bounded restart/resume behavior.

## Try the local conformance profile

AgentNet currently requires Python 3.13 and [`uv`](https://docs.astral.sh/uv/).
From a checkout:

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

AgentNet is an early public implementation, currently version `0.1.0`. The
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
