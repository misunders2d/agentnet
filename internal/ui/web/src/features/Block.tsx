// Block a person's direct messages (Google Play user-generated content
// policy; docs/DECISIONS.md, November 25, 2026). Private to you and kept on
// all your devices: their direct messages, questions and tasks are held
// quietly. Groups you share don't change.
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { Confirm } from "./Message.actions";

export function useBlocked(person?: string) {
  const store = useApp();
  return useStore(store, s => !!person && (s.blockPending[person] ?? !!s.overview?.blocked?.includes(person)));
}

export function BlockConfirm({ person, name, open, onOpenChange }: { person: string; name: string; open: boolean; onOpenChange: (open: boolean) => void }) {
  const store = useApp();
  return <Confirm open={open} onOpenChange={onOpenChange} title={"Block " + name + "?"} ok="Block"
    onOk={() => { onOpenChange(false); void store.setBlocked(person, true, name); }}>
    <p>{name} won’t be able to message you, ask your assistants questions or give them tasks directly.</p>
    <p>Groups you share don’t change. You can unblock them here at any time.</p>
  </Confirm>;
}

// BlockedBar takes the composer's place in a chat with a blocked person.
export function BlockedBar({ person, name }: { person: string; name: string }) {
  const store = useApp();
  return <div role="status" className="flex items-center gap-3 border-t border-hairline bg-surface px-4 py-3">
    <p className="min-w-0 flex-1 text-[15px] text-text-2">You blocked {name}. Their direct messages are hidden.</p>
    <Button size="sm" variant="act" onClick={() => void store.setBlocked(person, false, name)}>Unblock</Button>
  </div>;
}
