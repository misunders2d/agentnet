/** Exact OMP/Pi local tools over a sealed process-bound IPC descriptor. */

import { createHmac, randomBytes } from "node:crypto";
import { closeSync, fstatSync, readSync } from "node:fs";
import { createConnection } from "node:net";
import { Type } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { AgentNetLocalOperationError, localResponseResult } from "./pi_response.ts";

type Binding = {
	capability: string;
	session_id: string;
	socket_path: string;
};

let cachedBinding: Binding | undefined;
let pendingBinding: Promise<Binding> | undefined;

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

function parseBinding(buffer: Buffer): Binding {
	const raw = buffer.toString("utf8");
	const value = JSON.parse(raw) as Record<string, unknown>;
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
		canonical(value) !== raw ||
		canonical(keys) !== canonical(expected) ||
		value.schema !== "agentnet.ipc.issued-child.v1" ||
		typeof value.capability !== "string" ||
		typeof value.session_id !== "string" ||
		typeof value.socket_path !== "string"
	) throw new Error("AgentNet local binding schema is invalid");
	return value as unknown as Binding;
}

function bindingFromDescriptor(rendered: string): Binding {
	if (!/^[0-9]+$/.test(rendered)) throw new Error("AgentNet local binding descriptor is invalid");
	const descriptor = Number(rendered);
	const metadata = fstatSync(descriptor);
	let buffer: Buffer;
	if (metadata.isFile()) {
		if (metadata.size < 2 || metadata.size > 65536) throw new Error("AgentNet local binding was not activated");
		buffer = Buffer.alloc(metadata.size);
		if (readSync(descriptor, buffer, 0, metadata.size, 0) !== metadata.size) {
			throw new Error("AgentNet local binding read failed");
		}
	} else if (metadata.isFIFO()) {
		const chunks: Buffer[] = [];
		let total = 0;
		for (;;) {
			const chunk = Buffer.alloc(16384);
			const read = readSync(descriptor, chunk, 0, chunk.length, null);
			if (read === 0) break;
			total += read;
			if (total > 65536) throw new Error("AgentNet local binding response is oversized");
			chunks.push(chunk.subarray(0, read));
		}
		if (total < 2) throw new Error("AgentNet local binding was not activated");
		buffer = Buffer.concat(chunks, total);
	} else {
		throw new Error("AgentNet local binding descriptor type is invalid");
	}
	return parseBinding(buffer);
}

function bindingFromEndpoint(endpoint: string): Promise<Binding> {
	if (!/^\\\\\.\\pipe\\agentnet-binding-[A-Za-z0-9_-]{24,}$/.test(endpoint)) {
		return Promise.reject(new Error("AgentNet local binding endpoint is invalid"));
	}
	const deadline = Date.now() + 10000;
	return new Promise((resolve, reject) => {
		const attempt = () => {
			const socket = createConnection(endpoint);
			let response = Buffer.alloc(0);
			let settled = false;
			const retry = (error: Error) => {
				if (settled) return;
				settled = true;
				socket.destroy();
				if (Date.now() >= deadline) reject(error);
				else setTimeout(attempt, 50);
			};
			socket.setTimeout(1000);
			socket.on("data", (chunk) => {
				response = Buffer.concat([response, chunk]);
				if (response.length < 4) return;
				const length = response.readUInt32BE(0);
				if (length < 2 || length > 65536) return retry(new Error("AgentNet binding response length rejected"));
				if (response.length < length + 4) return;
				try {
					const parsed = parseBinding(response.subarray(4, length + 4));
					settled = true;
					socket.end();
					resolve(parsed);
				} catch (error) {
					retry(error instanceof Error ? error : new Error("AgentNet binding response rejected"));
				}
			});
			socket.on("end", () => retry(new Error("AgentNet binding endpoint closed early")));
			socket.on("timeout", () => retry(new Error("AgentNet binding endpoint timed out")));
			socket.on("error", (error) => retry(error));
		};
		attempt();
	});
}

async function binding(): Promise<Binding> {
	if (cachedBinding) return cachedBinding;
	if (pendingBinding) return pendingBinding;
	const descriptor = process.env.AGENTNET_LOCAL_BINDING_FD;
	const endpoint = process.env.AGENTNET_LOCAL_BINDING_ENDPOINT;
	if (descriptor && endpoint) throw new Error("AgentNet local binding has conflicting locators");
	if (descriptor) {
		if (!/^[0-9]+$/.test(descriptor)) {
			throw new Error("AgentNet local binding descriptor is invalid");
		}
		delete process.env.AGENTNET_LOCAL_BINDING_FD;
		try {
			cachedBinding = bindingFromDescriptor(descriptor);
			return cachedBinding;
		} finally {
			closeSync(Number(descriptor));
		}
	}
	if (endpoint) {
		delete process.env.AGENTNET_LOCAL_BINDING_ENDPOINT;
		pendingBinding = bindingFromEndpoint(endpoint).then((value) => {
			cachedBinding = value;
			return value;
		});
		return pendingBinding;
	}
	throw new Error(
		"AgentNet local binding is unavailable: package installation alone does not activate it; " +
		"launch this measured Pi through agentnet supervisor-run with local_bindings_required=true",
	);
}

type CanonicalMethod =
	| "agentnet.inbox"
	| "agentnet.inbox.acknowledge"
	| "agentnet.recipient.resolve"
	| "agentnet.send"
	| "agentnet.file.send"
	| "agentnet.file.status"
	| "agentnet.file.download"
	| "agentnet.conversation.create"
	| "agentnet.conversation.action"
	| "agentnet.conversation.thread"
	| "agentnet.room.create"
	| "agentnet.room.member.add"
	| "agentnet.room.get"
	| "agentnet.room.send"
	| "agentnet.obligation.inbox"
	| "agentnet.obligation.list"
	| "agentnet.obligation.get"
	| "agentnet.obligation.transition"
	| "agentnet.obligation.cancel"
	| "agentnet.obligation.reconcile";

async function invoke(method: CanonicalMethod, args: Record<string, unknown>): Promise<unknown> {
	const local = await binding();
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
			const value: unknown = JSON.parse(raw);
			socket.end();
			if (canonical(value) !== raw) {
				return reject(new AgentNetLocalOperationError("local_operation_rejected"));
			}
			try {
				resolve(localResponseResult(value));
			} catch (error) {
				reject(error);
			}
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
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			after_cursor: Type.Optional(Type.Integer({ minimum: 0 })),
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 100 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.inbox", {
				collaboration_scope_id: params.collaboration_scope_id,
				after_cursor: params.after_cursor ?? 0,
				limit: params.limit ?? 25,
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_inbox_acknowledge",
		label: "AgentNet acknowledge inbox event",
		description: "Record durable custody for one exact mailbox event.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			event_id: Type.String({
				minLength: 1,
				maxLength: 256,
				pattern: "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
			}),
			envelope_digest: Type.String({ pattern: "^[a-f0-9]{64}$" }),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.inbox.acknowledge", params);
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_recipient_resolve",
		label: "AgentNet resolve recipient",
		description: "Resolve one human-readable recipient to an exact authorized enrolled endpoint.",
		parameters: Type.Object({
			query: Type.String({ minLength: 1, maxLength: 256 }),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.recipient.resolve", params);
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_send",
		label: "AgentNet send",
		description: "Send to either one friendly exact recipient or explicit exact harness IDs as this enrolled harness.",
		parameters: Type.Object({
			recipient_query: Type.Optional(Type.String({ minLength: 1, maxLength: 256 })),
			recipients: Type.Optional(Type.Array(Type.String({ minLength: 1, maxLength: 256 }), {
				minItems: 1,
				maxItems: 1000,
			})),
			payload: Type.Record(Type.String(), Type.Unknown()),
			idempotency_key: Type.String({ minLength: 16, maxLength: 256 }),
			classification: Type.Optional(Type.Union([
				Type.Literal("C0"), Type.Literal("C1"), Type.Literal("C2"), Type.Literal("C3"),
			])),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const arguments_: Record<string, unknown> = {
				payload: params.payload,
				idempotency_key: params.idempotency_key,
				classification: params.classification ?? "C1",
			};
			if (params.recipient_query !== undefined) arguments_.recipient_query = params.recipient_query;
			if (params.recipients !== undefined) arguments_.recipients = params.recipients;
			const result = await invoke("agentnet.send", arguments_);
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_file_send",
		label: "AgentNet send file",
		description: "Send one file to exact authorized enrolled endpoints from this sealed endpoint binding.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			recipients: Type.Array(Type.String(), { minItems: 1, maxItems: 1000 }),
			source_path: Type.String({ minLength: 1, maxLength: 4096 }),
			media_type: Type.String({ minLength: 3, maxLength: 255 }),
			classification: Type.Optional(Type.Union([
				Type.Literal("C0"), Type.Literal("C1"), Type.Literal("C2"), Type.Literal("C3"),
			])),
			idempotency_key: Type.String({ minLength: 16, maxLength: 256 }),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.file.send", {
				collaboration_scope_id: params.collaboration_scope_id,
				recipients: params.recipients,
				source_path: params.source_path,
				media_type: params.media_type,
				classification: params.classification ?? "C1",
				idempotency_key: params.idempotency_key,
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_file_status",
		label: "AgentNet file status",
		description: "Read the authorized durable state of one file transfer.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			transfer_id: Type.String({ minLength: 1, maxLength: 256 }),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.file.status", params);
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_file_download",
		label: "AgentNet download file",
		description: "Download one authorized released artifact through this exact endpoint binding.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			artifact_id: Type.String({ minLength: 1, maxLength: 256 }),
			destination_path: Type.String({ minLength: 1, maxLength: 4096 }),
			idempotency_key: Type.String({ minLength: 16, maxLength: 256 }),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.file.download", params);
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_conversation_create",
		label: "AgentNet create conversation",
		description: "Create an authorized corporate conversation for this harness.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			conversation_id: Type.String({ minLength: 1, maxLength: 256 }),
			member_harness_ids: Type.Array(Type.String(), { minItems: 1, maxItems: 1000 }),
			classification: Type.Optional(Type.Union([
				Type.Literal("C0"), Type.Literal("C1"), Type.Literal("C2"), Type.Literal("C3"),
			])),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.conversation.create", {
				collaboration_scope_id: params.collaboration_scope_id,
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
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
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
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			conversation_id: Type.String({ minLength: 1, maxLength: 256 }),
			thread_id: Type.String({ minLength: 1, maxLength: 256 }),
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 1000 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.conversation.thread", {
				collaboration_scope_id: params.collaboration_scope_id,
				conversation_id: params.conversation_id,
				thread_id: params.thread_id,
				limit: params.limit ?? 100,
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_room_create",
		label: "AgentNet create room",
		description: "Create an authorized room as this exact enrolled harness.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			classification: Type.Optional(Type.Union([
				Type.Literal("C0"), Type.Literal("C1"), Type.Literal("C2"), Type.Literal("C3"),
			])),
			persistent: Type.Optional(Type.Boolean()),
			expires_at: Type.Optional(Type.Union([
				Type.String({ format: "date-time" }),
				Type.Null(),
			])),
			policy: Type.Optional(Type.Union([
				Type.Record(Type.String(), Type.Unknown()),
				Type.Null(),
			])),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.room.create", {
				collaboration_scope_id: params.collaboration_scope_id,
				classification: params.classification ?? "C1",
				persistent: params.persistent ?? true,
				expires_at: params.expires_at ?? null,
				policy: params.policy ?? null,
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_room_member_add",
		label: "AgentNet add room member",
		description: "Add one ordinary member to an authorized room.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			room_id: Type.String({ minLength: 1, maxLength: 256 }),
			harness_id: Type.String({ minLength: 1, maxLength: 256 }),
			role: Type.Optional(Type.Union([
				Type.Literal("member"), Type.Literal("guest"), Type.Literal("moderator"),
			])),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.room.member.add", {
				collaboration_scope_id: params.collaboration_scope_id,
				room_id: params.room_id,
				harness_id: params.harness_id,
				role: params.role ?? "member",
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_room_get",
		label: "AgentNet room detail",
		description: "Describe one room visible to this exact enrolled harness.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			room_id: Type.String({ minLength: 1, maxLength: 256 }),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.room.get", params);
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_room_send",
		label: "AgentNet room send",
		description: "Send an artifact-free message to current members of an authorized room.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			room_id: Type.String({ minLength: 1, maxLength: 256 }),
			recipients: Type.Array(Type.String(), { minItems: 1, maxItems: 1000 }),
			payload: Type.Record(Type.String(), Type.Unknown()),
			idempotency_key: Type.String({ minLength: 16, maxLength: 256 }),
			expected_control_sequence: Type.Integer({ minimum: 1 }),
			classification: Type.Optional(Type.Union([
				Type.Literal("C0"), Type.Literal("C1"), Type.Literal("C2"), Type.Literal("C3"),
			])),
			conversation_id: Type.Optional(Type.Union([
				Type.String({ minLength: 1, maxLength: 256 }),
				Type.Null(),
			])),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.room.send", {
				collaboration_scope_id: params.collaboration_scope_id,
				room_id: params.room_id,
				recipients: params.recipients,
				payload: params.payload,
				idempotency_key: params.idempotency_key,
				expected_control_sequence: params.expected_control_sequence,
				classification: params.classification ?? "C1",
				conversation_id: params.conversation_id ?? null,
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_obligation_inbox",
		label: "AgentNet obligation inbox",
		description: "Read content-free response-obligation attention counters.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.obligation.inbox", params);
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
	pi.registerTool({
		name: "agentnet_obligation_list",
		label: "AgentNet obligation list",
		description: "List response obligations visible to this authenticated harness.",
		parameters: Type.Object({
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			role: Type.Optional(Type.Union([
				Type.Literal("requester"), Type.Literal("responsible"), Type.Literal("any"),
			])),
			states: Type.Optional(Type.Array(Type.String(), { maxItems: 10 })),
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 1000 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.obligation.list", {
				collaboration_scope_id: params.collaboration_scope_id,
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
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
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
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
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
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
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
			collaboration_scope_id: Type.String({ minLength: 1, maxLength: 256 }),
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 1000 })),
		}, { additionalProperties: false }),
		async execute(_id, params) {
			const result = await invoke("agentnet.obligation.reconcile", {
				collaboration_scope_id: params.collaboration_scope_id,
				limit: params.limit ?? 100,
			});
			return { content: [{ type: "text", text: canonical(result) }], details: result };
		},
	});
}
