// Emoji: the full picker (search, categories, grid, skin tone) built on
// frimousse with emoji data served from our own origin (offline is normal),
// and the quick reactions row used on messages.
import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Menu } from "@base-ui/react/menu";
import { EmojiPicker as Picker, defaultEmojiDataResolver, useSkinTone, type EmojiData, type EmojiDataResolver, type SkinTone } from "frimousse";
import { IconChevronDown, IconDots, IconMoodPlus, IconSearch, IconX } from "@tabler/icons-react";
import { usePortal } from "../owned";

const toneKey = "agentnet.messenger.skin-tone";
// The emoji data ships in the package (m/emoji/en/), next to this module.
const emojiBase = new URL("m/emoji", import.meta.url).href;
const tones: SkinTone[] = ["none", "light", "medium-light", "medium", "medium-dark", "dark"];

function savedTone(): SkinTone {
  try {
    const t = localStorage.getItem(toneKey) as SkinTone | null;
    return t && tones.includes(t) ? t : "none";
  } catch { return "none"; }
}

const reducedMotion = () => matchMedia("(prefers-reduced-motion: reduce)").matches;

interface Section { label: string; emoji: string; row: number }

/** sections places each category the way frimousse lays out its list: a
 *  header, then its emoji in rows of `columns`. */
function sections(d: EmojiData, columns: number): Section[] {
  const groups = new Map<number, { first: string; count: number }>();
  for (const e of d.emojis) {
    const g = groups.get(e.category);
    if (g) g.count++;
    else groups.set(e.category, { first: e.emoji, count: 1 });
  }
  const out: Section[] = [];
  let row = 0;
  for (const c of d.categories) {
    const g = groups.get(c.index);
    if (!g) continue;
    out.push({ label: c.label, emoji: g.first, row });
    row += Math.ceil(g.count / columns);
  }
  return out;
}

// The picker is never taller than what a phone's keyboard leaves visible
// (--an-viewport-h, set by the host while the iOS keyboard covers the page;
// docs/UI_SKINS.md), so its search field and its emoji both stay in view.
export function EmojiPicker({ onPick, onClose }: { onPick: (emoji: string) => void; onClose?: () => void }) {
  const [data, setData] = useState<EmojiData | null>(null);
  const [failed, setFailed] = useState(false);
  const [tone, setTone] = useState<SkinTone>(savedTone);
  const [choosingTone, setChoosingTone] = useState(false);
  const [search, setSearch] = useState("");
  const [current, setCurrent] = useState(0);
  const viewport = useRef<HTMLDivElement>(null);
  const columns = useMemo(() => (innerWidth < 440 ? 7 : 8), []);
  const parts = useMemo(() => (data ? sections(data, columns) : []), [data, columns]);

  // The default resolver fetches, caches and drops emoji this device cannot
  // draw; we keep a copy to place the category tabs. A failed load shows an
  // empty picker with words, not an error in the console.
  const resolve = useCallback<EmojiDataResolver>(async (locale, options) => {
    try {
      const d = await defaultEmojiDataResolver(locale, options);
      setData(d);
      return d;
    } catch (e) {
      if (options.signal?.aborted) throw e;
      setFailed(true);
      return { locale, emojis: [], categories: [], skinTones: { light: "", "medium-light": "", medium: "", "medium-dark": "", dark: "" } };
    }
  }, []);

  const metrics = () => {
    const list = viewport.current?.querySelector("[frimousse-list]");
    if (!list) return null;
    const s = getComputedStyle(list);
    return { row: parseFloat(s.getPropertyValue("--frimousse-row-height")) || 44, head: parseFloat(s.getPropertyValue("--frimousse-category-header-height")) || 32 };
  };
  const topOf = (i: number, m: { row: number; head: number }) => i * m.head + parts[i].row * m.row;

  // A jump glides only a short way (a long glide takes over a second); while
  // it moves, the tab stays on where it goes instead of every category passed.
  const going = useRef<{ to: number; until: number } | null>(null);
  const jump = (i: number) => {
    const m = metrics(), vp = viewport.current;
    if (!m || !vp) return;
    const top = topOf(i, m);
    const glide = !reducedMotion() && Math.abs(top - vp.scrollTop) < 3 * vp.clientHeight;
    going.current = glide ? { to: i, until: Date.now() + 1000 } : null;
    vp.scrollTo({ top, behavior: glide ? "smooth" : "auto" });
    setCurrent(i);
  };

  const scrolled = () => {
    const m = metrics(), vp = viewport.current;
    if (!m || !vp || search) return;
    let i = 0;
    while (i + 1 < parts.length && topOf(i + 1, m) <= vp.scrollTop + 1) i++;
    const g = going.current;
    if (g && Date.now() < g.until && i !== g.to) return;
    going.current = null;
    if (i !== current) setCurrent(i);
  };

  const chooseTone = (t: SkinTone) => {
    setTone(t);
    setChoosingTone(false);
    try { localStorage.setItem(toneKey, t); } catch { /* a convenience only */ }
  };

  const keys = (e: KeyboardEvent) => {
    if (e.key === "Escape" && onClose) { e.stopPropagation(); onClose(); }
  };

  return (
    <Picker.Root locale="en" emojibaseUrl={emojiBase} resolveEmojiData={resolve} columns={columns} skinTone={tone}
      onEmojiSelect={(e) => onPick(e.emoji)} onKeyDown={keys} aria-label="Emoji"
      className="flex h-[min(430px,70dvh,calc(var(--an-viewport-h,100dvh)_-_24px))] w-[min(408px,calc(100vw-24px))] flex-col overflow-hidden rounded-2xl bg-surface text-ink stroke shadow-pop">
      <div className="flex items-center gap-1.5 p-2 pb-1">
        <label className="flex h-11 min-w-0 flex-1 items-center gap-2 rounded-full bg-canvas px-3.5 stroke focus-within:border-agent-ink">
          <IconSearch size={18} stroke={2.2} className="shrink-0 text-muted" aria-hidden="true" />
          <Picker.Search value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search emoji" aria-label="Search emoji"
            style={{ fontSize: 15, outline: "none" }} className="h-full min-w-0 flex-1 bg-transparent placeholder:text-muted [&::-webkit-search-cancel-button]:hidden" />
        </label>
        <ToneButton tone={tone} open={choosingTone} onToggle={() => setChoosingTone((v) => !v)} />
        {onClose && (
          <button type="button" onClick={onClose} aria-label="Close emoji" className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-sunken">
            <IconX size={20} className="text-text-2" />
          </button>
        )}
      </div>

      {choosingTone ? <ToneRow tone={tone} onChoose={chooseTone} />
        : !search && parts.length > 0 && <CategoryTabs parts={parts} current={current} onJump={jump} />}

      <Picker.Viewport ref={viewport} onScroll={scrolled} className="relative min-h-0 flex-1 outline-none">
        <Picker.Loading className="absolute inset-0 grid place-items-center text-[13px] text-muted">
          <span className="an-late flex items-center gap-1.5">Loading emoji<span className="working-dot">.</span><span className="working-dot">.</span><span className="working-dot">.</span></span>
        </Picker.Loading>
        <Picker.Empty className="absolute inset-0 grid place-items-center px-6 text-center text-[13px] text-muted">
          {({ search }) => failed ? "Emoji couldn’t be loaded here. You can still type them with your keyboard." : <>No emoji for “{search}”.</>}
        </Picker.Empty>
        <Picker.List className="select-none pb-2" components={{
          CategoryHeader: ({ category, ...props }) => (
            <div {...props} className="bg-surface/95 px-3 pt-2.5 pb-1 text-[13px] font-semibold text-muted">{category.label}</div>
          ),
          Row: ({ children, ...props }) => <div {...props} className="px-2">{children}</div>,
          Emoji: ({ emoji, ...props }) => (
            <button {...props} className="grid h-11 w-[calc(100%/var(--frimousse-list-columns))] shrink-0 place-items-center rounded-xl data-[active]:bg-sunken">
              <span className="text-[26px] leading-none transition-transform duration-150 [[data-active]>&]:scale-115 motion-reduce:scale-100!">{emoji.emoji}</span>
            </button>
          ),
        }} />
      </Picker.Viewport>

      <div className="flex h-12 shrink-0 items-center gap-2.5 border-t border-hairline px-3.5" aria-hidden="true">
        <Picker.ActiveEmoji>
          {({ emoji }) => emoji
            ? <><span className="text-[26px] leading-none">{emoji.emoji}</span><span className="truncate text-[13px] font-semibold text-text-2 first-letter:uppercase">{emoji.label}</span></>
            : <span className="text-[13px] text-muted">Pick one, or search by name</span>}
        </Picker.ActiveEmoji>
      </div>
    </Picker.Root>
  );
}

const TAB = 44; // every category tab is a full 44px target

/** CategoryTabs: one tab per category while they all fit at 44px; when the
 *  picker is too narrow (phones), the last tabs move into a "More" menu, so
 *  nothing scrolls sideways and no tab is cut. */
function CategoryTabs({ parts, current, onJump }: { parts: Section[]; current: number; onJump: (i: number) => void }) {
  const portal = usePortal();
  const [box, setBox] = useState<HTMLDivElement | null>(null);
  const [fit, setFit] = useState(parts.length);
  useEffect(() => {
    if (!box) return;
    const measure = () => {
      const s = getComputedStyle(box);
      setFit(Math.max(2, Math.floor((box.clientWidth - parseFloat(s.paddingLeft) - parseFloat(s.paddingRight)) / TAB)));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(box);
    return () => ro.disconnect();
  }, [box]);
  const shown = parts.length > fit ? fit - 1 : parts.length;
  const rest = parts.slice(shown);
  const inRest = current >= shown;
  const tab = (on: boolean) => "relative grid h-11 min-w-11 flex-1 place-items-center transition-[filter,opacity] duration-200 "
    + (on ? "" : "opacity-60 grayscale-[.6] hover:opacity-100 hover:grayscale-0");
  const bar = <span aria-hidden="true" className="absolute inset-x-2.5 bottom-0 h-[3px] rounded-full bg-ink" />;
  return (
    <div ref={setBox} className="flex shrink-0 border-b border-hairline px-1">
      <div role="tablist" aria-label="Emoji categories" className="flex min-w-0 flex-[1_1_0]" style={{ flexGrow: shown }}>
        {parts.slice(0, shown).map((p, i) => (
          <button key={p.label} type="button" role="tab" aria-selected={i === current} aria-label={p.label} title={p.label} onClick={() => onJump(i)} className={tab(i === current)}>
            <span aria-hidden="true" className="text-[22px] leading-none">{p.emoji}</span>
            {i === current && bar}
          </button>
        ))}
      </div>
      {rest.length > 0 && (
        <Menu.Root modal={false}>
          <Menu.Trigger aria-label={"More categories" + (inRest ? ", now " + parts[current].label : "")} title="More categories"
            className={tab(inRest) + " flex-[1_1_0] rounded-xl data-[popup-open]:bg-sunken data-[popup-open]:opacity-100 data-[popup-open]:grayscale-0"}>
            <span aria-hidden="true" className="flex items-center gap-px">
              {inRest ? <><span className="text-[22px] leading-none">{parts[current].emoji}</span><IconChevronDown size={14} stroke={2.6} className="text-text-2" /></>
                : <IconDots size={22} stroke={2.4} className="text-text-2" />}
            </span>
            {inRest && bar}
          </Menu.Trigger>
          <Menu.Portal container={portal}>
            <Menu.Positioner side="bottom" align="end" sideOffset={4} collisionPadding={12} className="z-[60] outline-none">
              <Menu.Popup className="min-w-52 origin-[var(--transform-origin)] rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[transform,opacity] duration-200 ease-out-soft data-[ending-style]:scale-95 data-[ending-style]:opacity-0 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 motion-reduce:transition-opacity">
                {rest.map((p, j) => (
                  <Menu.Item key={p.label} onClick={() => onJump(shown + j)}
                    className="flex min-h-11 cursor-pointer items-center gap-3 rounded-xl px-2.5 outline-none select-none data-[highlighted]:bg-sunken">
                    <span aria-hidden="true" className="text-[22px] leading-none">{p.emoji}</span>
                    <span className={"font-semibold first-letter:uppercase " + (shown + j === current ? "" : "text-text-2")}>{p.label}</span>
                  </Menu.Item>
                ))}
              </Menu.Popup>
            </Menu.Positioner>
          </Menu.Portal>
        </Menu.Root>
      )}
    </div>
  );
}

function ToneButton({ tone, open, onToggle }: { tone: SkinTone; open: boolean; onToggle: () => void }) {
  const [, , variations] = useSkinTone("✋");
  const shown = variations.find((v) => v.skinTone === tone)?.emoji || "✋";
  return (
    <button type="button" onClick={onToggle} aria-expanded={open} aria-label="Skin tone" title="Skin tone"
      className={"grid size-11 shrink-0 place-items-center rounded-full " + (open ? "bg-sunken stroke" : "hover:bg-sunken")}>
      <span className="text-[22px] leading-none">{shown}</span>
    </button>
  );
}

function ToneRow({ tone, onChoose }: { tone: SkinTone; onChoose: (t: SkinTone) => void }) {
  const [, , variations] = useSkinTone("✋");
  return (
    <div role="radiogroup" aria-label="Skin tone" className="flex shrink-0 items-center gap-1 border-b border-hairline px-2 pb-1 fade-in">
      <span className="mr-auto pl-1.5 text-[13px] font-semibold text-muted">Skin tone</span>
      {variations.map((v) => (
        <button key={v.skinTone} type="button" role="radio" aria-checked={v.skinTone === tone} aria-label={v.skinTone === "none" ? "Default" : v.skinTone.replace("-", " ")}
          onClick={() => onChoose(v.skinTone)}
          className={"grid size-11 place-items-center rounded-full " + (v.skinTone === tone ? "bg-agent stroke" : "hover:bg-sunken")}>
          <span className="text-[22px] leading-none">{v.emoji}</span>
        </button>
      ))}
    </div>
  );
}

const quick = ["👍", "❤️", "😂", "🎉", "🙏", "👀"];

/** QuickReactions: the six everyday reactions and a way to all of them.
 *  `chosen` are the ones this person already added (pressed). */
export function QuickReactions({ onPick, onMore, chosen = [] }: { onPick: (emoji: string) => void; onMore: () => void; chosen?: string[] }) {
  return (
    <div role="group" aria-label="React" className="inline-flex items-center gap-0.5 rounded-full bg-surface p-1 stroke">
      {quick.map((e) => {
        const on = chosen.includes(e);
        return (
          <button key={e} type="button" onClick={() => onPick(e)} aria-pressed={on} aria-label={"React " + e}
            className={"grid size-11 place-items-center rounded-full transition-transform duration-[180ms] hover:-translate-y-0.5 hover:bg-sunken motion-reduce:hover:translate-y-0 "
              + (on ? "bg-agent [box-shadow:inset_0_0_0_1.5px_var(--an-agent-ink)]" : "")}>
            <span className="text-[22px] leading-none">{e}</span>
          </button>
        );
      })}
      <span aria-hidden="true" className="mx-0.5 h-6 w-px bg-hairline" />
      <button type="button" onClick={onMore} aria-label="More reactions" title="More reactions" className="grid size-11 place-items-center rounded-full hover:bg-sunken">
        <IconMoodPlus size={22} stroke={1.8} className="text-text-2" />
      </button>
    </div>
  );
}

/** useEmojiPreload starts loading emoji data before the picker opens (the
 *  first open is then instant). */
export function useEmojiPreload(when: boolean) {
  useEffect(() => {
    if (!when) return;
    const c = new AbortController();
    Promise.resolve(defaultEmojiDataResolver("en", { emojibaseUrl: emojiBase, signal: c.signal })).catch(() => {});
    return () => c.abort();
  }, [when]);
}
