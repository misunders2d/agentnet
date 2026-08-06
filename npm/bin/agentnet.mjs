#!/usr/bin/env node

import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  accessSync,
  chmodSync,
  constants as fsConstants,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  realpathSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  forwardedSignals,
  platformStateRoot,
  supportedPlatform,
} from "../lib/platform.mjs";
import {
  argumentValue,
  privilegedApprovalDigest,
} from "../lib/server-setup-preflight.mjs";

const packageRoot = realpathSync(
  path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", ".."),
);
if (!supportedPlatform(process.platform)) {
  console.error(`AgentNet does not support host platform: ${process.platform}`);
  process.exit(1);
}

const userArguments = process.argv.slice(2);
const packagedSetup = userArguments[0] === "setup";
const setupApply = process.platform === "linux" &&
  userArguments[0] === "server-agent" && userArguments[1] === "setup" &&
  userArguments.includes("--apply");
const unsupportedTlsEnvironment = ["SSL_CERT_FILE", "SSL_CERT_DIR", "SSLKEYLOGFILE"];
if (setupApply && unsupportedTlsEnvironment.some((name) => Object.hasOwn(process.env, name))) {
  console.error("AgentNet setup rejects ambient TLS trust and key-log configuration.");
  process.exit(1);
}
const privilegedSetupApply = setupApply &&
  typeof process.getuid === "function" && process.getuid() === 0;

const systemPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";
const resolveCommand = (command, searchPath = process.env.PATH ?? "") => {
  const suffixes = process.platform === "win32" && path.extname(command) === ""
    ? ["", ...(process.env.PATHEXT ?? ".COM;.EXE;.BAT;.CMD").split(path.delimiter).filter(Boolean)]
    : [""];
  const names = [...new Set(suffixes.map((suffix) => `${command}${suffix}`))];
  const candidates = path.isAbsolute(command)
    ? names
    : searchPath.split(path.delimiter).filter(Boolean).flatMap(
      (entry) => names.map((name) => path.join(entry, name)),
    );
  for (const candidate of candidates) {
    try {
      accessSync(candidate, fsConstants.X_OK);
      return realpathSync(candidate);
    } catch {
      // Try next PATH candidate.
    }
  }
  return null;
};

const requireRootOwnedPath = (target, { recursive = false } = {}) => {
  const resolved = realpathSync(target);
  const protectedRoots = ["/home", "/root", "/run/user"];
  if (protectedRoots.some((root) => resolved === root || resolved.startsWith(`${root}/`))) {
    throw new Error("privileged AgentNet setup executable is hidden by ProtectHome");
  }
  for (let current = resolved; ; current = path.dirname(current)) {
    const info = lstatSync(current);
    const requiredMode = info.isDirectory() ? 0o001 : 0o005;
    if (info.uid !== 0 || (info.mode & 0o022) !== 0 || (info.mode & requiredMode) !== requiredMode) {
      throw new Error("privileged AgentNet setup requires root-owned non-writable service-readable executable lineage");
    }
    if (current === path.dirname(current)) break;
  }
  if (recursive) {
    const inspectTree = (directory) => {
      for (const entry of readdirSync(directory, { withFileTypes: true })) {
        const child = path.join(directory, entry.name);
        const info = lstatSync(child);
        const requiredMode = entry.isDirectory() ? 0o005 : 0o004;
        if (
          entry.isSymbolicLink() || info.uid !== 0 || (info.mode & 0o022) !== 0 ||
          (info.mode & requiredMode) !== requiredMode
        ) {
          throw new Error("privileged AgentNet setup requires one root-owned non-writable service-readable package tree");
        }
        if (entry.isDirectory()) inspectTree(child);
      }
    };
    inspectTree(resolved);
  }
  return resolved;
};

let nodeExecutable = realpathSync(process.execPath);
let uvExecutable = process.env.AGENTNET_UV || "uv";
let systemctlExecutable;
let useraddExecutable;
const resolvedUv = resolveCommand(uvExecutable);
if (!resolvedUv) {
  console.error("AgentNet requires an executable uv installation.");
  process.exit(1);
}
uvExecutable = resolvedUv;
if (privilegedSetupApply) {
  try {
    nodeExecutable = requireRootOwnedPath(nodeExecutable);
    requireRootOwnedPath(packageRoot, { recursive: true });
    uvExecutable = requireRootOwnedPath(uvExecutable);
    const resolvedSystemctl = resolveCommand("systemctl", systemPath);
    const resolvedUseradd = resolveCommand("useradd", systemPath);
    if (!resolvedSystemctl || !resolvedUseradd) {
      throw new Error("required setup host tool is unavailable");
    }
    systemctlExecutable = requireRootOwnedPath(resolvedSystemctl);
    useraddExecutable = requireRootOwnedPath(resolvedUseradd);
    const agentnetExecutable = requireRootOwnedPath(
      path.join(packageRoot, "npm", "bin", "agentnet.mjs"),
    );
    const expectedDigest = argumentValue(userArguments, "--expected-request-digest");
    const digestEnvironment = {
      ...process.env,
      AGENTNET_EXECUTABLE: agentnetExecutable,
      AGENTNET_NODE_EXECUTABLE: nodeExecutable,
      AGENTNET_PACKAGE_ROOT: packageRoot,
      AGENTNET_SYSTEMCTL: systemctlExecutable,
      AGENTNET_USERADD: useraddExecutable,
      AGENTNET_UV: uvExecutable,
    };
    if (
      !/^[a-f0-9]{64}$/u.test(expectedDigest) ||
      privilegedApprovalDigest(userArguments, digestEnvironment) !== expectedDigest
    ) {
      throw new Error("approved digest mismatch");
    }
  } catch {
    console.error(
      "Privileged AgentNet setup requires the exact frozen digest and absolute root-owned, service-visible Node.js, uv, launcher, package, systemctl, and useradd provenance.",
    );
    process.exit(1);
  }
}

const metadata = JSON.parse(readFileSync(path.join(packageRoot, "package.json"), "utf8"));
const installIdentity = createHash("sha256")
  .update(packageRoot, "utf8")
  .digest("hex")
  .slice(0, 12);
const minimumUvVersion = [0, 11, 28];
const uvVersion = spawnSync(uvExecutable, ["--version"], {
  encoding: "utf8",
  shell: false,
});
if (uvVersion.error?.code === "ENOENT") {
  console.error("AgentNet requires uv 0.11.28 or newer on PATH: https://docs.astral.sh/uv/");
  process.exit(1);
}
if (uvVersion.error || uvVersion.status !== 0) {
  console.error("AgentNet could not determine the installed uv version.");
  process.exit(1);
}
const uvMatch = /^uv (\d+)\.(\d+)\.(\d+)(?:\s|$)/.exec(uvVersion.stdout.trim());
if (!uvMatch) {
  console.error("AgentNet could not parse the installed uv version.");
  process.exit(1);
}
const actualUvVersion = uvMatch.slice(1).map(Number);
const versionAtLeast = (actual, minimum) => {
  for (let index = 0; index < minimum.length; index += 1) {
    if (actual[index] > minimum[index]) return true;
    if (actual[index] < minimum[index]) return false;
  }
  return true;
};
if (!versionAtLeast(actualUvVersion, minimumUvVersion)) {
  console.error(
    `AgentNet requires uv 0.11.28 or newer; found ${actualUvVersion.join(".")}. ` +
      "Upgrade uv explicitly, then retry.",
  );
  process.exit(1);
}

let stateRoot;
if (!privilegedSetupApply) {
  try {
    stateRoot = path.resolve(platformStateRoot(process.platform, process.env, os.homedir()));
  } catch (error) {
    console.error(error instanceof Error ? error.message : "AgentNet host state path is invalid");
    process.exit(1);
  }
}
let runtimeRoot;
if (privilegedSetupApply) {
  const setupRoot = "/var/lib/agentnet-setup";
  mkdirSync(setupRoot, { recursive: true, mode: 0o700 });
  const setupRootStat = lstatSync(setupRoot);
  if (
    !setupRootStat.isDirectory() || setupRootStat.isSymbolicLink() || setupRootStat.uid !== 0 ||
    (setupRootStat.mode & 0o077) !== 0
  ) {
    console.error("Privileged AgentNet setup runtime custody conflicts with the fixed profile.");
    process.exit(1);
  }
  chmodSync(setupRoot, 0o700);
  runtimeRoot = path.join(setupRoot, "npm-runtime", `${metadata.version}-${installIdentity}`);
} else {
  const packageOwnedRuntime = path.join(
    stateRoot,
    "agentnet",
    "npm-runtime",
    `${metadata.version}-${installIdentity}`,
  );
  runtimeRoot = packagedSetup
    ? packageOwnedRuntime
    : process.env.AGENTNET_NPM_RUNTIME_DIR
      ? path.resolve(process.env.AGENTNET_NPM_RUNTIME_DIR)
      : packageOwnedRuntime;
}

try {
  mkdirSync(runtimeRoot, { recursive: true, mode: 0o700 });
  const runtimeStat = lstatSync(runtimeRoot);
  if (
    !runtimeStat.isDirectory() || runtimeStat.isSymbolicLink() ||
    (
      packagedSetup && process.platform !== "win32" &&
      typeof process.getuid === "function" && runtimeStat.uid !== process.getuid()
    )
  ) {
    console.error(
      packagedSetup
        ? "AgentNet setup could not access its package-owned Python runtime."
        : "AgentNet npm runtime path must be a real directory.",
    );
    process.exit(1);
  }
  if (process.platform !== "win32") {
    chmodSync(runtimeRoot, 0o700);
  } else {
    const runtimeMode = readdirSync(runtimeRoot).length === 0 ? "initialize" : "verify";
    const aclScript = path.join(packageRoot, "npm", "lib", "windows-runtime-acl.ps1");
    const aclCheck = spawnSync(
      "powershell.exe",
      [
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-ExecutionPolicy",
        "Bypass",
        "-File",
        aclScript,
        runtimeRoot,
        runtimeMode,
      ],
      { encoding: "utf8", shell: false, windowsHide: true },
    );
    if (aclCheck.error || aclCheck.status !== 0) {
      if (packagedSetup) throw new Error("invalid runtime DACL");
      console.error("AgentNet Windows npm runtime root failed private-DACL verification.");
      process.exit(1);
    }
  }
} catch (error) {
  if (!packagedSetup) throw error;
  console.error("AgentNet setup could not access its package-owned Python runtime.");
  process.exit(1);
}

const verify = userArguments[0] === "verify";
const uvArguments = [
  "run",
  "--project",
  packageRoot,
  "--frozen",
  "--no-default-groups",
  "--python",
  "3.13.13",
];
if (verify) uvArguments.push("--extra", "test");
if (packagedSetup) {
  uvArguments.push(
    "--managed-python",
    "--no-active",
    "--no-config",
    "python",
    "-I",
    "-B",
    "-m",
    "agentnet",
    ...userArguments,
  );
} else {
  uvArguments.push("agentnet", ...userArguments);
}

const inheritedEnvironment = privilegedSetupApply
  ? {
      PATH: systemPath,
      HOME: "/root",
      LANG: "C.UTF-8",
      ...(process.env.SUDO_UID && /^\d+$/.test(process.env.SUDO_UID)
        ? { SUDO_UID: process.env.SUDO_UID }
        : {}),
    }
  : { ...process.env };
if (packagedSetup) {
  for (const name of [
    "CONDA_PREFIX",
    "PYTHONHOME",
    "PYTHONPATH",
    "UV_NO_SYNC",
    "UV_PROJECT",
    "UV_PYTHON",
    "UV_PYTHON_INSTALL_DIR",
    "UV_WORKING_DIRECTORY",
    "VIRTUAL_ENV",
  ]) {
    delete inheritedEnvironment[name];
  }
}
const child = spawn(uvExecutable, uvArguments, {
  stdio: "inherit",
  env: {
    ...inheritedEnvironment,
    AGENTNET_PACKAGE_ROOT: packageRoot,
    AGENTNET_NODE_EXECUTABLE: nodeExecutable,
    AGENTNET_UV: uvExecutable,
    AGENTNET_NPM_RUNTIME_DIR: runtimeRoot,
    PYTHONDONTWRITEBYTECODE: "1",
    PYTHONPYCACHEPREFIX: path.join(runtimeRoot, "pycache"),
    UV_NO_MODIFY_PATH: "1",
    UV_PROJECT_ENVIRONMENT: runtimeRoot,
  },
  shell: false,
});

for (const signal of forwardedSignals(process.platform)) {
  process.on(signal, () => {
    if (!child.killed) child.kill(signal);
  });
}

child.on("error", (error) => {
  if (error.code === "ENOENT") {
    console.error("AgentNet requires uv on PATH: https://docs.astral.sh/uv/");
  } else {
    console.error(`AgentNet could not start uv: ${error.message}`);
  }
  process.exitCode = 1;
});

child.on("exit", (code, signal) => {
  if (signal) {
    process.exitCode = { SIGHUP: 129, SIGINT: 130, SIGTERM: 143, SIGBREAK: 149 }[signal] ?? 1;
    return;
  }
  process.exitCode = code ?? 1;
});
