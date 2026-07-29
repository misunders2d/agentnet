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

## Always-on server setup

This is a fail-closed product workflow—not a manual integration recipe. The CLI surface is `agentnet server-agent setup`, but invoke it only through the resolved absolute root-owned launcher. Read [ordinary-server-setup.md](ordinary-server-setup.md). Select either the unchanged scanner-backed [request-v1 example](examples/ordinary-server-setup-request.json) or restricted communication-only [request-v2 example](examples/ordinary-server-communication-only-setup-request.json). Sensitive values remain owner-only file references. Then run the no-managed-host-write plan (npm may materialize its caller-owned Python runtime):

```bash
<resolved-root-owned-agentnet-path> server-agent setup --request /home/operator/.config/agentnet-setup/server-setup.json
```

After one frozen human approval, target server agent runs:

```bash
sudo -- <resolved-root-owned-agentnet-path> server-agent setup \
  --request /home/operator/.config/agentnet-setup/server-setup.json \
  --expected-request-digest <approved-request-digest> \
  --apply --start
```

The wrapper owns fixed dedicated identities, private roots, Approval provisioning, Core bootstrap, mode-applicable scanner trust, hardened units, bounded start/restart, and redacted evidence. Communication-only mode carries only `offline_custody`, creates no scanner trust/artifact key, and rejects artifact routes/bindings before custody; it does not prove FILE/G13 or ship readiness. It verifies existing operator-owned HTTPS routes to loopback Core and Approval. It does not mutate DNS, TLS certificates, reverse-proxy configuration, PostgreSQL administration, firewall policy, identity, or authority. Missing infrastructure produces one blocker; never make the operator build glue or assemble an undocumented stack.

### Destructive package-owned server reset

Use only when user explicitly requests removal/reinstall of AgentNet-managed server state and approves exact destructive scope. Server's local Manager runs this command; never put it in browser instructions or fresh-laptop prompt:

```bash
sudo -- <resolved-root-owned-agentnet-path> server-agent reset \
  --retain-external-prerequisites \
  --confirm-package-state-removal
```

Both flags are mandatory. Reset retains PostgreSQL and cluster data, Node/uv runtimes, proxy/TLS, operator configuration, and service identities. It preserves root-only `/var/lib/agentnet-setup/setup.lock` as permanent reset/setup coordination state, removes only exact allowlisted package deployment state, rejects unknown ownership/custody, proves units inactive before deletion, reloads systemd, and returns sanitized reset evidence. It is server-manager-only recovery—not secret rotation, browser onboarding, or routine cleanup.

OIDC discovery is public-only by default. A private/non-global provider is allowed only when configuration pins its exact HTTPS origin, exact JWK thumbprints, and explicit canonical private CIDRs and/or endpoint addresses; the direct TLS transport may connect only to the validated address tuple. Loopback, link-local, multicast, reserved, documentation, benchmark, transition/softwire, and IPv4-mapped addresses remain forbidden. Do not suggest hosts-file tricks, DNS rebinding, proxy mirrors, or weakened SSRF checks.

Before creating server state, verify that operator has approved values for:

- nonproduction or production trust-domain identifier;
- distinct Core and Approval HTTPS origins plus exact service audience;
- ordinary profile's fixed local PostgreSQL role/database `agentnet`, socket `/var/run/postgresql`, exact unshadowed `local agentnet agentnet peer` rule, and environment reference containing canonical peer DSN;
- Core and Approval OIDC policy files;
- exact owner/approver policy, selected artifact mode, mode-applicable scanner trust, retention, and recovery policy.

No server-harness identity profile exists yet. Setup creates state first and reports `waiting_owner_oidc_or_passkey`; guided enrollment then produces exact identity used by offline activation.

The underlying expert primitive below is not the ordinary setup path. Product setup invokes it internally; do not make a human or remote Manager assemble these steps. Its remote `db.example` DSN is deliberately non-ordinary; ordinary setup only accepts `postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet`:

```bash
agentnet network create \
  --config agentnet.json \
  --data-dir .agentnet/server \
  --domain corp.example \
  --public-base-url https://agentnet.example \
  --oidc-config oidc-enrollment.json \
  --artifact-mode enabled \
  --scanner-trust-config scanner-trust.json \
  --database-url postgresql://agentnet@db.example/agentnet \
  --database-url-env AGENTNET_DATABASE_URL
```

Restricted expert communication-only primitive requires explicit mode and forbids scanner input:

```bash
agentnet network create \
  --config agentnet.json \
  --data-dir .agentnet/server \
  --domain corp.example \
  --public-base-url https://agentnet.example \
  --oidc-config oidc-enrollment.json \
  --artifact-mode disabled \
  --database-url postgresql://agentnet@db.example/agentnet \
  --database-url-env AGENTNET_DATABASE_URL
```

Enabled-without-scanner and disabled-with-scanner inputs fail before config, key, or data-directory mutation.

For an existing config that was not created by `network create`, provision its
schema/keys without inventing identity or authority:

```bash
agentnet bootstrap-server-agent --config agentnet.json
```

Product setup invokes the shipped WebAuthn component under the dedicated approval-service OS identity. The following commands document the primitive and exceptional existing-state diagnosis; they are not manual ordinary setup. The default profile may colocate this service with Core/PostgreSQL on
the existing server; the optional high-assurance profile uses separate
administration. Never copy its private config, signer keys, record key,
database, or capability URLs into enrolled-agent storage:

```bash
agentnet approval provision \
  --config /var/lib/agentnet-approval/config.json \
  --data-dir /var/lib/agentnet-approval/state \
  --public-origin https://approval.corp.example \
  --rp-id approval.corp.example \
  --verifier-id approval.corp.example \
  --approvers /root/agentnet-approval-approvers.json
agentnet approval status --config /var/lib/agentnet-approval/config.json
agentnet approval serve \
  --config /var/lib/agentnet-approval/config.json \
  --host 127.0.0.1 --port 8090
agentnet approval register-begin \
  --config /var/lib/agentnet-approval/config.json \
  --approver security-owner
agentnet approval request-create \
  --config /var/lib/agentnet-approval/config.json \
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

The ordinary always-on server ceremony is defined only in
[ordinary-server-setup.md](ordinary-server-setup.md). Do not reuse generic
per-user `.agentnet` paths or bare `agentnet` PATH lookups for that profile; its
commands use dedicated service identities, exact `/var/lib/agentnet` custody,
and the resolved absolute root-owned launcher. Fresh-laptop guided enrollment
is separately defined in [fresh-laptop-onboarding.md](fresh-laptop-onboarding.md).

The expert manual `join begin`/`join complete` commands remain compatible but require explicit
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
evidence. Published `0.1.18` does not qualify. In 0.1.32, ordinary-server setup
owns the dedicated responder service, config, and systemd-delivered credential;
do not recreate it with generic `supervisor-run` or expose its private paths.
Its package-internal diagnostic surface is `agentnet c0-pilot responder
--check`, but operators use only redacted setup/service evidence unless a
separate exact diagnostic action is approved.

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
