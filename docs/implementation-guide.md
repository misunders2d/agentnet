# AgentNet

A runnable, self-hosted ordinary-agent communication extension. The same
Python package and API serve a laptop agent and an always-on server agent; the
server profile changes durable storage, enrollment, and capability gates, not
the product role. There is no separate Hub service or privileged Hub identity,
and this is not a patch to Claude, Codex, Pi, Antigravity, agent-deck, or any
other harness.

The current build provides working local semantics and strict fail-closed seams
for all architecture areas. It does **not** claim production certification.
SQLite acceptance is named `accepted_local`, synthetic identities are visibly
non-production, and federation/C3/peer-mesh/semantic-worker/protected-effect
features default off. The ordinary `always_on_server_agent` profile fails
closed until PostgreSQL, enrollment, keys, capabilities, and enabled-feature
evidence are present.

## Runnable today

Prerequisites: Python 3.13 and `uv`.

```bash
UV_CACHE_DIR=/tmp/uv-cache uv sync --extra test
uv run agentnet demo --data-dir /tmp/agentnet-demo
uv run agentnet a2a-demo
uv run agentnet harness-probe --data-dir /tmp/agentnet-harness-probes
uv run agentnet harness-probe --harness pi --data-dir /tmp/agentnet-harness-probes
uv run agentnet harness-demo --data-dir /tmp/agentnet-harness-demo
uv run agentnet init --config agentnet-config.json --data-dir .agentnet --domain local.example
uv run agentnet status --config agentnet-config.json
uv run agentnet serve --config agentnet-config.json --host 127.0.0.1 --port 8080
uv run agentnet verify
```

The advanced post-enrollment two-agent Compose comparator accepts no mutable runtime image tags. It is not the default zero-state installer and does not own Approval provisioning; use `agentnet server-agent setup` for the ordinary one-server profile. Operators using this separate comparator must supply verified repository and digest pairs through
`AGENTNET_SERVER_AGENT_IMAGE_REPOSITORY`/`AGENTNET_SERVER_AGENT_IMAGE_DIGEST`,
`AGENTNET_POSTGRES_IMAGE_REPOSITORY`/`AGENTNET_POSTGRES_IMAGE_DIGEST`, and
`AGENTNET_NGINX_IMAGE_REPOSITORY`/`AGENTNET_NGINX_IMAGE_DIGEST`. Build the AgentNet image with
an owner-verified Python base digest, for example
`--build-arg AGENTNET_PYTHON_BASE_DIGEST=<64 lowercase hex characters>`, publish it
to the operator-controlled registry, and use the resulting registry manifest
digest. The build also requires an owner-verified immutable uv image digest via
`--build-arg AGENTNET_UV_IMAGE_DIGEST=<64 lowercase hex characters>`; the Dockerfile
installs the locked Hatchling backend first and then installs the project from
the committed `uv.lock` with `uv sync --frozen --no-build-isolation`. A local
image ID is not a registry manifest digest and must not be substituted.

The demo creates two deterministic-only synthetic identities, stores explicitly
marked C0 synthetic bytes through a non-networked test lane, and reconciles the
offline recipient mailbox. It prints an explicit warning and only claims
`accepted_local`; the lane cannot carry C1/C2/C3 data, tasks, grants, rooms, or
effects and is not exposed over HTTP or MCP.

By default, `harness-probe` verifies all four exact Claude, Codex, Pi, and
Antigravity binary pins for the G01 gate. `--harness pi` (or another single
harness) is a diagnostic-only probe: it reports only that executable and never
claims four-harness readiness. `harness-demo` then starts each one in a distinct private
background lifecycle, exercises durable local custody, content-free passive
status, explicit human open, and bounded shutdown. It sends no semantic content
to any model and makes no inference or external-conformance claim.

Credentialed semantic evidence is deliberately separate and fail-closed:

```bash
AGENTNET_RUN_LIVE_HARNESS_INFERENCE=1 \
AGENTNET_LIVE_CLEAN_EVIDENCE_DIR=/owner/evidence \
AGENTNET_LIVE_CLEAN_EVIDENCE_KEY_ID=owner-key-1 \
AGENTNET_LIVE_CLEAN_EVIDENCE_PUBLIC_KEY=/owner/evidence/public.pem \
AGENTNET_LIVE_SANDBOX_EGRESS_WRAPPER=/owner/bin/evidenced-broker-wrapper \
uv run agentnet harness-live-gate --harness all --data-dir /tmp/agentnet-harness-live
```

Claude additionally requires `AGENTNET_LIVE_CLAUDE_BROKER_KEY` and
`AGENTNET_LIVE_CLAUDE_BROKER_URL`; Codex requires the corresponding
`AGENTNET_LIVE_CODEX_*` values. Pi and Antigravity require explicit owner-only
private auth directories and broker origins through
`AGENTNET_LIVE_PI_PRIVATE_AUTH_DIR`/`AGENTNET_LIVE_PI_BROKER_URL` and
`AGENTNET_LIVE_ANTIGRAVITY_PRIVATE_AUTH_DIR`/`AGENTNET_LIVE_ANTIGRAVITY_BROKER_URL`.
Credentials are injected only into their bound private worker and are never
accepted as command-line arguments or printed. Missing evidence, binary, or
credential fails the requested gate; it is never reported as a skip.

The npm/Pi package bundles the `agentnet-operator` skill under
`skills/agentnet-operator/` plus the fixed `agentnet server-agent setup`
implementation. The skill routes target-local install, ordinary server setup,
local-conformance, identity, supervisor, Pi-binding, and troubleshooting work to
product-owned commands and fail-closed references. Loading the skill does not
initialize or activate AgentNet and never grants identity or authority.

The real-network install-and-use contract is exactly the stable requirement set:
no reduced communication product, synthetic C0 substitute, or extra privileged
Hub product. AgentNet must ship or explicitly provision the maintained
mechanisms, adapters, manifests, and deterministic preflight checks required by
the selected supported profile. Operators supply approved hosts, secret values,
owner policy decisions, trust roots, and required human ceremonies; they do not
write missing approval services, scanners, storage adapters, receipt logic, or
vendor glue. A missing product component is a named blocker, not an operator
integration assignment or justification to weaken identity, authority,
durability, artifact, task, room, federation, or non-interruption semantics.

## Product-owned ordinary Linux server setup

The default host path is one fixed wrapper, not a general deployment framework. The target agent first resolves system-wide root-owned AgentNet, Node.js, and `uv` executables accessible to the locked service identities:

```bash
# no privileged or managed-host writes; npm may materialize caller-owned runtime
<resolved-root-owned-agentnet-path> server-agent setup --request /home/operator/.config/agentnet-setup/server-setup.json

# one frozen approved scope
sudo -- <resolved-root-owned-agentnet-path> server-agent setup \
  --request /home/operator/.config/agentnet-setup/server-setup.json \
  --expected-request-digest <approved-request-digest> \
  --apply --start
```

The strict request contains public metadata and absolute references only. Secret
values remain in owner-only Core and Approval environment files. Request-v1 is
an immutable scanner-backed compatibility contract: it omits `artifact_mode`,
requires `scanner_trust_file`, and uses approval-digest-v2 plus marker-v2.
Request-v2 requires explicit `artifact_mode`, uses approval-digest-v3 plus
marker-v3, and cannot reuse request-v1 approval or marker evidence. In v2,
`enabled` requires scanner trust; `disabled` forbids that field, including JSON
`null`, and selects exactly the `offline_custody` capability. The
no-managed-host-write plan validates semantic broker-credential policy, OIDC
callbacks, approver policy, mode-dependent scanner trust, fixed local PostgreSQL
peer contract, and exact service-visible Node/uv/AgentNet/`systemctl`/`useradd`
paths. The versioned approval digest binds request/input fingerprints, stable
no-follow executable hashes, and one hash computed from deterministic
path/type/size/content records for the exact root-owned AgentNet package tree
executed through `uv run --project`. Privileged apply reproduces the digest in
the npm launcher, repeats full Python preflight under the setup lock, and blocks
if any descriptor metadata or package-tree content changed. Node and Python each
compare two bounded descriptor snapshots for privileged setup inputs, accumulate
partial reads, and retain metadata/path custody checks so same-size drift does
not depend on filesystem timestamp advancement. Setup disables
Python bytecode writes so execution cannot alter the approved package tree. It
also re-reads full effective Approval trust after Core bootstrap, including
signer key IDs/public keys, and blocks before unit/marker commit on drift.

Ordinary PostgreSQL contract is fixed:

```text
OS user: agentnet
DB role/database: agentnet / agentnet
socket: /var/run/postgresql
DSN: postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet
HBA: local agentnet agentnet peer
ident map: none (exact names match)
```

Apply creates the fixed Core OS identity when absent, then runs a bounded
read-only canary as that identity. A second read-only probe under local
`postgres` identity checks parsed current-file `pg_hba_file_rules` and
`pg_ident_file_mappings` plus `pg_conf_load_time()` freshness against both auth
files; parse errors, stale reload state, broad/shadowing/mapped/non-peer rules,
TCP transport, wrong role/database, or a non-writable primary block before
AgentNet environment/config/database writes. AgentNet never edits PostgreSQL
role/database/HBA/ident state or reloads PostgreSQL. Those are separately
approved operator actions. A first apply may therefore return
`postgres_auth_not_ready` after creating the Core OS identity plus root-owned
`/var/lib/agentnet-setup` npm runtime and lock custody needed to execute and
serialize setup. It creates no AgentNet environment, Core/Approval config,
database schema, unit, Approval identity, or service. Install the exact scoped
rule, reload PostgreSQL, and rerun the same setup digest.

After the database gate, wrapper converges on Approval and isolated C0 responder
identities, private roots, Approval provisioning, Core bootstrap, fixed loopback
ports, five hardened systemd units, and structured redacted evidence. Before any Approval/Core
product subprocess, it uses `lstat` to reject symlink, dangling-symlink,
nonregular, ownership, and mode conflicts at fixed config/state/data child
paths. Existing service state and newly realized Core state are recursively
custody-checked before further product writes or unit/marker commit. Retry never skips bootstrap
from old marker data: it reloads Core after bootstrap, revalidates Approval,
writes exact units, then commits the request-versioned marker through
same-request, prior-byte compare-and-swap. Manual marker/config/unit surgery is
unsupported.

### Communication-only request-v2

Use
`skills/agentnet-operator/references/examples/ordinary-server-communication-only-setup-request.json`
only when signed communication testing must proceed before a maintained scanner
producer exists. It declares `artifact_mode: "disabled"`, omits
`scanner_trust_file`, provisions no scanner trust, artifact key, or artifact
directory, and accepts exactly `offline_custody`. Existing or symlinked artifact
key/directory residue blocks setup and runtime; AgentNet never deletes or
silently reinterprets it.

All artifact service methods and artifact HTTP routes fail with
`artifacts_disabled` before request-body parsing, identity/policy decisions,
metadata lookup, audit/capability issuance, object bytes, mailbox custody, or
task custody. Unsupported-event replay applies the same artifact-binding gate.
Empty-artifact signed messages, conversations, response obligations, and scoped
downward task custody remain available. Task testing stops at
`accepted_queued`; payload release, semantic execution, data access, tools, and
business effects require separate authority.

This restricted profile is for first-message testing. It does not satisfy any
`FILE-*` requirement, G13, production durability, production certification, or
ship readiness. Artifact-enabled request-v1 and request-v2 behavior remains
scanner-backed and unchanged.

A remote Manager does not execute target-host commands. Target coding agent
follows bundled
`skills/agentnet-operator/references/ordinary-server-setup.md`; remote peers may
provide immutable package instructions and inspect sanitized evidence only.
Start verifies local and public Core/Approval health. Signed broker readiness uses
host trust visible to CPython `ssl.create_default_context()` with certificate and
hostname verification; ambient `SSL_CERT_FILE`, `SSL_CERT_DIR`, and `SSLKEYLOGFILE`
are unsupported, fail closed before setup, and are removed from all four process-spawning service units; the fifth unit is the timer that invokes the hardened renewal service. Before owner enrollment, honest status is
`waiting_owner_oidc_or_passkey`.

Owner registration, workforce OIDC, WebAuthn UV, and exact server-harness
enrollment remain explicit human ceremonies. Server-local manager stages remote
guided enrollment; owner opens only fixed public Core `/activate` in a normal
browser. Both fixed activation routes are unauthenticated and rate-limited,
accept no selector/private input, and callback requires exact server-staged
approved OIDC owner identity. Approval result returns automatically to exact
waiting process through signed broker using purpose-separated possession;
owner transfers no URL, code, receipt, or secret. After `join guided` finishes identity-only, Core is stopped,
`server-agent activate` binds the exact identity offline, and the same setup
request with its approved
`--expected-request-digest` plus `--apply --start` restarts and
checks it. Final status is `operational`, `identity_enrolled=true`, and
`authority_granted=false`. Guided success also reports
`approval_delivery=automatic_possession_bound_signed_broker`; absence/mismatch
blocks current no-transfer onboarding claim. This proves neither production
durability nor any business permission.

Destructive package recovery is server-manager-only:
`agentnet server-agent reset --retain-external-prerequisites --confirm-package-state-removal`.
It requires explicit approval for both flags, takes permanent root-only setup
lock before inventory, rejects unknown custody, removes only allowlisted package
deployment units/state, preserves coordination lock/root, proves units inactive,
and reloads systemd on exact retry. It retains PostgreSQL, runtimes, package,
proxy/TLS/DNS/firewall inputs, operator config, and service identities. It is
never an owner-browser/fresh-laptop step or secret-rotation path.

## WebAuthn-UV approval service and deployment profiles

AgentNet includes the approval ceremony component under `agentnet approval`.
For the default self-hosted profile, run Core, PostgreSQL, and approval on the
existing server under distinct OS identities, credentials, storage roots, and
loopback services. The owner uses a WebAuthn authenticator outside the enrolling
harness. This profile reports `independent_boundary_proven: false`; it does not
claim protection from shared-server root compromise. A separately administered
approval host remains the optional high-assurance profile.

Normal onboarding does not require another computer, another person, Infisical
or another named secret manager, or per-command infrastructure approvals. Use
one frozen deployment approval; ask again only for materially changed,
destructive, restart, privilege-expanding, or high-risk scope. AgentNet resolves
hostnames, callbacks, package integrity, and configuration metadata rather than
asking the human for technical values.

The ordinary C0 profile requires the bounded
`authorization.bootstrap_plan.approve` purpose. It does not mount or advertise
the legacy wildcard founder ceremony. Bootstrap-plan approval binds one exact
server-resolved guided harness pair and one purpose-specific browser summary;
the plan commit prepares only the exact five communication plus five matching
revoke entitlements behind a `pending` guard. The dedicated signed C0 service is
the only runtime path that may activate or consume them. Generic policy,
messaging, mailbox, and administrative revocation paths deny all plan-issued
entitlements.

Under the dedicated approval-service OS identity, create an owner-only approver
specification:

```json
{
  "approvers": [
    {
      "principal_id": "security-owner",
      "authority_kind": "human",
      "domain_id": "corp.example",
      "allowed_purposes": [
        "authorization.bootstrap_plan.approve",
        "authorization.elevation.approve",
        "identity.credential.recover.approve",
        "identity.enrollment.approve",
        "identity.harness.revoke.approve",
        "organization.relationship.accept"
      ]
    }
  ]
}
```

Use mode `0600` for that file and an owner-only parent. Provisioning is
non-overwriting, creates separate signer/record keys and an exact-catalog
SQLite database, registers no passkey, and grants no authority:

```bash
agentnet approval provision \
  --config /etc/agentnet-approval/config.json \
  --data-dir /var/lib/agentnet-approval \
  --public-origin https://approval.corp.example \
  --rp-id approval.corp.example \
  --verifier-id approval.corp.example \
  --approvers /root/agentnet-approval-approvers.json \
  --owner-oidc-config /root/agentnet-approval-owner-oidc.json \
  --internal-core-credential-env AGENTNET_APPROVAL_CORE_TOKEN
agentnet approval status --config /etc/agentnet-approval/config.json
```

Core and Approval internal POSTs require both the runtime Bearer and the signed
one-use broker proof introduced with ApprovalStore schema v3. Upgrade Core and
Approval from the same immutable package together; there is deliberately no
Bearer-only mixed-version compatibility mode. Approval migrates exact v1/v2/v3
stores atomically to v4 before startup, and any catalog/history/replay-store
uncertainty keeps internal routes denied. A transport retry creates a fresh
broker nonce while retaining the original business idempotency key.

The provision result prints only core trust material: verifier ID, approver
identity, public receipt-signing key, allowed purposes, and signer key ID.
Install that public trust in each core's existing
`IndependentApprovalVerifier`; never copy approval private keys, record key, DB,
or browser capability into an agent host.

`serve` deliberately binds only an explicit loopback IP. Place a separately
credentialed HTTPS reverse-proxy role in front without changing the exact
configured origin/RP ID; keep access logs free of bodies. The proxy may share
the existing server in the default profile or use separate administration in
the optional high-assurance profile:

```bash
agentnet approval serve \
  --config /etc/agentnet-approval/config.json \
  --host 127.0.0.1 --port 8090
agentnet approval register-begin \
  --config /etc/agentnet-approval/config.json \
  --approver security-owner
```

`register-begin` prints only the stable public `/approval` entrypoint. The owner
opens that page, signs in with the exact preapproved OIDC account, and registers
a phishing-resistant passkey with user verification. OIDC uses Authorization
Code + PKCE/state/nonce. The service permanently pins the resulting owner
binding, rotates server-side browser sessions, and binds each WebAuthn challenge
to the exact active session plus configured RP ID, origin, and verifier ID. The
preauth cookie is Secure, HttpOnly, `__Host-`, and `SameSite=Lax` so the browser
can return from a cross-site IdP; authenticated session and CSRF cookies remain
`SameSite=Strict`.
Concurrent tabs retain independent OIDC state; a replacement session receives a
fresh challenge and the old session fails closed.

In the ordinary owner-OIDC profile, only the signed internal Core broker may
create approval requests. Stable browser APIs resolve encrypted request
capabilities inside Approval after exact owner principal/domain/session checks;
they never return a capability or receipt. The page shows a bounded human
summary and invokes WebAuthn UV. Possession-bound Core requests finish with
`waiting_agent` or `retrieved` and never display or regenerate a human value.
Legacy claim-code requests may display a short-lived one-time code; regeneration
requires the current code to remain unexpired, unretrieved, and below per-code
and cumulative limits and cannot revive an expired code merely because the
receipt remains current. The legacy `request-create` and fragment-capability
browser flow remain available
only in explicit lab profiles without owner OIDC; do not use that compatibility
path for the ordinary onboarding journey.

For product-brokered requests, configure only the runtime environment-variable
**name** above; inject one high-entropy secret separately into approval service
and Core runtimes. Never put its value in JSON, command arguments, logs, or
support messages. Broker routes are absent when the reference is unset. Core
can create/status an exact request and retrieve an already-issued receipt, but
cannot approve or sign. Approval capability stays encrypted on approval host. In a stable owner-OIDC
profile, `pending` and `watch` emit only content-free counts; `watch --open`
opens the public `/approval` page once when work appears. The browser lists and
selects exact requests only after owner authentication. Neither command emits
request IDs, purpose, digest, capability, canonical transaction, or receipt:

```bash
agentnet approval pending --config /etc/agentnet-approval/config.json
agentnet approval watch --config /etc/agentnet-approval/config.json --open
```

Explicit lab profiles without owner OIDC retain `request-create`, request IDs,
`approval open --request-id`, and fragment-capability URLs for compatibility.
Those commands are not part of ordinary onboarding. Stable profiles reject
manual request creation and make `approval open --request-id` open only the
public `/approval` page without resolving or printing the supplied request ID.

After WebAuthn, possession-bound Core mode displays no claim code or receipt.
Waiting process retains Core continuation/begin state. For OIDC, Core derives a
transaction-specific Approval possession secret with HKDF; for bootstrap plans,
Core generates and encrypts a distinct high-entropy secret. Core sends only that
secret's SHA-256 hash when creating the Approval request and later proves exact
purpose-separated possession through signed broker retrieval. Browser receives only
`waiting_agent`/`retrieved` status. No extra person, host, Slack/A2A relay,
copy/paste, or second report channel is required. Wrong secret, sixth cumulative
failure, expiry, replay, regenerated-code request, or conflicting retrieval
digest fails closed. Exact retries return same current receipt to Core.
Candidate never receives receipt or approval capability URL. Guided enrollment
composes this broker with hash-only Core continuation state and `agentnet join
guided`. Core stages exact request outside its database transaction, retrieves
receipt without consuming it, and leaves `EnrollmentService.complete()` as sole
atomic receipt/challenge consumer. A crash after that commit reconstructs exact
created binding and encrypts same completion response on retry. Legacy
claim-code retrieval remains explicit compatibility only.

Revoke a lost authenticator from the approval-service administrative context:

```bash
agentnet approval credential-revoke \
  --config /etc/agentnet-approval/config.json \
  --approver security-owner \
  --credential-id '<base64url credential id>' \
  --reason 'lost authenticator'
```

When the last active credential is revoked, pending requests expire. Missing
service, TLS, passkey, signer, record key, schema integrity, configured purpose,
or current challenge blocks approval. SQLite reports `single_host_local_only`;
the default profile also reports `independent_boundary_proven=false`. Real
Google/passkey, shared-host attack, rotation/recovery, backup/restore, and
operator evidence remain required for the claims they support. Separate-host
administration is required only for the optional high-assurance independence
claim.

## Confidential workforce OIDC

Public OIDC configuration never contains a client secret. Select one explicit
token-endpoint method:

- `none` — public Authorization Code + PKCE client; default for existing config;
- `client_secret_post` — confidential secret in the token form body;
- `client_secret_basic` — confidential HTTP Basic authentication.

Confidential methods require `client_secret_env`, which names a runtime
environment variable. Missing, empty, control-character-containing, or oversized
runtime values block composition before enrollment. Provider discovery must
advertise the selected confidential method. AgentNet does not infer a method
from secret presence or discovery ordering.

Google Workspace Web application profile:

```json
{
  "issuer": "https://accounts.google.com",
  "allowed_endpoint_origins": [
    "https://accounts.google.com",
    "https://oauth2.googleapis.com",
    "https://www.googleapis.com"
  ],
  "client_id": "<google-web-client-id>",
  "audience": "<google-web-client-id>",
  "redirect_uri": "https://agentnet.bezosapp.uk/v1/enrollment/oidc/callback",
  "allowed_signing_algorithms": ["RS256"],
  "token_endpoint_auth_method": "client_secret_post",
  "client_secret_env": "AGENTNET_OIDC_CLIENT_SECRET"
}
```

Merge this fragment into the complete OIDC enrollment profile containing the
independent approval trust anchors. In Google Cloud, use OAuth client type
**Web application** and authorize exactly
`https://agentnet.bezosapp.uk/v1/enrollment/oidc/callback`. Supply the secret
only through the private runtime environment named above; do not place it in
the JSON file, command line, logs, evidence, or support messages. Rotation takes
effect only after fresh runtime composition. A method change changes the
internal-invitation verifier identity, so finish or restart in-flight invitation
OIDC authorizations during upgrade.

These mechanics are locally tested. They do not prove a live Google ceremony,
independent approval deployment, production key custody, or owner-policy gates.

## Ordinary server-agent activation

`agentnet server-agent setup` is the ordinary entry point and composes the
lower-level `network create` primitive. It provisions the namespace, PostgreSQL
schema, Approval and owner-only software-key files plus scanner/artifact state
only when artifact mode is enabled. Communication-only mode provisions neither
scanner trust nor artifact state. Setup does not invent an enrolled identity or
authority. For a release that ships fixed remote guided flow, server-local AgentNet manager
completes exact OIDC, candidate-key possession, and WebAuthn human-approval
enrollment with one resumable command, then binds offline configuration
explicitly. Commands below are manager-owned server actions, never owner
instructions:

```bash
sudo -u agentnet -H <resolved-root-owned-agentnet-path> join guided \
  --server https://agents.corp.example \
  --domain corp.example \
  --harness pi \
  --name server-agent-1 \
  --state /var/lib/agentnet/guided-join.json \
  --identity /var/lib/agentnet/server-agent-identity.json \
  --browser remote
sudo systemctl stop agentnet-core.service
sudo -u agentnet -H <resolved-root-owned-agentnet-path> server-agent activate \
  --config /var/lib/agentnet/agentnet.json \
  --identity /var/lib/agentnet/server-agent-identity.json
sudo -- <resolved-root-owned-agentnet-path> server-agent setup \
  --request /home/operator/.config/agentnet-setup/server-setup.json \
  --expected-request-digest <approved-request-digest> \
  --apply --start
```

During the sole supported `0.1.31 -> 0.1.32` recovery, the manager reruns the
same approved setup request; operators do not invoke the internal binding
transition directly. Setup first validates the exact public 0.1.31 two-unit
marker and prior bytes. If the retained ordinary-server credential is non-lab
and expired no more than 24 hours earlier, 0.1.32 proves possession of its
existing key, preserves principal/harness/key and all authority/message state,
advances the credential epoch once, durably reconciles response loss, and then
commits the five-unit marker. Apply without `--start` performs no service start
or restart. A current, changed, revoked, older, repeated, or differently
versioned binding fails closed; reset or re-enrollment is not a fallback.

Guided command defaults to local system browser without printing authorization
URL. Explicit server-only `--browser remote` stores authorization URL encrypted
inside Core continuation custody, opens/discloses nothing, and waits. Owner opens
only fixed public Core `/activate`; unauthenticated/rate-limited activation
routes accept no selector/private input, select exactly one waiting unexpired
remote transaction, then redirect browser through OIDC to fixed Approval page.
Callback requires exact server-staged approved OIDC owner identity. Wrong account
returns retryable `activation_wrong_account` without staging challenge or
Approval request. Approval page polls at most 10 seconds for exact request to
close callback/poll race. Zero, multiple, expired, rejected, local-browser,
malformed, or conflicted state returns `remote_activation_unavailable`. Both modes store candidate key/state as owner-only files and poll
only Core. Exact process retrieves receipt automatically with private possession
state. Browser/human receives no claim code, receipt, continuation, broker
secret, or private URL. On success command replaces pending state with minimal
completion marker and reports zero granted authority. It never accepts or writes
a receipt file.

Pre-callback `awaiting_oidc` alone consumes the 60-poll anti-abuse budget.
Callback and Approval phases remain subject to 2–10 second polling control but
run until the fresh enrollment-challenge expiry. A local timeout with
nonterminal Core state resumes by rerunning the exact command. If Core proves
the stored continuation `expired` or `failed`, rerun that exact command with
`--replace-terminal-state`; it refuses absent, completed, malformed,
argument-drifted, or nonterminal state, reuses the same candidate key, and starts
a fresh OIDC transaction. Never delete/edit the state file or move it between
systems. Begin response loss reuses the persisted exact idempotency key and
returns the committed Core winner; request drift or ambiguous custody fails
closed.

`join begin`/`join complete` remain available only as the compatible expert
manual ceremony.

Fresh-laptop enrollment remains identity-only. A remote administrator must
never request or copy the beneficiary identity file/private key merely to issue
messaging authority. The generic `agentnet admin entitlement issue` command and
legacy founder ceremony are **not** an approved fallback for the ordinary C0
pilot: they cannot bind the exact harness pair, C0 payloads, event lineage,
mailbox ownership, use counts, and immediate five-entitlement cleanup required
by `docs/ZERO_STATE_C0_PILOT.md`.

The current repository candidate implements the fixed `BootstrapGrantPlan`, C0
service, and package-owned isolated responder. Never issue a live packet merely
from branch documentation: verify the installed release's actual CLI, package
evidence, deployment, and C0 gate first. Ordinary-server setup owns and runs the
dedicated responder service; operators must not recreate it through generic
`supervisor-run` or expose its private configuration or credential paths. When
those checks pass, the exact fresh-laptop sequence is:

```bash
# Fresh laptop, only after exact WebAuthn-approved plan commit.
agentnet c0-pilot start --identity .agentnet/identity.json
agentnet c0-pilot status --identity .agentnet/identity.json
agentnet c0-pilot complete --identity .agentnet/identity.json
```

No command accepts plan, peer, direction, payload, event, acknowledgement,
digest, entitlement, or use-count selectors. Public output is schema plus one
sanitized stage. `waiting_owner` means the request has local recipient custody;
`waiting_fresh` means the owner retrieved/acknowledged the exact request and the
fixed correlated reply has local recipient custody. Only
`COMPLETED_C0_ROUND_TRIP` means all seven typed facts and exact five-power cleanup
committed. `invalidated` is terminal identity-set drift, not a retry prompt.
Transport ACK/prose/status alone never proves completion.

Use no company or personal data. The responder invokes no model, semantic
worker, task, file, effect, tool, or A2A subsystem. The proof remains
same-principal, two-harness and `accepted_local`; it does not prove
distinct-principal policy or production durability.

The beneficiary-principal path never opens beneficiary private state. Core still
rejects a missing, inactive, non-human, or cross-domain principal and stale
policy revision. Replace the example `--policy-revision 1` with the exact current
domain policy revision when it is not 1. Principal and harness identifiers remain inside the authenticated Core/Manager
PD-001 path and are omitted from public human reports. Under current recorded
PD-002 default, exact waiting process retrieves Approval result automatically
through possession-bound signed broker; owner moves no code, receipt, URL, or
secret. Enrollment itself grants none of these entitlements.

Activation acquires the exact configured runtime lease with a distinct
activation owner. A running process therefore blocks the command; activation
never fences or restarts a live server. It runs no migrations or artifact
recovery. While holding that lease, it verifies the same PostgreSQL store has
the exact active domain, human principal, harness, credential ID/epoch,
non-lab assurance, and public key named by the owner-only identity profile. It
also requires exact service origin and audience equality.

The config replacement is owner-only and race-checked. Only
`enrolled_harness_id` and `enrolled_credential_id` change. No entitlement,
grant, relationship, capability, feature, A2A route, relay route, tool, data
access, service start, or business authority is created. An exact repeat is
idempotent. A different, retired, revoked, expired, stale, or mismatched
binding fails closed; credential rotation requires a future explicit binding
update rather than implicit startup rebinding.

Local harness bindings are an explicit ordinary-extension feature. Package
installation alone does not activate them. `agentnet supervisor-run` expects a
separate owner-only `agentnet-supervisor.json`; do not pass the core
`agentnet.json`. Validate it before launch:

```bash
agentnet supervisor-run --config agentnet-supervisor.json --check
```

Set `local_bindings_required` to `true` in that supervisor config. The measured
child receives its capability only after launch; loading the Pi extension in an
ordinary foreground Pi process therefore remains unavailable by design.

Enable `local_bindings` in the core configuration, include the `local_binding`
capability limit, and provide an owner-only capability-root file plus a private
Unix-socket path. Production
environment loading uses `AGENTNET_LOCAL_IPC_CAPABILITY_ROOT_FILE` and
`AGENTNET_LOCAL_IPC_SOCKET_PATH` (with optional TTL/frame limits). The extension
derives the actor from the enrolled current credential on every call. It issues
a Pi capability only after the child is running and measured, so the opaque
value must be delivered over the supervisor's private post-launch channel; it
is never a command-line, MCP, A2A, or caller-supplied bearer.

## Receive and acknowledge mailbox custody

After an enrolled recipient has durably persisted the exact inbox bytes and
dedup state, it can record the protocol's baseline delivery acknowledgement:

```bash
agentnet message inbox --identity .agentnet/recipient-identity.json
agentnet message acknowledge EVENT_ID \
  --envelope-digest ENVELOPE_DIGEST \
  --identity .agentnet/recipient-identity.json
```

The acknowledgement request is signed over its exact path and body. Recipient
identity is derived from the current credential, not a command argument. The
server checks the immutable envelope digest and writes `recipient_committed`
with one recipient-owned receipt and audit record. Exact response-loss retries
return that receipt without another transition. This operation does not claim
presentation, human reading, model processing, response-obligation progress,
task payload release, or a business effect. Harness-local tools expose the same
operation as `agentnet.inbox.acknowledge`.

## Upload and download bounded artifacts

These commands require artifact mode `enabled`. Communication-only servers
return `artifacts_disabled`; operators must not create artifact keys/directories
or add scanner trust manually to bypass the approved setup request.

An enrolled operator can place one explicit local file into the existing staged
artifact lifecycle without granting scanner or release authority:

```bash
agentnet artifact upload ./report.pdf \
  --identity .agentnet/sender-identity.json \
  --idempotency-key ARTIFACT_UPLOAD_KEY \
  --media-type application/pdf \
  --origin operator-selected-report \
  --classification C1
```

The command reads at most 16 MiB through one non-symlink, caller-owned regular
file descriptor, hashes those exact bytes, then performs signed reservation,
raw-byte upload, and manifest promotion. Initial success is `quarantined` with
`scanner_state: pending` and `released: false`. Exact retries use the same
idempotency key. If transport fails after reservation, retry first: the command
does not automatically abort because the server may already have committed a
later stage. Unpromoted reservations remain resumable until expiry and continue
to consume their reserved byte quota. The command never records a scanner
attestation, releases the artifact, returns the private object key, or attaches
unreleased bytes to a message. Once the operator knows an unpromoted reservation
is no longer needed, abort it explicitly:

```bash
agentnet artifact abort RESERVATION_ID \
  --identity .agentnet/sender-identity.json
```

After a separately authorized scanner and release service complete their own
steps, the entitled recipient can inspect content-free state and download:

```bash
agentnet artifact lifecycle ARTIFACT_ID \
  --identity .agentnet/recipient-identity.json
agentnet artifact download ARTIFACT_ID \
  --identity .agentnet/recipient-identity.json \
  --output ./report.pdf
```

Download issuance and consumption stay inside the signed client call. The
short-lived exact-harness capability is never printed. Output must not already
exist; AgentNet creates one exclusive `0600` file in a caller-owned directory
that is not group/world writable, writes at most 16 MiB, and fsyncs file and
directory. The printed plaintext SHA-256 and size let the recipient compare
expected integrity through an authorized channel.

These operator commands are not exposed as model-visible MCP/Pi path tools.
Passing arbitrary host paths would let a compromised model select files, while
passing bytes/base64 would expose protected content to model context and local
binding logs. A future harness artifact tool requires an owner-selected staging
root and opaque supervisor-issued handles; until then, use the explicit CLI or
signed client API. Task custody alone still grants neither artifact access nor
protected payload release; the exact recipient-owned supervisor flow below is
required.

## Implemented kernel

- exact verified actor union: human+harness, host guest+harness, workload, or
  `external_human_unverified` A2A;
- P-256 purpose-bound proofs, body/path/audience binding, freshness, and
  persistent replay rejection;
- exact enrollment transcript, PoP, atomic binding, lab-only approval verifier,
  and per-harness revocation without sibling revocation;
- human-only positive authorization, deny-only harness/device/session
  eligibility, one coherent revision, exact task grants, and audited decisions;
- directed `may_assign`: in-scope administrator-to-subordinate custody becomes
  `accepted_queued`; reverse/lateral/out-of-scope remains `pending_human`;
- encrypted local supervisor queues, authenticated live watch plus cursor
  fallback, separate worker lifecycle, explicit-open inbox, automatic durable
  obligation-counter reconciliation, content-free status, and no foreground
  message API;
- transactional per-recipient mailbox, at-least-once/idempotent submission,
  exact recipient-harness custody acknowledgement (`recipient_committed`) with
  one-write retry convergence, actor-owned receipts, expiry, cancellation, and
  `effect_unknown` controls;
- staged artifact reservation, immutable encrypted quarantine, exact manifest,
  scanner attestation, policy-gated release, and single-use download capability;
- rooms, from-join membership, temporary meetings, explicit frozen ownership
  transfer, and tombstone fallback;
- bilateral host-local guest schemas, non-transitive trust, sponsor/host revoke;
- official A2A Python SDK 1.1.0 routes and strict mapping/security helpers;
- one canonical local-tool composition service for MCP and Pi direct Unix IPC,
  with server-derived actors, current credential-epoch fencing, measured
  per-child capabilities, persistent replay rejection, and no caller
  bearer/identity arguments; the ordinary supervisor launches owner-only,
  parent-measured MCP endpoints and directly delivers sealed Pi capabilities;
  the exact local tool set includes direct send/inbox/inbox-acknowledge,
  conversation create/action/thread, and response-obligation
  inbox/list/get/progress/cancel/reconcile operations;
- provider-neutral interfaces for PostgreSQL, artifact storage, Cedar,
  SPIFFE/SPIRE, maintained MLS, workflow engines, and future mailbox relays;
- audit hash chain/checkpoints, quotas, privacy classes, redacted attention,
  non-enumerating directory, version negotiation, and generated JSON Schemas.

## Relationship governance workflow

The HTTP examples below show body shapes only. Every call still requires the
ordinary extension's authenticated request proof; a body field such as
`actor`, `verified`, or `policy_decision_id` is rejected and can never replace
the transport-derived actor.

1. The current owner of the proposed administrator endpoint, holding the exact
   `organization.relationship.propose` entitlement, submits:

   ```text
   POST /v1/relationships
   {
     "relationship": { ... exact Relationship ... },
     "proposal_expires_at": "... timezone-aware timestamp ..."
   }
   ```

   The `201` response key is `proposal`. Its lifecycle state is `proposed`, its
   activation basis is null, and it has no assignment or other authority.

2. Normal activation uses a fresh receipt from the independently configured
   verifier:

   ```text
   POST /v1/relationships/{relationship_id}/accept
   {
     "approval": { ... strict signed independent-approval receipt ... },
     "expected_transaction_digest": "... 64 lowercase hex ...",
     "expected_relationship_revision": 1,
     "expected_lifecycle_revision": 1
   }
   ```

   The verifier, not the caller, establishes the approver identity and owner
   kind. They must exactly equal the current human principal or host-local guest
   owner of the subordinate harness. The purpose must be
   `organization.relationship.accept`; the receipt is transaction-bound,
   fresh, one-use, and signed by a configured trusted key. Activation also
   rechecks both endpoint owners and credential epochs, domain/policy epochs,
   proposal expiry, relationship expiry, and predecessor revision.

   Before consuming the verified receipt, the same transaction inserts a
   pending `organization.relationship.activate` audit intent that binds the
   exact transaction/digest/revisions, transition, activation actor and basis,
   receipt evidence, activation time, and custody-only authority effect. The
   receipt is consumed, the edge is compare-and-swapped active, and the exact
   intent is completed at that same activation time before commit. Persisted
   authority checks reject a missing, pending, malformed, or inconsistent
   intent.

3. The separately implemented exception mechanism is two-step:

   ```text
   POST /v1/relationships/{relationship_id}/policy-exceptions
   { "exception": { ... }, "command": { ... signed authority command ... } }

   POST /v1/relationships/{relationship_id}/policy-exceptions/activate
   {
     "policy_exception_id": "...",
     "expected_transaction_digest": "...",
     "expected_relationship_revision": 1,
     "expected_lifecycle_revision": 1
   }
   ```

   Recording binds the exception to the exact transaction digest, relationship
   and lifecycle revisions, policy/domain and endpoint credential epochs, and
   an expiry no later than the proposal expiry. Activation consumes it once.
   The activation caller must be a current exact relationship participant or
   the exact recorded signer harness. Merely recording an exception creates no
   edge. Exception activation uses the same pending-to-completed exact local
   activation intent, with the recorded exception digest/reference in place of
   receipt evidence.

4. `GET /v1/relationships/{relationship_id}` is participant-scoped and
   non-enumerating. `POST /v1/relationships/{relationship_id}/revoke` accepts
   only an exact signed command at the current lifecycle revision. An endpoint
   can revoke/exit under `organization.relationship.revoke`; a nonparticipant
   requires the distinct `organization.relationship.admin_revoke` entitlement.
   All relationship responses and handled errors use `Cache-Control: no-store`.

Renewal never edits an active edge. Submit a new relationship ID at the next
coherent relationship revision and obtain fresh consent (or a fresh exact
exception). The proposal includes the predecessor snapshot. Activation
atomically supersedes that predecessor only if its lifecycle is unchanged;
revocation, expiry, or another activation makes the stale operation conflict.
At most one edge for an exact directed pair is active.

An active `may_assign` relationship only permits in-scope administrator-to-
subordinate `accepted_queued` custody. Protected reads, semantic processing,
tools, and effects still require the subordinate owner's current authority,
exact task intent/grant, and every normal policy check.

### Task-custody deadline and payload behavior

Every accepted assignment has an exact timezone-aware deadline. A supplied
deadline is preserved. If omitted, AgentNet derives a whole-second deadline
from the normalized immutable event creation time, capped by the exact
assignment scope's complete `max_duration` and one second before relationship
expiry. Before computing the custody digest and committing the event, it writes
that deadline into the canonical request and event `effect_deadline` and caps
`delivery_expires_at` at the same value. Exact retries reuse the stored bytes;
retry wall time never extends the accepted window.

Task custody is deliberately metadata-only at all generic read surfaces.
Mailbox reconciliation, conversation-thread reads, supervisor explicit-open,
and background delivery return `payload: null`, `payload_available: false`, and
an immutable custody reference for a task assignment or task-linked control.
The check uses the typed event/task fields as well as
`payload_access=task_grant_required`, so records without that marker are
also withheld; a removed or substituted marker fails immutable-envelope
validation rather than revealing bytes. Ordinary non-task messages continue to
return their authorized payloads.

Protected task processing uses a separate signed supervisor lifecycle; generic
reads never gain an unlock option:

1. `/v1/supervisor/executions/authorize` binds the exact recipient, event,
   cursor, envelope/payload, action `task.process`, event resource, mailbox
   source, receipt sink, classification, policy decision, and current TaskGrant.
   This step consumes the grant's one execution use.
2. The supervisor persists the redacted item in its encrypted local queue and
   records `/custody` with the exact server-known queue ID.
3. `/payload-release` fresh-checks actor, grant dimensions/revocation/expiry,
   policy/credential/domain epochs, delivery/effect/retention boundaries,
   active conflict-free execution intent, immutable bytes, and provenance. It
   commits one disclosure receipt plus audit record before returning plaintext.
4. Exact response-loss retry repeats current-state checks and returns the same
   receipt without another grant use. Revocation or drift denies the retry.
5. `/result` requires that exact release receipt; custody-only or migrated
   `result_uploaded` state cannot obtain payload retroactively.

The release response grants payload access and semantic processing for that
exact task only. `tool_authorized` and `effect_authorized` remain false. Current
TaskGrant has no implicit tool, network-origin, budget, credential, artifact, or
business-effect authority; those require separately modeled grants and effect
reservations. No `include_payload` or caller-selected idempotency field exists.

Both activation-intent variants are local database provenance. They do not
prove independent publication or witnessing, and a coherently compromised
database can alter local edge and intent rows together. Production operation
still requires the independent audit exporter/checkpoint/witness and
reconciliation evidence tracked by the release gates.

### Versioned schema and recovery

AgentNet has no supported prototype/pre-release database format. Immutable
migration 1 is the complete first-release schema and retains checksum
`c472c4442fce9195580bd55d6f01d831f9ef34cb8cc34b8389b72b1c572d484f`.
Current unreleased Core schema v5 adds durable protected payload-release
receipts in migration 2, guided OIDC enrollment continuation in migration 3,
the bounded C0 bootstrap-plan contract in migration 4, and exact OIDC-begin
response-loss recovery, current-credential renewal custody, and one-time
setup-bound expired ordinary-server replacement custody in migration 5.
Fresh SQLite and PostgreSQL stores create schema v5. An existing SQLite store
upgrades only when metadata, every migration record/checksum, and the entire
N/N-1 v4 object catalog match exactly; the v4→v5 change commits atomically or
rolls back without partial objects. PostgreSQL verifies contiguous checksums and
the complete live table, column type/null/default, constraint-definition, and
non-constraint-index catalog before v4 migration, after migration, and on every
v5 open.
Unknown, missing, altered, prototype, noncontiguous, future, or unsupported
older state fails closed before use.

Do not edit version metadata or infer authority from unilateral/prototype
records. Transition from exploratory data requires reviewed export of
non-authority content into a fresh current store and fresh exact bilateral
consent. Restore only an exact signed and verified compatible AgentNet backup;
rollback cannot synthesize/reactivate authority or downgrade metadata.

Backup manifest, trust, seal, and archive publication is fail-closed and bound
to the exact source database, schema, domain, key epoch, and bytes. If rollback
cleanup cannot prove that it still owns the installed pathname, AgentNet
atomically moves the product-visible name to a random owner-only
`.agentnet-quarantine-*` file. It intentionally retains that file for an
authenticated operator to inspect and remove out of band; it does not unlink a
path that another same-UID process may have replaced. A post-commit close or
durability failure is reported as publication-outcome unknown, not successful
rollback.

### Conflict adjudication and derived provenance

Assignment requests may include a strict typed resource intent. Incompatible
active intents enter deterministic `conflict_pending` records atomically. The
subordinate's exact current human/guest positive-authority owner can list only
their conflicts and must decide an exact revision-bound partition of every
current member. Released intents must be mutually compatible; rejects propagate
across overlapping conflicts, and an event queues only after all its pending
memberships clear. Concurrent/stale decisions and authority-epoch drift fail
closed. This process releases custody only and grants no data, semantic, tool,
or business-effect authority.

Workload event replies bind exactly one local causal parent; AgentNet resolves
the parent's immutable provenance digest in the same transaction and rejects a
missing parent, replay mismatch, classification reduction, sink widening, or
policy drift. Artifact promotion can include exact parent provenance references
and canonical transformation steps; all parents are server-resolved and every
executor must equal the authenticated harness. Derived records remain tainted,
unreviewed, and scan-pending. Public provenance origin registration accepts
only human input from the exact authenticated human harness; composed services,
not callers, create server origins.

The relationship-governance workflow is implementation evidence only.
**ORG-006 remains
owner-blocked:** the accountable owner has not approved eligible proposers or
proposal-entitlement holders, which roles may receive policy-exception or
admin-override entitlements, when such mechanisms may be used, or
mandatory-relationship, notice, review, retention, and appeal rules. Nothing
here claims those policies or their production operation have passed a gate.

## Security boundary

Protected production features are not enabled by a mock, safe default, skipped
test, or interface stub. The following remain real external/owner gates:

- PD-001 through PD-011 accountable owner decisions and the separate ORG-006
  relationship-governance policy;
- version-pinned Claude/Codex/Pi/Antigravity isolation and recovery;
- real workforce IdP operation over the direct validated-address OIDC transport,
  plus independent WebAuthn/OOB approval and target-platform key custody;
- A2A TCK, cross-SDK, certificate, callback, and public-peer evidence;
- HA PostgreSQL/object-store failover, fencing, PITR, and restore;
- maintained MLS lifecycle, bilateral partner lab, real scanner/WORM/KMS roots;
- adaptive hostile-model trials and signed installer/update lifecycle.

See [REQUIREMENTS_STATUS.md](REQUIREMENTS_STATUS.md) and
[docs/GATE_EVIDENCE.md](docs/GATE_EVIDENCE.md) for the evidence ledger.

## Repository map

```text
src/agentnet/
  adapters/       credential-free harness capability paths
  approval/       independent approval verifier contracts
  artifacts/      quarantine, manifest, scan, release, download
  authorization/  policy decisions, grants, elevation, Cedar seam
  bindings/       canonical MCP/local tools and measured Pi direct IPC
  core/           one ordinary-extension composition root
  delivery/       actor-owned state machine
  federation/     bilateral host-local guests
  gateways/       isolated public A2A boundary
  identity/       domains, actors, enrollment, credentials, revocation
  mailbox/        per-recipient custody and future custodian seam
  mesh/           disabled future opportunistic/distributed seams
  messaging/      immutable event construction
  organization/   directed relationships and assignments
  protocol/       canonical models, negotiation, A2A mapping, schema catalog
  rooms/          governance, meetings, maintained-MLS seam
  security/       signatures, proof, replay, encryption, update controls
  storage/        SQLite local profile and PostgreSQL readiness gate
  supervisor/     local queue, worker launcher, model-egress broker
schemas/v1/       generated versioned JSON Schemas
tests/            hermetic, integration, adversarial, and explicit external gates
docs/             architecture, bake-offs, threats, milestones, gates
```

## Source-of-truth boundary

The repository-local `docs/specification.md`, `docs/requirements.md`, and
`docs/final-verification.md` are the authoritative implementation handoff.
Their current hashes are recorded in `docs/ARCHITECTURE.md` and enforced by the
release verifier; sealed-audit hashes remain separately labeled as historical
evidence and must not be confused with the current annotated bytes.
