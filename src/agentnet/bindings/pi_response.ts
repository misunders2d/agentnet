const SAFE_ERROR_CODE = /^[a-z][a-z0-9_]{0,127}$/;

export class AgentNetLocalOperationError extends Error {
	readonly code: string;

	constructor(code: string) {
		super(`AgentNet local operation rejected: ${code}`);
		this.name = "AgentNetLocalOperationError";
		this.code = code;
	}
}

export function localResponseResult(value: unknown): unknown {
	if (typeof value !== "object" || value === null || Array.isArray(value)) {
		throw new AgentNetLocalOperationError("local_operation_rejected");
	}
	const response = value as Record<string, unknown>;
	const keys = Object.keys(response).sort();
	if (
		keys.length === 2
		&& keys[0] === "ok"
		&& keys[1] === "result"
		&& response.ok === true
	) {
		return response.result;
	}
	if (
		keys.length === 1
		&& keys[0] === "error"
		&& typeof response.error === "string"
		&& SAFE_ERROR_CODE.test(response.error)
	) {
		throw new AgentNetLocalOperationError(response.error);
	}
	throw new AgentNetLocalOperationError("local_operation_rejected");
}
