// Office's identity (Comic kit slot, skin/README.md): a plain workspace for
// people who want a messenger that looks like the tools they already use.

export const skin = {
  id: "office",
  name: "Office",
  blurb: "Plain and quiet, like a workplace messenger: no stickers, no noise.",
  storage: "office",
  themeKey: "office.theme",
};

/** Headings the skin may word its own way; actions keep AgentNet's words. */
export const words = {
  chats: "Chats",
};

/** A grey rounded square with a hash: a plain channel mark. */
export function Mark() {
  return (
    <svg viewBox="0 0 44 44" className="size-11 shrink-0" aria-hidden="true">
      <rect x="1" y="1" width="42" height="42" rx="8" fill="#1F2024" />
      <path d="M17 12l-3 20M30 12l-3 20M12 18h21M11 26h21" stroke="#FFFFFF" strokeWidth="2.6" strokeLinecap="round" />
    </svg>
  );
}
