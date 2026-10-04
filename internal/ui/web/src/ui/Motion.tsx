// Comic's motion: what changes on screen never goes through a blank frame.
// Swap keeps what was shown on top of what replaces it while it fades away
// (the new content is there, complete, underneath from the first frame);
// usePresence keeps a closing layer drawn while it slides out. The motion
// itself is CSS (styles.css, "motion"): with reduced motion it is opacity
// only. Nothing waits for an animation: content that is ready shows at
// once, and what is leaving is inert, so input is never blocked.
import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";

/** Leaving is true inside content that is on its way out (a Swap's old
 *  side): it is only a picture of what was there, so it opens no sheets. */
export const Leaving = createContext(false);
export const useLeaving = () => useContext(Leaving);

type Gone = { id: string; node: ReactNode };
const SETTLE = new Set(["fade-in", "pop-in"]); // entrance animations (styles.css)

/** Swap shows children, keyed by id. When id changes, the previous
 *  children stay mounted (same DOM, same scroll) above the new ones and
 *  leave with the `leave` animation, while the new ones enter with
 *  `enter`. The first render does not animate. */
export function Swap({ id, children, className = "", side = "", enter, leave, ms, label }: {
  id: string; children: ReactNode;
  className?: string;                                // the Swap's own box
  side?: string;                                     // the layout of each side (both fill the box)
  enter: string; leave: string; ms: number;        // CSS classes (styles.css) and the leave duration
  label?: string;                                    // a data-swap name, for checks
}) {
  const [st, setSt] = useState<{ id: string; gone: Gone | null }>({ id, gone: null });
  const last = useRef<ReactNode>(children);
  if (st.id !== id) setSt({ id, gone: { id: st.id, node: last.current } }); // during render: the first frame already has both
  useLayoutEffect(() => { last.current = children; });
  const gone = st.gone;
  // What arrives with a swap is there at once: the entrance fades of its own
  // parts (fade-in, pop-in) are finished before the first frame, so it never
  // shows faded under what leaves. Parts added later animate as usual.
  const cur = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!gone || !cur.current?.getAnimations || typeof CSSAnimation === "undefined") return;
    for (const a of cur.current.getAnimations({ subtree: true })) {
      if (a instanceof CSSAnimation && SETTLE.has(a.animationName)) a.finish();
    }
  }, [gone]);
  useEffect(() => {
    if (!gone) return;
    const t = setTimeout(() => setSt((s) => (s.gone === gone ? { ...s, gone: null } : s)), ms);
    return () => clearTimeout(t);
  }, [gone, ms]);
  // The new side comes first and the old one after it, so React keeps the
  // old side's elements where they are (moving them would reset scrolling).
  return (
    <div className={"relative isolate " + className} data-swap={label}>
      {/* Both sides have the same tree shape (div > Leaving > content): the side that
          leaves keeps its mounted content, it is not drawn again from scratch. */}
      <div key={id} ref={cur} className={side + " " + (gone ? enter : "")} data-swap-side="current"><Leaving.Provider value={false}>{children}</Leaving.Provider></div>
      {gone && (
        // Opaque, over the new side, which is complete under it from the first frame.
        <div key={gone.id} inert aria-hidden="true" className={side + " an-swap-gone " + leave} data-swap-side="leaving">
          <Leaving.Provider value={true}>{gone.node}</Leaving.Provider>
        </div>
      )}
    </div>
  );
}

/** usePresence keeps the last value drawn for ms after it becomes null,
 *  so a layer can slide away: { shown, leaving }. A new value cancels the
 *  leaving at once. */
export function usePresence<T>(value: T | null, ms: number): { shown: T | null; leaving: boolean } {
  const [st, setSt] = useState<{ last: T | null; leaving: boolean }>({ last: value, leaving: false });
  if (value !== null && value !== st.last) setSt({ last: value, leaving: false });
  else if (value === null && st.last !== null && !st.leaving) setSt({ last: st.last, leaving: true });
  useEffect(() => {
    if (!st.leaving) return;
    const t = setTimeout(() => setSt((s) => (s.leaving ? { last: null, leaving: false } : s)), ms);
    return () => clearTimeout(t);
  }, [st.leaving, ms]);
  return { shown: value ?? st.last, leaving: value === null && st.leaving };
}

/** useSettled is false for ms after key changes to a non-empty value, then
 *  true: a layer that slid in has finished arriving. */
export function useSettled(key: string, ms: number): boolean {
  const [done, setDone] = useState<string>(key);
  useEffect(() => {
    if (!key) { setDone(""); return; }
    const t = setTimeout(() => setDone(key), ms);
    return () => clearTimeout(t);
  }, [key, ms]);
  return done === key;
}

/** Durations (ms) of the CSS motion in styles.css; leaving content is
 *  removed when its animation has run. */
export const MOTION = { push: 280, pop: 240, pane: 220, page: 240, topic: 200, ready: 160 } as const;
