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
	if (!rendered || !/^[0-9]+$/.test(rendered)) {
		throw new Error(
			"AgentNet local binding is unavailable: package installation alone does not activate it; " +
			"launch this measured Pi through agentnet supervisor-run with local_bindings_required=true",
		);
	}
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

type CanonicalMethod =
	| "agentnet.inbox"
	| "agentnet.send"
	| "agentnet.conversation.create"
	| "agentnet.conversation.action"
	| "agentnet.conversation.thread"
	| "agentnet.obligation.inbox"
	| "agentnet.obligation.list"
	| "agentnet.obligation.get"
	| "agentnet.obligation.transition"
	| "agentnet.obligation.cancel"
	| "agentnet.obligation.reconcile";

function invoke(method: CanonicalMethod, args: Record<string, unknown>): Promise<unknown> {
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
	pi.registerTool({
		name: "agentnet_conversation_create",
		label: "AgentNet create conversation",
		description: "Create an authorized corporate conversation for this harness.",
		parameters: Type.Object({
			conversation_id: Type.String({ minLength: 1, maxLength: 256 }),
			member_harness_ids: Type.Array(Type.String(), { minItems: 1, maxItems: 1000 }),
			classification: Type.Optional(Type.Union([
				Type.Literal("C0"), Type.Literal("C1"), Type.Literal("C2"), Type.Literal("C3"),
			])),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.conversation.create", {
				conversation_id: params.conversation_id,
				member_harness_ids: params.member_harness_ids,
				classification: params.classification ?? "C1",
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_conversation_action",
		label: "AgentNet conversation action",
		description: "Post a typed action, request obligation, or bound obligation response.",
		parameters: Type.Object({
			recipients: Type.Array(Type.String(), { minItems: 1, maxItems: 1000 }),
			conversation_id: Type.String({ minLength: 1, maxLength: 256 }),
			thread_id: Type.String({ minLength: 1, maxLength: 256 }),
			action: Type.Record(Type.String(), Type.Unknown()),
			idempotency_key: Type.String({ minLength: 16, maxLength: 256 }),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.conversation.action", params);
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_conversation_thread",
		label: "AgentNet conversation thread",
		description: "Read one authorized corporate conversation thread.",
		parameters: Type.Object({
			conversation_id: Type.String({ minLength: 1, maxLength: 256 }),
			thread_id: Type.String({ minLength: 1, maxLength: 256 }),
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 1000 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.conversation.thread", {
				conversation_id: params.conversation_id,
				thread_id: params.thread_id,
				limit: params.limit ?? 100,
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_obligation_inbox",
		label: "AgentNet obligation inbox",
		description: "Read content-free response-obligation attention counters.",
		parameters: Type.Object({}, { additionalProperties: false }),
		async execute() {
			const result = await invoke("agentnet.obligation.inbox", {});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_obligation_list",
		label: "AgentNet obligation list",
		description: "List response obligations visible to this authenticated harness.",
		parameters: Type.Object({
			role: Type.Optional(Type.Union([
				Type.Literal("requester"), Type.Literal("responsible"), Type.Literal("any"),
			])),
			states: Type.Optional(Type.Array(Type.String(), { maxItems: 10 })),
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 1000 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.obligation.list", {
				role: params.role ?? "any",
				states: params.states ?? [],
				limit: params.limit ?? 100,
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_obligation_get",
		label: "AgentNet obligation detail",
		description: "Fetch one response obligation and its transition history.",
		parameters: Type.Object({
			obligation_id: Type.String({ minLength: 1, maxLength: 256 }),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.obligation.get", params);
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_obligation_transition",
		label: "AgentNet obligation progress",
		description: "Record responsible-recipient progress on an obligation.",
		parameters: Type.Object({
			obligation_id: Type.String({ minLength: 1, maxLength: 256 }),
			to_state: Type.Union([
				Type.Literal("recipient_committed"), Type.Literal("acknowledged"),
				Type.Literal("in_progress"), Type.Literal("pending_human"), Type.Literal("blocked"),
			]),
			reason: Type.Optional(Type.String({ minLength: 1, maxLength: 128 })),
			expected_revision: Type.Optional(Type.Integer({ minimum: 1 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.obligation.transition", {
				...params,
				reason: params.reason ?? "recipient_update",
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_obligation_cancel",
		label: "AgentNet obligation cancel",
		description: "Cancel an open obligation as its accountable requester.",
		parameters: Type.Object({
			obligation_id: Type.String({ minLength: 1, maxLength: 256 }),
			reason_code: Type.Optional(Type.String({ minLength: 1, maxLength: 128 })),
			expected_revision: Type.Optional(Type.Integer({ minimum: 1 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.obligation.cancel", {
				...params,
				reason_code: params.reason_code ?? "requester_canceled",
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_obligation_reconcile",
		label: "AgentNet obligation reconcile",
		description: "Reconcile durable obligation custody and deadlines.",
		parameters: Type.Object({
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 1000 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.obligation.reconcile", { limit: params.limit ?? 100 });
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
}
