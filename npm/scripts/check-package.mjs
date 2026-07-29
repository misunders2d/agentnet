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
  "evidence/local/2026-07-13-final/artifacts/agentnet-0.1.0-py3-none-any.whl",
  "evidence/local/2026-07-13-final/artifacts/agentnet-0.1.0.tar.gz",
  "evidence/local/2026-07-28-v0.1.31/artifacts/RETENTION.md",
  "evidence/local/2026-07-28-v0.1.31/artifacts/agentnet-0.1.31-py3-none-any.whl",
  "evidence/local/2026-07-28-v0.1.31/artifacts/agentnet-0.1.31.tar.gz",
  "skills/**/*.md",
  "skills/**/*.json",
  "tests/fixtures/**/*.json",
];
for (const relative of requiredPublishedFiles) {
  if (!metadata.files?.includes(relative)) fail(`published files exclude ${relative}`);
}
for (const forbidden of ["evidence/**/*.whl", "evidence/**/*.gz"]) {
  if (metadata.files?.includes(forbidden)) fail(`published files include historical archives: ${forbidden}`);
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
const runtimeVersion = readFileSync(path.join(root, "src/agentnet/__init__.py"), "utf8")
  .match(/^__version__ = "([^"]+)"$/m)?.[1];
if (!runtimeVersion || runtimeVersion !== metadata.version) {
  fail("npm and Python runtime versions differ");
}
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
  "npm/lib/platform.mjs",
  "npm/lib/windows-runtime-acl.ps1",
  "npm/scripts/check-packed-package.mjs",
  "skills/agentnet-operator/SKILL.md",
  "skills/agentnet-operator/references/safe-commands.md",
  "skills/agentnet-operator/references/fail-closed-boundaries.md",
  "skills/agentnet-operator/references/required-communication-scope.md",
  "skills/agentnet-operator/references/fresh-laptop-onboarding.md",
  "skills/agentnet-operator/references/ordinary-server-setup.md",
  "skills/agentnet-operator/references/examples/fresh-laptop-single-prompt.md",
  "skills/agentnet-operator/references/examples/ordinary-server-setup-request.json",
  "skills/agentnet-operator/references/examples/ordinary-server-communication-only-setup-request.json",
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
const ordinaryServerText = readFileSync(
  path.join(root, "skills/agentnet-operator/references/ordinary-server-setup.md"),
  "utf8",
);
const safeCommandsText = readFileSync(
  path.join(root, "skills/agentnet-operator/references/safe-commands.md"),
  "utf8",
);
const scannerBackedRequest = JSON.parse(
  readFileSync(
    path.join(root, "skills/agentnet-operator/references/examples/ordinary-server-setup-request.json"),
    "utf8",
  ),
);
const communicationOnlyRequest = JSON.parse(
  readFileSync(
    path.join(
      root,
      "skills/agentnet-operator/references/examples/ordinary-server-communication-only-setup-request.json",
    ),
    "utf8",
  ),
);
for (const required of [
  "name: agentnet-operator",
  "description:",
  "docs/requirements.md",
  "blocked: product component not yet shipped",
  "local_bindings_required=true",
  "Fresh-laptop onboarding is human-mediated",
  "references/fresh-laptop-onboarding.md",
  "references/examples/fresh-laptop-single-prompt.md",
  "references/ordinary-server-setup.md",
  "fixed `server-agent setup` flow",
  "For `server-agent reset`, treat request as destructive server-manager-only recovery",
]) {
  if (!skillText.includes(required)) fail(`AgentNet operator skill is missing: ${required}`);
}
if (
  scannerBackedRequest.schema !== "agentnet.server-setup.request.v1"
  || Object.hasOwn(scannerBackedRequest, "artifact_mode")
  || typeof scannerBackedRequest.scanner_trust_file !== "string"
) {
  fail("scanner-backed setup request does not preserve strict request-v1 semantics");
}
if (
  communicationOnlyRequest.schema !== "agentnet.server-setup.request.v2"
  || communicationOnlyRequest.artifact_mode !== "disabled"
  || Object.hasOwn(communicationOnlyRequest, "scanner_trust_file")
) {
  fail("communication-only setup request does not preserve strict disabled-mode omission");
}
for (const required of [
  "one hash computed from deterministic path/type/size/content records for the full root-owned AgentNet package tree executed by `uv run --project`",
  "postgresql://agentnet@%2Fvar%2Frun%2Fpostgresql/agentnet",
  "then return `postgres_auth_not_ready`. It creates no AgentNet environment, Core/Approval config, database schema, unit, Approval identity, or service.",
  "**Before first apply only**, correcting private environment values while preserving approved absolute files and variable-name sets keeps same plan digest",
  "first approved apply may create fixed Core OS identity plus root-owned `/var/lib/agentnet-setup` npm runtime and lock custody",
  "Version 0.1.31 has no package-owned in-place broker-credential or database-password rotation transition",
  "Permanent `/var/lib/agentnet-setup/setup.lock` and its root remain as coordination state",
  "Request-v2 writes marker-v3 binding exact `artifact_mode`",
  "communication-only restricted",
]) {
  if (!ordinaryServerText.includes(required)) {
    fail(`ordinary-server setup reference is missing canonical contract: ${required}`);
  }
}
for (const required of [
  "/var/lib/agentnet-approval/config.json",
  "/var/lib/agentnet-approval/state",
  "deliberately non-ordinary",
]) {
  if (!safeCommandsText.includes(required)) {
    fail(`safe commands reference is missing canonical ordinary-server boundary: ${required}`);
  }
}
for (const forbidden of [
  "/etc/agentnet-approval/config.json",
  "postgresql://agentnet@127.0.0.1/agentnet",
]) {
  if (skillText.includes(forbidden) || ordinaryServerText.includes(forbidden) || safeCommandsText.includes(forbidden)) {
    fail(`AgentNet operator skill contains stale ordinary-server contract: ${forbidden}`);
  }
}
const onboardingText = readFileSync(
  path.join(root, "skills/agentnet-operator/references/fresh-laptop-onboarding.md"),
  "utf8",
);
for (const required of [
  "The unconnected laptop has no agent inbox",
  "Required bootstrap packet",
  "Canonical public onboarding prompt example",
  "single fresh-laptop onboarding prompt",
  "Any unresolved required placeholder blocks issuance",
  "Full C0 success",
  "AgentNet `0.1.8` fails this gate",
  "references/examples/fresh-laptop-single-prompt.md",
]) {
  if (!onboardingText.includes(required)) fail(`fresh-laptop onboarding reference is missing: ${required}`);
}
if (onboardingText.includes("AgentNet blank-laptop onboarding — exact public packet")) {
  fail("fresh-laptop onboarding contract embeds the canonical packet instead of routing to the example");
}
const onboardingExampleText = readFileSync(
  path.join(root, "skills/agentnet-operator/references/examples/fresh-laptop-single-prompt.md"),
  "utf8",
);
for (const required of [
  "AgentNet blank-laptop onboarding — exact public packet",
  "<ONBOARDING_MODE>",
  "mode `identity_only`",
  "mode `c0_pilot`",
  "first_message_blocked_explicit_authority_required",
  "BootstrapGrantPlan",
  "Do not run `agentnet admin entitlement issue`",
  "No other human setup",
  "no extra approval host",
  "No relay channel or second person is required",
  "Do not report or relay principal/harness IDs",
  "Infisical or other named secret manager",
  "per-command setup approvals",
  "Never ask for another command packet, hostname, URL, callback, hash, identifier, config value",
]) {
  if (!onboardingExampleText.includes(required)) fail(`fresh-laptop prompt example is missing: ${required}`);
}
for (const forbidden of [
  "BEGIN PRIVATE KEY",
  "agcap1.",
  "Authorization: Bearer",
  "<APPROVER_NAME>",
  "<APPROVAL_CODE_CHANNEL>",
  "<ADMINISTRATOR_NAME>",
  "<PRINCIPAL_ID_REPORTING_APPROVED>",
  "<MESSAGING_TEST_IN_SCOPE>",
  "server-side onboarding orchestrator",
  "Three separate issuance records are expected",
  "server-side administrator issues these three grants",
]) {
  if (onboardingExampleText.includes(forbidden)) fail(`fresh-laptop prompt example contains forbidden or obsolete content: ${forbidden}`);
}
if (skillText.includes("AgentNet blank-laptop onboarding — exact public packet")) {
  fail("AgentNet operator SKILL.md embeds the canonical packet");
}
const onboardingEvals = JSON.parse(
  readFileSync(path.join(root, "skills/agentnet-operator/evals/evals.json"), "utf8"),
);
const expectedOnboardingEvalIds = [
  "c0-binding-invalidation-is-terminal",
  "c0-fixed-commands-and-cleanup-only",
  "c0-success-requires-approved-seven-fact-sequence",
  "fresh-agent-receives-bootstrap-packet",
  "fresh-laptop-approval-result-is-automatic",
  "fresh-laptop-canonical-single-prompt-is-mandatory",
  "fresh-laptop-default-needs-no-extra-approval-host",
  "fresh-laptop-human-copy-paste-bootstrap",
  "fresh-laptop-human-never-supplies-technical-metadata",
  "fresh-laptop-messaging-authority-blocked",
  "fresh-laptop-never-requires-infisical",
  "fresh-laptop-one-consolidated-setup-approval",
  "fresh-laptop-rejects-invalid-onboarding-mode",
  "fresh-laptop-rejects-three-grant-c0-fallback",
  "guided-join-terminal-recovery-is-explicit-and-key-preserving",
  "headless-server-uses-fixed-browser-only-activation",
  "hub-generates-public-onboarding-packet",
  "identity-only-mode-skips-c0-phase",
  "ordinary-server-communication-only-explicit-v2",
  "ordinary-server-communication-only-rejects-legacy-evidence",
  "ordinary-server-configured-not-started-resume",
  "ordinary-server-disabled-mode-rejects-null-scanner-field",
  "ordinary-server-enabled-mode-requires-scanner-before-mutation",
  "ordinary-server-human-ceremony-remains-explicit",
  "ordinary-server-invalid-broker-blocks-before-mutation",
  "ordinary-server-marker-never-proves-readiness",
  "ordinary-server-missing-route-blocks",
  "ordinary-server-postgres-first-apply-safe-partial-state",
  "ordinary-server-postgres-peer-block-and-resume",
  "ordinary-server-rejects-home-runtime",
  "ordinary-server-remote-manager-never-shells",
  "ordinary-server-request-v2-requires-explicit-artifact-mode",
  "ordinary-server-resumes-exact-request",
  "ordinary-server-runtime-drift-invalidates-digest",
  "ordinary-server-uses-product-owned-setup",
  "repository-candidate-does-not-unblock-installed-release",
  "server-reset-is-destructive-manager-only-recovery",
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
for (const marker of [
  "privilegedSetupApply",
  "PATHEXT",
  'from "../lib/server-setup-preflight.mjs"',
  "privilegedApprovalDigest(userArguments, digestEnvironment)",
  "requireRootOwnedPath(packageRoot, { recursive: true })",
  "info.isDirectory() ? 0o001 : 0o005",
  "AGENTNET_NODE_EXECUTABLE: nodeExecutable",
  "AGENTNET_PACKAGE_ROOT: packageRoot",
  "AGENTNET_SYSTEMCTL: systemctlExecutable",
  "AGENTNET_USERADD: useraddExecutable",
  'PYTHONDONTWRITEBYTECODE: "1"',
  'const setupRoot = "/var/lib/agentnet-setup"',
  "const inheritedEnvironment = privilegedSetupApply",
]) {
  if (!launcherText.includes(marker)) {
    fail(`npm launcher is missing privileged setup provenance marker: ${marker}`);
  }
}

if (!process.exitCode) console.log("npm package check: PASS");
