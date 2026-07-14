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

- Linux
- Node.js 22.19 or newer
- [`uv`](https://docs.astral.sh/uv/) on `PATH`

The launcher uses the committed `uv.lock`, asks `uv` for Python `>=3.13,<3.15`, and keeps an environment keyed by package version and install identity under the current user's state directory. Global npm and Pi-managed copies therefore do not rebind each other's Python environment. Override that location with an absolute `AGENTNET_NPM_RUNTIME_DIR` when needed.

Installation does not enroll a person, create an identity, start a supervisor, activate a local binding, or grant authority. The Pi package contributes the AgentNet extension, not Pi skills. Its tools become usable only inside a measured Pi child launched by an enrolled AgentNet supervisor with `local_bindings_required=true`; there is no ambient fallback.

## PATH checks

If npm reports a successful global install but `agentnet` is not found:

```bash
command -v agentnet
npm prefix -g
"$(npm prefix -g)/bin/agentnet" --version
export PATH="$(npm prefix -g)/bin:$PATH"
```

Also verify `uv` independently:

```bash
command -v uv
```

Install `uv` from <https://docs.astral.sh/uv/> if that command prints nothing.

## Verify a source checkout before publication

```bash
npm run check:package
npm pack --dry-run
npm test
```

The package includes the retained local test and evidence set so `agentnet verify` can run from the installed artifact.

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
