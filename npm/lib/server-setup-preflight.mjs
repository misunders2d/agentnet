import { createHash } from "node:crypto";
import {
  closeSync,
  constants as fsConstants,
  fstatSync,
  lstatSync,
  openSync,
  readdirSync,
  readSync,
  realpathSync,
} from "node:fs";
import path from "node:path";
import { SERVICE_HIDDEN_ROOTS, requireServiceVisiblePath } from "./platform.mjs";

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
const baseInputFields = [
  "core_environment_file",
  "approval_environment_file",
  "oidc_provider_file",
  "approval_owner_oidc_file",
  "approval_approvers_file",
];

const requestEvidenceProfile = (request) => {
  if (request.schema === "agentnet.server-setup.request.v1") {
    if (request.artifact_mode !== undefined || typeof request.scanner_trust_file !== "string") {
      throw new Error("setup request artifact profile is invalid");
    }
    return {
      artifactMode: "enabled",
      digestSchema: "agentnet.server-setup.approval-digest.v2",
      inputFields: [...baseInputFields, "scanner_trust_file"],
    };
  }
  if (request.schema !== "agentnet.server-setup.request.v2") {
    throw new Error("setup request schema is invalid");
  }
  if (request.artifact_mode === "enabled" && typeof request.scanner_trust_file === "string") {
    return {
      artifactMode: "enabled",
      digestSchema: "agentnet.server-setup.approval-digest.v3",
      inputFields: [...baseInputFields, "scanner_trust_file"],
    };
  }
  if (request.artifact_mode === "disabled" && request.scanner_trust_file === undefined) {
    return {
      artifactMode: "disabled",
      digestSchema: "agentnet.server-setup.approval-digest.v3",
      inputFields: baseInputFields,
    };
  }
  throw new Error("setup request artifact profile is invalid");
};

export const argumentValue = (userArguments, name) => {
  const indexes = userArguments.flatMap((value, index) => value === name ? [index] : []);
  if (indexes.length !== 1 || indexes[0] + 1 >= userArguments.length) {
    throw new Error(`missing ${name}`);
  }
  return userArguments[indexes[0] + 1];
};

const readBoundedSnapshot = (descriptor, expectedSize, reader) => {
  const payload = Buffer.alloc(expectedSize + 1);
  let offset = 0;
  while (offset < payload.length) {
    const count = reader(
      descriptor,
      payload,
      offset,
      payload.length - offset,
      offset,
    );
    if (count === 0) break;
    offset += count;
  }
  return payload.subarray(0, offset);
};

export const readPrivateSetupInput = (
  filename,
  maximum,
  environment,
  reader = readSync,
  statReader = fstatSync,
) => {
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
    const before = statReader(descriptor, { bigint: true });
    if (
      !before.isFile() || ![0, sudoUid].includes(Number(before.uid)) ||
      before.nlink !== 1n || (before.mode & 0o77n) !== 0n ||
      before.size < 1n || before.size > BigInt(maximum)
    ) {
      throw new Error("setup input custody is unsafe");
    }
    const expectedSize = Number(before.size);
    let first;
    let middle;
    let second;
    let after;
    try {
      first = readBoundedSnapshot(descriptor, expectedSize, reader);
      middle = statReader(descriptor, { bigint: true });
      second = readBoundedSnapshot(descriptor, expectedSize, reader);
      after = statReader(descriptor, { bigint: true });
    } catch {
      throw new Error("setup input changed while being read");
    }
    if (
      first.length !== expectedSize || !first.equals(second) ||
      [middle, after].some((snapshot) =>
        snapshot.dev !== before.dev || snapshot.ino !== before.ino ||
        snapshot.size !== before.size || snapshot.mtimeNs !== before.mtimeNs ||
        snapshot.ctimeNs !== before.ctimeNs
      )
    ) {
      throw new Error("setup input changed while being read");
    }
    return first;
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

const unchangedMetadata = (before, after) => [
  "dev", "ino", "mode", "uid", "gid", "nlink", "size", "mtimeNs", "ctimeNs",
].every((field) => before[field] === after[field]);

export const stableExecutableSha256 = (filename, readChunk = readSync) => {
  const descriptor = openSync(
    filename,
    fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW | fsConstants.O_CLOEXEC,
  );
  try {
    const before = fstatSync(descriptor, { bigint: true });
    if (!before.isFile() || before.nlink < 1n || before.size < 1n) {
      throw new Error("setup runtime executable custody is unsafe");
    }
    const digest = createHash("sha256");
    const buffer = Buffer.allocUnsafe(1_048_576);
    for (;;) {
      const count = readChunk(descriptor, buffer, 0, buffer.length, null);
      if (!Number.isSafeInteger(count) || count < 0 || count > buffer.length) {
        throw new Error("setup runtime executable read is invalid");
      }
      if (count === 0) break;
      digest.update(buffer.subarray(0, count));
    }
    const after = fstatSync(descriptor, { bigint: true });
    if (!unchangedMetadata(before, after)) {
      throw new Error("setup runtime executable changed during preflight");
    }
    return digest.digest("hex");
  } finally {
    closeSync(descriptor);
  }
};

export const stablePackageTreeSha256 = (root) => {
  if (typeof root !== "string" || !path.isAbsolute(root) || path.normalize(root) !== root || realpathSync(root) !== root) {
    throw new Error("setup package root is invalid");
  }
  const maximumRecords = 20_000;
  const maximumBytes = 536_870_912n;
  let totalBytes = 0n;
  const records = [{ path: ".", type: "directory" }];

  const stableFile = (filename, relative) => {
    const descriptor = openSync(
      filename,
      fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW | fsConstants.O_CLOEXEC,
    );
    try {
      const before = fstatSync(descriptor, { bigint: true });
      if (!before.isFile() || before.size < 0n) {
        throw new Error("setup package tree contains an unsupported entry");
      }
      totalBytes += before.size;
      if (totalBytes > maximumBytes || before.size > BigInt(Number.MAX_SAFE_INTEGER)) {
        throw new Error("setup package tree exceeds fixed evidence bound");
      }
      const digest = createHash("sha256");
      const buffer = Buffer.allocUnsafe(1_048_576);
      for (;;) {
        const count = readSync(descriptor, buffer, 0, buffer.length, null);
        if (count === 0) break;
        digest.update(buffer.subarray(0, count));
      }
      const after = fstatSync(descriptor, { bigint: true });
      if (!unchangedMetadata(before, after)) {
        throw new Error("setup package tree changed during preflight");
      }
      records.push({
        path: relative,
        sha256: digest.digest("hex"),
        size: Number(before.size),
        type: "file",
      });
    } finally {
      closeSync(descriptor);
    }
  };

  const visit = (directory) => {
    const before = lstatSync(directory, { bigint: true });
    if (!before.isDirectory() || before.isSymbolicLink()) {
      throw new Error("setup package tree contains an unsupported entry");
    }
    const entries = readdirSync(directory, { withFileTypes: true })
      .sort((left, right) => Buffer.compare(Buffer.from(left.name, "utf8"), Buffer.from(right.name, "utf8")));
    for (const entry of entries) {
      const filename = path.join(directory, entry.name);
      const relative = path.relative(root, filename).split(path.sep).join("/");
      const metadata = lstatSync(filename, { bigint: true });
      if (metadata.isSymbolicLink()) {
        throw new Error("setup package tree contains a symbolic link");
      }
      if (metadata.isDirectory()) {
        records.push({ path: relative, type: "directory" });
        visit(filename);
      } else if (metadata.isFile()) {
        stableFile(filename, relative);
      } else {
        throw new Error("setup package tree contains an unsupported entry");
      }
      if (records.length > maximumRecords) {
        throw new Error("setup package tree exceeds fixed evidence bound");
      }
    }
    const after = lstatSync(directory, { bigint: true });
    if (!unchangedMetadata(before, after)) {
      throw new Error("setup package tree changed during preflight");
    }
  };

  visit(root);
  return canonicalDigest({
    records,
    schema: "agentnet.package-tree-content.v1",
  });
};

const runtimeIdentity = (environment, hiddenRoots) => {
  const fields = {
    agentnet_executable: environment.AGENTNET_EXECUTABLE,
    node_executable: environment.AGENTNET_NODE_EXECUTABLE,
    systemctl_executable: environment.AGENTNET_SYSTEMCTL,
    useradd_executable: environment.AGENTNET_USERADD,
    uv_executable: environment.AGENTNET_UV,
  };
  const result = {};
  for (const [name, filename] of Object.entries(fields)) {
    if (typeof filename !== "string" || !path.isAbsolute(filename) || path.normalize(filename) !== filename) {
      throw new Error("setup runtime identity is invalid");
    }
    const resolved = realpathSync(filename);
    if (resolved !== filename) throw new Error("setup runtime identity is not canonical");
    requireServiceVisiblePath(resolved, name.replace("_executable", ""), hiddenRoots);
    result[name] = filename;
    result[name.replace("_executable", "_sha256")] = stableExecutableSha256(filename);
  }
  const packageRoot = environment.AGENTNET_PACKAGE_ROOT;
  if (typeof packageRoot !== "string") throw new Error("setup package root is invalid");
  requireServiceVisiblePath(packageRoot, "package root", hiddenRoots);
  result.package_root = packageRoot;
  result.package_tree_sha256 = stablePackageTreeSha256(packageRoot);
  return result;
};

// `serviceHiddenRoots` exists so hermetic fixture trees can exercise the digest
// contract itself.  The privileged launcher never passes it, so the managed
// sandbox roots are always enforced on a real host.
export const privilegedApprovalDigest = (
  userArguments,
  environment = process.env,
  { serviceHiddenRoots = SERVICE_HIDDEN_ROOTS } = {},
) => {
  const requestPath = argumentValue(userArguments, "--request");
  const requestPayload = readPrivateSetupInput(requestPath, 1_048_576, environment);
  const request = JSON.parse(requestPayload.toString("utf8"));
  if (request === null || Array.isArray(request) || typeof request !== "object") {
    throw new Error("setup request is invalid");
  }
  const evidenceProfile = requestEvidenceProfile(request);
  const references = {};
  for (const field of evidenceProfile.inputFields) {
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
    schema: evidenceProfile.digestSchema,
    request_file_sha256: sha256(requestPayload),
    referenced_inputs: references,
    runtime_identity: runtimeIdentity(environment, serviceHiddenRoots),
  });
};
