#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import {
  accessSync,
  chmodSync,
  constants as fsConstants,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { stablePackageTreeSha256 } from "../lib/server-setup-preflight.mjs";

if (process.platform !== "win32") process.umask(0o022);

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const temporary = mkdtempSync(path.join(os.tmpdir(), "agentnet-packed-check-"));
chmodSync(temporary, 0o755);
const initName = process.platform === "linux" && (() => {
  try { return readFileSync("/proc/1/comm", "utf8").trim(); } catch { return ""; }
})();
const unrelated = path.join(temporary, "unrelated");
const state = path.join(temporary, "state");
const home = path.join(temporary, "home");
const npmCache = path.join(temporary, "npm-cache");
const timeout = 10 * 60 * 1000;
const protectedServiceRoots = ["/home", "/root", "/run/user", "/tmp", "/var/tmp"];

for (const directory of [unrelated, state, home, npmCache]) {
  mkdirSync(directory, { recursive: true, mode: 0o700 });
}

const writePrivate = (file, value) => {
  writeFileSync(file, typeof value === "string" ? value : `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
  chmodSync(file, 0o600);
};

const scannerPublicKey = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAELh/2FNSADEBCRZPRN7OMxfF3xJpu
04cThwOVHuyhEwl/iDcYB+XmcjSPTW5owy7+fdOD8jZIi4wR1lZ96za/7g==
-----END PUBLIC KEY-----
`;

const run = (command, arguments_, options = {}) => {
  const completed = spawnSync(command, arguments_, {
    cwd: options.cwd ?? root,
    env: options.env ?? process.env,
    encoding: options.capture ? "utf8" : undefined,
    stdio: options.capture ? ["ignore", "pipe", "pipe"] : "inherit",
    maxBuffer: 128 * 1024 * 1024,
    timeout,
    shell: false,
  });
  if (completed.error) throw completed.error;
  if (completed.status !== 0) {
    if (options.capture) {
      process.stderr.write(completed.stdout ?? "");
      process.stderr.write(completed.stderr ?? "");
    }
    throw new Error(`${command} exited with status ${completed.status}`);
  }
  return completed;
};

const requireNoVerificationResidue = (packageRoot) => {
  const violations = [];
  const visit = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const child = path.join(directory, entry.name);
      if (
        entry.name === ".hypothesis" || entry.name === ".pytest_cache" ||
        entry.name === "__pycache__" || entry.name.endsWith(".pyc")
      ) {
        violations.push(path.relative(packageRoot, child));
      }
      if (entry.isDirectory()) visit(child);
    }
  };
  visit(packageRoot);
  if (violations.length !== 0) {
    throw new Error(`installed verification polluted package tree: ${violations.slice(0, 10).join(", ")}`);
  }
};

const requireSafeInstalledModes = (packageRoot) => {
  if (process.platform === "win32") return;
  const violations = [];
  const visit = (target) => {
    const metadata = statSync(target);
    const mode = metadata.mode & 0o777;
    const required = metadata.isDirectory() ? 0o005 : 0o004;
    if ((mode & 0o022) !== 0 || (mode & required) !== required) {
      violations.push(`${path.relative(packageRoot, target) || "."}:${mode.toString(8)}`);
    }
    if (!metadata.isDirectory()) return;
    for (const entry of readdirSync(target, { withFileTypes: true })) {
      if (entry.isSymbolicLink()) {
        violations.push(`${path.relative(packageRoot, path.join(target, entry.name))}:symlink`);
        continue;
      }
      visit(path.join(target, entry.name));
    }
  };
  visit(packageRoot);
  if (violations.length !== 0) {
    throw new Error(`installed package tree has unsafe mode: ${violations.slice(0, 10).join(", ")}`);
  }
};

const isProtectedServicePath = (candidate) => protectedServiceRoots.some((rootPath) => (
  candidate === rootPath || candidate.startsWith(`${rootPath}${path.sep}`)
));

if (initName === "systemd" && !isProtectedServicePath(path.resolve(temporary))) {
  rmSync(temporary, { recursive: true, force: true });
  throw new Error("packed setup probe temporary root is not hidden by the managed service sandbox");
}

const resolveCommand = (command, searchPath) => {
  for (const entry of searchPath.split(path.delimiter).filter(Boolean)) {
    const candidate = path.join(entry, command);
    try {
      accessSync(candidate, fsConstants.X_OK);
      return realpathSync(candidate);
    } catch {
      // Try the next PATH entry.
    }
  }
  return null;
};

const lineageBlocker = (target) => {
  const filesystemRoot = path.parse(target).root;
  for (let item = target; item !== filesystemRoot; item = path.dirname(item)) {
    const metadata = statSync(item);
    if (metadata.uid !== 0 || (metadata.mode & 0o022) !== 0) {
      return "unsafe_executable";
    }
    const required = metadata.isDirectory() ? 0o001 : 0o005;
    if ((metadata.mode & required) !== required) {
      return "service_executable_inaccessible";
    }
  }
  return null;
};

const expectedBlockedSetupBlocker = (environment) => {
  const nodeExecutable = realpathSync(process.execPath);
  const pathNodeExecutable = resolveCommand("node", environment.PATH ?? "");
  const uvExecutable = resolveCommand("uv", environment.PATH ?? "");
  if (!pathNodeExecutable) {
    throw new Error("packed setup probe could not resolve Node from PATH");
  }
  if (pathNodeExecutable !== nodeExecutable) {
    throw new Error("packed setup probe PATH Node does not match the checker runtime");
  }
  if (!uvExecutable) {
    throw new Error("packed setup probe could not resolve uv from PATH");
  }
  for (const target of [nodeExecutable, uvExecutable]) {
    if (isProtectedServicePath(target)) {
      return "service_executable_inaccessible";
    }
    const blocker = lineageBlocker(target);
    if (blocker) return blocker;
  }
  // Service-safe Node and uv executables reach the installed package tree next.
  // The tree is deliberately under the protected temporary root asserted above.
  return "service_executable_inaccessible";
};

const requirePackedLocalCommunication = (packageRoot, launcher, environment) => {
  const runtime = path.join(temporary, "local-communication-runtime");
  const workspace = path.join(temporary, "local-communication-workspace");
  mkdirSync(runtime, { recursive: true, mode: 0o700 });
  mkdirSync(workspace, { recursive: true, mode: 0o700 });
  const lifecycleEnvironment = {
    ...environment,
    AGENTNET_NPM_RUNTIME_DIR: runtime,
    PYTHONPYCACHEPREFIX: path.join(runtime, "pycache"),
    UV_PROJECT_ENVIRONMENT: runtime,
  };
  for (const name of [
    "ALL_PROXY", "HTTPS_PROXY", "HTTP_PROXY", "PYTHONHOME", "PYTHONPATH",
    "VIRTUAL_ENV", "all_proxy", "http_proxy", "https_proxy",
  ]) {
    delete lifecycleEnvironment[name];
  }
  lifecycleEnvironment.NO_PROXY = "127.0.0.1,localhost";
  lifecycleEnvironment.no_proxy = "127.0.0.1,localhost";
  run(
    resolveCommand("uv", lifecycleEnvironment.PATH ?? "") ?? "uv",
    [
      "run", "--project", packageRoot, "--frozen", "--no-default-groups",
      "--python", "3.13.13", "python", "-B", "-I",
      path.join(packageRoot, "scripts", "ci", "packaged_local_communication_e2e.py"),
      "run", "--package-root", packageRoot, "--launcher", launcher,
      "--workspace", workspace,
    ],
    { cwd: unrelated, env: lifecycleEnvironment },
  );
  if (readdirSync(workspace).length !== 0) {
    throw new Error("packaged local communication gate left workspace residue");
  }
};

const requireBlockedSetup = (launcher, request, options) => {
  const expectedBlocker = expectedBlockedSetupBlocker(options.env);
  const completed = spawnSync(
    launcher,
    ["server-agent", "setup", "--request", request],
    {
      ...options,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      maxBuffer: 128 * 1024 * 1024,
      timeout,
      shell: false,
    },
  );
  if (completed.error) throw completed.error;
  if (completed.status !== 1) {
    throw new Error(`packed user-owned setup probe expected status 1, got ${completed.status}`);
  }
  let evidence;
  try {
    evidence = JSON.parse(completed.stdout);
  } catch {
    throw new Error("packed setup probe did not return structured blocker evidence");
  }
  if (
    evidence.schema !== "agentnet.server-setup.evidence.v1" ||
    evidence.status !== "blocked" ||
    evidence.blocker !== expectedBlocker ||
    evidence.authority_granted !== false ||
    evidence.identity_enrolled !== false
  ) {
    throw new Error(
      `packed setup probe returned unexpected blocker evidence: status=${String(evidence.status)} blocker=${String(evidence.blocker)} expected=${expectedBlocker}`,
    );
  }
};

try {
  let packageRoot = root;
  for (let generation = 1; generation <= 2; generation += 1) {
    const packDirectory = path.join(temporary, `pack-${generation}`);
    const prefix = path.join(temporary, `prefix-${generation}`);
    mkdirSync(packDirectory, { recursive: true, mode: 0o700 });
    mkdirSync(prefix, { recursive: true, mode: 0o755 });
    chmodSync(prefix, 0o755);

    const packed = run(
      "npm",
      ["pack", "--json", "--ignore-scripts", "--pack-destination", packDirectory],
      {
        cwd: packageRoot,
        capture: true,
        env: { ...process.env, npm_config_cache: npmCache },
      },
    );
    const parsed = JSON.parse(packed.stdout);
    const manifest = Array.isArray(parsed) ? parsed[0] : Object.values(parsed)[0];
    if (!manifest?.filename) throw new Error("npm pack did not report a tarball filename");
    const launcherArchiveEntry = (manifest.files ?? []).find(
      (entry) => entry.path === "npm/bin/agentnet.mjs",
    );
    if (launcherArchiveEntry?.mode !== 0o755) {
      throw new Error(
        `generation ${generation} packed launcher mode must be 0755, got ${String(launcherArchiveEntry?.mode)}`,
      );
    }
    const names = new Set((manifest.files ?? []).map((entry) => entry.path));
    if (names.has(".gitignore") || names.has(".npmignore")) {
      throw new Error(`generation ${generation} tarball depends on a root ignore file`);
    }
    const tarball = path.join(packDirectory, manifest.filename);

    run("npm", [
      "install",
      "--prefix",
      prefix,
      "--umask=0022",
      "--ignore-scripts",
      "--no-audit",
      "--no-fund",
      tarball,
    ], { env: { ...process.env, npm_config_cache: npmCache } });

    packageRoot = path.join(
      prefix,
      "node_modules",
      "@misunders2d",
      "agentnet",
    );
    if (initName === "systemd") {
      const installedLauncherMode = (
        statSync(path.join(packageRoot, "npm", "bin", "agentnet.mjs")).mode & 0o777
      );
      if (installedLauncherMode !== 0o755) {
        throw new Error(
          `generation ${generation} installed launcher mode must be 0755, got ${installedLauncherMode.toString(8)}`,
        );
      }
    }
    requireSafeInstalledModes(packageRoot);
    const environment = {
      ...process.env,
      HOME: home,
      PYTHONDONTWRITEBYTECODE: "1",
      UV_CACHE_DIR: process.env.UV_CACHE_DIR ?? path.join(temporary, "uv-cache"),
      XDG_STATE_HOME: state,
      npm_config_cache: npmCache,
    };
    for (const name of [
      "AGENTNET_NPM_RUNTIME_DIR",
      "AGENTNET_PACKAGE_ROOT",
      "AGENTNET_UV",
      "PYTHONHOME",
      "PYTHONPATH",
      "UV_PROJECT_ENVIRONMENT",
      "UV_PYTHON",
      "VIRTUAL_ENV",
    ]) {
      delete environment[name];
    }

    const launcher = path.join(prefix, "node_modules", ".bin", "agentnet");
    requireNoVerificationResidue(packageRoot);
    const packageTreeBeforeVerify = stablePackageTreeSha256(packageRoot);
    run(launcher, ["verify"], { cwd: unrelated, env: environment });
    requireNoVerificationResidue(packageRoot);
    const packageTreeAfterVerify = stablePackageTreeSha256(packageRoot);
    if (packageTreeAfterVerify !== packageTreeBeforeVerify) {
      throw new Error("installed verification mutated package tree contents");
    }
    run(launcher, ["server-agent", "setup", "--help"], { cwd: unrelated, env: environment });

    if (generation === 2) {
      const packageTreeBeforeCommunication = stablePackageTreeSha256(packageRoot);
      requirePackedLocalCommunication(packageRoot, launcher, environment);
      requireNoVerificationResidue(packageRoot);
      const packageTreeAfterCommunication = stablePackageTreeSha256(packageRoot);
      if (packageTreeAfterCommunication !== packageTreeBeforeCommunication) {
        throw new Error("packaged local communication gate mutated package tree contents");
      }
    }

    if (initName === "systemd") {
      const inputs = path.join(temporary, `server-setup-inputs-${generation}`);
      mkdirSync(inputs, { mode: 0o700 });
      const coreEnv = path.join(inputs, "core.env");
      const approvalEnv = path.join(inputs, "approval.env");
      const coreOidc = path.join(inputs, "core-oidc.json");
      const ownerOidc = path.join(inputs, "owner-oidc.json");
      const approvers = path.join(inputs, "approvers.json");
      const scannerTrust = path.join(inputs, "scanner-trust.json");
      const request = path.join(inputs, "server-setup.json");
      writePrivate(coreEnv, "AGENTNET_DATABASE_URL=postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet\nAGENTNET_APPROVAL_CORE_TOKEN=synthetic-packed-check-token-0123456789abcdef0123456789\n");
      writePrivate(approvalEnv, "AGENTNET_APPROVAL_CORE_TOKEN=synthetic-packed-check-token-0123456789abcdef0123456789\n");
      writePrivate(coreOidc, {
        issuer: "https://accounts.example",
        client_id: "packed-core-client",
        redirect_uri: "https://core.corp.example/v1/enrollment/oidc/callback",
        token_endpoint_auth_method: "none",
        allowed_endpoint_origins: ["https://accounts.example"],
        allowed_signing_algorithms: ["RS256"],
        binding_assurance: "hardware_bound",
      });
      writePrivate(ownerOidc, {
        issuer: "https://accounts.example",
        client_id: "packed-approval-client",
        redirect_uri: "https://approval.corp.example/v1/approval/owner/oidc/callback",
        token_endpoint_auth_method: "none",
        allowed_endpoint_origins: ["https://accounts.example"],
        allowed_signing_algorithms: ["RS256"],
      });
      writePrivate(approvers, { approvers: [{
        principal_id: "packed-owner",
        authority_kind: "human",
        domain_id: "corp.example",
        allowed_purposes: [
          "authorization.bootstrap_plan.approve",
          "authorization.elevation.approve",
          "identity.credential.recover.approve",
          "identity.enrollment.approve",
          "identity.harness.revoke.approve",
          "organization.relationship.accept",
        ],
        oidc_issuer: "https://accounts.example",
        oidc_subject: "packed-owner-subject",
      }] });
      writePrivate(scannerTrust, {
        trusted_public_keys: { "packed-scanner-key": scannerPublicKey },
        required_engine: "packed-check-scanner",
        required_rules_digest: "a".repeat(64),
        required_profile_digest: "b".repeat(64),
      });
      writePrivate(request, {
        schema: "agentnet.server-setup.request.v1",
        profile: "always_on_server_agent",
        domain_id: "corp.example",
        service_audience: "urn:agentnet:corp.example:corporate-api",
        runtime_instance_id: "packed-server-1",
        core_public_origin: "https://core.corp.example",
        approval_public_origin: "https://approval.corp.example",
        database_url: "postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet",
        database_url_env: "AGENTNET_DATABASE_URL",
        core_environment_file: coreEnv,
        approval_environment_file: approvalEnv,
        oidc_provider_file: coreOidc,
        approval_owner_oidc_file: ownerOidc,
        approval_approvers_file: approvers,
        scanner_trust_file: scannerTrust,
        approval_approver_principal_id: "packed-owner",
        approval_verifier_id: "approval.corp.example",
      });
      requireBlockedSetup(launcher, request, { cwd: unrelated, env: environment });
    }
  }
  console.log("two-generation packed npm package check: PASS");
} finally {
  if (process.env.AGENTNET_KEEP_PACKED_CHECK !== "1") {
    rmSync(temporary, { recursive: true, force: true });
  } else {
    console.log(`packed npm package check retained: ${temporary}`);
  }
}
