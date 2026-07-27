# Product-owned ordinary Linux server setup

Read this file only for the default self-hosted `always_on_server_agent` profile. Target server coding agent owns every host command. Remote Managers may provide immutable public package metadata and inspect sanitized evidence, but must not shell into target or handcraft AgentNet users, roots, units, markers, or recovery state.

## Outcome and hard boundaries

`agentnet server-agent setup` is only ordinary host-setup entry point. It is a fixed state machine, not deployment DSL.

AgentNet setup may manage only:

- locked `agentnet` and `agentnet-approval` OS identities;
- `/var/lib/agentnet`, `/var/lib/agentnet-approval`, root-only `/var/lib/agentnet-setup`, and `/etc/agentnet-secrets`;
- `agentnet-core.service` and `agentnet-approval.service`;
- loopback Core `127.0.0.1:8080` and Approval `127.0.0.1:8090`.

Operator-owned prerequisites remain:

- immutable system-wide root-owned package plus Node.js, `uv`, `systemctl`, and `useradd` executables visible to hardened services;
- local PostgreSQL cluster, role/database, exact peer-auth rule, and PostgreSQL reload;
- distinct public DNS/TLS reverse-proxy routes;
- workforce OIDC registrations and owner-only runtime secret files;
- maintained scanner public trust when artifacts are enabled; no scanner input when communication-only mode is selected;
- human OIDC/WebAuthn ceremonies and policy decisions.

Setup verifies those boundaries but never edits PostgreSQL authentication files, DNS, certificates, reverse proxy, firewall, unrelated services, identity, or authority. PostgreSQL administration/reload, setup apply/start, enrollment, activation, and authority are separate approval boundaries.

## State machine — follow in order

```text
package verification
  -> owner-only input preparation
  -> no-managed-host-write plan
  -> frozen setup approval
  -> apply: runtime/input recheck under lock
  -> create fixed Core identity if absent
  -> PostgreSQL service-identity + live-rule gate
  -> create Approval identity if absent
  -> AgentNet private roots/env/config/bootstrap
  -> exact unit write
  -> version-selected marker compare-and-swap commit
  -> optional bounded service start
  -> loopback/public health
  -> human OIDC/WebAuthn
  -> offline activation
  -> same-digest apply/start
  -> readiness, identity true, authority false
```

Never reorder or skip gates. Never infer setup success from package installation, process exit alone, an old marker, Manager A2A acknowledgement, public 200 without exact JSON identity, or an existing config file.

## Phase 1 — verify immutable service-visible runtime

Resolve exact system-wide paths before preparing approval scope:

```bash
command -v agentnet
command -v node
command -v uv
readlink -f "$(command -v agentnet)"
readlink -f "$(command -v node)"
readlink -f "$(command -v uv)"
agentnet --version
node --version
uv --version
```

Required:

- canonical absolute paths;
- root-owned, not group/other writable;
- package tree root-owned and service-readable;
- Node.js, uv, AgentNet, `systemctl`, and `useradd` executable bytes stable between plan and locked apply;
- no selected path or resolved ancestor under `/root`, `/home`, or `/run/user` because units use `ProtectHome=true`;
- no temporary, per-user, NVM-home, shell-wrapper, request-selected, or ambient-PATH fallback at apply.

If current Node/uv/package resolves under a protected home, stop during plan. Install or copy the verified immutable runtime into a system location under a separate approved package-install action. Do not patch rendered units manually.

## Phase 2 — prepare owner-only inputs

Use one owner-only staging directory. Every staged file is canonical, regular, mode `0600`, link count `1`, and outside repositories/chat/logs. Select one profile before creating request bytes:

| Profile | Request | Files | Capabilities and artifact boundary |
|---|---|---|---|
| scanner-backed request-v1 | [`request.v1` example](examples/ordinary-server-setup-request.json) | `core.env`, `approval.env`, `core-oidc.json`, `approval-owner-oidc.json`, `approvers.json`, `scanner-trust.json`, `server-setup.json` | `offline_custody`, `artifact_storage`; scanner trust required |
| communication-only restricted | [`request.v2` example](examples/ordinary-server-communication-only-setup-request.json) | same set except no `scanner-trust.json` | only `offline_custody`; artifact routes and non-empty message/task artifact bindings fail before custody |

Request-v1 forbids `artifact_mode` and retains original scanner-backed meaning. Request-v2 requires explicit `artifact_mode`; `disabled` forbids `scanner_trust_file`, including JSON `null`, while `enabled` requires it. Never edit the v1 example into v2 or reuse old approval/marker evidence across versions. Communication-only mode is for native messaging and task-custody testing while artifacts are unavailable; it does not satisfy FILE-001..006, G13, production, or ship readiness.

Copy the exact mode-matching example and replace every illustrative value with approved local metadata.

`core.env` contains:

- `AGENTNET_DATABASE_URL=postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet`
- confidential Core OIDC secret only when selected method requires it;
- `AGENTNET_APPROVAL_CORE_TOKEN`.

`approval.env` contains:

- confidential Approval OIDC secret only when selected method requires it;
- same `AGENTNET_APPROVAL_CORE_TOKEN` value.

Broker credential contract is exact: 43–512 printable ASCII characters in range `0x21..0x7e`; no whitespace, quotes, backslash, control, Unicode, empty value, or Core/Approval mismatch. Setup validates this before AgentNet users/files/database state are created. Correcting only private environment values while preserving the approved absolute files and variable-name sets keeps the same digest; rerun plan to confirm it before retry. A changed file path, variable-name set, public input fingerprint, request, or runtime identity requires a new digest and approval.

Environment syntax is strict unquoted `NAME=value`. Whitespace, shell quoting, backslashes, duplicate names, empty values, or setup-owned interpreter variables are rejected.

Never print or copy environment values, private keys, signer custody, approval receipts, claim codes, or identity files.

## Phase 3 — PostgreSQL prerequisite contract

Ordinary profile uses one fixed local Unix-socket peer contract:

| Field | Exact value |
|---|---|
| OS service user | `agentnet` |
| PostgreSQL role | `agentnet` |
| Database | `agentnet` |
| Socket directory | `/var/run/postgresql` |
| DSN | `postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet` |
| Auth method | `peer` |
| `pg_ident` map | none; exact OS/DB names match |
| Required HBA line | `local agentnet agentnet peer` |
| Placement | before every potentially matching broader `local` rule |

Operator provisions local cluster, LOGIN role, database ownership/privileges, exact HBA line, and reload. AgentNet does not edit those resources.

Apply validates all of this before writing AgentNet environments/configs or running `network create`:

1. forks bounded read-only canary under exact `agentnet` OS identity;
2. connects through fixed Unix socket;
3. verifies `current_user=agentnet`, `current_database=agentnet`, Unix-socket transport, and writable primary;
4. under local `postgres` OS identity, reads parsed current-file `pg_hba_file_rules` and `pg_ident_file_mappings`, then proves `pg_conf_load_time()` is not older than either auth file;
5. combines that freshness proof with successful service-identity canary and rejects parse errors, mapped/broad/shadowing/non-peer rule, or stale unreloaded file state.

If service user does not exist yet, first approved apply may create fixed Core OS identity plus root-owned `/var/lib/agentnet-setup` npm runtime and lock custody, then return `postgres_auth_not_ready`. It creates no AgentNet environment, Core/Approval config, database schema, unit, Approval identity, or service. That is expected safe partial state. Stop. Obtain separate exact approval for operator-owned PostgreSQL role/HBA/reload work, perform it locally, then rerun same AgentNet request digest. Do not count root/operator connectivity, TCP/SCRAM/trust, raw config grep, or Core health as peer-auth proof.

## Phase 4 — no-managed-host-write plan

Run without `sudo` or `--apply`:

```bash
<resolved-root-owned-agentnet-path> server-agent setup \
  --request /home/operator/.config/agentnet-setup/server-setup.json
```

Require:

- `schema=agentnet.server-setup.evidence.v1`;
- `status=planned`;
- one 64-hex `request_digest`;
- request bytes, canonical mode-applicable input references, and non-secret fingerprints are bound. Request-v1 uses approval digest v2; request-v2 uses approval digest v3 and binds explicit artifact mode. Both bind exact Node.js, uv, AgentNet, `systemctl`, and `useradd` executable paths/content and one hash computed from deterministic path/type/size/content records for the full root-owned AgentNet package tree executed by `uv run --project`;
- `managed_units` exactly `agentnet-approval.service` and `agentnet-core.service`;
- fixed loopback ports;
- PostgreSQL prerequisite manifest showing exact peer contract and apply-time canary pending;
- `identity_enrolled=false`;
- `authority_granted=false`;
- `production_durability_proven=false`.

Plan performs no privileged or managed-host mutation: it does not create service identities, AgentNet roots, secret copies, database schema/config, units, or services. The npm launcher may materialize its caller-owned Python runtime before Python preflight; that is code-install/runtime state, not server setup state. Any invalid runtime, input custody, secret policy, OIDC contract, callback, mode-applicable scanner trust, DB contract, or digest blocks before managed-host mutation.

Show human concise frozen scope. Ask again when scope or executable bytes change, or when a separate destructive/restart/privilege-expanding/high-risk action appears. PostgreSQL administration/reload remains separate even when plan describes exact required rule.

## Phase 5 — apply and start

After exact setup approval:

```bash
sudo -- <resolved-root-owned-agentnet-path> server-agent setup \
  --request /home/operator/.config/agentnet-setup/server-setup.json \
  --expected-request-digest <approved-request-digest> \
  --apply --start
```

Privileged npm launcher reproduces digest before creating its Python runtime. Python then performs full read-only preflight, acquires setup lock, rereads inputs/runtime, and requires same digest before first managed write.

Expected outcomes:

- `status=blocked`, `blocker=postgres_auth_not_ready`: Core identity and root-owned setup runtime/lock may now exist; no AgentNet env, Core/Approval config, database schema, unit, Approval identity, or service was created. Complete separately approved PostgreSQL prerequisite and rerun same digest.
- `configured_not_started`: apply succeeded without `--start`; rerun same digest with approved `--start`.
- `waiting_owner_oidc_or_passkey`: services and exact health are ready; identity and authority remain false.
- `operational`: only after offline activation and exact Core readiness; identity true, authority false.

Start scope is limited to:

1. systemd daemon reload;
2. Approval enable/start;
3. Core enable;
4. Core restart;
5. exact active-state checks;
6. loopback and public `/healthz` identity checks;
7. Core loopback/public `/readyz` only after identity activation.

## Retry, marker, and failure rules

Same approved request is deliberately retryable.

- Bootstrap is rerun/revalidated for preexisting Core state; old marker never causes bootstrap skip.
- Core config is reloaded after bootstrap.
- Approval trust is reloaded and revalidated.
- Exact unit bytes are written before marker commit.
- Request-v1 writes marker-v2 and retains same-request marker-v1 migration.
- Request-v2 writes marker-v3 binding exact `artifact_mode`; marker-v1/v2 cannot satisfy request-v2.
- Marker update requires same request and exact prior-byte compare-and-swap under setup lock.
- Marker is provenance only. It never proves identity, authority, service health, readiness, PostgreSQL durability, or business effect.

For a failure, preserve named blocker and stage. Nonzero product commands report bounded stage/exit/stderr-presence evidence; raw stderr/env/arguments are never emitted. Timeout/start failures remain typed. Fix only named external prerequisite, then rerun exact request. Never manually edit/delete marker, config, keys, DB, identities, or units; never use `--force`.

A changed request, input fingerprint, executable path/hash, unit set, marker request, state invariant, or package requires new plan and approval. Do not refresh marker by hand.

## Human ceremonies and activation

Setup never automates OIDC or WebAuthn.

1. Register owner passkey through shipped Approval surface under dedicated identity:

   ```bash
   sudo -u agentnet-approval -H <resolved-root-owned-agentnet-path> approval register-begin \
     --config /var/lib/agentnet-approval/config.json \
     --approver <resolved-owner-principal-id>
   ```

2. Owner signs in and registers phishing-resistant passkey with user verification.
3. On private unrecorded POSIX TTY, run guided identity-only enrollment under Core identity:

   ```bash
   sudo -u agentnet -H <resolved-root-owned-agentnet-path> join guided \
     --server <resolved-core-https-origin> \
     --domain <resolved-domain> \
     --harness <resolved-supported-harness> \
     --name <resolved-server-display-name> \
     --state /var/lib/agentnet/guided-join.json \
     --identity /var/lib/agentnet/server-agent-identity.json \
     --browser terminal
   ```

4. Owner opens only TTY-disclosed HTTPS URL, completes OIDC/WebAuthn, and enters short-lived claim code into masked TTY. Never relay URL/code through chat, A2A, logs, or files.
5. Stop Core, activate exact identity offline, then rerun same setup digest:

   ```bash
   sudo systemctl stop agentnet-core.service
   sudo -u agentnet -H <resolved-root-owned-agentnet-path> server-agent activate \
     --config /var/lib/agentnet/agentnet.json \
     --identity /var/lib/agentnet/server-agent-identity.json
   sudo -- <resolved-root-owned-agentnet-path> server-agent setup \
     --request /home/operator/.config/agentnet-setup/server-setup.json \
     --expected-request-digest <approved-request-digest> \
     --apply --start
   ```

Final setup must report `operational`, `identity_enrolled=true`, exact public Core readiness, and `authority_granted=false`. Enrollment remains identity-only.

## Evidence limits and cleanup

Keep only redacted structured evidence needed for audit: request digest, package version, public origins, fixed prerequisite facts, stage/status, units, and non-secret blockers. Never retain environment values, reusable secret hashes, raw stderr, private enrollment material, or credential-bearing URLs.

Setup does not prove HA, PITR, KMS/HSM custody, independent shared-host protection, external conformance, production certification, native cross-host messaging, or authority. Communication-only setup also proves no artifact, scanner, FILE/G13, or ship outcome. Clean-host testing must separately prove package install, plan, PostgreSQL gate, apply, start, health, identity false, authority false, injected retries, exact mode/capabilities, mode-applicable scanner/artifact absence or readiness, and complete test-fixture cleanup.
