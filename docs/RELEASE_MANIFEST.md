# Release Manifest

Snapshot: 2026-07-27
Candidate: `agentnet 0.1.29`
Latest published package: `agentnet 0.1.28`
Evidence profile: OIDC callback interoperability repair candidate

This is not a production release. It is the human projection of
`RELEASE_MANIFEST.json`; local evidence cannot promote external, privileged,
owner, installer, or production-topology gates.

## Release claim

| Field | Value |
|---|---|
| Must-not-ship gate status | `BLOCKED` |
| Production ready | `false` |
| Ship eligible | `false` |
| Reason | Every must-not-ship gate remains non-passed; locally implementable accepted blockers are closed, while required P/E/O evidence remains absent. |

## Runtime and dependency lock

| Input | Pin/digest |
|---|---|
| Runtime | CPython `3.13.13` |
| Python range | `>=3.13, <3.15` |
| `uv.lock` | format `1`, revision `3`, SHA-256 `7be97ebc73c2ff70486aa60e019d7bbfa4bb05addb618ac338ab258c23f9b1a3` |
| `pyproject.toml` | SHA-256 `1ffbf4bfaf9ab9b9a85d913e8ac555c4323d27a12adfd7871b623107f1c9fcbe` |
| Build backend | `hatchling==1.28.0` and editable-build helper `editables==0.5`, both in the `build` dependency group and frozen lock |

The production Docker recipe installs the locked build group, then installs
the project with `uv sync --frozen --no-build-isolation`. Runtime, test, and
build direct dependencies are exact-pinned and checked against the complete
locked name/version resolution.
`jsonschema==4.26.0` remains the direct runtime pin for immutable response
schemas. `psutil==7.2.2` supplies cross-platform process metadata under
AgentNet-owned repeated-time/digest/account fencing. Conditional
`pywin32==312; sys_platform == 'win32'` supplies Windows SID, DACL, named-pipe,
and Job Object mechanisms. Neither package owns identity or authority.
`webauthn==3.0.0` is the exact maintained server-side registration and
assertion verification mechanism for the separately operated approval service;
AgentNet still owns identity, purpose, transaction, receipt, audit, and
lifecycle semantics.

## Protocol pins

| Protocol | Exact profile |
|---|---|
| A2A | release `1.0.1`; wire `1.0`; Python SDK `1.1.0` |
| A2A bindings | exact literals `HTTP+JSON` and `JSONRPC` |
| MCP | spec `2025-11-25`; Python SDK `1.28.1` |

A2A is an external low-trust edge. MCP and direct IPC share a server-bound
canonical composition service. Linux/macOS use peer-credential Unix sockets;
Windows uses protected remote-rejecting named pipes with server-derived client
PID. Pi capabilities use sealed memfd, a read-only inherited pipe, or a one-time
exact-process Windows pipe. Exact installed semantic interoperability and
privileged hostile-host qualification remain external under G05/G07.

## Generated schema catalog

| Schema | SHA-256 |
|---|---|
| `actor.json` | `88f69c93f49ef23016c5a6200ab7935188f536cfbd0577abc26387924e7660c0` |
| `artifact-manifest.json` | `425b9ffd781aa5dc93da4634ab1b53cd4198bcaa527cfdd5b6874604f3fc2d0b` |
| `audit-intent.json` | `4bbc0fc0356d07cf37772d277e4a1d0f6cfc235fe35ccbef50d45ddeb5c70232` |
| `enrollment-transaction.json` | `0c0a3cf5782b2a6248fb0b55db0f91b717506b3fb389fd8aaedcd4b9cfbdddaa` |
| `event.json` | `97e98d8f8ec0efdb07ba040989b9d38f27fca25aead97d403b5c4b965df69adb` |
| `federation-invitation.json` | `28824856533340c49866dda831fd4465c7115f8665dcae21aa84524fadeaa9f8` |
| `identity.json` | `1d35bfc082e379df29d900dcc867cda914d3aeeb13bedb303ee964455663a317` |
| `independent-approval-receipt.json` | `0031076ce642f25942539ac5d3159155afac965c20ae3d94dd9af811363d3648` |
| `internal-invitation-acceptance.json` | `cd598888e8c72734aead3b9b77f6bfb4e0638776147e383783b674e8980a916c` |
| `internal-invitation-record.json` | `0ffa16ad6bd4c9ee9b556c04bfe7a97db3695ccbd0e67a899512aad2e3b11849` |
| `internal-invitation-request.json` | `2b28222f11e4a2b8019f2fb6bc18cb595ce481e2d54f711fc2921aef12b63899` |
| `internal-invitation-transaction.json` | `35c51dd0045954c37e8bde6f59b1fc0563025eaf388f39067e439f00f12d8fc7` |
| `presence.json` | `6b19a5d5c0f74e5d9cb2336b20dcdab790a25321e2e20bfbeb4ab302839cf1c5` |
| `protocol-error.json` | `d7830dc9e90872d20fafc109b575261c2cd97cbce2700d9150edd8f794b03b21` |
| `provenance-reference.json` | `18a052c3635e8c04783bad66cec1cd0932c786fe00bdd404f2c33b09a40ad82a` |
| `receipt.json` | `11f21ebdb2d846a0f5795bcb57705d7170c3e8947fde5ba38b8df6074ebb39c9` |
| `relationship-consent-transaction.json` | `96ef4cd4198df69aaf54610a1084fb3dc9d6320f4489da875bf0dc3c47477ce3` |
| `relationship-policy-exception.json` | `22d19d9f7f0e714a0de914e0090d5a5474ecd63c6239212268ab6e73470a8d20` |
| `relationship.json` | `86b536881373bd6f4e507ad8983573953a1238e92f57fe321eafb88af21ef013` |
| `revocation.json` | `0ef0be7d658008ee159d5e1b82f524f5f73d48f09153330888514b9381645903` |
| `room.json` | `4c974837c2e355297dfa1012709b7d5dfa5c32f26f8fd99dd88f6d54ab39be52` |
| `task-conflict-adjudication.json` | `fb25bd652867057f901b843d0821f08768f30b16b8e5cb26afef37469729a242` |
| `task-conflict-outcome.json` | `b3777bec880c580f5922addc5159f8fcdaad22610a0cc0079a2ee1cf2891b0d1` |
| `task-execution-intent.json` | `38daca5b00e2e24d086bee72c090b4f1868c32cbb850cd02420728a383720262` |
| `task-grant.json` | `5b51a9f0ad10541b91f7abc764270e2af2d591624b51586f9615259fcaecd57c` |

## Component decisions

These status strings are exact projections of the machine manifest.

| Component key | Status |
|---|---|
| `canonical_schemas` | `BUILD_OWN` |
| `supervisor_harness_lifecycle` | `BUILD_OWN_INTEGRATION` |
| `a2a_gateway` | `ACCEPT_BASELINE_PRODUCTION_GATE_OPEN` |
| `a2a_internal_fabric` | `REJECT_AS_CORE` |
| `agntcy_oasf` | `NOT_SELECTED_EXTERNAL_BAKEOFF_OPEN` |
| `agntcy_directory` | `NOT_SELECTED_EXTERNAL_BAKEOFF_OPEN` |
| `agntcy_slim` | `NOT_SELECTED_BASELINE_COMPARATOR_ONLY` |
| `matrix_components` | `REJECT_AS_AUTHORITY_COMPONENT` |
| `mls` | `C3_DISABLED_EXTERNAL_MLS_OWNER_BLOCKED` |
| `spiffe_spire` | `REGISTERED_WORKLOAD_BOUNDARY_BUILT_EXTERNAL_SPIFFE_OPEN` |
| `human_auth_approval` | `OIDC_WEBAUTHN_COMPONENT_BUILT_EXTERNAL_OWNER_BLOCKED` |
| `cedar` | `OPTIONAL_TARGET_RUNTIME_NOT_ENABLED` |
| `postgresql` | `MULTI_HOST_PRIMARY_RECONNECT_FENCED_LOCAL_HA_EXTERNAL` |
| `artifact_store` | `PERSISTENT_BYTE_QUOTA_FILESYSTEM_POSTGRES_MANIFEST_RESTORE_EXTERNAL` |
| `file_safety` | `LOCAL_PREFILTER_QUARANTINE_ATTESTATION_SCANNER_EXTERNAL` |
| `mcp_local_binding` | `PARENT_BOUND_MCP_AND_PI_CAPABILITY_COMPOSED_INTEROP_EXTERNAL` |
| `clean_worker_launcher` | `DETERMINISTIC_INSTALLED_TESTED_SEMANTIC_EXTERNAL` |
| `cryptographic_primitives` | `REUSE_REQUIRED_PROFILE_GATE_OPEN` |
| `temporal_style_workflow` | `EXPLICIT_EFFECT_LIFECYCLE_COMPOSED` |
| `audit_witness` | `HASH_CHAIN_AND_RELEASE_INTENT_BUILT_WITNESS_EXTERNAL` |
| `future_hubless_custody` | `ONE_HOP_PEER_RELAY_COMPOSED_DISTRIBUTED_AUTHORITY_DISABLED` |

## Must-not-ship gate status

No gate is passed. `REVIEWED_PARTIAL` means a local/external-shaped record was
reviewed; it does not mean the required evidence tier or official gate passed.

| Gate | Status | External evidence |
|---|---|---|
| G01 | `BLOCKED_EXTERNAL` | `MISSING` |
| G02 | `BLOCKED_EXTERNAL` | `MISSING` |
| G03 | `BLOCKED_EXTERNAL` | `MISSING` |
| G04 | `FAILED` | `REVIEWED_PARTIAL` |
| G05 | `BLOCKED_EXTERNAL` | `MISSING` |
| G06 | `BLOCKED_OWNER` | `MISSING` |
| G07 | `BLOCKED_EXTERNAL` | `MISSING` |
| G08 | `BLOCKED_OWNER` | `MISSING` |
| G09 | `BLOCKED_EXTERNAL` | `MISSING` |
| G10 | `PARTIAL` | `MISSING` |
| G11 | `BLOCKED_OWNER` | `MISSING` |
| G12 | `BLOCKED_OWNER` | `MISSING` |
| G13 | `BLOCKED_OWNER` | `MISSING` |
| G14 | `BLOCKED_EXTERNAL` | `MISSING` |
| G15 | `BLOCKED_EXTERNAL` | `MISSING` |
| G16 | `BLOCKED_OWNER` | `MISSING` |
| G17 | `BLOCKED_OWNER` | `MISSING` |
| G18 | `BLOCKED_EXTERNAL` | `MISSING` |
| G19 | `BLOCKED_OWNER` | `MISSING` |

## External supply-chain evidence

| Evidence key | Status | Passed |
|---|---|---|
| `sbom` | `EXTERNAL_REQUIRED` | `false` |
| `provenance` | `EXTERNAL_REQUIRED` | `false` |
| `signature` | `EXTERNAL_REQUIRED` | `false` |
| `installer_lifecycle` | `EXTERNAL_REQUIRED` | `false` |

## Published-release and local-candidate evidence

Published `0.1.22` corrected the PostgreSQL 18 reserved SQL alias exposed by
exact public-artifact startup. Tag `v0.1.23` contained the reviewed product-owned
ordinary Linux setup boundary, but its staging workflow stopped before npm
staging because one hermetic interruption test mocked `/usr/bin/useradd` on a
runner without that path. No `0.1.23` package was staged or published.

Published `0.1.24` changed only that fixture to mock AgentNet's validated
host-tool resolver directly. Runtime behavior remained the reviewed fixed setup:
strict request/reference custody, exact secret-free plan/apply digest approval,
separate locked Core and Approval identities, create-or-exact-match managed
state, bounded systemd scope, exact loopback/public health identity,
interruption recovery, and one canonical bundled operator workflow. Setup grants
neither identity nor authority.

Published `0.1.25` repairs two JSON-RPC interoperability defects exposed by the
pinned official A2A TCK at commit
`5996b79f9cefa6fc390980e383e358a66fb9e49e`: it serves `/rpc` and `/rpc/`
through the same strict endpoint without a POST redirect, and restores an absent
SDK request tenant only from the exact verified opaque route binding. Missing or
spoofed route tokens and conflicting request/context tenants fail closed. Signed
same-idempotency requests across both aliases create no second effect, rejected
requests persist no event/task/task-event residue, and inaccessible or missing
tasks retain non-enumerating `TASK_NOT_FOUND` behavior.

The focused local A2A lane reports `57 passed`; the source lane excluding
installed-live inference and this release-manifest self-check reports `1386
passed, 15 expected host/PostgreSQL skips`. Focused official JSON-RPC checks
report `3 passed`; the full JSON-RPC MUST run reports `50 passed, 11 failed, 174
skipped`, so G04 remains `FAILED`. Exact prepublication, retained-artifact,
recursive packed, Pi-loader, and public-package identity results are recorded in
the immutable `0.1.25` package evidence manifest.

Published `0.1.27` adds the explicit request-v2 communication-only ordinary-
server profile: exact `offline_custody`, version-disjoint digest-v3/marker-v3,
no scanner or artifact state, fail-closed artifact services/routes/bindings,
and signed local message/mailbox/ACK/task-custody evidence. Its immutable tag
and public npm package bind commit
`4641503ac6ee398db44f2c3fffe4c639b7c60561`. That evidence does not prove a
created network, fresh-laptop enrollment, or native cross-host message.

Published `0.1.28` repairs three failures exposed only by the Hub's root-installed
`0.1.27` verifier. Python and Node privileged setup-input readers now take two
bounded content snapshots, accumulate short reads, retain metadata/path custody
checks, and fail closed when snapshots differ even if a filesystem does not
advance same-size rewrite timestamps. Launcher tests separately prove the
structured non-root-ownership and root-owned/non-traversable rejection states
instead of matching caller-dependent English text. Ordinary-user and UID-0
user-namespace focused runs each report `7 passed`; the full server-setup and npm
conformance lanes report `92 passed` and `10 passed`. The final source and two
clean recursively packed npm generations each report `1418 passed, 16 expected
platform/dedicated-PostgreSQL skips`; package, release-verifier, launcher,
recursive-package, and byte-identical wheel/sdist checks pass. Those lanes
exclude installed-live-inference, subprocess-lifecycle, and bake-off-evidence
files; the two installed-harness pin failures remain non-green and were not
rerun or waived. The UID-0 lane uses a Linux user namespace and is not external
privileged-host qualification. Exact public-package root-installed Hub verification later passed, after which a
real owner Google OIDC attempt exposed callback-shape rejection before transaction
claim; owner binding and passkey counts remained zero.

Candidate `0.1.29` replaces exact-two-total-parameter callback parsing with one
shared strict parser. Decoded names remain distinct through duplicate rejection,
including unknown names. Success is exactly recognized `code`+`state`; provider
failure is exactly recognized `error`+`state` with optional bounded metadata;
mixed/orphan shapes deny. Unique unrecognized OAuth response extensions are
ignored, never trusted or rendered. Bound provider errors terminally consume
only the matching pending owner, enrollment, or recovery transaction without
token exchange. Existing cookie/state, PKCE, nonce, issuer/audience/signature,
expiry, replay, and owner-binding checks remain in force. The failed public
callback URL is not retried or reused. Focused callback checks report `92
passed`; source and both clean recursively packed npm generations each report
`1443 passed, 16 expected platform/dedicated-PostgreSQL skips`. Release verifier,
`agentnet verify`, package checks, Codex/Claude/constitution reviews, and two
byte-identical wheel/sdist builds pass. External installation and live enrollment
evidence remain absent until exact public-package deployment and a fresh OIDC
transaction complete.

Privileged clean-host apply, cross-SDK/public-peer interoperability, live
OIDC/WebAuthn ceremony, independent public-artifact deployment, and production
evidence remain pending or separately gated. No production-certification or
gate-promotion claim is made.

## Verification boundary

`scripts/verify_release.py` checks authoritative-source hashes, frozen runtime,
test and build dependencies, byte-current generated schemas, deployment and
evidence input hashes, exact 85-row/11-decision ledgers, machine/human gate
status parity, component/protocol projections, and blocked release claims. A Git
checkout must retain the exact candidate-artifact `.gitignore`; npm installs,
where npm strips nested `.gitignore` metadata, must retain the exact portable
`RETENTION.md` and archive set instead. The verifier cannot certify the
explicitly missing privileged, external, owner, signing, installer, HA/PITR,
real-harness, partner, or public-peer evidence.
