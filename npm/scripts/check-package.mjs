#!/usr/bin/env node

import { accessSync, constants, lstatSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const fail = (message) => {
  console.error(`npm package check: FAIL - ${message}`);
  process.exitCode = 1;
};
const metadata = JSON.parse(readFileSync(path.join(root, "package.json"), "utf8"));
const pyproject = readFileSync(path.join(root, "pyproject.toml"), "utf8");

if (metadata.name !== "@misunders2d/agentnet") fail("scoped package name changed");
if (!metadata.keywords?.includes("pi-package")) fail("pi-package keyword missing");
if (metadata.pi?.extensions?.length !== 1) fail("exact Pi extension manifest missing");
if (metadata.pi?.extensions?.[0] !== "./src/agentnet/bindings/pi_extension.ts") {
  fail("Pi extension path changed");
}
if (metadata.pi?.image !== "https://raw.githubusercontent.com/misunders2d/agentnet/main/docs/assets/agentnet-overview.png") {
  fail("Pi package preview image missing or changed");
}
if (metadata.bin?.agentnet !== "npm/bin/agentnet.mjs") fail("agentnet launcher missing");
if (!metadata.os?.includes("linux") || metadata.os.length !== 1) fail("Linux-only qualification changed");
const version = pyproject.match(/^version = "([^"]+)"$/m)?.[1];
if (!version || version !== metadata.version) fail("npm and Python versions differ");
if (!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(metadata.version)) {
  fail("package version is not valid semantic versioning");
}

for (const relative of [
  "LICENSE",
  "README.md",
  "docs/assets/agentnet-overview.png",
  "uv.lock",
  "pyproject.toml",
  "npm/bin/agentnet.mjs",
  "src/agentnet/bindings/pi_extension.ts",
  "src/agentnet/adapters/claude.py",
  "src/agentnet/adapters/codex.py",
  "src/agentnet/adapters/pi.py",
  "src/agentnet/adapters/antigravity.py",
]) {
  try {
    accessSync(path.join(root, relative), constants.R_OK);
  } catch {
    fail(`required package file missing: ${relative}`);
  }
}

const launcher = path.join(root, "npm/bin/agentnet.mjs");
if ((lstatSync(launcher).mode & 0o111) === 0) fail("agentnet launcher is not executable");

if (!process.exitCode) console.log("npm package check: PASS");
