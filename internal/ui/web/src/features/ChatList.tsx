// The chat list: the people, groups and agents this person talks with,
// newest first, with what waits for them on top. Phones show it as the
// Chats tab; desktops keep it as the middle column beside the open chat.
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { IconChevronRight, IconRobot, IconSearch, IconX } from "@tabler/icons-react";
import { useAgentNames, useApp, useWide } from "../context";
import { useStore } from "../store";
import type { T } from "../api";
import { Reason, agentName, convTitle, decidable, firstLine, personOf, senderOf, type ChatItem } from "../model";
import { useNeedsYou } from "./Approvals";
import { ChatRow } from "./ChatList.row";
import { chatItems, personAt } from "./ChatList.words";
import { GroupInvitations } from "./ChatList.invites";
import { TopicResults } from "./ChatList.topics";
import { CandidateRow, NewChatButton, candidates, matches, startChat } from "./NewChat";
import { WorkspacePill } from "./WorkspaceSwitcher";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Button, IconButton } from "../ui/Button";

type Filter = "all" | ChatItem["kind"];
const filters: { id: Filter; label: string }[] = [
  { id: "all", label: "All" }, { id: "person", label: "People" }, { id: "agent", label: "Agents" }, { id: "group", label: "Groups" },
];

// The title's highlighter stripe: saturated yellow in light; in dark the same
// yellow at about a third, so the light letters stay readable where they cross it.
const stripe = "relative z-0 after:absolute after:-left-1 after:-right-1.5 after:bottom-0.5 after:-z-10 after:h-[38%] after:-rotate-[1.5deg] after:rounded after:bg-act after:content-[''] "
  + "dark:after:bg-act/35 [@media(prefers-color-scheme:dark)]:[:root:not([data-theme=light])_&]:after:bg-act/35";

const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

export function ChatList() {
  const store = useApp();
  const wide = useWide();
  const overview = useStore(store, (s) => s.overview);
  const loadError = useStore(store, (s) => s.loadError);
  const open = useStore(store, (s) => s.open);
  const agentNames = useAgentNames();
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [searching, setSearching] = useState(false);
  const [topicHits, setTopicHits] = useState(0); // topics the search found (ChatList.topics.tsx)
  const search = useRef<HTMLInputElement>(null);

  const items = useMemo(() => chatItems(overview, agentNames), [overview, agentNames]);
  const threads = useMemo(() => new Map((overview?.threads || []).map((t) => [t.id, t])), [overview]);
  const summaries = useMemo(() => new Map((overview?.dms || []).map((d) => [d.id, d])), [overview]);
  const kinds = new Set(items.map((i) => i.kind));
  const shownFilters = kinds.size > 1 ? filters.filter((f) => f.id === "all" || kinds.has(f.id)) : [];
  const active: Filter = filter !== "all" && kinds.has(filter) ? filter : "all";
  const q = query.trim();
  const shown = items.filter((i) => (active === "all" || i.kind === active)
    && matches(q, i.title, i.subtitle, i.note, i.last, ...(i.members || []), summaries.get(i.open.id)?.title, threads.get(i.open.id)?.title, ...(i.topics || []).map((t) => t.title)));
  const unread = items.reduce((n, i) => n + i.unread, 0);
  const showSearch = wide || searching || !!q;

  // Ctrl K (⌘K) finds a chat from anywhere on this screen.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setSearching(true);
        requestAnimationFrame(() => { search.current?.focus(); search.current?.select(); });
      }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, []);

  const closeSearch = () => { setQuery(""); setSearching(false); };
  const title = <h1 className={"font-display text-[34px] font-extrabold leading-none tracking-[-.03em] lg:text-[30px] " + stripe}>Chats</h1>;

  return (
    <section aria-label="Chats" className="pb-8 lg:pb-4">
      {wide ? (
        <div className="flex items-center justify-between gap-3 px-4 pt-5">{title}<NewChatButton /></div>
      ) : (
        <>
          <div className="flex items-center gap-2 px-4 pt-3">
            <WorkspacePill />
            <span className="flex-1" />
            <IconButton label="Search chats" onClick={() => { setSearching(true); requestAnimationFrame(() => search.current?.focus()); }}
              aria-expanded={showSearch} className="bg-surface stroke"><IconSearch size={20} stroke={2.2} /></IconButton>
            <NewChatButton />
          </div>
          <div className="flex items-end justify-between px-4 pt-5">
            {title}
            {unread > 0 && <span className="pb-1 text-[13.5px] font-bold text-text-2 tnum">{unread} unread</span>}
          </div>
        </>
      )}

      {showSearch && (
        <div className="flex items-center gap-1 px-4 pt-4">
          <label className="flex h-11 min-w-0 flex-1 items-center gap-2 rounded-xl bg-surface pl-3 stroke has-[input:focus-visible]:outline-3 has-[input:focus-visible]:outline-offset-2 has-[input:focus-visible]:outline-agent-ink">
            <IconSearch size={18} stroke={2.2} aria-hidden="true" className="shrink-0" />
            <span className="sr-only">Search chats</span>
            <input ref={search} type="search" value={query} onChange={(e) => setQuery(e.target.value)} enterKeyHint="search"
              onKeyDown={(e) => { if (e.key === "Escape") { if (query) setQuery(""); else if (!wide) closeSearch(); } }}
              placeholder="Search people, agents, groups" autoComplete="off"
              className="min-w-0 flex-1 bg-transparent text-[16px] outline-none placeholder:text-muted focus-visible:outline-none lg:text-[15px] [&::-webkit-search-cancel-button]:hidden" />
            {query ? <IconButton label="Clear search" onClick={() => { setQuery(""); search.current?.focus(); }} className="shrink-0"><IconX size={18} /></IconButton>
              : wide && <kbd className="mr-2.5 shrink-0 rounded-md border border-hairline px-1.5 py-0.5 font-sans text-[12px] font-bold text-text-2">{isMac ? "⌘ K" : "Ctrl K"}</kbd>}
          </label>
          {!wide && <button type="button" onClick={closeSearch} className="min-h-11 shrink-0 rounded-full px-3 font-semibold text-text-2 hover:bg-sunken">Cancel</button>}
        </div>
      )}

      {!overview ? (loadError ? <LoadFailed text={loadError} /> : <Skeleton />) : (
        <>
          {!q && <NeedsYouBanner overview={overview} agentNames={agentNames} />}
          {!q && <GroupInvitations />}
          {overview.persons && !overview.person && <NoName />}
          {shownFilters.length > 0 && (
            <div role="group" aria-label="Show" className="flex gap-2 overflow-x-auto px-4 pt-4 [scrollbar-width:none] lg:px-3">
              {shownFilters.map((f) => (
                <button key={f.id} type="button" aria-pressed={active === f.id} onClick={() => setFilter(f.id)} className="group min-w-11 shrink-0 py-1 lg:py-1.5">
                  <span className={"flex h-9 min-w-11 items-center justify-center gap-1.5 rounded-full px-3.5 text-[14px] font-extrabold stroke transition-colors duration-200 ease-out-soft lg:h-8 lg:px-3 lg:text-[13px] "
                    + (active === f.id ? "bg-ink text-canvas" : "bg-surface text-ink group-hover:bg-sunken")}>
                    {f.id === "agent" && <IconRobot size={16} stroke={2.2} aria-hidden="true" />}{f.label}
                  </span>
                </button>
              ))}
            </div>
          )}
          {shown.length > 0 && (
            <ul className={"pt-2 " + (wide ? "flex flex-col gap-0.5 px-2" : "")} aria-label={q ? "Matching chats" : "Chats"}>
              {shown.map((i) => (
                <li key={i.key} className={wide ? "" : "relative [&+&]:before:absolute [&+&]:before:top-0 [&+&]:before:right-4 [&+&]:before:left-[76px] [&+&]:before:border-t [&+&]:before:border-hairline"}>
                  <ChatRow item={i} summary={summaries.get(i.open.id)} overview={overview} wide={wide}
                    selected={wide && !!open && open.id === i.open.id} onOpen={() => void store.open(i.open)} />
                </li>
              ))}
            </ul>
          )}
          {!q && !items.length && overview.person && <NoChats />}
          {q && overview.topic_list && <TopicResults query={q} onCount={setTopicHits} />}
          {q && <SearchExtras query={q} overview={overview} none={!shown.length && !topicHits} />}
        </>
      )}
    </section>
  );
}

/** What the banner names: the newest thing that waits, and where it is decided. */
interface Named { face: ReactNode; headline: string; line: string; go: () => void }

/** NeedsYouBanner names who wants what, and opens exactly that: the newest
 *  request in its conversation (or device thread), or the OKs when it is
 *  decided there. It counts and names only what this device decides. */
function NeedsYouBanner({ overview: o, agentNames }: { overview: T.Overview; agentNames: Record<string, string> }) {
  const store = useApp();
  const count = useNeedsYou(); // the same count as the OKs badge and header
  if (!count) return null;
  const me = o.person;
  let named: Named | null = null;
  const review = (o.review || []).filter((r) => !r.notice).sort((a, b) => (b.at || "").localeCompare(a.at || ""))[0];
  const conv = decidable(o).sort((a, b) => b.at.localeCompare(a.at))[0];
  const device = (o.links || []).find((l) => l.state === "pending");
  if (review && (!conv || review.at >= conv.at)) { // what is asked, then who asks
    // Whose agent asks; the row below says on which device (a phone's banner has room for one).
    const who = agentName(undefined, agentNames, personAt(review.peer, o), me);
    named = {
      face: <AgentAvatar seed={review.peer} size={40} mood="waiting" />,
      headline: firstLine(review.excerpt, 120) || (review.kind === "task" ? "A task for your agent" : "A question for your agent"),
      line: (review.kind === "task" ? "Task from " : review.kind === "question" ? "Question from " : "From ") + who,
      go: () => void store.open({ kind: "thread", id: review.id, focus: review.id }),
    };
  } else if (conv) {
    const invite = conv.reason === Reason.invite;
    const p = personOf(conv.peer, o);
    named = {
      face: invite ? <AgentAvatar seed={o.me.address} size={40} mood="waiting" /> : <PersonAvatar name={senderOf(conv, o)} seed={p?.person || p?.address || conv.peer} size={40} />,
      headline: invite ? convTitle(conv, o) : firstLine(conv.excerpt, 120) || convTitle(conv, o),
      line: invite ? "It joins only when you say so" : convTitle(conv, o),
      go: () => void store.open({ kind: "dm", id: conv.conv, focus: conv.id }),
    };
  } else if (device) {
    named = {
      face: <PersonAvatar name={me?.label || "Me"} seed={me?.person || "me"} size={40} />,
      headline: "A new device, “" + device.name + "”, wants to join as you",
      line: "Approve it only if it is yours",
      go: () => store.showTab("oks"),
    };
  }
  if (!named) return null; // only group invitations: their cards are right below
  return (
    <div className="px-4 pt-6 lg:px-3">
      <button type="button" onClick={named.go}
        className="relative flex w-full items-center gap-3 rounded-[18px] bg-act py-3 pl-3 pr-2.5 text-left text-act-ink stroke shadow-pop press">
        <span className="absolute -top-3 left-3.5 -rotate-3 rounded-md bg-white px-2 py-[3px] text-[11px] font-extrabold uppercase leading-none tracking-[.07em] text-[#1B1530] stroke tnum">
          {count === 1 ? "1 needs your OK" : count + " need your OK"}
        </span>
        <span className="pt-1">{named.face}</span>
        <span className="min-w-0 flex-1 pt-1 text-act-ink">
          <span className="line-clamp-2 font-display text-[16px] font-bold leading-[1.2]">{named.headline}</span>
          <span className="mt-0.5 block truncate text-[13.5px] font-semibold">{named.line}</span>
        </span>
        <span aria-hidden="true" className="grid size-10 shrink-0 place-items-center rounded-full bg-[#1B1530] text-white"><IconChevronRight size={22} stroke={2.6} /></span>
        <span className="sr-only">Review</span>
      </button>
    </div>
  );
}

/** SearchExtras: people with no chat yet who match, so a search can start one. */
function SearchExtras({ query, overview, none }: { query: string; overview: T.Overview; none: boolean }) {
  const store = useApp();
  const [busy, setBusy] = useState("");
  const { people, server } = candidates(overview);
  const fresh = people.concat(server).filter((c) => !c.chat && matches(query, c.name));
  if (!fresh.length) return none ? (
    <div className="px-6 py-12 text-center">
      <p className="font-display text-[20px] font-bold">Nothing matches “{query}”</p>
      <p className="mt-1 text-text-2">Try a name, a group, or words from a message.</p>
    </div>
  ) : null;
  return (
    <div className="px-2 pt-4 lg:px-1">
      <h2 className="px-4 pb-1 text-[12px] font-extrabold uppercase tracking-[.08em] text-muted lg:px-3">Start a new chat</h2>
      <ul className="px-2">
        {fresh.map((c) => (
          <li key={c.key}><CandidateRow c={c} busy={busy === c.key} disabled={!!busy}
            onPick={async () => { setBusy(c.key); await startChat(store, c); setBusy(""); }} /></li>
        ))}
      </ul>
    </div>
  );
}

function NoChats() {
  return (
    <div className="flex flex-col items-center px-8 pt-14 pb-6 text-center">
      <div aria-hidden="true" className="relative mb-6 h-28 w-40">
        <span className="absolute top-2 left-1 -rotate-6 rounded-[20px] rounded-bl-md bg-surface px-4 py-3 stroke shadow-pop-sm">
          <span className="flex gap-1.5"><i className="working-dot size-2 rounded-full bg-muted" /><i className="working-dot size-2 rounded-full bg-muted" /><i className="working-dot size-2 rounded-full bg-muted" /></span>
        </span>
        <span className="absolute right-1 bottom-0 rotate-6"><AgentAvatar seed="hello" size={56} mood="waiting" /></span>
      </div>
      <h2 className="font-display text-[22px] font-bold">No chats yet</h2>
      <p className="mt-1.5 max-w-[30ch] text-text-2">Start one with <b className="font-bold text-ink">New</b>: pick a person to talk to, or make a group.</p>
      <div className="mt-5"><NewChatButton size="lg" /></div>
    </div>
  );
}

function NoName() {
  const store = useApp();
  return (
    <div className="mx-4 mt-5 rounded-2xl bg-surface p-4 stroke lg:mx-3">
      <p className="font-display text-[18px] font-bold">Choose the name people see</p>
      <p className="mt-1 text-[14px] text-text-2">You need one before you can chat with people here.</p>
      <Button variant="act" size="sm" className="mt-3" onClick={() => store.showTab("settings")}>Choose my name</Button>
    </div>
  );
}

function LoadFailed({ text }: { text: string }) {
  const store = useApp();
  return (
    <div role="alert" className="mx-4 mt-6 rounded-2xl bg-danger-bg p-4 text-danger stroke lg:mx-3">
      <p className="font-bold">Your chats didn’t load</p>
      <p className="mt-1 text-[14px]">{text}</p>
      <Button variant="outline" size="sm" className="mt-3" onClick={() => store.retryNow()}>Try again</Button>
    </div>
  );
}

function Skeleton() {
  return (
    <ul aria-label="Loading chats" aria-busy="true" className="flex flex-col gap-1 px-4 pt-5 lg:px-3">
      {[0, 1, 2, 3, 4, 5].map((n) => (
        <li key={n} className="flex items-center gap-3 py-2.5 motion-safe:animate-pulse">
          <span className="size-12 shrink-0 rounded-full bg-hairline" />
          <span className="flex flex-1 flex-col gap-2">
            <span className="h-3.5 rounded-full bg-hairline" style={{ width: 40 + ((n * 23) % 35) + "%" }} />
            <span className="h-3 rounded-full bg-hairline/70" style={{ width: 60 + ((n * 17) % 30) + "%" }} />
          </span>
        </li>
      ))}
    </ul>
  );
}
