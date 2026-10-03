// Avatars: people are circles with their initials on a warm tint; agents are
// rounded squares with a robot face (our own drawing) and one of eight
// hues picked from their key; guests get a dashed coral ring everywhere.
import { hue, initials } from "../model";

const PEOPLE = ["#FFB4A2", "#B5E3C4", "#A8D8FF", "#FFD6A5", "#D9C2FF", "#FFC6E0", "#C7F0E8", "#F6E3A1"];
const AGENTS = ["#8E7BFF", "#43C59E", "#F2A93B", "#4CB4E7", "#E86FA6", "#9BCB45", "#F07F5A", "#6E8BF5"];

type Size = 20 | 24 | 28 | 32 | 36 | 40 | 48 | 56 | 72;

export function PersonAvatar({ name, seed, size = 40, online, guest, me }: { name: string; seed: string; size?: Size; online?: boolean | null; guest?: boolean; me?: boolean }) {
  const bg = PEOPLE[hue(seed)];
  const fs = Math.round(size * 0.4);
  return (
    <span className="relative inline-flex shrink-0" aria-hidden="true" data-size={size}>
      <span className={"grid place-items-center rounded-full font-display font-bold text-[#1B1530] select-none stroke" + (guest ? " [outline:2px_dashed_var(--an-guest)] outline-offset-2" : "")}
        style={{ width: size, height: size, background: bg, fontSize: fs }} data-me={me ? "" : undefined}>
        {initials(name)}
      </span>
      {online != null && size >= 28 && <Presence online={online} size={size} />}
    </span>
  );
}

export function Presence({ online, size }: { online: boolean; size: number }) {
  const d = size >= 48 ? 14 : 12;
  return (
    <span className={"absolute rounded-full border-2 border-canvas " + (online ? "bg-online" : "bg-canvas [box-shadow:inset_0_0_0_2px_var(--an-muted)]")}
      title={online ? "Online" : "Offline"} style={{ width: d, height: d, right: -2, bottom: -2 }} />
  );
}

export type Mood = "neutral" | "working" | "done" | "waiting" | "asleep";

export function AgentAvatar({ seed, size = 40, mood = "neutral", guest, device }: { seed: string; size?: Size; mood?: Mood; guest?: boolean; device?: "laptop" | "server" | "phone" }) {
  const color = AGENTS[hue(seed)];
  const r = Math.round(size * 0.3);
  const face = size >= 20;
  const full = size >= 32;
  return (
    <span className="relative inline-flex shrink-0" aria-hidden="true">
      <span className={"grid place-items-center stroke " + (guest ? "[outline:2px_dashed_var(--an-guest)] outline-offset-2" : "") + (mood === "asleep" ? " saturate-[.6] opacity-80" : "")}
        style={{ width: size, height: size, borderRadius: r, background: color }}>
        {face && <RobotFace size={size} mood={mood} full={full} />}
      </span>
      {device && size >= 32 && (
        <span className="absolute -bottom-1 -right-1 grid place-items-center rounded-md bg-surface stroke text-ink" style={{ width: Math.round(size * 0.42), height: Math.round(size * 0.42) }}>
          <DeviceGlyph kind={device} />
        </span>
      )}
    </span>
  );
}

function RobotFace({ size, mood, full }: { size: number; mood: Mood; full: boolean }) {
  // A visor with two eyes; the mouth and antenna only at 32px and up.
  const s = size * 0.78;
  const eyes = mood === "asleep" ? <><path d="M8 13h4M20 13h4" /></>
    : mood === "done" ? <><path d="M7.5 13.5q2-2.4 4 0M20.5 13.5q2-2.4 4 0" /></>
    : mood === "waiting" ? <><circle cx="10" cy="11.6" r="1.7" fill="currentColor" stroke="none" /><circle cx="22" cy="11.6" r="1.7" fill="currentColor" stroke="none" /></>
    : <><circle className={mood === "working" ? "robot-scan" : ""} cx="10" cy="13" r="1.9" fill="currentColor" stroke="none" /><circle className={mood === "working" ? "robot-scan" : ""} cx="22" cy="13" r="1.9" fill="currentColor" stroke="none" /></>;
  return (
    <svg viewBox="0 0 32 32" width={s} height={s} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" className="text-[#1B1530]">
      {full && <><path d="M16 3.5v3" /><circle cx="16" cy="3" r="1.4" fill="currentColor" stroke="none" /></>}
      <rect x="4" y="7" width="24" height="13" rx="6" fill="#FFFFFF" />
      {eyes}
      {full && (mood === "done" ? <path d="M12 24.5q4 3 8 0" /> : mood === "working" ? <path d="M12.5 25h7" /> : <path d="M13 24.5q3 1.6 6 0" />)}
    </svg>
  );
}

function DeviceGlyph({ kind }: { kind: "laptop" | "server" | "phone" }) {
  return (
    <svg viewBox="0 0 16 16" width="70%" height="70%" fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round">
      {kind === "laptop" && <><rect x="3" y="3.5" width="10" height="7" rx="1" /><path d="M1.5 12.5h13" /></>}
      {kind === "server" && <><rect x="3" y="2.5" width="10" height="4.5" rx="1" /><rect x="3" y="9" width="10" height="4.5" rx="1" /><path d="M5.5 4.75h.01M5.5 11.25h.01" /></>}
      {kind === "phone" && <><rect x="5" y="1.5" width="6" height="13" rx="1.4" /><path d="M7.5 12h1" /></>}
    </svg>
  );
}

export function GroupAvatar({ names, seed, size = 40 }: { names: string[]; seed: string; size?: Size }) {
  const shown = names.slice(0, 3);
  if (shown.length < 2) return <PersonAvatar name={names[0] || "Group"} seed={seed} size={size} />;
  const small = (Math.round(size * 0.62) as Size);
  return (
    <span className="relative inline-block shrink-0" aria-hidden="true" style={{ width: size, height: size }}>
      <span className="absolute left-0 top-0"><PersonAvatar name={shown[0]} seed={seed + shown[0]} size={small} /></span>
      <span className="absolute right-0 bottom-0"><PersonAvatar name={shown[1]} seed={seed + shown[1]} size={small} /></span>
    </span>
  );
}
