// agentnet-pi-extension v1: installed by `agentnet hooks install pi`; `agentnet hooks remove pi` removes it.
//
// Tells this Pi session, as metadata only, what arrived in AgentNet since the
// session last checked: when the session starts, when a prompt starts, once
// at the end of a run (one extra model request, like Claude's Stop hook), and
// while Pi is idle (a visible notice that the next turn also sees; no model
// turn is started). Message text is never shown here, and nothing is
// approved, answered or run because of a notice.
import { spawn } from "node:child_process";
import * as fs from "node:fs";

const AGENTNET: string = "__AGENTNET_BIN__"; // filled in at install
const HOME: string = "__AGENTNET_HOME__";
const KIND = "agentnet";

type Reply = { text?: string; ack?: { pos?: number; has_pos?: boolean; release?: string } };

// hook runs `agentnet hook pi` with one event; a missing agent, an error or
// no news all come back as undefined.
function hook(input: Record<string, unknown>): Promise<Reply | undefined> {
	return new Promise((resolve) => {
		let out = "";
		let p;
		try {
			p = spawn(AGENTNET, ["--home", HOME, "hook", "pi"], { stdio: ["pipe", "pipe", "ignore"] });
		} catch {
			return resolve(undefined);
		}
		p.stdout.on("data", (d: Buffer) => (out += d.toString()));
		p.on("error", () => resolve(undefined));
		p.on("close", (code: number) => {
			if (code !== 0 || out.trim() === "") return resolve(undefined);
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

	// One read-show-acknowledge at a time, so two events never show the same
	// arrivals twice. show returns false when the text was not handed over.
	function transaction(event: string, extra: Record<string, unknown>, show: (text: string) => boolean): Promise<void> {
		const gen = generation;
		const sid = session;
		const run = lock.then(async () => {
			if (!sid || gen !== generation) return;
			const r = await hook({ session_id: sid, hook_event_name: event, ...extra });
			if (!r?.text || gen !== generation) return; // replaced or shut down meanwhile: not shown, not acknowledged
			if (!show(r.text)) return;
			if (r.ack) await hook({ session_id: sid, hook_event_name: "Ack", ...r.ack });
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

	function idle() {
		timer = undefined;
		const ctx = ctxRef;
		if (!ctx || !ctx.isIdle()) return; // the run's own boundaries will tell it
		transaction("Idle", {}, (text) => {
			const lines = text.split("\n").filter((l) => l.startsWith("- ")).length;
			ctx.ui.notify(lines > 0 ? `AgentNet: ${lines} new` : "AgentNet: new activity", "info");
			pi.sendMessage({ customType: KIND, content: text, display: true }, { triggerTurn: false, deliverAs: "nextTurn" });
			return true;
		});
	}

	pi.on("session_start", async (_event: unknown, ctx: any) => {
		stopWatching();
		generation++;
		ctxRef = ctx;
		session = ctx.sessionManager.getSessionId();
		continued = false;
		watch();
		// Shown in the transcript and seen by the next turn.
		await transaction("SessionStart", {}, (text) => {
			pi.sendMessage({ customType: KIND, content: text, display: true }, { triggerTurn: false, deliverAs: "nextTurn" });
			return true;
		});
	});

	pi.on("before_agent_start", async () => {
		continued = false;
		watch();
		let message: unknown;
		await transaction("UserPromptSubmit", {}, (text) => {
			message = { customType: KIND, content: text, display: true };
			return true;
		});
		return message ? { message } : undefined;
	});

	pi.on("agent_before_settle", async () => {
		let result: unknown;
		await transaction("Stop", { stop_hook_active: continued }, (text) => {
			continued = true;
			result = { entries: [{ type: "custom_message", customType: KIND, content: text, display: true }], continue: true };
			return true;
		});
		return result;
	});

	pi.on("session_shutdown", async () => {
		generation++;
		stopWatching();
		ctxRef = undefined;
		session = "";
	});
}
