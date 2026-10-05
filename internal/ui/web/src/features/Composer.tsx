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
import { agentName, deviceTarget, deviceWho, firstLine, niceDevice, participants, personName, threadAuthor, whoName } from "../model";
import { useStore, type Draft, type StagedFile } from "../store";
import { EmojiPicker, useEmojiPreload } from "./Emoji";
import { DropTarget, FilesTray, bytes, draftFiles, overLimit, releaseFiles, useFileDrop } from "./Composer.files";
import { candidates, decode, encode, guestAuthor, matches, shift, trigger, type Candidate, type Span } from "./Composer.mentions";
import { Field, IntentRow, PlusMenu, ReplyChip, SendButton, menuIcons, type MenuAction } from "./Composer.parts";
import { MentionList, optionId } from "./Composer.picker";
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
  const people = useMemo(() => (dm ? candidates(dm, overview, names) : []), [dm, overview, names]);
  const humanGuest = dm?.role === "human_guest";
  const visitor = dm?.role === "visitor";
  const author = dm ? guestAuthor(dm) : undefined;

  // A received request this person may still answer by hand (its actions say so).
  const answerable = thread && draft.replyTo ? (thread.messages || []).find((m) => m.id === draft.replyTo && (m.actions || []).includes("reply")) : undefined;
  if (answerable) answering.current.add(answerable.id);
  // Chosen to answer by hand, but handled meanwhile (accepted, answered,
  // closed): sending now would ask the device something new instead.
  const handled = !!thread && !!draft.replyTo && !answerable && answering.current.has(draft.replyTo);

  const target: Target = useMemo(() => {
    if (thread) {
      if (answerable) return { kind: "answer", id: answerable.id, task: answerable.kind === "task" };
      const to = deviceTarget(thread.peer, overview); // a device that runs no agent cannot be asked
      return to.kind === "agent" ? { kind: "device", name: deviceAgent(thread.peer, overview, names), seed: thread.peer, ask: true }
        : to.kind === "person" ? { kind: "device", name: to.name, seed: thread.peer, ask: false }
        : { kind: "none", note: to.note };
    }
    if (dm && draft.agent) {
      const a = (dm.agents || []).find((x) => x.pid === draft.agent);
      const p = participants(dm, overview, names).find((x) => x.pid === draft.agent);
      const name = p?.name || "This agent";
      return { kind: "agent", pid: draft.agent, name, seed: p?.seed || draft.agent, canAsk: !!a?.can_ask, why: a ? a.state_text : "It is no longer in this chat." };
    }
    return { kind: "conversation" };
  }, [dm, thread, draft.agent, answerable?.id, answerable?.kind, overview, names]);
  const asking = target.kind === "agent" || (target.kind === "device" && target.ask);
  const doIt = asking && !!draft.doIt;
  const latest = (): Draft => store.get().drafts[conv] ?? EMPTY;

  // Why nothing can be written here now, in words.
  const mine = humanGuest && !author ? (dm?.guests || []).find((g) => g.host_here) : undefined;
  const closed: { title: string; detail?: string } | null =
    humanGuest && !author ? (mine?.state === "invited" ? { title: "Join to write here", detail: "You’re invited as a guest. Accept the invitation to write; your draft stays." }
      : mine && (mine.state === "dismissed" || mine.state === "left") ? { title: "You’re no longer a guest here", detail: "New messages don’t reach you. What was already shared stays with you." }
      : { title: "You can read this chat but not write in it", detail: dm?.frozen })
    : dm?.frozen ? { title: "Nothing more can be sent here", detail: dm.frozen }
    : thread?.key.pending ? { title: "Sending is paused", detail: "This agent’s identity changed. Check it before writing again." }
    : target.kind === "none" ? { title: "Nothing to ask here", detail: target.note + " To answer one of its requests yourself, choose Reply on it." }
    : null;

  const files = draft.files || [];
  const filesAllowed = !!lim && !closed && target.kind !== "answer" && (!visitor || (target.kind === "agent" && target.canAsk));
  const limit = !files.length ? "" : filesAllowed ? overLimit(files, lim)
    : target.kind === "answer" ? "Files can’t go with an answer. Remove them to send." : "Files can’t be sent here. Remove them to send.";
  const sending = conv in busy;
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
      next.agent = undefined;
      next.doIt = undefined;
      announce((target.kind === "agent" ? target.name : "The agent") + " is no longer asked; this goes to everyone here.");
    }
    const plain = dropped.filter((s) => s.kind !== "agent");
    if (plain.length) announce(plain.map((s) => "@" + s.name).join(", ") + (plain.length > 1 ? " are" : " is") + " no longer a mention; the text stays.");
    write(next, at);
  }

  function pick(c: Candidate) {
    if (!trig || caret == null) return;
    const d = latest();
    const token = "@" + c.name + " ";
    const cur = text.slice(0, trig.start) + token + text.slice(caret);
    let { kept } = shift(spans, text, cur);
    if (c.kind === "agent") kept = kept.filter((s) => s.kind !== "agent"); // one agent at a time
    const span: Span = { start: trig.start, name: c.name, kind: c.kind, id: c.id };
    write({
      ...d, text: encode(cur, [...kept, span]),
      ...(c.kind === "agent" ? { agent: c.id, doIt: c.id === d.agent ? d.doIt : false, replyTo: undefined } : {}),
    }, trig.start + token.length);
    if (c.kind === "agent") announce("Asking " + c.name + ". Alt+D switches between Answer and Do it.");
  }

  function stopAsking() {
    const d = latest();
    const was = decode(d.text);
    write({ ...d, text: encode(was.text, was.spans.filter((s) => s.kind !== "agent")), agent: undefined, doIt: undefined });
  }

  const choose = (v: boolean) => write({ ...latest(), doIt: v || undefined });

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
    const list = [...(e.clipboardData?.files || [])];
    if (!list.length || e.clipboardData.getData("text/plain")) return;
    e.preventDefault();
    addFiles(list, true);
  };

  // ---- sending
  async function send() {
    if (!ready || (!dm && !thread)) return;
    const c = conv, d = latest(), to = target, here = dm, device = thread;
    const was = decode(d.text);
    const body = encode(was.text, was.spans, true).trim();
    const sent = d.files || [];
    const progress = (s: string) => setBusy((b) => ({ ...b, [c]: s }));
    const title = here ? (here.kind === "group" ? here.title || "the group" : personName(here.peer)) : to.kind === "device" ? to.name : "that chat";
    if (to.kind === "none") return;
    progress("");
    setNotice(null);
    typing.stop();
    try {
      // Each file is handed to this computer's AgentNet once; a retry after a
      // failure hands over only the rest. (In a browser device the engine
      // takes the file itself in place of an id.)
      const ids: unknown[] = [];
      for (const [i, f] of sent.entries()) {
        if (f.staged) { ids.push(f.staged); continue; }
        progress(sent.length > 1 ? "Preparing file " + (i + 1) + " of " + sent.length + "…" : "Preparing the file…");
        const id = await store.api.stage(f.file);
        ids.push(id);
        if (typeof id === "string") keepStaged(c, f.key, id);
      }
      progress(sent.length ? "Sending…" : "");
      const fileIds = ids.length ? (ids as string[]) : undefined;
      const reply = d.replyTo && messageOf(d.replyTo, words, here, device) ? d.replyTo : undefined;
      let r: T.Sent | undefined;
      try {
        if (to.kind === "answer") announce((await store.api.act({ do: "reply", id: to.id, body })).note || "Answer sent.");
        else if (to.kind === "agent") r = await store.api.askAgent({ pid: to.pid, kind: d.doIt ? "task" : "question", body, files: fileIds });
        else if (device) {
          const last = (device.messages || []).at(-1);
          // A new topic starts a separate conversation with this agent; otherwise the thread continues.
          const kind = to.kind === "device" && !to.ask ? "message" : d.doIt ? "task" : "question"; // a person's device: a plain message
          r = await store.api.send({ to: device.peer, kind, body, reply_to: d.newTopic ? undefined : last?.id, quote:reply, files: fileIds });
          if (d.newTopic && r) { store.setDraft(c, { ...(store.get().drafts[c] ?? EMPTY), newTopic: false }); void store.open({ kind: "thread", id: r.id }); }
        } else r = await store.api.sendDM({ conv: c, body, reply_to: reply, quote:reply, files: fileIds, ...(here && guestAuthor(here) ? { pid: guestAuthor(here)!.pid } : {}) });
      } finally {
        forgetStaged(c, sent); // a send takes the files it names, sent or refused
      }
      clearSent(c, d, sent);
      if (!r) { /* answered: the request's own card shows what happened */ }
      else if (r.state === "queued") store.toast("Will send when you’re back online.");
      else if (r.state === "waiting") store.toast("Kept here, not sent yet: " + (r.detail || "they can’t receive messages right now."));
      else if (r.state === "receiver_waiting" && r.detail) store.toast(r.detail);
      void store.refetch();
    } catch (e) {
      const why = errorText(e).replace(/\.?$/, ".");
      if (visible.current === c) setNotice({ conv: c, text: "Not sent: " + why + (sent.length ? " Your text and files are still here." : " Your text is still here.") });
      else store.toast("Not sent to " + title + ": " + why + " It’s kept in that chat.", "error");
    } finally {
      setBusy(({ [c]: _, ...rest }) => rest);
    }
  }

  function keepStaged(c: string, key: string, id: string) {
    const d = store.get().drafts[c];
    if (d?.files) store.setDraft(c, { ...d, files: d.files.map((f) => (f.key === key ? { ...f, staged: id } : f)) });
  }
  function forgetStaged(c: string, list: StagedFile[]) {
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
      if (cur.doIt === d.doIt) next.doIt = undefined;
    }
    if (cur.replyTo === d.replyTo) next.replyTo = undefined;
    store.setDraft(c, next);
    releaseFiles(list);
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
  function formKeys(e: KeyboardEvent) {
    if (e.altKey && e.code === "KeyD" && asking && !gone) { e.preventDefault(); choose(!doIt); }
  }

  if (!dm && !thread) return null;

  // ---- what the composer says
  const replyMsg = draft.replyTo && target.kind !== "agent" ? messageOf(draft.replyTo, words, dm, thread) : undefined;
  const placeholder = target.kind === "answer" ? (target.task ? "Write your reply" : "Write your answer")
    : target.kind === "device" && !target.ask ? "Message " + target.name
    : target.kind === "agent" || target.kind === "device" ? (doIt ? "Tell " + target.name + " what to do" : "Ask " + target.name)
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
    : doIt ? "Do it: send " + target.name + " a task" : "Ask " + target.name;
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
    <form ref={form} onSubmit={(e: FormEvent) => { e.preventDefault(); void send(); }} onKeyDown={formKeys} {...watch} aria-label="Write a message" data-conv={conv}
      className="relative border-t-[1.5px] border-outline bg-surface px-3 pt-2 pb-[max(10px,env(safe-area-inset-bottom))] lg:border-t lg:px-6 lg:pt-3 lg:pb-4">
      {open && <MentionList id={listId} items={offered} active={active} query={trig?.query || ""} onPick={pick} onHover={setActive} />}
      <p className="sr-only" aria-live="polite">{said}</p>

      {replyMsg && (target.kind === "answer"
        ? <ReplyChip title={"Answering " + replyMsg.who} text={replyMsg.text} cancel="Don’t answer it here"
            note={target.task ? "You reply yourself; your agent won’t run this task." : "You answer this yourself; your agent won’t."}
            onCancel={() => { write({ ...latest(), replyTo: undefined }); focusField(); }} />
        : <ReplyChip title={"Replying to " + replyMsg.who} text={replyMsg.text} cancel="Cancel reply"
            onCancel={() => { write({ ...latest(), replyTo: undefined }); focusField(); }} />)}
      {thread && draft.newTopic && !replyMsg && (
        <ReplyChip title={"New topic" + ("name" in target ? " with " + target.name : "")} text="Your next message starts a separate conversation." cancel="Keep this conversation"
          onCancel={() => { write({ ...latest(), newTopic: false }); focusField(); }} />
      )}
      <FilesTray files={files} lim={lim} busy={sending} onRemove={removeFile} />
      {(target.kind === "agent" || (target.kind === "device" && target.ask)) && !handled && (
        <IntentRow name={target.name} seed={target.seed} doIt={doIt} disabled={gone} onChoose={(v) => { choose(v); focusField(); }}
          onStop={target.kind === "agent" ? () => { stopAsking(); focusField(); } : undefined}
          note={gone && target.kind === "agent" ? target.name + " can’t be asked now" + (target.why ? " (" + target.why.replace(/\.$/, "") + ")" : "") + ". Remove the mention to write to everyone instead."
            : doIt ? "Anything an owner must OK will wait for them." : undefined} />
      )}
      {status && <StatusLine status={status} />}

      <div ref={row} className="flex items-end gap-2">
        <PlusMenu actions={actions} disabled={sending} onOpenChange={setMenuOpen}
          finalFocus={() => !emojiAfterMenu.current} onClosed={() => { emojiAfterMenu.current = false; }} />
        <Field ref={field} mirror={mirror} wide={wide} text={text} spans={spans} agent={draft.agent} value={text} placeholder={placeholder} aria-label={placeholder}
          enterKeyHint={wide ? "send" : "enter"} autoComplete="off" spellCheck
          aria-autocomplete="list" aria-controls={open && offered.length ? listId : undefined}
          aria-activedescendant={open && offered.length ? optionId(listId, Math.min(active, offered.length - 1)) : undefined}
          onChange={(e) => { edit(e.target.value); setCaret(e.target.selectionStart); setDismissed(-1); typing.typed(!!e.target.value.trim()); }}
          onSelect={(e) => setCaret(e.currentTarget.selectionStart === e.currentTarget.selectionEnd ? e.currentTarget.selectionStart : null)}
          onBlur={() => { setCaret(null); typing.stop(); }}
          onScroll={(e) => { if (mirror.current) mirror.current.scrollTop = e.currentTarget.scrollTop; }}
          onKeyDown={keys} onPaste={paste} />
        <SendButton doIt={doIt} ready={ready} busy={sending} label={sendLabel} />
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
