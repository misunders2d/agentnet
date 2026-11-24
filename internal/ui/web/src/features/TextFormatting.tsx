import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode, type RefObject } from "react";
import { Dialog } from "@base-ui/react/dialog";
import { Menu } from "@base-ui/react/menu";
import { IconChevronRight } from "@tabler/icons-react";
import { useModal, usePortal } from "../owned";
import { Button } from "../ui/Button";
import { coarse } from "./Composer.focus";
import type { Span } from "./Composer.mentions";
import { fieldSelection, formats, formatText, type Format, type Selection } from "./TextFormatting.model";

const mac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
const popup = "min-w-44 max-h-[var(--available-height)] overflow-y-auto rounded-xl bg-surface p-1 text-ink outline-none stroke shadow-pop";
const item = "flex min-h-9 cursor-pointer items-center justify-between gap-4 rounded-lg px-2.5 text-sm outline-none select-none data-[highlighted]:bg-sunken lg:min-h-8";

type LinkSelection = { scope: string; text: string; selection: Selection };

export function useTextFormatting(
  field: RefObject<HTMLTextAreaElement | null>, scope: string, text: string, refs: Span[],
  write: (value: string, refs: Span[]) => void, browser: boolean,
) {
  const trigger = useId();
  const [hasSelection, setHasSelection] = useState(false);
  const [menu, setMenu] = useState<{ anchor?: { getBoundingClientRect(): DOMRect } } | null>(null);
  const [link, setLink] = useState<LinkSelection | null>(null);
  const [url, setURL] = useState("");
  const restore = useRef<{ text: string; selection: Selection } | null>(null);
  const portal = usePortal();
  const modal = useModal(!!link);
  const touch = coarse();
  useEffect(() => { setLink(null); setMenu(null); setHasSelection(false); restore.current = null; }, [scope]);
  useEffect(() => {
    const element = field.current;
    if (!touch || !element) return;
    // Chromium's native textarea selection events can stay inside the
    // skin's shadow root, outside React's delegated selection listener.
    const read = () => setHasSelection(!!fieldSelection(element));
    element.addEventListener("select", read);
    element.addEventListener("selectionchange", read);
    return () => {
      element.removeEventListener("select", read);
      element.removeEventListener("selectionchange", read);
    };
  }, [field, scope, touch]);

  useLayoutEffect(() => {
    const change = restore.current;
    if (change?.text !== text || !field.current) return;
    restore.current = null;
    const {from, to, direction} = change.selection;
    field.current.setSelectionRange(from, to, direction);
  }, [text, field]);

  function select(element: HTMLTextAreaElement) {
    if (touch) setHasSelection(!!fieldSelection(element));
  }

  function commit(style: Format, selection: Selection, href = "") {
    const change = formatText(text, refs, selection, style, href);
    if (!change) return false;
    restore.current = { text: change.value, selection: change.selection };
    write(change.value, change.refs);
    return true;
  }

  function run(style: Format) {
    const element = field.current;
    const selection = element && fieldSelection(element);
    if (!selection || !element || element.value !== text) return;
    if (style === "link") {
      setURL("");
      setLink({ scope, text, selection });
    } else {
      commit(style, selection);
      element.focus(touch ? undefined : { preventScroll: true });
    }
  }

  function key(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (!(event.ctrlKey || event.metaKey) || event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229 || !fieldSelection(event.currentTarget)) return false;
    const format = formats.find(action => action.key === event.code && (event.altKey
      ? "browserShortcut" in action && event.ctrlKey && !event.shiftKey && !event.getModifierState("AltGraph")
      : action.shortcut.startsWith("Shift+") === event.shiftKey));
    if (!format) return false;
    event.preventDefault();
    event.stopPropagation();
    run(format.id);
    return true;
  }

  function items() {
    return formats.map(format => {
      const primary = (mac ? "⌘" : "Ctrl+") + format.shortcut;
      const alternate = "browserShortcut" in format ? "Ctrl+Alt+" + format.browserShortcut : "";
      return <Menu.Item key={format.id} className={item} onClick={() => run(format.id)}
        title={alternate ? primary + " / " + alternate : undefined}>
        {format.label}
        {!touch && <span className="text-[11px] text-muted" aria-hidden="true">{browser && alternate ? alternate : primary}</span>}
      </Menu.Item>;
    });
  }

  function context(event: MouseEvent, disabled: boolean) {
    const element = field.current;
    if (disabled || event.shiftKey || !element || event.target !== element || !fieldSelection(element)) return;
    event.preventDefault();
    event.stopPropagation();
    const { left, top } = element.getBoundingClientRect();
    const x = event.clientX || left, y = event.clientY || top;
    setMenu({ anchor: { getBoundingClientRect: () => new DOMRect(x, y, 0, 0) } });
  }

  function controls(children: ReactNode, { disabled = false, className = "" } = {}) {
    const selected = hasSelection && !disabled;
    const position = "z-50 outline-none";
    const validURL = /^(https?:\/\/|mailto:)[^<>\s]+$/i.test(url);
    return <div className={"relative min-w-0 " + className} onContextMenu={event => context(event, disabled)}>
      {children}
      <Menu.Root open={!!menu} triggerId={trigger} modal={false} onOpenChange={open => setMenu(current => open ? current || {} : null)}>
        <Menu.Trigger id={trigger} hidden={!selected || !touch} onMouseDown={event => event.preventDefault()}
          className="absolute right-2 bottom-full z-30 mb-1 min-h-11 rounded-full bg-surface px-3 text-sm font-semibold text-ink stroke shadow-pop">Formatting</Menu.Trigger>
        <Menu.Portal container={portal}>
          <Menu.Positioner anchor={menu?.anchor} positionMethod={menu?.anchor ? "fixed" : "absolute"} side={touch ? "top" : "bottom"} align={touch ? "end" : "start"}
            className={position} sideOffset={4} collisionPadding={8}>
            <Menu.Popup className={popup} finalFocus={() => link ? false : field.current}>
              {touch ? items() : <Menu.SubmenuRoot>
                <Menu.SubmenuTrigger className={item} openOnHover={false}>Formatting <IconChevronRight size={15} aria-hidden /></Menu.SubmenuTrigger>
                <Menu.Portal container={portal}>
                  <Menu.Positioner className={position} sideOffset={4} collisionPadding={8}>
                    <Menu.Popup className={popup}>{items()}</Menu.Popup>
                  </Menu.Positioner>
                </Menu.Portal>
              </Menu.SubmenuRoot>}
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>
      <Dialog.Root open={!!link} onOpenChange={open => { if (!open) setLink(null); }} modal="trap-focus">
        <Dialog.Portal container={portal}>
          <Dialog.Backdrop className="fixed inset-0 z-50 bg-scrim/40" />
          <Dialog.Popup {...modal} finalFocus={field}
            className="fixed top-1/2 left-1/2 z-50 w-[min(380px,calc(100%-24px))] -translate-1/2 rounded-2xl bg-canvas p-4 text-ink outline-none stroke shadow-pop">
            <Dialog.Title className="mb-2 text-lg font-bold">Add link</Dialog.Title>
            <form onSubmit={event => {
              event.preventDefault();
              event.stopPropagation();
              if (link?.scope === scope && link.text === text && validURL && commit("link", link.selection, url)) setLink(null);
            }}>
              <input aria-label="Link URL" value={url} onChange={event => setURL(event.target.value)} autoComplete="off" inputMode="url"
                className="min-h-11 w-full rounded-lg bg-surface px-2 text-[16px] stroke" />
              <div className="mt-3 flex justify-end gap-2">
                <Dialog.Close render={<Button variant="ghost">Cancel</Button>} />
                <Button type="submit" variant="act" disabled={!validURL}>Apply link</Button>
              </div>
            </form>
          </Dialog.Popup>
        </Dialog.Portal>
      </Dialog.Root>
    </div>;
  }

  return { select, key, controls };
}
