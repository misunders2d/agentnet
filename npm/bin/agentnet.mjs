#!/usr/bin/env node

import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, lstatSync, mkdirSync, readFileSync, realpathSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = realpathSync(
  path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", ".."),
);
const metadata = JSON.parse(readFileSync(path.join(packageRoot, "package.json"), "utf8"));
const installIdentity = createHash("sha256")
  .update(packageRoot, "utf8")
  .digest("hex")
  .slice(0, 12);

if (process.platform !== "linux") {
  console.error("AgentNet 0.1.x npm packages support Linux only; this platform is not qualified.");
  process.exit(1);
}

const stateRoot = process.env.XDG_STATE_HOME
  ? path.resolve(process.env.XDG_STATE_HOME)
  : path.join(os.homedir(), ".local", "state");
const runtimeRoot = process.env.AGENTNET_NPM_RUNTIME_DIR
  ? path.resolve(process.env.AGENTNET_NPM_RUNTIME_DIR)
  : path.join(stateRoot, "agentnet", "npm-runtime", `${metadata.version}-${installIdentity}`);

mkdirSync(runtimeRoot, { recursive: true, mode: 0o700 });
const runtimeStat = lstatSync(runtimeRoot);
if (!runtimeStat.isDirectory() || runtimeStat.isSymbolicLink()) {
  console.error("AgentNet npm runtime path must be a real directory.");
  process.exit(1);
}
chmodSync(runtimeRoot, 0o700);

const userArguments = process.argv.slice(2);
const verify = userArguments[0] === "verify";
const uvArguments = [
  "run",
  "--project",
  packageRoot,
  "--frozen",
  "--no-default-groups",
  "--python",
  ">=3.13,<3.15",
];
if (verify) uvArguments.push("--extra", "test");
uvArguments.push("agentnet", ...userArguments);

const uvExecutable = process.env.AGENTNET_UV || "uv";
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

for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
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
    process.exitCode = { SIGHUP: 129, SIGINT: 130, SIGTERM: 143 }[signal] ?? 1;
    return;
  }
  process.exitCode = code ?? 1;
});
