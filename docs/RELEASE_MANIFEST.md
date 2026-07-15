# Release Manifest

Snapshot: 2026-07-15
Candidate: `agentnet 0.1.7`
Profile: self-hosted local conformance candidate

This is not a production release. It is the human projection of
`RELEASE_MANIFEST.json`; local evidence cannot promote external, privileged,
owner, installer, or production-topology gates.

## Release claim

| Field | Value |
|---|---|
| Status | `BLOCKED` |
| Production ready | `false` |
| Ship eligible | `false` |
| Reason | Every must-not-ship gate remains non-passed; locally implementable accepted blockers are closed, while required P/E/O evidence remains absent. |

## Runtime and dependency lock

| Input | Pin/digest |
|---|---|
| Runtime | CPython `3.13.13` |
| Python range | `>=3.13, <3.15` |
| `uv.lock` | format `1`, revision `3`, SHA-256 `551136325276ec37b35359aa77ea73a84133d86ebddba7076a5b12a27bcb69a4` |
| `pyproject.toml` | SHA-256 `0d4d5ddce4417944e26fc1ae1a99e312761f7f33f25b9ca5d36587f171bcb2c4` |
| Build backend | `hatchling==1.28.0` and editable-build helper `editables==0.5`, both in the `build` dependency group and frozen lock |

The production Docker recipe installs the locked build group, then installs
the project with `uv sync --frozen --no-build-isolation`. Runtime, test, and
build direct dependencies are exact-pinned and checked against the complete
locked name/version resolution.
`jsonschema==4.26.0` remains the direct runtime pin for immutable response
schemas. `webauthn==3.0.0` is the exact maintained server-side registration and
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
canonical composition service; the ordinary supervisor launches parent-bound
MCP endpoints and directly delivers sealed Pi capabilities. Exact installed
semantic interoperability remains external under G05.

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

## Verification boundary

`scripts/verify_release.py` checks authoritative-source hashes, frozen runtime,
test and build dependencies, byte-current generated schemas, deployment and
evidence input hashes, exact 85-row/11-decision ledgers, machine/human gate
status parity, component/protocol projections, and blocked release claims.
It cannot certify the explicitly missing privileged, external, owner, signing,
installer, HA/PITR, real-harness, partner, or public-peer evidence.
