// The skin's identity (skin/README.md): its manifest id and name, the words
// Settings uses for it, its mark, the prefix of everything it keeps in this
// browser's storage, and the few headings a skin may word its own way. An
// overlay skin replaces this file; nothing here grants anything.

export const skin = {
  /** The manifest id (skin.json "id"). */
  id: "comic",
  name: "Comic",
  /** One line for Settings → Appearance → Skin. */
  blurb: "AgentNet’s own skin: chats, agents and Review as bright stickers.",
  /** localStorage key prefix: drafts, emoji skin tone (docs/UI_SKINS.md, Storage). */
  storage: "agentnet.messenger",
  /** The remembered light/dark choice. */
  themeKey: "agentnet.theme",
};

/** Headings the skin may word its own way; actions keep AgentNet's words. */
export const words = {
  chats: "Chats",
};

/** The skin's mark, 44px: Comic's speech bubbles in its sticker style. */
export function Mark() {
  return (
    <svg viewBox="0 0 44 44" className="size-11 shrink-0" aria-hidden="true">
      <rect x="1" y="1" width="42" height="42" rx="12" fill="#FFD43B" stroke="#1B1530" strokeWidth="1.5" />
      <path d="M9 13h17a3 3 0 0 1 3 3v6a3 3 0 0 1-3 3h-9l-5 4v-4h-3a3 3 0 0 1-3-3v-6a3 3 0 0 1 3-3z" fill="#FFFFFF" stroke="#1B1530" strokeWidth="1.5" strokeLinejoin="round" />
      <path d="M33 20h2a3 3 0 0 1 3 3v5a3 3 0 0 1-3 3h-1v3l-4-3h-7a3 3 0 0 1-3-3" fill="#A897FF" stroke="#1B1530" strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  );
}
