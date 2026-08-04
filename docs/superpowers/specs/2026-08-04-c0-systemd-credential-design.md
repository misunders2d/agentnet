# C0 systemd credential custody fix

## Goal

Unblock the first real AgentNet message without replacing the disposable VM, owner OIDC/passkey registration, or enrolled server credential identities. Correct the package defect in a `0.1.40` candidate, reinstall it on the same server, and continue the existing guided enrollment journey.

Affected requirements: `ID-004`, `COM-002`, `COM-003`, `SEC-006`, `OPS-001`, `OPS-006`, and `OPS-007`.

## Observed defect

The generated `agentnet-c0-responder.service` uses systemd `LoadCredential` and passes `%d/signing-key.pem`. On the target Ubuntu 24.04/systemd 255 host, systemd presents that readable credential as a single-link regular file owned by `root:root` with mode `0440`. The responder currently sends the credential through the owner-file validator, which requires the service UID and no group or other permission bits. The shipped unit and validator therefore cannot both pass.

## Design

Keep the existing owner-file validator unchanged for the responder configuration. Add a separate credential reader for the signing key. It opens with `O_NOFOLLOW`, bounds size, verifies a regular single-link file, and accepts only one of these custody forms:

1. the existing direct owner-only file form; or
2. the exact `signing-key.pem` path beneath the process-provided `CREDENTIALS_DIRECTORY`, with systemd custody: `root:root`, no write or execute bits, and no access for others. The observed `0440` mode is accepted.

Any missing or mismatched credential directory, different filename/path, symlink, extra link, wrong owner/group, broader mode, writable mode, oversized file, short read, or invalid key fails closed. The key remains in systemd's credential mount and process memory; the fix does not copy it, widen permissions, or transfer long-lived key custody to `agentnet-c0`.

## Alternatives rejected

- Copy the key into `/var/lib/agentnet-c0`: adds persistent key material and cleanup/recovery races.
- Point the service directly at `/var/lib/agentnet/guided-join.key.pem` and change ownership: broadens responder custody and weakens role separation.
- Relax the generic owner-file check: would weaken configuration and unrelated private-file validation.

## Compatibility and recovery

No schema, protocol, or state migration is added. Existing OIDC/passkey registration, enrolled actor IDs, credentials, PostgreSQL state, and activation binding are retained. Installing the corrected package and rerunning the same package-owned setup command is the recovery path. If validation still fails, the responder stays failed and setup remains blocked at `service_runtime_binding`; no synthetic enrollment or authority fallback is allowed.

## Verification

1. Add a focused regression test before implementation for the systemd `root:root` `0440` credential case.
2. Add negative cases for path, owner/group, mode, symlink/link count, and missing credential-directory provenance.
3. Run the focused C0 responder and server-setup tests.
4. Build the unpublished `0.1.40` candidate, install it on the same VM, and run the actual systemd unit with the retained credential.
5. Rerun package-owned setup and verify Core, Approval, C0 responder, and renewal units converge.
6. Continue the existing real guided enrollment, enroll an independent laptop endpoint, and prove the first native message/receipt path.
