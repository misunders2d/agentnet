/** Exact Pi local tools over direct process-bound Unix IPC. */

import { createHmac, randomBytes } from "node:crypto";
import { fstatSync, readSync } from "node:fs";
import { createConnection } from "node:net";
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type Binding = {
	capability: string;
	session_id: string;
	socket_path: string;
};

let cachedBinding: Binding | undefined;

function canonical(value: unknown): string {
	if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
	if (value !== null && typeof value === "object") {
		const record = value as Record<string, unknown>;
		return `{${Object.keys(record)
			.sort()
			.map((key) => `${JSON.stringify(key)}:${canonical(record[key])}`)
			.join(",")}}`;
	}
	return JSON.stringify(value);
}

function binding(): Binding {
	if (cachedBinding) return cachedBinding;
	const rendered = process.env.AGENTNET_LOCAL_BINDING_FD;
	if (!rendered || !/^[0-9]+$/.test(rendered)) throw new Error("AgentNet local binding is unavailable");
	const descriptor = Number(rendered);
	const size = fstatSync(descriptor).size;
	if (size < 2 || size > 65536) throw new Error("AgentNet local binding was not activated");
	const buffer = Buffer.alloc(size);
	if (readSync(descriptor, buffer, 0, size, 0) !== size) throw new Error("AgentNet local binding read failed");
	const value = JSON.parse(buffer.toString("utf8")) as Record<string, unknown>;
	const keys = Object.keys(value).sort();
	const expected = [
		"capability",
		"credential_epoch",
		"credential_id",
		"expires_at",
		"harness_id",
		"schema",
		"session_id",
		"socket_path",
	];
	if (
		canonical(value) !== buffer.toString("utf8") ||
		canonical(keys) !== canonical(expected) ||
		value.schema !== "agentnet.ipc.issued-child.v1" ||
		typeof value.capability !== "string" ||
		typeof value.session_id !== "string" ||
		typeof value.socket_path !== "string"
	) throw new Error("AgentNet local binding schema is invalid");
	cachedBinding = value as unknown as Binding;
	return cachedBinding;
}

function invoke(method: "agentnet.inbox" | "agentnet.send", args: Record<string, unknown>): Promise<unknown> {
	const local = binding();
	const nonce = randomBytes(24).toString("hex");
	const request = { arguments: args, method };
	const authenticated = { nonce, request, session_id: local.session_id };
	const authenticator = createHmac("sha256", Buffer.from(local.capability, "ascii"))
		.update(Buffer.concat([Buffer.from("AgentNet-IPC-FRAME\0"), Buffer.from(canonical(authenticated))]))
		.digest("base64url");
	const frame = Buffer.from(canonical({
		authenticator,
		capability: local.capability,
		nonce,
		request,
		session_id: local.session_id,
	}));
	const packet = Buffer.alloc(4 + frame.length);
	packet.writeUInt32BE(frame.length, 0);
	frame.copy(packet, 4);
	return new Promise((resolve, reject) => {
		const socket = createConnection(local.socket_path);
		let response = Buffer.alloc(0);
		socket.setTimeout(10000);
		socket.on("connect", () => socket.write(packet));
		socket.on("data", (chunk) => {
			response = Buffer.concat([response, chunk]);
			if (response.length < 4) return;
			const length = response.readUInt32BE(0);
			if (length < 2 || length > 1048576) return reject(new Error("AgentNet local response length rejected"));
			if (response.length < length + 4) return;
			const raw = response.subarray(4, length + 4).toString("utf8");
			const value = JSON.parse(raw) as Record<string, unknown>;
			socket.end();
			if (canonical(value) !== raw || value.ok !== true || !("result" in value)) {
				return reject(new Error("AgentNet local operation rejected"));
			}
			resolve(value.result);
		});
		socket.on("timeout", () => socket.destroy(new Error("AgentNet local operation timed out")));
		socket.on("error", reject);
	});
}

export default function (pi: ExtensionAPI) {
	pi.registerTool({
		name: "agentnet_inbox",
		label: "AgentNet inbox",
		description: "Read this exact enrolled harness mailbox.",
		parameters: Type.Object({
			after_cursor: Type.Optional(Type.Integer({ minimum: 0 })),
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 100 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.inbox", {
				after_cursor: params.after_cursor ?? 0,
				limit: params.limit ?? 25,
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_send",
		label: "AgentNet send",
		description: "Send an authorized corporate message as this exact enrolled harness.",
		parameters: Type.Object({
			recipients: Type.Array(Type.String(), { minItems: 1, maxItems: 1000 }),
			payload: Type.Record(Type.String(), Type.Unknown()),
			idempotency_key: Type.String({ minLength: 16, maxLength: 256 }),
			classification: Type.Optional(Type.Union([
				Type.Literal("C0"), Type.Literal("C1"), Type.Literal("C2"), Type.Literal("C3"),
			])),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.send", {
				recipients: params.recipients,
				payload: params.payload,
				idempotency_key: params.idempotency_key,
				classification: params.classification ?? "C1",
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
}

