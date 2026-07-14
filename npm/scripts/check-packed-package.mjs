#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const temporary = mkdtempSync(path.join(os.tmpdir(), "agentnet-packed-check-"));
const packDirectory = path.join(temporary, "pack");
const prefix = path.join(temporary, "prefix");
const unrelated = path.join(temporary, "unrelated");
const state = path.join(temporary, "state");
const home = path.join(temporary, "home");
const timeout = 10 * 60 * 1000;

for (const directory of [packDirectory, prefix, unrelated, state, home]) {
  mkdirSync(directory, { recursive: true, mode: 0o700 });
}

const run = (command, arguments_, options = {}) => {
  const completed = spawnSync(command, arguments_, {
    cwd: options.cwd ?? root,
    env: options.env ?? process.env,
    encoding: options.capture ? "utf8" : undefined,
    stdio: options.capture ? ["ignore", "pipe", "pipe"] : "inherit",
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

try {
  const packed = run(
    "npm",
    ["pack", "--json", "--ignore-scripts", "--pack-destination", packDirectory],
    { capture: true },
  );
  const parsed = JSON.parse(packed.stdout);
  const manifest = Array.isArray(parsed) ? parsed[0] : Object.values(parsed)[0];
  if (!manifest?.filename) throw new Error("npm pack did not report a tarball filename");
  const tarball = path.join(packDirectory, manifest.filename);

  run("npm", [
    "install",
    "--prefix",
    prefix,
    "--ignore-scripts",
    "--no-audit",
    "--no-fund",
    tarball,
  ]);

  const environment = {
    ...process.env,
    HOME: home,
    PYTHONDONTWRITEBYTECODE: "1",
    UV_CACHE_DIR: process.env.UV_CACHE_DIR ?? path.join(temporary, "uv-cache"),
    XDG_STATE_HOME: state,
  };
  for (const name of [
    "AGENTNET_NPM_RUNTIME_DIR",
    "AGENTNET_PACKAGE_ROOT",
    "AGENTNET_UV",
    "UV_PROJECT_ENVIRONMENT",
    "UV_PYTHON",
    "VIRTUAL_ENV",
  ]) {
    delete environment[name];
  }

  const launcher = path.join(prefix, "node_modules", ".bin", "agentnet");
  run(launcher, ["verify"], { cwd: unrelated, env: environment });
  console.log("packed npm package check: PASS");
} finally {
  if (process.env.AGENTNET_KEEP_PACKED_CHECK !== "1") {
    rmSync(temporary, { recursive: true, force: true });
  } else {
    console.log(`packed npm package check retained: ${temporary}`);
  }
}
