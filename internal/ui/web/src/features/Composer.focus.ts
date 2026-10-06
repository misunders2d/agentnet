// Where the cursor goes after a choice made around the message field
// (MEL-519). On a phone, focusing the field opens the keyboard over half
// the chat, so there the composer puts the cursor back after a choice (an
// intent, a chip's cancel, "Don't ask") only when the person was typing as
// the choice started; a Reply they chose always puts it there. A desktop
// always gets the cursor back, without scrolling. On a touch screen a focus
// never uses preventScroll: the browser must bring the field into view
// above the keyboard. Every choice in or for the composer uses this one helper.
import { useRef, type RefObject } from "react";
import { focusedIn } from "../owned";

/** coarse: the device's main pointer is a finger (a phone or a tablet). */
export const coarse = (): boolean => matchMedia("(pointer: coarse)").matches;

/** place puts the cursor in a message field, synchronously: iOS opens its
 *  keyboard only for a focus() inside the tap's own handler. */
function place(ta: HTMLTextAreaElement) {
  if (ta.disabled || focusedIn(ta) === ta) return;
  if (coarse()) ta.focus();
  else ta.focus({ preventScroll: true });
}

/** useFieldFocus is the composer's rule. Spread `watch` on the composer's
 *  form: as each tap or key press there starts, it notes whether the field
 *  had the cursor. Call focusField() after a choice made around the field,
 *  and focusField(true) when the person asked for the field (Reply). */
export function useFieldFocus(field: RefObject<HTMLTextAreaElement | null>) {
  const typing = useRef(false); // the field had the cursor as the current choice started
  const note = () => { const ta = field.current; typing.current = !!ta && focusedIn(ta) === ta; };
  const focusField = (asked = false) => {
    const ta = field.current, was = typing.current;
    typing.current = false;
    if (ta && (asked || was || !coarse())) place(ta);
  };
  return { watch: { onPointerDownCapture: note, onKeyDownCapture: note }, focusField };
}

/** focusComposer: a Reply chosen outside the composer (a message's sheet,
 *  an approval card) puts the cursor in that conversation's field within
 *  the same tap. Nothing happens when that conversation's composer is not
 *  shown, or while a dialog keeps the app inert (close it first, with
 *  flushSync, so the field can take focus in the same tap). */
export function focusComposer(root: HTMLElement, conv: string) {
  const ta = root.querySelector<HTMLTextAreaElement>('form[data-conv="' + CSS.escape(conv) + '"] textarea');
  if (ta && !ta.closest("[inert]")) place(ta);
}
