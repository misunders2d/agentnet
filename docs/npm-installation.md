# npm and Pi installation

AgentNet ships as one multi-harness npm package. After the maintainer publishes the public scoped package:

```bash
pi install npm:@misunders2d/agentnet
```

The same package exposes the shared AgentNet CLI for Claude, Codex, Pi, Antigravity, and server-agent operation:

```bash
npm install -g @misunders2d/agentnet
agentnet --version
agentnet --help
```

Before npm publication, Pi can install this repository directly:

```bash
pi install git:github.com/misunders2d/agentnet
```

## Requirements

- Linux, macOS, or Windows
- Node.js 22.19 or newer
- [`uv`](https://docs.astral.sh/uv/) 0.11.28 or newer on `PATH`

The launcher uses the committed `uv.lock` and selects the release-certified CPython `3.13.13` runtime. The Python package remains compatible with `>=3.13,<3.15`, but the npm launcher is deliberately stricter so different hosts cannot silently verify the same release with different interpreter minors. It keeps an environment keyed by package version and install identity under the current user's state directory. Global npm and Pi-managed copies therefore do not rebind each other's Python environment. Override that location with an absolute `AGENTNET_NPM_RUNTIME_DIR` when needed.

Host state uses XDG state paths on Linux, `~/Library/Application Support/agentnet` on macOS, and `%LOCALAPPDATA%\\agentnet` on Windows. POSIX state is owner-mode checked. Windows private state and npm runtime roots use protected current-user DACLs and reject reparse-point roots. Linux uses descriptor-pinned executable measurement; macOS and Windows use repeated process creation-time, account, parent, path identity, and executable digest checks. The latter path-based measurement is fail-closed but is not claimed equivalent to Linux `/proc/<pid>/exe` against privileged path replacement.

Installation does not enroll a person, create an identity, start a supervisor, activate a local binding, or grant authority. The Pi package contributes the AgentNet extension plus the `agentnet-operator` skill and fixed `agentnet server-agent setup` implementation. The skill routes the target coding agent through read-only planning, one approved apply/start, genuine OIDC/WebAuthn ceremonies, offline activation, and structured verification; the skill itself grants no authority and performs no automatic initialization. Extension tools become usable only inside a measured Pi child launched by an enrolled AgentNet supervisor with `local_bindings_required=true`; there is no ambient fallback.

## Product-owned ordinary Linux server setup

The default self-hosted profile is installed and operated locally by the target server's coding agent. Server setup requires system-wide root-owned AgentNet, Node.js, and `uv` executables readable/executable by locked service identities. Target coding agent must reject any per-user, home, temporary, writable, or request-selected launcher before privileged invocation; code inside an untrusted launcher cannot make its own execution as root safe. Launcher and Python provenance checks are defense in depth after that precondition. A remote Manager may provide immutable public package/version instructions and inspect sanitized evidence, but must not shell into the host or invent users, directories, units, or recovery steps.

Prepare the strict non-secret request with owner-only sensitive file references described by the bundled `skills/agentnet-operator/references/ordinary-server-setup.md`, then plan without writes:

```bash
<resolved-root-owned-agentnet-path> server-agent setup --request /home/operator/.config/agentnet-setup/server-setup.json
```

After one frozen human-approved scope:

```bash
sudo -- <resolved-root-owned-agentnet-path> server-agent setup \
  --request /home/operator/.config/agentnet-setup/server-setup.json \
  --expected-request-digest <approved-request-digest> \
  --apply --start
```

The fixed wrapper manages two locked identities, two private data roots, root-only environment custody, Approval provisioning, Core/PostgreSQL bootstrap, maintained-scanner public trust, two hardened systemd units, bounded start/restart, and redacted step evidence. Exact reruns converge and conflicts never overwrite. It uses loopback Core `8080` and Approval `8090`, then verifies the operator-owned exact public HTTPS routes. It never mutates DNS, TLS certificates, proxy configuration, PostgreSQL administration, firewall policy, human identity, or authority.

Expected initial status is `waiting_owner_oidc_or_passkey`. The bundled skill then uses only dedicated-user Approval registration, `join guided --browser terminal`, offline `server-agent activate`, and an exact setup rerun. Final status is `operational`, `identity_enrolled=true`, public Core `/readyz` verified, and `authority_granted=false`. Human OIDC and passkey steps are never automated.

The supported real-network experience must deliver exactly the capability set in `docs/requirements.md`. AgentNet is responsible for shipping or explicitly provisioning the required maintained mechanisms, adapters, manifests, and preflight checks. The operator supplies approved infrastructure, secrets, owner decisions, trust roots, and human ceremonies—not custom integration code. Missing product components block the real-network path; local synthetic C0 is test evidence, not a smaller product substitute.

## PATH checks

If npm reports a successful global install but `agentnet` is not found:

```bash
command -v agentnet
npm prefix -g
"$(npm prefix -g)/bin/agentnet" --version
export PATH="$(npm prefix -g)/bin:$PATH"
```

On Windows PowerShell, use:

```powershell
Get-Command agentnet
npm prefix -g
& "$(npm prefix -g)\\agentnet.cmd" --version
Get-Command uv
```

On Linux/macOS, verify `uv` independently with `command -v uv`.
Run `uv --version` on every host and require 0.11.28 or newer. Install or explicitly upgrade `uv` from <https://docs.astral.sh/uv/> when needed; AgentNet never modifies the host `uv` installation automatically.

## Verify a source checkout before publication

```bash
npm run check:package
npm pack --dry-run
npm test
npm run check:packed
```

The package includes the retained local test and evidence set so `agentnet verify` can run from the installed artifact. `npm run check:packed` creates the actual npm tarball, clean-installs it into a temporary prefix, and runs the complete packaged verification suite from an unrelated working directory.

## First publication bootstrap

npm Trusted Publishing and `npm stage publish` require the package to already exist on npm. For the first `@misunders2d/agentnet` publication, the package owner must review the exact tarball and perform one direct, interactive publication with passkey/2FA. Do not push a `v*` tag before this bootstrap publication is complete.

After the package exists, configure its npm settings:

- Trusted Publisher owner: `misunders2d`
- Repository: `agentnet`
- Workflow: `publish.yml`
- Allowed action: `npm stage publish` only
- Publishing access: require 2FA and disallow tokens

No npm token or `NODE_AUTH_TOKEN` belongs in the repository or release workflow.

## Later releases

1. Bump `package.json` and `pyproject.toml` to the same version.
2. Run the full local validation above.
3. Commit and push `main`.
4. Create and push a protected tag exactly matching `v${package.json.version}`.
5. `.github/workflows/publish.yml` stages the package through npm Trusted Publishing.
6. Review or download the staged tarball on npmjs.com.
7. Approve it interactively with passkey/2FA.
8. Verify the published version, provenance, and a clean Pi installation.

Public publication remains owner-controlled; maintainers prepare, test, commit, tag, and push the release candidate but do not run `npm publish`.
