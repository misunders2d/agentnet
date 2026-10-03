// The screen title of OKs and Agents, with the same highlighter stripe as
// the chat list's: saturated yellow in light; in dark the same yellow at
// about a third, so the light letters stay readable where they cross it.
const stripe = "relative z-0 after:absolute after:-left-1 after:-right-1.5 after:bottom-0.5 after:-z-10 after:h-[38%] after:-rotate-[1.5deg] after:rounded after:bg-act after:content-[''] "
  + "dark:after:bg-act/35 [@media(prefers-color-scheme:dark)]:[:root:not([data-theme=light])_&]:after:bg-act/35";

export function ScreenTitle({ children }: { children: string }) {
  return <h1 className="font-display text-[28px] font-extrabold leading-none tracking-[-0.02em]"><span className={stripe}>{children}</span></h1>;
}
