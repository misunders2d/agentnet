# npm and Pi installation

AgentNet ships as one multi-harness npm package. After the maintainer publishes the public scoped package:

```bash
pi install npm:@misunders2d/agentnet
```

The same package exposes the shared AgentNet CLI for Claude, Codex, Pi, Antigravity, and server-agent operation:

```bash
npm install -g @misunders2d/agentnet
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

The launcher uses the committed `uv.lock`, asks `uv` for Python `>=3.13,<3.15`, and keeps its versioned environment under the current user's state directory. Override that location with an absolute `AGENTNET_NPM_RUNTIME_DIR` when needed.

Installation does not enroll a person, create an identity, start a supervisor, or grant authority. Use AgentNet's explicit network and enrollment commands after installing the package.

## Verify a source checkout before publication

```bash
npm run check:package
npm pack --dry-run
npm test
```

The package includes the retained local test and evidence set so `agentnet verify` can run from the installed artifact. Public publication remains owner-controlled; maintainers prepare, test, commit, tag, and push the release candidate.
