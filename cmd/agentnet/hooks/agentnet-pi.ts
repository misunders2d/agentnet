// agentnet-pi-extension v1: installed by `agentnet hooks install pi`; `agentnet hooks remove pi` removes it.
//
// Tells this Pi session, as metadata only, what arrived in AgentNet since the
// session last checked: when the session starts, when a prompt starts, once
// at the end of a run (one extra model request, like Claude's Stop hook), and
// while Pi is idle (a visible notice kept in the session for the next turn;
// no model turn is started). Message text is never shown here, and nothing
// is approved, answered or run because of a notice.
import { spawn } from "node:child_process";
import * as fs from "node:fs";

const AGENTNET: string = "__AGENTNET_BIN__"; // filled in at install
const HOME: string = "__AGENTNET_HOME__";
const KIND = "agentnet";
const HOOK_TIMEOUT_MS = 10000; // one `agentnet hook pi` call
const MAX_OUTPUT = 64 * 1024;

type Ack = { pos?: number; has_pos?: boolean; release?: string };
type Reply = { text?: string; ack?: Ack };

const children = new Set<any>();

// hook runs `agentnet hook pi` with one event, bounded in time and output.
// A missing agent, an error, a timeout or no news all come back undefined.
function hook(input: Record<string, unknown>): Promise<Reply | undefined> {
	return new Promise((resolve) => {
		let out = "";
		let failed = false;
		let p: any;
		try {
			p = spawn(AGENTNET, ["--home", HOME, "hook", "pi"], { stdio: ["pipe", "pipe", "ignore"] });
		} catch {
			return resolve(undefined);
		}
		children.add(p);
		const stop = () => {
			failed = true;
			try {
				p.kill("SIGKILL");
			} catch {}
		};
		const timer = setTimeout(stop, HOOK_TIMEOUT_MS);
		p.stdout.on("data", (d: Buffer) => {
			out += d.toString();
			if (out.length > MAX_OUTPUT) {
				out = "";
				stop();
			}
		});
		p.on("error", () => {
			failed = true;
		});
		p.on("close", (code: number) => {
			clearTimeout(timer);
			children.delete(p);
			if (failed || code !== 0 || out.trim() === "") return resolve(undefined);
			try {
				resolve(JSON.parse(out));
			} catch {
				resolve(undefined);
			}
		});
		p.stdin.on("error", () => {});
		p.stdin.end(JSON.stringify(input));
	});
}

function stopChildren() {
	for (const p of children) {
		try {
			p.kill("SIGKILL");
		} catch {}
	}
	children.clear();
}

export default function (pi: any) {
	// Sessions AgentNet's own worker starts stay silent.
	if (process.env.AGENTNET_BACKGROUND === "1") return;

	let session = "";
	let generation = 0; // changes with every session start and shutdown
	let ctxRef: any;
	let continued = false; // the end-of-run notice already asked for one more request
	let watcher: fs.FSWatcher | undefined;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let lock: Promise<unknown> = Promise.resolve();
	let seq = 0;
	// Notices returned to Pi but not yet seen in the session, by token. Pi
	// adds a returned message or entry only after the handler has returned,
	// so each is acknowledged when it shows up in the session; until then later
	// checks list only newer arrivals (after), and an unconfirmed notice is
	// dropped when the run settles, so it is offered again.
	const pending = new Map<string, Ack>();

	const after = () => Math.max(0, ...[...pending.values()].map((a) => (a.has_pos ? a.pos ?? 0 : 0)));

	function acknowledge(sid: string, ack: Ack) {
		lock = lock.then(() => hook({ session_id: sid, hook_event_name: "Ack", ...ack })).catch(() => {});
	}

	function confirm(token: string) {
		const ack = pending.get(token);
		if (!ack) return;
		pending.delete(token);
		acknowledge(session, ack);
	}

	// Notices whose message or entry is now in the session are acknowledged;
	// with drop, the rest are forgotten so they are offered again.
	function reconcile(ctx: any, drop: boolean) {
		if (pending.size === 0) return;
		try {
			for (const e of (ctx?.sessionManager?.getBranch?.() ?? []).slice(-50)) {
				const token = e?.customType === KIND ? e?.details?.token : undefined;
				if (token) confirm(token);
			}
		} catch {}
		if (drop) pending.clear();
	}

	// One read at a time; show hands the text over and returns whether it was
	// taken (true) and how it is acknowledged: now (it is already in the
	// session) or later (pending until Pi adds it).
	function transaction(event: string, extra: Record<string, unknown>, show: (text: string, details: object) => "now" | "later" | false): Promise<void> {
		const gen = generation;
		const sid = session;
		const run = lock.then(async () => {
			if (!sid || gen !== generation) return;
			const r = await hook({ session_id: sid, hook_event_name: event, after: after(), ...extra });
			if (!r?.text || gen !== generation) return; // replaced or shut down meanwhile: not shown, not acknowledged
			const token = `${sid}:${++seq}`;
			const how = show(r.text, { token });
			if (!how || !r.ack) return;
			if (how === "now") await hook({ session_id: sid, hook_event_name: "Ack", ...r.ack });
			else pending.set(token, r.ack);
		});
		lock = run.catch(() => {});
		return run.catch(() => {});
	}

	function stopWatching() {
		if (timer) clearTimeout(timer);
		timer = undefined;
		watcher?.close();
		watcher = undefined;
	}

	// Idle arrivals: watch the AgentNet home for database writes (event-driven,
	// no polling) and, when Pi is idle, show what is new.
	function watch() {
		if (watcher || !ctxRef?.hasUI) return;
		try {
			watcher = fs.watch(HOME, (_ev, name) => {
				if (name && !String(name).startsWith("agent.db")) return;
				if (timer) clearTimeout(timer);
				timer = setTimeout(idle, 500);
			});
			watcher.on("error", stopWatching); // e.g. the home was removed; the next prompt tries again
		} catch {
			watcher = undefined; // no home yet
		}
	}

	// Pi adds a custom message sent while it is idle to the session before
	// sendMessage returns (no turn is started), so it is acknowledged at once.
	function note(text: string, details: object): "now" {
		pi.sendMessage({ customType: KIND, content: text, display: true, details }, { triggerTurn: false });
		return "now";
	}

	function idle() {
		timer = undefined;
		const ctx = ctxRef;
		if (!ctx || !ctx.isIdle()) return; // the run's own boundaries will tell it
		transaction("Idle", {}, (text, details) => {
			const lines = text.split("\n").filter((l) => l.startsWith("- ")).length;
			ctx.ui.notify(lines > 0 ? `AgentNet: ${lines} new` : "AgentNet: new activity", "info");
			return note(text, details);
		});
	}

	pi.on("session_start", async (_event: unknown, ctx: any) => {
		stopWatching();
		stopChildren();
		pending.clear();
		generation++;
		ctxRef = ctx;
		session = ctx.sessionManager.getSessionId();
		continued = false;
		watch();
		await transaction("SessionStart", {}, note);
	});

	pi.on("before_agent_start", async () => {
		continued = false;
		watch();
		let message: unknown;
		await transaction("UserPromptSubmit", {}, (text, details) => {
			message = { customType: KIND, content: text, display: true, details };
			return "later"; // acknowledged at message_end, once Pi has added it
		});
		return message ? { message } : undefined;
	});

	pi.on("message_end", async (event: any) => {
		const m = event?.message;
		if (m?.role === "custom" && m?.customType === KIND && m?.details?.token) confirm(m.details.token);
	});

	pi.on("agent_before_settle", async () => {
		let result: unknown;
		await transaction("Stop", { stop_hook_active: continued }, (text, details) => {
			continued = true;
			result = { entries: [{ type: "custom_message", customType: KIND, content: text, display: true, details }], continue: true };
			return "later"; // acknowledged when the entry is in the session (next turn or settle)
		});
		return result;
	});

	pi.on("turn_start", async (_event: unknown, ctx: any) => reconcile(ctx, false));
	pi.on("agent_settled", async (_event: unknown, ctx: any) => reconcile(ctx, true));

	pi.on("session_shutdown", async () => {
		generation++;
		stopWatching();
		stopChildren();
		pending.clear();
		ctxRef = undefined;
		session = "";
	});
}
