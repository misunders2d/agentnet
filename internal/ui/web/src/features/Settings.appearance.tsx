// Appearance: the theme (remembered in this browser only) and which
// interface AgentNet shows. Switching interface reloads the page, so it
// first makes sure nothing unsent is lost.
import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { IconCheck } from "@tabler/icons-react";
import { useEffect, useReducer, useRef, useState, useSyncExternalStore } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import type { Host } from "../host";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Tag } from "../ui/Tag";
import { Card, GroupLabel, Hint, PageHead } from "./Settings.parts";

export type Theme = "system" | "light" | "dark";
const themeKey = "agentnet.theme";

export function savedTheme(): Theme {
  try {
    const t = localStorage.getItem(themeKey);
    if (t === "light" || t === "dark") return t;
  } catch { /* storage may be off: follow the system */ }
  return "system";
}

/** applyTheme sets the page's theme; "system" follows the device's setting. */
export function applyTheme(t: Theme) {
  if (t === "system") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = t;
}

// The chosen theme, shared by the picker and the list's summary.
let chosen: Theme | null = null;
const themeSubs = new Set<() => void>();
export const useTheme = () => useSyncExternalStore(
  (f) => { themeSubs.add(f); return () => { themeSubs.delete(f); }; },
  () => (chosen ??= savedTheme()));

function setTheme(t: Theme) {
  chosen = t;
  applyTheme(t);
  try { localStorage.setItem(themeKey, t); } catch { /* remembered for this visit only */ }
  for (const f of themeSubs) f();
}

export const themeWords: Record<Theme, string> = { system: "Automatic", light: "Light", dark: "Dark" };

// The theme previews are little drawings of a chat in each palette.
const swatch = {
  light: { canvas: "#FFF8EC", theirs: "#FFFFFF", mine: "#FFF0B8", agent: "#EEEAFF", line: "#1B1530" },
  dark: { canvas: "#15111E", theirs: "#241E33", mine: "#3B3317", agent: "#2B2452", line: "#5A5075" },
};

function Preview({ t }: { t: "light" | "dark" }) {
  const c = swatch[t];
  return (
    <svg viewBox="0 0 120 72" className="block h-full w-full" aria-hidden="true">
      <rect width="120" height="72" fill={c.canvas} />
      <rect x="10" y="10" width="62" height="14" rx="7" fill={c.theirs} stroke={c.line} strokeWidth="1.5" />
      <rect x="50" y="30" width="60" height="14" rx="7" fill={c.mine} stroke={c.line} strokeWidth="1.5" />
      <rect x="10" y="50" width="70" height="14" rx="4" fill={c.agent} stroke={c.line} strokeWidth="1.5" />
    </svg>
  );
}

type Skin = T.Skin & { local?: boolean };
type LocalSkinsHost = Host & { manageLocalSkins?: (root: HTMLElement) => void };

const skinWords = (s: Skin) =>
  s.id === "default" ? "This messenger."
    : s.id === "classic" ? "The earlier AgentNet, with its Classic, Comic and Zoom views."
    : s.local ? "Stored in this browser. It can read your chats and act as you."
    : "Installed on this computer. It can read your chats and act as you.";

export function AppearanceSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const host = store.host as LocalSkinsHost;
  const theme = useTheme();
  const [, refresh] = useReducer((n: number) => n + 1, 0);
  const [target, setTarget] = useState<Skin | null>(null);
  useEffect(() => {
    // The host refreshes its list in place when a package is imported or removed.
    window.addEventListener("agentnet-skins-change", refresh);
    return () => window.removeEventListener("agentnet-skins-change", refresh);
  }, []);
  const skins = host.skins as Skin[];

  return (
    <>
      <PageHead title="Appearance" titleRef={titleRef} lead="How AgentNet looks here. Only this browser remembers it." />
      <div className="space-y-6">
        <section aria-labelledby="appearance-theme">
          <GroupLabel id="appearance-theme">Theme</GroupLabel>
          <RadioGroup value={theme} onValueChange={(v) => setTheme(v as Theme)} aria-labelledby="appearance-theme" className="grid grid-cols-3 gap-2.5">
            {(["system", "light", "dark"] as Theme[]).map((t) => (
              <label key={t} className={"press group flex cursor-pointer flex-col overflow-hidden rounded-2xl stroke " + (theme === t ? "bg-agent-fill text-ink shadow-pop-sm" : "bg-surface hover:bg-sunken")}>
                <span className="relative block aspect-[5/3] border-b-[1.5px] border-outline lg:border-b">
                  {t === "system"
                    ? <span className="absolute inset-0 grid grid-cols-2"><span className="overflow-hidden"><span className="block h-full w-[200%]"><Preview t="light" /></span></span><span className="overflow-hidden"><span className="-ml-[100%] block h-full w-[200%]"><Preview t="dark" /></span></span></span>
                    : <Preview t={t} />}
                </span>
                <span className="flex min-h-11 items-center justify-between gap-1 px-2.5 py-1.5">
                  <span className="text-[15px] font-semibold">{themeWords[t]}</span>
                  <Radio.Root value={t} className={"grid size-6 shrink-0 place-items-center rounded-full stroke " + (theme === t ? "bg-[#1B1530] text-act" : "bg-surface")}>
                    <Radio.Indicator><IconCheck size={14} stroke={3} aria-hidden="true" /></Radio.Indicator>
                  </Radio.Root>
                </span>
              </label>
            ))}
          </RadioGroup>
          <Hint className="mt-2.5 px-1">Automatic follows your device’s light or dark setting.</Hint>
        </section>

        <section aria-labelledby="appearance-interface">
          <GroupLabel id="appearance-interface">Interface</GroupLabel>
          <Card className="divide-y divide-hairline">
            {skins.map((s) => {
              const here = s.id === "default";
              return (
                <div key={s.id} className="flex min-h-16 items-center gap-3 px-4 py-3">
                  <div className="min-w-0 flex-1">
                    <p className="flex flex-wrap items-center gap-2 font-semibold">{s.name}{here && <Tag tone="ok">In use</Tag>}</p>
                    <Hint>{skinWords(s)}</Hint>
                  </div>
                  {!here && <Button size="sm" onClick={() => setTarget(s)}>Switch</Button>}
                </div>
              );
            })}
          </Card>
          {host.manageLocalSkins && <ImportSkins host={host} />}
        </section>
      </div>
      <SwitchInterface skin={target} onClose={() => setTarget(null)} />
    </>
  );
}

// ImportSkins mounts the host's own package manager (local-skins.mjs): it
// checks and stores a package in this browser. Choosing it afterwards
// goes through the host's trust card.
function ImportSkins({ host }: { host: LocalSkinsHost }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const root = ref.current;
    if (!root || !host.manageLocalSkins) return;
    host.manageLocalSkins(root);
    return () => root.replaceChildren();
  }, [host]);
  return (
    <Card className="mt-3 p-4">
      <div ref={ref} className={"space-y-2 text-[15px] [&_h3]:font-semibold [&_h3]:text-ink [&_p]:text-text-2 [&_p]:text-[13px] [&_[role=status]]:font-semibold [&_[role=status]]:text-ink "
        + "[&_label]:flex [&_label]:min-h-11 [&_label]:w-fit [&_label]:cursor-pointer [&_label]:items-center [&_label]:gap-2 [&_label]:rounded-full [&_label]:stroke [&_label]:bg-surface [&_label]:px-4 [&_label]:font-semibold [&_label]:hover:bg-sunken "
        + "[&_input[type=file]]:sr-only [&_label:has(:focus-visible)]:[outline:3px_solid_var(--an-agent-ink)] [&_label:has(:focus-visible)]:outline-offset-2 "
        + "[&_button]:ml-2 [&_button]:min-h-11 [&_button]:rounded-full [&_button]:px-3 [&_button]:font-semibold [&_button]:text-danger [&_button]:hover:bg-danger-bg"} />
    </Card>
  );
}

function SwitchInterface({ skin, onClose }: { skin: Skin | null; onClose: () => void }) {
  const store = useApp();
  const drafts = Object.values(store.get().drafts);
  const files = drafts.some((d) => d.files && d.files.length);
  const text = store.hasUnsent();
  const go = () => { if (skin) store.host.selectSkin(skin.id); };
  return (
    <Sheet open={!!skin} onOpenChange={(open) => { if (!open) onClose(); }} title={skin ? "Switch to " + skin.name + "?" : ""}
      footer={<div className="flex flex-col gap-2 sm:flex-row-reverse"><Button variant="act" className="sm:flex-1" disabled={files} onClick={go}>Switch</Button><Button variant="outline" className="sm:flex-1" onClick={onClose}>Stay here</Button></div>}>
      {skin && (
        <div className="space-y-3">
          <p>AgentNet reloads with {skin.name}.{skin.id !== "classic" && " You can come back here from its AgentNet menu."}</p>
          {files ? <p className="font-semibold text-danger">Files are waiting in a message you haven’t sent. Send or remove them first: they can’t survive the reload.</p>
            : text ? <p>Your unsent text stays saved here for when you come back.</p> : null}
          {skin.id !== "classic" && <Hint>It can read your chats and act as you, including sending messages and approving work. You’ll be asked to trust it first.</Hint>}
        </div>
      )}
    </Sheet>
  );
}
