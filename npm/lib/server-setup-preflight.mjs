import { createHash } from "node:crypto";
import {
  closeSync,
  constants as fsConstants,
  fstatSync,
  openSync,
  readFileSync,
  realpathSync,
} from "node:fs";
import path from "node:path";

const sha256 = (payload) => createHash("sha256").update(payload).digest("hex");
const canonicalize = (value) => {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.keys(value).sort().map((key) => [key, canonicalize(value[key])]),
    );
  }
  return value;
};
const canonicalDigest = (value) => sha256(
  Buffer.from(JSON.stringify(canonicalize(value)), "utf8"),
);
const inputFields = [
  "core_environment_file",
  "approval_environment_file",
  "oidc_provider_file",
  "approval_owner_oidc_file",
  "approval_approvers_file",
  "scanner_trust_file",
];

export const argumentValue = (userArguments, name) => {
  const indexes = userArguments.flatMap((value, index) => value === name ? [index] : []);
  if (indexes.length !== 1 || indexes[0] + 1 >= userArguments.length) {
    throw new Error(`missing ${name}`);
  }
  return userArguments[indexes[0] + 1];
};

const readPrivateSetupInput = (filename, maximum, environment) => {
  if (!path.isAbsolute(filename) || path.normalize(filename) !== filename || realpathSync(filename) !== filename) {
    throw new Error("setup input path is not canonical");
  }
  const sudoUid = /^\d+$/.test(environment.SUDO_UID ?? "")
    ? Number(environment.SUDO_UID)
    : null;
  const descriptor = openSync(
    filename,
    fsConstants.O_RDONLY | fsConstants.O_NONBLOCK | fsConstants.O_NOFOLLOW | fsConstants.O_CLOEXEC,
  );
  try {
    const before = fstatSync(descriptor, { bigint: true });
    if (
      !before.isFile() || ![0, sudoUid].includes(Number(before.uid)) ||
      before.nlink !== 1n || (before.mode & 0o77n) !== 0n ||
      before.size < 1n || before.size > BigInt(maximum)
    ) {
      throw new Error("setup input custody is unsafe");
    }
    const payload = readFileSync(descriptor);
    const after = fstatSync(descriptor, { bigint: true });
    if (
      BigInt(payload.length) !== before.size || after.dev !== before.dev ||
      after.ino !== before.ino || after.size !== before.size ||
      after.mtimeNs !== before.mtimeNs || after.ctimeNs !== before.ctimeNs
    ) {
      throw new Error("setup input changed while being read");
    }
    return payload;
  } finally {
    closeSync(descriptor);
  }
};

const environmentNames = (payload) => {
  const names = new Set();
  for (const line of payload.toString("utf8").split(/\r?\n/u)) {
    const stripped = line.trim();
    if (!stripped || stripped.startsWith("#")) continue;
    if (stripped !== line || !line.includes("=")) {
      throw new Error("setup environment is invalid");
    }
    const index = line.indexOf("=");
    const name = line.slice(0, index);
    const value = line.slice(index + 1);
    if (
      !/^[A-Z_][A-Z0-9_]{0,127}$/u.test(name) || names.has(name) || !value ||
      /[\s'"\\\x00-\x1f\x7f]/u.test(value)
    ) {
      throw new Error("setup environment is invalid");
    }
    names.add(name);
  }
  return [...names].sort();
};

export const privilegedApprovalDigest = (
  userArguments,
  environment = process.env,
) => {
  const requestPath = argumentValue(userArguments, "--request");
  const requestPayload = readPrivateSetupInput(requestPath, 1_048_576, environment);
  const request = JSON.parse(requestPayload.toString("utf8"));
  if (request === null || Array.isArray(request) || typeof request !== "object") {
    throw new Error("setup request is invalid");
  }
  const references = {};
  for (const field of inputFields) {
    const filename = request[field];
    if (typeof filename !== "string") throw new Error("setup request reference is invalid");
    const payload = readPrivateSetupInput(
      filename,
      field.endsWith("environment_file") ? 262_144 : 1_048_576,
      environment,
    );
    const fingerprint = field.endsWith("environment_file")
      ? canonicalDigest({ environment_names: environmentNames(payload) })
      : sha256(payload);
    references[field] = { path: filename, fingerprint };
  }
  return canonicalDigest({
    schema: "agentnet.server-setup.approval-digest.v1",
    request_file_sha256: sha256(requestPayload),
    referenced_inputs: references,
  });
};
