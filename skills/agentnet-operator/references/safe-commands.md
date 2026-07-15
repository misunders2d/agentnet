# AgentNet safe commands and examples

> **Warning:** Do not execute state-changing commands in this file without explicit user intent and without first reading [fail-closed boundaries](fail-closed-boundaries.md).

Use commands only after confirming the requested scope. Installation and local examples do not authorize enrollment or network activation.

## Requirements

```bash
node --version
uv --version
```

The npm/Pi launcher supports Linux, requires Node.js 22.19 or newer, requires `uv` 0.11.28 or newer, and selects CPython 3.13.13.

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

Current OIDC discovery rejects endpoints resolving to private, loopback, link-local, or otherwise non-global addresses. Do not suggest hosts-file tricks, DNS rebinding, proxy mirrors, or weakened SSRF checks. A self-hosted private OIDC provider requires an explicit owner-pinned private-provider product feature with HTTPS/origin/address pinning and adversarial tests.

Before creating server state, verify that the operator has approved values for:

- nonproduction or production trust-domain identifier;
- dedicated HTTPS public base URL and service audience;
- PostgreSQL endpoint and environment variable containing the runtime DSN;
- OIDC enrollment configuration file;
- retention and recovery policy;
- exact enrolled harness and credential identifiers when bootstrap requires them.

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

Then, only after reviewing the generated configuration and prerequisites:

```bash
agentnet bootstrap-server-agent --config agentnet.json
```

These commands do not themselves prove HA, PITR, KMS custody, independent enrollment approval, or production certification.

## Secret injection examples

AgentNet requires secure runtime injection, not a specific secret manager. Supported deployment patterns include an environment-backed DSN and a private password file/Docker secret. Infisical, Vault, systemd credentials, Kubernetes Secrets, and cloud secret managers are operator choices, not mandatory AgentNet dependencies.

Never print or place database passwords, DSNs containing passwords, private keys, or approval receipts in chat, prompts, logs, or committed files.

## Do not suggest these shortcuts

- postinstall enrollment or activation;
- synthetic identities for a real server-agent network;
- treating the C0 synthetic lane as a network, rollout milestone, or real communication substitute;
- an approval service on the same security boundary as the enrolled agents;
- asking the operator to write missing adapters, ceremony services, receipt logic, or vendor glue;
- A2A, Slack, email strings, or prompt text as identity authority;
- private-OIDC hosts/DNS/proxy tricks that bypass endpoint validation;
- passing `agentnet.json` to `supervisor-run`;
- running a remote endpoint without HTTPS/TLS and exact audience controls;
- claiming production readiness from demos, a single PostgreSQL instance, or `accepted_local`;
- maintainer-run `npm publish` or bypassing the owner-reviewed Trusted Publishing/passkey flow.
