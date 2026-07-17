#!/usr/bin/env node

import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync,
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

const packageRoot = realpathSync(
  path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", ".."),
);
const metadata = JSON.parse(readFileSync(path.join(packageRoot, "package.json"), "utf8"));
const installIdentity = createHash("sha256")
  .update(packageRoot, "utf8")
  .digest("hex")
  .slice(0, 12);

if (!supportedPlatform(process.platform)) {
  console.error(`AgentNet does not support host platform: ${process.platform}`);
  process.exit(1);
}

const uvExecutable = process.env.AGENTNET_UV || "uv";
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
try {
  stateRoot = path.resolve(platformStateRoot(process.platform, process.env, os.homedir()));
} catch (error) {
  console.error(error instanceof Error ? error.message : "AgentNet host state path is invalid");
  process.exit(1);
}
const runtimeRoot = process.env.AGENTNET_NPM_RUNTIME_DIR
  ? path.resolve(process.env.AGENTNET_NPM_RUNTIME_DIR)
  : path.join(stateRoot, "agentnet", "npm-runtime", `${metadata.version}-${installIdentity}`);

mkdirSync(runtimeRoot, { recursive: true, mode: 0o700 });
const runtimeStat = lstatSync(runtimeRoot);
if (!runtimeStat.isDirectory() || runtimeStat.isSymbolicLink()) {
  console.error("AgentNet npm runtime path must be a real directory.");
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
    console.error("AgentNet Windows npm runtime root failed private-DACL verification.");
    process.exit(1);
  }
}

const userArguments = process.argv.slice(2);
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
uvArguments.push("agentnet", ...userArguments);

const child = spawn(uvExecutable, uvArguments, {
  stdio: "inherit",
  env: {
    ...process.env,
    AGENTNET_PACKAGE_ROOT: packageRoot,
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
