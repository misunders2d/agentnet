import { saveBatch, acknowledge, recordFailure, type JournalEntry } from "../send-journal.mjs";
import { sendID } from "../optimistic.mjs";
import { pastePictures } from "../pictures.mjs";
// The message composer: one row of "+", the text and send. Typing @ offers
// the people and agents here; picking an agent asks it ("Zen should Answer |
// Do it") instead of writing to everyone. In a device conversation, replying
// to a request that waits for you answers it by hand (act reply: it takes the
// request over from your agent). Drafts are kept per conversation and only
// what was sent is cleared. Nothing typed here grants anything: asking goes
// through askAgent, and an owner's OK is never given from text.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ClipboardEvent, type FormEvent, type KeyboardEvent } from "react";
import { Popover } from "@base-ui/react/popover";
import { IconAlertCircle, IconAt, IconCloudOff, IconLock } from "@tabler/icons-react";
import { errorText, type T } from "../api";
import { useAgentNames, useApp, useWide } from "../context";
import { agentName, deviceTarget, deviceWho, firstLine, niceDevice, participants, personName, scopeMatches, threadAgentID, threadAuthor, whoName } from "../model";
import { useStore, type Draft, type StagedFile } from "../store";
import { EmojiPicker, useEmojiPreload } from "./Emoji";
import { DropTarget, FilesTray, bytes, draftFiles, overLimit, releaseFiles, useFileDrop } from "./Composer.files";
import { agentTargets, candidates, decode, encode, guestAuthor, matches, shift, trigger, type Candidate, type Span } from "./Composer.mentions";
import { Field, IntentRow, PlusMenu, ReplyChip, SendButton, menuIcons, type MenuAction } from "./Composer.parts";
import { MentionList, optionId } from "./Composer.picker";
import { useTeams } from "./Teams";
import { collectiveOptions } from "../collective-mentions.mjs";
import { useTypingSignal } from "./Composer.typing";
import { coarse, useFieldFocus } from "./Composer.focus";
import { usePortal } from "../owned";
import { TrustNotice } from "./Trust";

const EMPTY: Draft = { text: "" };

/** Who a send goes to: the conversation, an agent asked in it, the device of
 *  a device conversation (its agent asked; or, for a person's device that
 *  runs no agent, a plain message: ask false), nothing (your own device that
 *  runs no agent: only its requests are answered, by hand), or (answered by
 *  hand) a request received from it. model.deviceTarget decides which. */
type Target =
  | { kind: "conversation" }
  | { kind: "agent"; pid: string; name: string; seed: string; canAsk: boolean; why?: string }
  | { kind: "device"; name: string; seed: string; ask: boolean }
  | { kind: "none"; note: string }
  | { kind: "answer"; id: string; task: boolean };

export function Composer({ dm, thread }: { dm?: T.DMThread; thread?: T.Thread }) {
  const portal = usePortal();
  const store = useApp();
  const wide = useWide();
  const names = useAgentNames();
  const overview = useStore(store, (s) => s.overview);
  const conn = useStore(store, (s) => s.conn);
  const typingView = useStore(store, (s) => s.typing);
  const conv = dm?.id ?? thread?.id ?? "";
  const draft = useStore(store, (s) => s.drafts[conv]) ?? EMPTY;
  const lim = overview?.files;
  const words: Words = { overview, names };

  const form = useRef<HTMLFormElement>(null);
  const row = useRef<HTMLDivElement>(null);
  const field = useRef<HTMLTextAreaElement>(null);
  const mirror = useRef<HTMLDivElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const visible = useRef(conv);
  visible.current = conv;
  const caretNext = useRef<number | null>(null);
  const emojiAfterMenu = useRef(false);
  const androidPreparing = useRef(new Set<string>());
  const answering = useRef(new Set<string>());                        // requests this person chose to answer by hand

  const [busy, setBusy] = useState<Record<string, string>>({});      // conv → progress words while sending
  const [notice, setNotice] = useState<{ conv: string; text: string } | null>(null);
  const [said, setSaid] = useState("");                               // for screen readers
  const [caret, setCaret] = useState<number | null>(null);            // null while the field is not focused
  const [dismissed, setDismissed] = useState(-1);                     // the "@" whose picker was closed
  const [active, setActive] = useState(0);
  const [emojiOpen, setEmojiOpen] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [zone, setZone] = useState<HTMLElement | null>(null);
  useEmojiPreload(menuOpen);

  const { text, spans } = useMemo(() => decode(draft.text), [draft.text]);
  const write = (d: Draft, at?: number) => { if (at != null) caretNext.current = at; store.setDraft(conv, d); };
  // After a choice made around the text, the cursor goes back to it on a
  // desktop, and on a phone only when the person was typing (Composer.focus.ts).
  const { watch, focusField } = useFieldFocus(field);

  // ---- who is here and who this goes to
  const {view: teams} = useTeams(!!dm && caret !== null && !!trigger(text, caret));
  const people = useMemo(() => {
    const here = dm ? candidates(dm, overview, names, draft.newTopic ? "__new__" : draft.topic || "") : [];
    return [...collectiveOptions(here, teams), ...here];
  }, [dm, overview, names, teams, draft.topic, draft.newTopic]);
  const humanGuest = dm?.role === "human_guest";
  const visitor = dm?.role === "visitor";
  const author = dm ? guestAuthor(dm, draft.newTopic ? "__new__" : draft.topic || "") : undefined;

  // A received request this person may still answer by hand (its actions say so).
  const answerable = thread && draft.replyTo ? (thread.messages || []).find((m) => m.id === draft.replyTo && (m.actions || []).includes("reply")) : undefined;
  if (answerable) answering.current.add(answerable.id);
  // Chosen to answer by hand, but handled meanwhile (accepted, answered,
  // closed): sending now would ask the device something new instead.
  const handled = !!thread && !!draft.replyTo && !answerable && answering.current.has(draft.replyTo);

  const selectedPIDs = agentTargets(text, spans, draft.agent);
  const target: Target = useMemo(() => {
    if (thread) {
      if (answerable) return { kind: "answer", id: answerable.id, task: answerable.kind === "task" };
      const to = deviceTarget(thread.peer, overview); // a device that runs no agent cannot be asked
      if (threadAgentID(thread) && to.kind !== "agent") return { kind: "none", note: "The selected agent is unavailable on that computer." };
      return to.kind === "agent" ? { kind: "device", name: deviceAgent(thread.peer, overview, names, threadAgentID(thread)), seed: thread.peer, ask: true }
        : to.kind === "person" ? { kind: "device", name: to.name, seed: thread.peer, ask: false }
        : { kind: "none", note: to.note };
    }
    if (dm && selectedPIDs.length) {
      const a = (dm.agents || []).find((x) => x.pid === selectedPIDs[0]);
      const p = participants(dm, overview, names).find((x) => x.pid === selectedPIDs[0]);
      const name = p?.name || "This agent";
      return { kind: "agent", pid: selectedPIDs[0], name: selectedPIDs.length > 1 ? selectedPIDs.map(pid => participants(dm, overview, names).find(x => x.pid === pid)?.name || "Unavailable agent").join(", ") : name, seed: p?.seed || selectedPIDs[0], canAsk: selectedPIDs.some(pid => dm.agents?.some(x => x.pid === pid && x.can_ask && scopeMatches(x, draft.newTopic ? "__new__" : draft.topic || ""))), why: a ? scopeMatches(a, draft.newTopic ? "__new__" : draft.topic || "") ? a.state_text : "It is invited to a different topic." : "It is no longer in this chat." };
    }
    return { kind: "conversation" };
  }, [dm, thread, draft.agent, draft.text, draft.topic, draft.newTopic, answerable?.id, answerable?.kind, overview, names]);
  const latest = (): Draft => store.get().drafts[conv] ?? EMPTY;

  // Why nothing can be written here now, in words.
  const mine = humanGuest && !author ? (dm?.guests || []).find((g) => g.host_here && scopeMatches(g, draft.newTopic ? "__new__" : draft.topic || "")) : undefined;
  const closed: { title: string; detail?: string } | null =
    humanGuest && !author ? (mine?.state === "invited" ? { title: "Join to write here", detail: "You’re invited as a guest. Accept the invitation to write; your draft stays." }
      : mine && (mine.state === "dismissed" || mine.state === "left") ? { title: "You’re no longer a guest here", detail: "New messages don’t reach you. What was already shared stays with you." }
      : { title: "You can read this chat but not write in it", detail: dm?.frozen || "Choose the topic you were invited to." })
    : dm?.frozen ? { title: "Nothing more can be sent here", detail: dm.frozen }
    : thread?.key.pending ? { title: "Sending is paused", detail: "This agent’s identity changed. Check it before writing again." }
    : target.kind === "none" ? { title: "Nothing to ask here", detail: target.note + " To answer one of its requests yourself, choose Reply on it." }
    : null;

  const files = draft.files || [];
  const filesAllowed = !!lim && !closed && target.kind !== "answer" && (!visitor || (target.kind === "agent" && target.canAsk));
  const limit = !files.length ? "" : filesAllowed ? overLimit(files, lim)
    : target.kind === "answer" ? "Files can’t go with an answer. Remove them to send." : "Files can’t be sent here. Remove them to send.";
  const sending = store.host.platform === "android" && !!busy[conv]; // Android exposes pre-admission progress without a queued receipt
  const gone = target.kind === "agent" && !target.canAsk;
  const needsAgent = visitor && target.kind !== "agent";
  const ready = !closed && !sending && !gone && !needsAgent && !handled && !limit && (!!text.trim() || (files.length > 0 && target.kind !== "answer"));

  const typingScope = dm && !closed && !visitor && !(dm.kind === "group" && target.kind === "agent") ? { conv: dm.id } : null;
  const typing = useTypingSignal(store.api, typingScope, typingView);

  // ---- the @ picker
  const trig = dm && caret != null && !closed ? trigger(text, caret) : null;
  const open = !!trig && trig.start !== dismissed;
  const offered = useMemo(() => (open && trig ? matches(people, trig.query) : []), [open, trig?.query, people]);
  const listId = "mentions-" + conv.slice(0, 12);
  useEffect(() => setActive(0), [trig?.query, open]);

  // ---- keeping the field in shape: caret after programmatic edits, height, mirror scroll
  useLayoutEffect(() => {
    const ta = field.current;
    if (!ta) return;
    ta.style.height = "auto";
    ta.style.height = Math.min(ta.scrollHeight, wide ? 240 : 140) + "px";
    if (caretNext.current != null) {
      ta.setSelectionRange(caretNext.current, caretNext.current);
      setCaret(caretNext.current);
      caretNext.current = null;
    }
    if (mirror.current) mirror.current.scrollTop = ta.scrollTop;
  }, [text, wide, conv]);

  useLayoutEffect(() => { setZone(form.current?.parentElement ?? null); }, [conv, !!closed]);

  // On a wide screen, opening a conversation puts the cursor here (not on a
  // touch screen: that would open its keyboard).
  useEffect(() => { if (wide && conv && !coarse()) field.current?.focus({ preventScroll: true }); }, [conv]);

  // A reply chosen here (from a message, an approval card) puts the cursor
  // here, on a phone too. Chosen while an agent is addressed, it goes to the
  // conversation: the agent is let go.
  const lastReply = useRef({ conv, replyTo: draft.replyTo });
  useEffect(() => {
    const prev = lastReply.current;
    lastReply.current = { conv, replyTo: draft.replyTo };
    if (prev.conv !== conv || !draft.replyTo || draft.replyTo === prev.replyTo) return;
    if (draft.agent && dm) stopAsking();
    focusField(true);
  }, [conv, draft.replyTo]);

  // ---- editing
  const announce = (s: string) => setSaid(s);

  function edit(cur: string, at?: number) {
    const d = latest();
    const was = decode(d.text);
    const { kept, dropped } = shift(was.spans, was.text, cur);
    const next: Draft = { ...d, text: encode(cur, kept) };
    if (dropped.some((s) => s.kind === "agent" && s.id === d.agent)) {
      next.agent = agentTargets(cur, kept)[0];
      announce(next.agent ? "That mention was removed; the remaining selected agents will still be asked." : "No agents are selected; this goes to everyone here.");
    }
    const plain = dropped.filter((s) => s.kind !== "agent");
    if (plain.length) announce(plain.map((s) => "@" + s.name).join(", ") + (plain.length > 1 ? " are" : " is") + " no longer a mention; the text stays.");
    write(next, at);
  }

  function pick(c: Candidate) {
    if (!trig || caret == null) return;
    const d = latest();
    const selected = (c.targets || [c]).filter((p): p is Candidate & {kind: Span["kind"]} => p.kind !== "collective")
      .filter(p => c.kind !== "collective" || !spans.some(s => s.kind === p.kind && s.id === p.id));
    const token = selected.length ? selected.map(p => "@" + p.name).join(" ") + " " : "";
    const cur = text.slice(0, trig.start) + token + text.slice(caret);
    let { kept } = shift(spans, text, cur);
    // Keep every exact selected agent mention; target extraction deduplicates PIDs.
    let start = trig.start;
    const added: Span[] = selected.map(p => {const s = {start, name:p.name,kind:p.kind,id:p.id}; start += p.name.length+2; return s;});
    const agent = selected.find(p => p.kind === "agent");
    write({
      ...d, text: encode(cur, [...kept, ...added]),
      ...(agent ? { agent: agent.id, replyTo: undefined } : {}),
    }, trig.start + token.length);
    if (c.kind === "collective") announce(selected.length ? "@"+c.name+" expanded to "+selected.length+" recipients. Review their names before sending." : "Everyone from @"+c.name+" is already selected.");
    else if (agent) announce("Asking " + c.name + ".");
  }

  function stopAsking() {
    const d = latest();
    const was = decode(d.text);
    write({ ...d, text: encode(was.text, was.spans.filter((s) => s.kind !== "agent")), agent: undefined });
  }


  function insert(s: string) {
    const ta = field.current;
    const from = ta ? ta.selectionStart : text.length, to = ta ? ta.selectionEnd : text.length;
    edit(text.slice(0, from) + s + text.slice(to), from + s.length);
  }

  // ---- files
  function addFiles(list: File[], pasted = false) {
    if (!filesAllowed) { setNotice({ conv, text: closed ? closed.title + "." : target.kind === "answer" ? "Files can’t go with an answer." : "Files can’t be sent here." }); return; }
    const add = draftFiles(list, pasted);
    if (!add.length) return;
    setNotice(null);
    const d = latest();
    write({ ...d, files: [...(d.files || []), ...add] });
  }
  const dropped = useCallback((list: File[]) => addFiles(list), [conv, filesAllowed, closed?.title]);
  const dragRect = useFileDrop(zone, filesAllowed && !sending, dropped);

  function removeFile(key: string) {
    const d = latest();
    const f = (d.files || []).find((x) => x.key === key);
    if (!f) return;
    releaseFiles([f]);
    if (typeof f.staged === "string") store.api.discard([f.staged]).catch(() => {}); // AgentNet also drops it on its own within the hour
    write({ ...d, files: (d.files || []).filter((x) => x.key !== key) });
    setNotice(null);
  }

  const paste = (e: ClipboardEvent) => {
    if (!filesAllowed || sending) return;
    const ta = field.current;
    const host = store.host;
    const here = () => visible.current === conv && field.current === ta;
    void pastePictures(e, (list) => { if (here()) addFiles(list, true); }, {
      clipboardImage: host.clipboardImage ? () => host.clipboardImage!() : undefined,
      insertText: (s) => {
        if (!here() || !ta) return;
        const value = decode(latest().text).text;
        const from = ta.selectionStart, to = ta.selectionEnd;
        edit(value.slice(0, from) + s + value.slice(to), from + s.length);
      },
      error: (text) => { if (here()) setNotice({ conv, text }); },
    });
  };

  // ---- sending
  async function send(retry?: { id: string; c: string; d: Draft; to: Target; here?: T.DMThread; device?: T.Thread; fanout?: boolean; group?: string }) {
    if (store.host.platform === "android") { if (!retry) await sendAndroid(); return; }
    if (!retry && (!ready || (!dm && !thread))) return;
    if (!retry && dm) {
      const captured = latest(), decoded = decode(captured.text);
      const pids = agentTargets(decoded.text, decoded.spans, captured.agent);
      if (pids.length > 1) {
        const topic = captured.newTopic ? sendID() : captured.topic, group = sendID();
        clearSent(conv, captured, captured.files || []);
        if (captured.newTopic) store.setDraft(conv, { ...store.draft(conv), newTopic: false, topic });
        // Each target owns a stable request ID, preview, staged files and retry.
        const plans = pids.map((pid, index) => {
          const a = dm.agents?.find(x => x.pid === pid);
          const p = participants(dm, overview, names).find(x => x.pid === pid);
          const to: Target = {kind:"agent",pid,name:p?.name || "Unavailable agent",seed:p?.seed || pid,canAsk:!!a?.can_ask && scopeMatches(a, captured.newTopic ? "__new__" : captured.topic || ""),why:a?.state_text};
          const d = {...captured, agent:pid, topic, newTopic:false, files:(captured.files || []).map(f => ({...f,staged:index === 0 ? f.staged : undefined,url:undefined}))};
          return {id:sendID(),c:conv,d,to,here:dm,fanout:true,group};
        });
        await Promise.all(plans.map(plan => send(plan)));
        releaseFiles(captured.files || []);
        return;
      }
    }
    const { c, d: captured, to, here, device } = retry || { c: conv, d: latest(), to: target, here: dm, device: thread };
    // Allocate the optional topic before yielding so a second send keeps it.
    const d = here && captured.newTopic ? { ...captured, topic: captured.topic && retry ? captured.topic : sendID() } : captured;
    const id = retry?.id || sendID(), group = retry?.group;
    const was = decode(d.text);
    const body = encode(was.text, was.spans, true).trim();
    const sent = d.files || [];
    const progress = (s: string) => setBusy((b) => ({ ...b, [c]: s }));
    const title = here ? (here.kind === "group" ? here.title || "the group" : personName(here.peer)) : to.kind === "device" ? to.name : "that chat";
    if (to.kind === "none") return;
    if (store.isActive()) {
      clearSent(c, d, sent);
      if (d.newTopic && !retry) store.setDraft(c, { ...store.draft(c), newTopic: false, ...(here ? { topic: d.topic } : {}) });
    }
    store.sends.begin(c, { id, ...(here ? { lid: id, origin: "ui", send_group: group, send_group_author: store.get().overview?.me.fingerprint, pid: to.kind === "agent" ? to.pid : "" } : { author: { label: "You", about: "" }, to: device?.peer }),
      _topic: !!d.newTopic, topic: d.topic, dir: "out", from: store.get().overview?.me.address || "", kind: to.kind === "answer" ? "answer" : here && to.kind !== "agent" || to.kind === "device" && !to.ask ? "message" : "question", body, at: new Date().toISOString(),
      reply_to: d.replyTo, quote: d.replyTo, attachments: sent.map(f => ({ name: f.name, size: f.size, openable: false })), files: sent.map(f => ({ name: f.name, size: f.size, openable: false })) },
      () => { store.sends.remove(id); void send({ id, c, d, to, here, device, fanout:retry?.fanout, group }); });
    setNotice(null);
    typing.stop();
    try {
      await store.sends.ready(id); // previous turn reaches durable storage before this one is prepared
      // Each file is handed to this computer's AgentNet once; a retry after a
      // failure hands over only the rest. (In a browser device the engine
      // takes the file itself in place of an id.)
      const ids: unknown[] = [];
      for (const [i, f] of sent.entries()) {
        if (f.staged) { ids.push(f.staged); continue; }
        progress(sent.length > 1 ? "Preparing file " + (i + 1) + " of " + sent.length + "…" : "Preparing the file…");
        const id = await store.api.stage(f.file);
        f.staged = id;
        ids.push(id);
        if (typeof id === "string") keepStaged(c, f.key, id);
      }
      progress(sent.length ? "Sending…" : "");
      const fileIds = ids.length ? (ids as string[]) : undefined;
      const reply = d.replyTo && messageOf(d.replyTo, words, here, device) ? d.replyTo : undefined;
      let r: T.Sent | undefined;
      try {
        if (to.kind === "answer") announce((await store.api.act({ do: "reply", id: to.id, send_id: id, body })).note || "Answer sent.");
        else if (to.kind === "agent") r = await store.api.askAgent({ id, send_group: group, pid: to.pid, kind: "question", body, topic:d.topic, files: fileIds });
        else if (device) {
          const last = (device.messages || []).at(-1);
          // A new topic starts a separate conversation with this agent; otherwise the thread continues.
          r = await store.api.send({ id, to: device.peer, agent_id: to.kind === "device" && to.ask ? threadAgentID(device) : undefined, kind: to.kind === "device" && !to.ask ? "message" : "question", body, reply_to: d.newTopic ? undefined : last?.id, quote:reply, files: fileIds });
          // Keep the preview in the visible conversation throughout saving;
          // once the new topic exists its loaded view takes over the same id.
          if (d.newTopic) store.sends.move(id, id);
          else if ((last as (T.Message & { _topic?: boolean }) | undefined)?._topic) store.sends.move(id, last!.id);
          if (d.newTopic && r && store.isActive()) { store.setDraft(c, { ...(store.get().drafts[c] ?? EMPTY), newTopic: false }); void store.open({ kind: "thread", id: r.id }); }
        } else r = await store.api.sendDM({ id, conv: c, topic:d.topic,body, reply_to: reply, quote:reply, files: fileIds, ...(here && guestAuthor(here, d.topic || "") ? { pid: guestAuthor(here, d.topic || "")!.pid } : {}) });
      } finally {
        for (const f of sent) f.staged = undefined;
        forgetStaged(c, sent); // a send takes the files it names, sent or refused
      }
      store.sends.finish(id, r);
      releaseFiles(sent);

      if (!r) { /* answered: the request's own card shows what happened */ }
      else if (r.state === "queued") store.toast("Sending…");
      else if (r.state === "waiting") store.toast("Kept here, not sent yet: " + (r.detail || "they can’t receive messages right now."));
      else if (r.state === "receiver_waiting" && r.detail) store.toast(r.detail);
      void store.refetch();
    } catch (e) {
      if (!store.sends.has(id)) return; // a pushed durable turn already proves this save
      if (!store.isActive()) { store.sends.fail(id, errorText(e)); return; }
      const current = store.get().drafts[c] ?? EMPTY;
      const restored = !retry?.fanout && !current.text && !(current.files || []).length;
      if (restored) { store.setDraft(c, { ...d, files: sent }); store.sends.remove(id); }
      else store.sends.fail(id, errorText(e));
      const why = errorText(e).replace(/\.?$/, ".");
      if (visible.current === c) setNotice({ conv: c, text: "Not sent: " + why + (restored ? " Your text and files are restored." : " Your newer draft is kept. Retry the failed message above.") });
      else store.toast("Not sent to " + title + ": " + why + " It’s kept in that chat.", "error");
    } finally {
      setBusy(({ [c]: _, ...rest }) => rest);
    }
  }

  // Android freezes the complete native request journal before clearing any
  // draft. Recovery retries these payloads explicitly; it never recreates IDs.
  async function sendAndroid() {
    if (!ready || (!dm && !thread) || androidPreparing.current.has(conv)) return;
    const c = conv, captured = latest(), here = dm, device = thread;
    const host = store.host, workspace = host.workspace.id;
    const decoded = decode(captured.text), body = encode(decoded.text, decoded.spans, true).trim();
    const pids = here ? agentTargets(decoded.text, decoded.spans, captured.agent) : [];
    const topic = here && captured.newTopic ? sendID() : captured.topic;
    const group = pids.length > 1 ? sendID() : undefined;
    const destinations: Target[] = pids.length > 1 ? pids.map(pid => {
      const a = here!.agents?.find(x => x.pid === pid);
      const person = participants(here!, overview, names).find(x => x.pid === pid);
      return {kind:"agent",pid,name:person?.name || "Unavailable agent",seed:person?.seed || pid,
        canAsk:!!a?.can_ask && scopeMatches(a, captured.newTopic ? "__new__" : captured.topic || ""),why:a?.state_text};
    }) : [target];
    if (destinations.every(to => to.kind === "none")) return;
    const sent = captured.files || [];
    const plans: {entry: JournalEntry; to: Target}[] = [];
    androidPreparing.current.add(c);
    setBusy(b=>({...b,[c]:"Preparing send…"}));
    try {
      for (const [index, to] of destinations.entries()) {
        const id = sendID(), fileIDs: string[] = [];
        for (const f of sent) {
          setBusy(b => ({...b,[c]:"Preparing files…"}));
          const staged = index === 0 && typeof f.staged === "string" ? f.staged : await store.api.stage(f.file);
          if (typeof staged !== "string") throw new Error("The native file stage is unavailable.");
          if (index === 0) {f.staged=staged;keepStaged(c,f.key,staged);}
          fileIDs.push(staged);
        }
        const files = fileIDs.length ? fileIDs : undefined;
        const reply = captured.replyTo && messageOf(captured.replyTo, words, here, device) ? captured.replyTo : undefined;
        let endpoint: JournalEntry["endpoint"], request: Record<string,unknown>;
        if (to.kind === "answer") {endpoint="/api/act";request={do:"reply",id:to.id,send_id:id,body};}
        else if (to.kind === "agent") {endpoint="/api/dm/agent/ask";request={id,send_group:group,pid:to.pid,kind:"question",body,topic,files};}
        else if (device) {const last=(device.messages || []).at(-1);endpoint="/api/send";request={id,to:device.peer,
          agent_id:to.kind === "device" && to.ask ? threadAgentID(device) : undefined,
          kind:to.kind === "device" && !to.ask ? "message" : "question",body,
          reply_to:captured.newTopic ? undefined : last?.id,quote:reply,files};}
        else {endpoint="/api/dm/send";request={id,conv:c,topic,body,reply_to:reply,quote:reply,files,
          ...(here && guestAuthor(here, topic || "") ? {pid:guestAuthor(here, topic || "")!.pid} : {})};}
        // JSON removes optional undefined fields and captures no File objects.
        plans.push({to,entry:{id,conversation:c,endpoint,request:JSON.parse(JSON.stringify(request)),createdAt:new Date().toISOString()}});
      }
      saveBatch(localStorage,workspace,plans.map(p=>p.entry));
    } catch(e) {
      setNotice({conv:c,text:"Send was not submitted. Your draft is kept: "+errorText(e)});
      setBusy(({[c]:_,...rest})=>rest);androidPreparing.current.delete(c);return;
    }
    if (store.isActive()) {
      clearSent(c,captured,sent);
      if (captured.newTopic) store.setDraft(c,{...store.draft(c),newTopic:false,...(here?{topic}:{})});
    }
    setBusy(b=>({...b,[c]:"Submitting…"}));setNotice(null);typing.stop();
    const preview = (entry: JournalEntry, to: Target) => {
      store.sends.begin(c,{id:entry.id,...(here?{lid:entry.id,origin:"ui",send_group:group,
        send_group_author:overview?.me.fingerprint,pid:to.kind === "agent" ? to.pid : ""}:{author:{label:"You",about:""},to:device?.peer}),
        _topic:!!captured.newTopic,topic,dir:"out",from:overview?.me.address || "",
        kind:to.kind === "answer" ? "answer" : to.kind === "agent" || (to.kind === "device" && to.ask) ? "question" : "message",
        body,at:entry.createdAt,reply_to:captured.replyTo,quote:captured.replyTo,
        attachments:sent.map(f=>({name:f.name,size:f.size,openable:false})),files:sent.map(f=>({name:f.name,size:f.size,openable:false}))},
        undefined);
    };
    const submit = async (entry: JournalEntry): Promise<void> => {
      let result: T.Sent & {note?:string};
      try {
        // Busy and the per-conversation preparation guard serialize this
        // composer's gestures. There is no ledger row before admission.
        result = await host.api<T.Sent & {note?:string}>(entry.endpoint,entry.request);
      } catch(e) {
        const why=errorText(e);
        try {recordFailure(localStorage,workspace,entry.id,why);} catch(_) { /* original exact request stays retained */ }
        store.toast("Send needs attention. Its exact request is kept for explicit retry: "+why,"error");
        return;
      }
      // The successful native result proves admission. Presentation/storage
      // failures after this point must never relabel the request as failed.
      try {acknowledge(localStorage,workspace,entry.id);} catch(e) {
        store.toast("Saved by AgentNet, but the recovery record could not be cleared: "+errorText(e),"error");
      }
      try {
        preview(entry,plans.find(p=>p.entry.id===entry.id)!.to);
        if (entry.endpoint === "/api/act") {store.sends.finish(entry.id,undefined);announce(result.note || "Answer saved.");}
        else {store.sends.finish(entry.id,result);
          if (device && captured.newTopic && store.isActive()) void store.open({kind:"thread",id:result.id}).catch(e=>store.toast("Saved by AgentNet; could not open the conversation: "+errorText(e),"error"));
        }
        void store.refetch();
      } catch(e) {
        store.toast("Saved by AgentNet; could not update the view: "+errorText(e),"error");
      }
    };
    await Promise.all(plans.map(p=>submit(p.entry)));
    // Each request retains its original stage IDs even after failure. A missing
    // stage is a visible retry refusal, never a newly staged replacement intent.
    for (const f of sent) f.staged=undefined;
    forgetStaged(c,sent);releaseFiles(sent);androidPreparing.current.delete(c);setBusy(({[c]:_,...rest})=>rest);
  }

  function keepStaged(c: string, key: string, id: string) {
    const d = store.get().drafts[c];
    if (d?.files) store.setDraft(c, { ...d, files: d.files.map((f) => (f.key === key ? { ...f, staged: id } : f)) });
  }
  function forgetStaged(c: string, list: StagedFile[]) {
    if (!store.isActive()) return;
    const d = store.get().drafts[c], keys = new Set(list.map((f) => f.key));
    if (d?.files) store.setDraft(c, { ...d, files: d.files.map((f) => (keys.has(f.key) ? { ...f, staged: undefined } : f)) });
  }
  // clearSent removes from a conversation's draft exactly what was sent:
  // text typed meanwhile, or files added meanwhile, stay.
  function clearSent(c: string, d: Draft, list: StagedFile[]) {
    const cur = store.get().drafts[c] ?? EMPTY, keys = new Set(list.map((f) => f.key));
    const next: Draft = { ...cur, files: (cur.files || []).filter((f) => !keys.has(f.key)) };
    if (cur.text === d.text) {
      next.text = "";
      if (cur.agent === d.agent) next.agent = undefined;
    }
    if (cur.replyTo === d.replyTo) next.replyTo = undefined;
    store.setDraft(c, next);

  }

  // ---- keys
  function keys(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (open) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); setDismissed(trig!.start); return; }
      if (offered.length && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
        e.preventDefault();
        setActive((i) => (i + (e.key === "ArrowDown" ? 1 : -1) + offered.length) % offered.length);
        return;
      }
      if (offered.length && ((e.key === "Enter" && !e.ctrlKey && !e.metaKey && !e.shiftKey) || e.key === "Tab")) {
        e.preventDefault();
        pick(offered[Math.min(active, offered.length - 1)]);
        return;
      }
    }
    // Backspace right after a mention takes the whole mention away.
    if (e.key === "Backspace" && !e.altKey && !e.ctrlKey && !e.metaKey && e.currentTarget.selectionStart === e.currentTarget.selectionEnd) {
      const at = e.currentTarget.selectionStart, s = spans.find((x) => x.start + x.name.length + 1 === at);
      if (s) { e.preventDefault(); edit(text.slice(0, s.start) + text.slice(at), s.start); return; }
    }
    if (e.key === "Enter" && !e.nativeEvent.isComposing) {
      if (e.ctrlKey || e.metaKey || (wide && !e.shiftKey && !e.altKey)) { e.preventDefault(); void send(); }
    }
  }

  if (!dm && !thread) return null;

  // ---- what the composer says
  const replyMsg = draft.replyTo && target.kind !== "agent" ? messageOf(draft.replyTo, words, dm, thread) : undefined;
  const placeholder = target.kind === "answer" ? (target.task ? "Write your reply" : "Write your answer")
    : target.kind === "device" && !target.ask ? "Message " + target.name
    : target.kind === "agent" || target.kind === "device" ? "Ask " + target.name
    : visitor ? "Type @ to ask an agent"
    : humanGuest ? "Message everyone here"
    : dm?.kind === "group" ? "Message " + (dm.title || "the group")
    : "Message " + personName(dm?.peer);
  const status: Status | null = notice?.conv === conv ? { text: notice.text, kind: "error" }
    : handled ? { text: "This request was handled meanwhile, so it can’t be answered here now. Cancel the reply to send something new.", kind: "error" }
    : limit ? { text: limit, kind: "error" }
    : sending && busy[conv] ? { text: busy[conv], kind: "progress" }
    : needsAgent ? { text: "You’re here because of your agent. Type @ to ask it something.", kind: "info" }
    : conn === "lost" || conn === "gone" ? { text: "You’re offline. What you send waits here and goes when you’re back online.", kind: "offline" }
    : null;
  const sendLabel = target.kind === "conversation" || target.kind === "none" || (target.kind === "device" && !target.ask) ? "Send"
    : target.kind === "answer" ? (target.task ? "Send your reply" : "Send your answer")
    : "Ask " + target.name;
  const actions: MenuAction[] = [
    ...(dm && !humanGuest && !visitor ? [{ label: "Bring someone in", sub: "They see only what you share", icon: menuIcons.invite, tone: "bg-guest-bg text-guest-ink", run: () => store.openInvite(dm.id) }] : []),
    { label: "Photo or file", sub: filesAllowed && lim ? "Up to " + lim.max_count + " at once, " + bytes(lim.max_file) + " each" : target.kind === "answer" ? "Files can’t go with an answer" : "Files can’t be sent here", icon: menuIcons.file, tone: "bg-agent text-agent-ink", disabled: !filesAllowed, run: () => picker.current?.click() },
    { label: "Emoji", icon: menuIcons.emoji, tone: "bg-act text-act-ink", run: () => { emojiAfterMenu.current = true; setEmojiOpen(true); } }, // opens as the menu leaves: no gap between them
  ];

  // A changed identity: checked and trusted from here (on a computer).
  if (closed && thread?.key.pending && !dm) return <TrustNotice thread={thread} />;
  if (closed) return (
    <div className="flex items-start gap-3 border-t-[1.5px] border-outline bg-surface px-4 pt-3 pb-[max(12px,env(safe-area-inset-bottom))] lg:border-t lg:px-6 lg:py-4">
      <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-sunken text-muted"><IconLock size={20} aria-hidden="true" /></span>
      <div className="min-w-0 py-0.5">
        <p className="font-semibold">{closed.title}</p>
        {closed.detail && <p className="text-[13px] text-text-2">{closed.detail}</p>}
      </div>
    </div>
  );

  return (
    <form ref={form} onSubmit={(e: FormEvent) => { e.preventDefault(); void send(); }} {...watch} aria-label="Write a message" data-conv={conv}
      className="relative border-t-[1.5px] border-outline bg-surface px-3 pt-2 pb-[max(10px,env(safe-area-inset-bottom))] lg:border-t lg:px-6 lg:pt-3 lg:pb-4">
      {open && <MentionList id={listId} items={offered} active={active} query={trig?.query || ""} onPick={pick} onHover={setActive} />}
      <p className="sr-only" aria-live="polite">{said}</p>

      {replyMsg && (target.kind === "answer"
        ? <ReplyChip title={"Answering " + replyMsg.who} text={replyMsg.text} cancel="Don’t answer it here"
            note={target.task ? "You reply yourself; your agent won’t run this task." : "You answer this yourself; your agent won’t."}
            onCancel={() => { write({ ...latest(), replyTo: undefined }); focusField(); }} />
        : <ReplyChip title={"Replying to " + replyMsg.who} text={replyMsg.text} cancel="Cancel reply"
            onCancel={() => { write({ ...latest(), replyTo: undefined }); focusField(); }} />)}
      {(thread || dm) && draft.newTopic && !replyMsg && (
        <ReplyChip title={"New topic" + ("name" in target ? " with " + target.name : "")} text="Your next message starts a separate conversation." cancel="Keep this conversation"
          onCancel={() => { write({ ...latest(), newTopic: false }); focusField(); }} />
      )}
      <FilesTray files={files} lim={lim} busy={sending} onRemove={removeFile} />
      {(target.kind === "agent" || (target.kind === "device" && target.ask)) && !handled && (
        <IntentRow name={target.name} seed={target.seed}
          onStop={target.kind === "agent" ? () => { stopAsking(); focusField(); } : undefined}
          note={gone && target.kind === "agent" ? target.name + " can’t be asked now" + (target.why ? " (" + target.why.replace(/\.$/, "") + ")" : "") + ". Remove the mention to write to everyone instead."
            : undefined} />
      )}
      {status && <StatusLine status={status} />}

      <div ref={row} className="flex items-end gap-2">
        <PlusMenu actions={actions} disabled={sending} onOpenChange={setMenuOpen}
          finalFocus={() => !emojiAfterMenu.current} onClosed={() => { emojiAfterMenu.current = false; }} />
        <Field ref={field} mirror={mirror} wide={wide} text={text} spans={spans} value={text} placeholder={placeholder} aria-label={placeholder}
          enterKeyHint={wide ? "send" : "enter"} autoComplete="off" spellCheck
          aria-autocomplete="list" aria-controls={open && offered.length ? listId : undefined}
          aria-activedescendant={open && offered.length ? optionId(listId, Math.min(active, offered.length - 1)) : undefined}
          onChange={(e) => { edit(e.target.value); setCaret(e.target.selectionStart); setDismissed(-1); typing.typed(!!e.target.value.trim()); }}
          onSelect={(e) => setCaret(e.currentTarget.selectionStart === e.currentTarget.selectionEnd ? e.currentTarget.selectionStart : null)}
          onBlur={() => { setCaret(null); typing.stop(); }}
          onScroll={(e) => { if (mirror.current) mirror.current.scrollTop = e.currentTarget.scrollTop; }}
          onKeyDown={keys} onPaste={paste} />
        <SendButton ready={ready} busy={sending} label={sendLabel} />
      </div>

      <input ref={picker} type="file" multiple hidden tabIndex={-1} onChange={(e) => { addFiles([...(e.target.files || [])]); e.target.value = ""; }} />
      {dragRect && <DropTarget rect={dragRect} lim={lim} />}

      <Popover.Root open={emojiOpen} onOpenChange={setEmojiOpen}>
        <Popover.Portal container={portal}>
          <Popover.Positioner anchor={row} side="top" align="start" sideOffset={8} collisionPadding={12} className="z-40">
            <Popover.Popup finalFocus={field} aria-label="Emoji"
              className="origin-[var(--transform-origin)] outline-none transition-[transform,opacity] duration-200 ease-out-soft data-[ending-style]:scale-95 data-[ending-style]:opacity-0 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 motion-reduce:transition-opacity">
              <EmojiPicker onPick={(e) => { insert(e); setEmojiOpen(false); }} onClose={() => setEmojiOpen(false)} />
            </Popover.Popup>
          </Popover.Positioner>
        </Popover.Portal>
      </Popover.Root>
    </form>
  );
}

interface Status { text: string; kind: "error" | "progress" | "info" | "offline" }

/** StatusLine: one line above the field saying what is wrong or happening, in words. */
function StatusLine({ status }: { status: Status }) {
  const icon = status.kind === "error" ? <IconAlertCircle size={16} stroke={2.2} />
    : status.kind === "offline" ? <IconCloudOff size={16} stroke={2.2} />
    : status.kind === "info" ? <IconAt size={16} stroke={2.2} />
    : <span className="flex gap-0.5 text-[10px] leading-none"><span className="working-dot">●</span><span className="working-dot">●</span><span className="working-dot">●</span></span>;
  return (
    <p role={status.kind === "error" ? "alert" : "status"}
      className={"mb-2 flex items-start gap-1.5 px-1 text-[13px] leading-snug fade-in " + (status.kind === "error" ? "font-semibold text-danger" : "text-text-2")}>
      <span aria-hidden="true" className="mt-px grid h-4 shrink-0 place-items-center">{icon}</span>
      <span className="min-w-0">{status.text}</span>
    </p>
  );
}

interface Words { overview: T.Overview | null; names: Record<string, string> }

/** inSentence: "Your agent" reads "your agent" inside a sentence; names stay. */
const inSentence = (s: string) => (s.startsWith("Your ") ? "your" + s.slice(4) : s);

/** ownerOf finds the person a device address belongs to, as the overview knows them. */
function ownerOf(address: string, o: T.Overview | null): T.PersonView | null {
  const has = (p?: T.PersonView | null) => !!p && (p.address === address || (p.devices || []).some((d) => d.address === address));
  if (!o) return null;
  if (has(o.person) || (o.person && o.me.address === address)) return o.person!;
  return (o.people || []).find(has) || null;
}

/** deviceAgent: the agent a device conversation writes to (the device's
 *  default), as model.agentName words it: "Vitalii’s agent", or the device. */
function deviceAgent(address: string, o: T.Overview | null, names: Record<string, string>, agentId?: string): string {
  const host = ownerOf(address, o);
  return host || (agentId && names[agentId]) ? agentName(agentId, names, host, o?.person) : niceDevice(address);
}

/** messageOf finds a message of the open conversation and says, in words,
 *  who wrote it and how it starts (for the reply chip; mentions read as @Name). */
function messageOf(id: string, w: Words, dm?: T.DMThread, thread?: T.Thread): { who: string; text: string } | undefined {
  if (dm) {
    const m = (dm.messages || []).find((x) => x.id === id);
    if (!m) return undefined;
    const me = w.overview?.person;
    // An agent wrote it only when the message says so (m.pid alone is also
    // the agent a person asked); otherwise the person whose device sent it.
    const agentWrote = m.verified_agent; // a DM turn is an agent's only when proven
    const agent = agentWrote && m.pid ? (dm.agents || []).find((a) => a.pid === m.pid) : undefined;
    const sentFrom = (p?: T.PersonView | null) => !!p && (p.address === m.from || (p.devices || []).some((d) => d.address === m.from));
    const guest = (dm.guests || []).find((g) => sentFrom(g.host));
    const person = [dm.peer, ...(dm.members || []), ...(w.overview?.people || [])].find(sentFrom);
    const who = agent ? inSentence(agentName(agent.agent_id, w.names, agent.host, me))
      : agentWrote ? inSentence(agentName(m.agent_id, w.names, ownerOf(m.from, w.overview), me))
      : m.dir === "out" && !m.excerpt_pid ? "yourself"
      : guest ? personName(guest.host) : person ? personName(person) : niceDevice(m.from) || "someone";
    return { who, text: m.deleted ? "Deleted message" : firstLine(m.text || m.body, 90) || ((m.attachments || []).length ? "Files" : "") };
  }
  const m = (thread?.messages || []).find((x) => x.id === id);
  if (!m || !thread) return undefined;
  // Who wrote it as model.threadAuthor says: your own phone is you, a person's device that person, an agent device its agent.
  const a = threadAuthor(m, w.overview, w.names, thread.peer, thread.messages || []);
  const who = a.mine ? "yourself" : a.agent ? inSentence(a.name) : a.name;
  return { who, text: m.deleted ? "Deleted message" : firstLine(m.text || m.body, 90) || ((m.files || []).length ? "Files" : "") };
}
