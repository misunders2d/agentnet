# Product-owned ordinary Linux server setup

Read this file only for the default self-hosted `always_on_server_agent` profile. The target server's coding agent owns every host command. A remote Manager may provide the immutable public package/version and inspect sanitized evidence, but must not shell into the target or handcraft users, directories, units, or recovery steps.

## Fixed boundary

`agentnet server-agent setup` is the only ordinary host-setup entry point. It composes shipped Approval, PostgreSQL bootstrap, Core, scanner trust, systemd, guided enrollment, and activation surfaces. It is not a deployment DSL.

AgentNet manages only:

- locked `agentnet` and `agentnet-approval` OS identities;
- `/var/lib/agentnet`, `/var/lib/agentnet-approval`, root-only `/var/lib/agentnet-setup`, and `/etc/agentnet-secrets`;
- `agentnet-core.service` and `agentnet-approval.service`;
- loopback Core `127.0.0.1:8080` and Approval `127.0.0.1:8090`.

Operator-owned prerequisites remain a system-wide root-owned AgentNet package plus Node.js and `uv` executables accessible to locked service identities, PostgreSQL, exact distinct public DNS/TLS routes, workforce OIDC registration, secret injection files, maintained scanner public trust, and owner policy. Target coding agent must reject per-user, home, temporary, group-writable, or request-selected AgentNet launchers before privileged invocation; code inside an untrusted launcher cannot make its own execution as root safe. Product provenance checks remain defense in depth. Existing self-hosted HTTPS proxy routes must map exact Core and Approval origins to those loopback ports. Setup verifies AgentNet-specific JSON identity on both public `/healthz` routes before reporting started and verifies Core `/readyz` after activation. It never mutates DNS, certificates, proxy configuration, PostgreSQL administration, firewall policy, or unrelated services.

If any prerequisite is absent, return one named blocker. Do not improvise cloud/provider automation, direct plaintext exposure, a general deployment framework, or remote shell choreography.

## Resolve inputs without interrogating the human

Target coding agent resolves technical metadata from approved local infrastructure and public package/provider records. Human should not supply callback URLs, RP IDs, audiences, identifiers, package paths, or config syntax.

Prepare one owner-only staging directory and these owner-only files:

1. `core.env` — exact runtime DSN under `AGENTNET_DATABASE_URL`, confidential Core OIDC secret when required, and shared high-entropy `AGENTNET_APPROVAL_CORE_TOKEN`.
2. `approval.env` — confidential Approval owner-OIDC secret when required and same broker token.
3. `core-oidc.json` — provider fields only; callback must equal `<core-origin>/v1/enrollment/oidc/callback`.
4. `approval-owner-oidc.json` — callback must equal `<approval-origin>/v1/approval/owner/oidc/callback`.
5. `approvers.json` — exact preapproved human owner and mandatory purposes.
6. `scanner-trust.json` — maintained scanner public keys and exact engine/rules/profile digests; no private key.
7. `server-setup.json` — strict non-secret request whose sensitive values are file references. Copy [ordinary-server-setup-request.json](examples/ordinary-server-setup-request.json) and replace every illustrative value, including `operator`, with exact approved local values. Keep it valid comment-free JSON.

All seven files must be regular, canonical, owner-only mode `0600`, have link count exactly `1`, and remain outside chat/logs/repositories. Use an operator-owned private directory readable during non-root planning; root apply accepts that exact owner through `sudo`. Environment lines are strict unquoted `NAME=value`; whitespace, shell quoting, backslashes, duplicate names, or empty values are rejected. Database DSN in request is password-free and must exactly match named runtime environment value.

Never print or copy environment values, client secrets, broker tokens, private keys, signer custody, approval receipts, claim codes, or identity files.

## One frozen setup approval

Run read-only plan first:

```bash
<resolved-root-owned-agentnet-path> server-agent setup --request /home/operator/.config/agentnet-setup/server-setup.json
```

Require `schema=agentnet.server-setup.evidence.v1`, `status=planned`, exact request digest, two managed units, fixed loopback ports, and all authority/identity/durability claims false. Show human one concise frozen scope. Ask again only if scope changes or a new destructive, restart, privilege-expanding, or high-risk action appears.

After that one approval, target agent runs:

```bash
sudo -- <resolved-root-owned-agentnet-path> server-agent setup \
  --request /home/operator/.config/agentnet-setup/server-setup.json \
  --expected-request-digest <approved-request-digest> \
  --apply --start
```

Expected pre-enrollment status: `waiting_owner_oidc_or_passkey`. Exact reruns converge. Conflicting users, roots, configs, units, ports, package versions, or secret references block without overwrite. A partial failure preserves completed product state; rerun the same request rather than deleting state or changing identifiers.

`--apply` requires root. `--start` is valid only with `--apply`. Start scope is limited to daemon reload, Approval enable/start, Core enable, and Core restart. Setup also checks both loopback and both public HTTPS health routes.

## Human ceremonies and activation

Setup never automates OIDC or WebAuthn. After services start:

1. Under the dedicated Approval identity, run the shipped registration command and let owner open stable public Approval page:

   ```bash
   sudo -u agentnet-approval -H <resolved-root-owned-agentnet-path> approval register-begin \
     --config /var/lib/agentnet-approval/config.json \
     --approver <resolved-owner-principal-id>
   ```

2. Owner signs in and registers phishing-resistant passkey with user verification.
3. Under dedicated Core identity on a private unrecorded POSIX TTY, run only guided enrollment:

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

4. Owner opens only TTY-disclosed HTTPS URL, completes OIDC and WebAuthn approval, then enters short-lived claim code into masked TTY prompt. Never relay URL/code through chat, A2A, logs, or files.
5. Stop Core, activate exact identity while offline, then rerun product setup so Core restarts from the bound config:

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

Final setup status must be `operational`, `identity_enrolled=true`, public Core `/readyz` verified, and `authority_granted=false`. Enrollment remains identity-only. New laptops use canonical fresh-laptop packet and `join guided`; no `join begin`, generic entitlement, beneficiary identity transfer, or implicit authority.

## Evidence and recovery

Keep only structured redacted setup output needed for audit. It may contain request digest, public origins, step names/status, package version, managed units, and non-secret blockers. It must not contain executable paths, environment values, or private enrollment material.

- Same request + same state: converge on `already_satisfied` where applicable.
- Interrupted provisioning/bootstrap: rerun exact request.
- Public route unhealthy: fix operator-owned TLS route, then rerun; do not bind Core remotely over plaintext.
- Existing managed state differs: stop and review. Never `--force`, delete keys/DB, replace identity, or rewrite units as recovery.
- Activation response uncertain: inspect bound config and exact identity locally; do not create another identity.
- Setup does not prove HA, PITR, KMS/HSM custody, independent shared-host protection, external conformance, production certification, or any business authority.
