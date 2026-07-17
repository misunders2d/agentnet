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
if (metadata.pi?.skills?.length !== 1 || metadata.pi.skills[0] !== "./skills") {
  fail("exact Pi skill manifest missing or changed");
}
if (metadata.pi?.image !== "https://raw.githubusercontent.com/misunders2d/agentnet/main/docs/assets/agentnet-overview.png") {
  fail("Pi package preview image missing or changed");
}
const requiredPublishedFiles = [
  "docs/assets/agentnet-overview.png",
  "evidence/gates/G04/2026-07-13-alpha2-http-json/REVIEW.md",
  "evidence/gates/G04/2026-07-13-alpha2-http-json/compatibility.html",
  "evidence/gates/G04/2026-07-13-alpha2-http-json/junitreport.xml",
  "evidence/gates/G04/2026-07-13-alpha2-http-json/tck_report.html",
  "evidence/local/2026-07-17-v0.1.11/artifacts/RETENTION.md",
  "skills/**/*.md",
];
for (const relative of requiredPublishedFiles) {
  if (!metadata.files?.includes(relative)) fail(`published files exclude ${relative}`);
}
if (metadata.bin?.agentnet !== "npm/bin/agentnet.mjs") fail("agentnet launcher missing");
const supportedPlatforms = ["linux", "darwin", "win32"];
if (JSON.stringify(metadata.os) !== JSON.stringify(supportedPlatforms)) {
  fail("supported operating-system matrix changed");
}
if (metadata.scripts?.["check:packed"] !== "node npm/scripts/check-packed-package.mjs") {
  fail("full packed-package check is not wired");
}
if (!metadata.scripts?.check?.endsWith("&& npm run check:packed")) {
  fail("prepublication check does not run the full packed-package gate");
}
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
  "npm/lib/windows-runtime-acl.ps1",
  "npm/scripts/check-packed-package.mjs",
  "skills/agentnet-operator/SKILL.md",
  "skills/agentnet-operator/references/safe-commands.md",
  "skills/agentnet-operator/references/fail-closed-boundaries.md",
  "skills/agentnet-operator/references/required-communication-scope.md",
  "skills/agentnet-operator/references/fresh-laptop-onboarding.md",
  "skills/agentnet-operator/evals/evals.json",
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

const skillText = readFileSync(path.join(root, "skills/agentnet-operator/SKILL.md"), "utf8");
for (const required of [
  "name: agentnet-operator",
  "description:",
  "docs/requirements.md",
  "blocked: product component not yet shipped",
  "local_bindings_required=true",
  "Fresh-laptop onboarding is human-mediated",
  "references/fresh-laptop-onboarding.md",
]) {
  if (!skillText.includes(required)) fail(`AgentNet operator skill is missing: ${required}`);
}
const onboardingText = readFileSync(
  path.join(root, "skills/agentnet-operator/references/fresh-laptop-onboarding.md"),
  "utf8",
);
for (const required of [
  "The unconnected laptop has no agent inbox",
  "Required bootstrap packet",
  "first-message verification",
  "AgentNet `0.1.8` fails this gate",
]) {
  if (!onboardingText.includes(required)) fail(`fresh-laptop onboarding reference is missing: ${required}`);
}
const onboardingEvals = JSON.parse(
  readFileSync(path.join(root, "skills/agentnet-operator/evals/evals.json"), "utf8"),
);
const expectedOnboardingEvalIds = [
  "fresh-agent-receives-bootstrap-packet",
  "fresh-laptop-human-copy-paste-bootstrap",
  "hub-generates-public-onboarding-packet",
  "v018-fresh-laptop-receipt-gap",
  "v019-guided-enrollment-is-identity-only",
];
const actualOnboardingEvalIds = Array.isArray(onboardingEvals)
  ? onboardingEvals.map((item) => item?.id).sort()
  : [];
if (JSON.stringify(actualOnboardingEvalIds) !== JSON.stringify(expectedOnboardingEvalIds)) {
  fail("fresh-laptop onboarding regression evals are missing or changed");
}

const launcher = path.join(root, "npm/bin/agentnet.mjs");
if (process.platform !== "win32" && (lstatSync(launcher).mode & 0o111) === 0) {
  fail("agentnet launcher is not executable");
}
const launcherText = readFileSync(launcher, "utf8");
if (!launcherText.includes('"3.13.13"') || launcherText.includes('">=3.13,<3.15"')) {
  fail("npm launcher is not pinned to certified CPython 3.13.13");
}
if (!launcherText.includes("minimumUvVersion = [0, 11, 28]")) {
  fail("npm launcher does not enforce the minimum supported uv version");
}

if (!process.exitCode) console.log("npm package check: PASS");
