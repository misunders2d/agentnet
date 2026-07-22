# AgentNet safe commands and examples

> **Warning:** Do not execute state-changing commands in this file without explicit user intent and without first reading [fail-closed boundaries](fail-closed-boundaries.md).

Use commands only after confirming the requested scope. Installation and local examples do not authorize enrollment or network activation.

## Requirements

```bash
node --version
uv --version
```

The npm/Pi launcher supports Linux, macOS, and Windows local profiles, requires Node.js 22.19 or newer, requires `uv` 0.11.28 or newer, and selects CPython 3.13.13. Production deployment remains Linux-first.

## Install examples

Try the Pi package temporarily:

```bash
pi -e npm:@misunders2d/agentnet
```

Install the Pi package:

```bash
pi install npm:@misunders2d/agentnet
```

Install the shared CLI:

```bash
npm install -g @misunders2d/agentnet
agentnet --version
agentnet --help
```

If the global command is missing:

```bash
command -v agentnet
npm prefix -g
"$(npm prefix -g)/bin/agentnet" --version
```

Do not add shell-profile changes without user approval.

## Local-conformance example

From a source checkout:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv sync --extra test
uv run agentnet demo --data-dir /tmp/agentnet-demo
uv run agentnet a2a-demo
uv run agentnet init \
  --config agentnet.json \
  --data-dir .agentnet \
  --domain local.example
uv run agentnet status --config agentnet.json
uv run agentnet serve \
  --config agentnet.json \
  --host 127.0.0.1 \
  --port 8080
```

This is local-conformance evidence. Synthetic actors and C0 test bytes may produce `accepted_local`; they do not prove real cross-host identity, authority, or production durability.

## Harness examples

Probe every pinned harness for the G01 local gate:

```bash
agentnet harness-probe --data-dir /tmp/agentnet-harness-probes
```

Probe one harness diagnostically:

```bash
agentnet harness-probe \
  --harness pi \
  --data-dir /tmp/agentnet-pi-probe
```

A single-harness result must remain labeled `diagnostic_only` and does not prove external conformance.

## Package verification

From the repository root:

```bash
npm run check:package
npm pack --dry-run --json --ignore-scripts
npm test
npm run check:packed
```

`check:packed` builds the real npm tarball, installs it in a clean temporary prefix, and runs packaged verification from an unrelated working directory.

## Supervisor and Pi binding example

Core configuration and supervisor configuration are separate:

```bash
agentnet status --config agentnet.json
agentnet supervisor-run \
  --config agentnet-supervisor.json \
  --check
```

Only after enrollment and explicit approval may an operator run the supervisor without `--check`. Pi binding requires `local_bindings_required=true`, an owner-only capability root, a private Unix socket, and a measured supervisor-launched Pi child.

## Always-on server preflight

This is a fail-closed product preflight—not a manual integration recipe. AgentNet must ship the deployment manifests, ceremony service, and adapters required by the supported profile. If the installed release lacks them, report `blocked: product component not yet shipped`; do not make the operator build glue code or assemble an undocumented vendor stack.

OIDC discovery is public-only by default. A private/non-global provider is allowed only when configuration pins its exact HTTPS origin, exact JWK thumbprints, and explicit canonical private CIDRs and/or endpoint addresses; the direct TLS transport may connect only to the validated address tuple. Loopback, link-local, multicast, reserved, documentation, benchmark, transition/softwire, and IPv4-mapped addresses remain forbidden. Do not suggest hosts-file tricks, DNS rebinding, proxy mirrors, or weakened SSRF checks.

Before creating server state, verify that the operator has approved values for:

- nonproduction or production trust-domain identifier;
- dedicated HTTPS public base URL and service audience;
- PostgreSQL endpoint and environment variable containing the runtime DSN;
- OIDC enrollment configuration file;
- retention and recovery policy;
- an exact independently approved identity profile produced by enrollment.

The CLI shape is:

```bash
agentnet network create \
  --config agentnet.json \
  --data-dir .agentnet/server \
  --domain corp.example \
  --public-base-url https://agentnet.example \
  --oidc-config oidc-enrollment.json \
  --database-url postgresql://agentnet@db.example/agentnet \
  --database-url-env AGENTNET_DATABASE_URL
```

For an existing config that was not created by `network create`, provision its
schema/keys without inventing identity or authority:

```bash
agentnet bootstrap-server-agent --config agentnet.json
```

Under the dedicated approval-service OS identity, use the shipped WebAuthn
component. The default profile may colocate this service with Core/PostgreSQL on
the existing server; the optional high-assurance profile uses separate
administration. Never copy its private config, signer keys, record key,
database, or capability URLs into enrolled-agent storage:

```bash
agentnet approval provision \
  --config /etc/agentnet-approval/config.json \
  --data-dir /var/lib/agentnet-approval \
  --public-origin https://approval.corp.example \
  --rp-id approval.corp.example \
  --verifier-id approval.corp.example \
  --approvers /root/agentnet-approval-approvers.json
agentnet approval status --config /etc/agentnet-approval/config.json
agentnet approval serve \
  --config /etc/agentnet-approval/config.json \
  --host 127.0.0.1 --port 8090
agentnet approval register-begin \
  --config /etc/agentnet-approval/config.json \
  --approver security-owner
agentnet approval request-create \
  --config /etc/agentnet-approval/config.json \
  --approver security-owner \
  --purpose identity.enrollment.approve \
  --transaction /root/exact-enrollment-transaction.json
```

`serve` requires a separately credentialed HTTPS reverse-proxy role at the exact
configured origin/RP ID. It may share the existing server in the default
profile. Provisioning grants no authority and registers no passkey. `status`
deliberately reports `independent_boundary_proven: false`; real passkey,
shared-host process-boundary, recovery, and owner evidence must be proven for
the default profile. Separate-host evidence belongs only to the optional
high-assurance profile.

For an installed release whose actual help exposes `join guided`, complete exact
OIDC/key-possession/Core-brokered independent approval enrollment. While the
server process is offline, bind only that exact identity. Replace `pi`, domain,
and display name with approved values:

```bash
agentnet join guided \
  --server https://agentnet.example \
  --domain corp.example \
  --harness pi \
  --name server-agent-1 \
  --state .agentnet/guided-join.json \
  --identity .agentnet/server-agent-identity.json \
  --browser terminal
agentnet server-agent activate \
  --config agentnet.json \
  --identity .agentnet/server-agent-identity.json
agentnet serve --config agentnet.json
```

Default `join guided` opens the system browser. This server-bootstrap example
uses explicit `--browser terminal`: AgentNet verifies private POSIX `/dev/tty`
and discloses the HTTPS URL only there for manual opening on the owner laptop.
Never record or relay that URL. Missing TTY, control bytes, partial writes, and
unsupported platforms fail closed with pending state retained. Both modes
prompt only for the human claim code and must finish as `enrolled_identity_only` and
`first_message_blocked_explicit_authority_required`. The expert manual
`join begin`/`join complete` commands remain compatible but require explicit
local challenge/receipt handling and are not the fresh-laptop workflow:

```bash
agentnet join begin \
  --server https://agentnet.example \
  --harness pi \
  --name server-agent-1 \
  --state .agentnet/join-pending.json
agentnet join complete \
  --state .agentnet/join-pending.json \
  --challenge .agentnet/challenge.json \
  --approval .agentnet/approval.json \
  --identity .agentnet/server-agent-identity.json
```

Activation uses the exact runtime lease to reject a live server, runs no
migrations, verifies the current PostgreSQL credential and owner-only private
key, and writes only enrolled harness/credential labels. It grants no authority
or capability and does not restart anything. These commands do not themselves
prove HA, PITR, KMS custody, independent enrollment approval, or production
certification.

## Release-gated fixed C0 pilot

Use this section only after verifying the installed release and its C0 release
evidence. Published `0.1.18` does not qualify. On the exact owner laptop:

```bash
agentnet supervisor-run --config agentnet-supervisor.json \
  --c0-pilot-responder --check
agentnet supervisor-run --config agentnet-supervisor.json \
  --c0-pilot-responder
```

On the exact fresh laptop after identity-only enrollment:

```bash
agentnet bootstrap-plan begin \
  --identity .agentnet/identity.json \
  --state .agentnet/bootstrap-plan-state.json
agentnet bootstrap-plan status \
  --identity .agentnet/identity.json \
  --state .agentnet/bootstrap-plan-state.json
agentnet bootstrap-plan complete \
  --identity .agentnet/identity.json \
  --state .agentnet/bootstrap-plan-state.json
agentnet c0-pilot start --identity .agentnet/identity.json
agentnet c0-pilot status --identity .agentnet/identity.json
agentnet c0-pilot complete --identity .agentnet/identity.json
```

`bootstrap-plan complete` alone returns `prepared_unusable`; it grants no generic
communication authority. The fixed C0 service internally activates the guard.
Do not add selectors or replace this sequence with generic message, inbox, ACK,
authority inventory, or entitlement commands. `waiting_owner` and
`waiting_fresh` are resumable; `expired` and `invalidated` are terminal. Only
`COMPLETED_C0_ROUND_TRIP` proves the exact seven facts and atomic cleanup of five
communication powers. The proof remains same-principal and `accepted_local`.

## Generic mailbox acknowledgement — never use for fixed C0

This generic example is outside the release-gated fixed C0 packet. Never use it
to select, retrieve, acknowledge, repair, or prove a fixed C0 event; the
dedicated C0 service owns those choices and facts.

For separately authorized non-C0 operations, after the exact recipient has
durably stored inbox bytes and dedup state, record recipient custody with the
event ID and envelope digest returned by `inbox`:

```bash
agentnet message inbox --identity .agentnet/recipient-identity.json
agentnet message acknowledge EVENT_ID \
  --envelope-digest ENVELOPE_DIGEST \
  --identity .agentnet/recipient-identity.json
```

This signed operation writes only `recipient_committed`. It does not prove
presentation, reading, processing, response-obligation acknowledgement, task
payload release, or an effect. Exact retries converge on the original receipt.
Do not acknowledge before the recipient's own durable storage and dedup commit.

## Protected delegated task execution

There is no operator command or generic mailbox flag that prints a protected
task payload. In source builds with schema migration 2, the enrolled background
supervisor performs the exact sequence internally:

```text
authorize task.process → enqueue redacted custody → acknowledge exact local queue
→ payload-release with durable receipt/audit → isolated worker → result upload
```

Authorization consumes one current event/resource/mailbox/receipt/classification-
bound TaskGrant use. Release consumes no second use and fails after actor,
credential, domain, policy, grant, intent, conflict, lifetime, digest, or
provenance drift. Result upload fails until release commits. Generic inbox,
conversation, relay, and supervisor reconciliation remain redacted.
`tool_authorized=false` and `effect_authorized=false`; never infer host paths,
network, budget, credentials, artifact access, protected output sinks, or
business effects from payload visibility. If the installed release lacks
migration 2 or `/v1/supervisor/executions/payload-release`, upgrade through the
owner-reviewed release path; do not bypass the gate.

## Bounded artifact examples

Upload one explicit caller-owned regular file into quarantine. Reuse the same
idempotency key only for an exact retry:

```bash
agentnet artifact upload ./report.pdf \
  --identity .agentnet/sender-identity.json \
  --idempotency-key ARTIFACT_UPLOAD_KEY \
  --media-type application/pdf \
  --origin operator-selected-report \
  --classification C1
```

Success does not mean safe or released: first state is `quarantined`, scanner
state is pending, and no message may claim the bytes are available. Scanner
attestation and release remain separate authorized service roles. After a
transport failure, retry with the same idempotency key; do not assume the last
stage failed. An unpromoted reservation remains resumable and consumes quota
until expiry. Abort only when it is known to remain unpromoted and is no longer
needed:

```bash
agentnet artifact abort RESERVATION_ID \
  --identity .agentnet/sender-identity.json
```

After independent scan and policy release, inspect state and download to a new
private file:

```bash
agentnet artifact lifecycle ARTIFACT_ID \
  --identity .agentnet/recipient-identity.json
agentnet artifact download ARTIFACT_ID \
  --identity .agentnet/recipient-identity.json \
  --output ./report.pdf
```

Upload and download are bounded to 16 MiB. Input must be a caller-owned regular
non-symlink file. Output must not exist and its containing directory must be
caller-owned and not group/world writable. The CLI never prints the private
object key or single-use download capability. Do not put file bytes/base64 or
arbitrary local paths into MCP/Pi/model tool arguments; safe harness transfer
requires future supervisor-managed opaque staging handles.

## Secret injection examples

AgentNet requires secure runtime injection, not a specific secret manager. Supported deployment patterns include an environment-backed DSN and a private password file/Docker secret. Infisical, Vault, systemd credentials, Kubernetes Secrets, and cloud secret managers are operator choices, not mandatory AgentNet dependencies.

Never print or place database passwords, DSNs containing passwords, private keys, or approval receipts in chat, prompts, logs, or committed files.

## Do not suggest these shortcuts

- postinstall enrollment or activation;
- synthetic identities for a real server-agent network;
- treating the C0 synthetic lane as a network, rollout milestone, or real communication substitute;
- an approval service readable or controllable by the enrolling harness; default server colocation is allowed only under a distinct OS identity, credential, storage root, and loopback service;
- asking the operator to write missing adapters, ceremony services, receipt logic, or vendor glue;
- A2A, Slack, email strings, or prompt text as identity authority;
- private-OIDC hosts/DNS/proxy tricks that bypass endpoint validation;
- passing `agentnet.json` to `supervisor-run`;
- running a remote endpoint without HTTPS/TLS and exact audience controls;
- claiming production readiness from demos, a single PostgreSQL instance, or `accepted_local`;
- maintainer-run `npm publish` or bypassing the owner-reviewed Trusted Publishing/passkey flow.
