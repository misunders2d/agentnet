import { useState } from "react";
import { IconLink } from "@tabler/icons-react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";

// The optional projection is also the read capability: guests and agent hosts
// never receive it. Drafts and their CAS revision stay local across pushes.
export function ConversationLinks({ thread }: { thread: T.DMThread }) {
  const store = useApp();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<{ base: T.ConversationResources; links: T.ResourceLink[] } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const resources = thread.resources;
  if (thread.kind !== "group" || thread.role !== "member" || !resources) return null;
  const currentLinks = resources.links ?? [];
  const editable = !thread.frozen;
  const changed = !!draft && (draft.base.rev !== resources.rev || draft.base.writer !== resources.writer);
  const edit = () => {
    setDraft({ base: resources, links: currentLinks.map(link => ({ ...link })) });
    setError("");
  };
  const close = (value: boolean) => {
    if (busy) return;
    setOpen(value);
    if (!value) { setDraft(null); setError(""); }
  };
  const update = (index: number, field: keyof T.ResourceLink, value: string) => {
    setDraft(current => current && ({ ...current, links: current.links.map((link, i) => i === index ? { ...link, [field]: value } : link) }));
  };
  const save = async () => {
    if (!draft || busy || !editable) return;
    const links = draft.links.map(link => ({ name: link.name.trim(), url: link.url.trim() }));
    for (const link of links) {
      if (!link.name || /\p{Cc}/u.test(link.name)) { setError("Give every link a name without control characters."); return; }
      try {
        const url = new URL(link.url);
        if (!/^https?:$/.test(url.protocol) || !url.hostname || url.username || url.password) throw Error();
      } catch { setError("Use a full http:// or https:// URL without a username or password."); return; }
    }
    // Capture the workspace-bound API before awaiting; a response never writes
    // into another conversation or clears any chat/composer state.
    const api = store.api;
    setBusy(true);
    setError("");
    try {
      await api.setConversationResources({ conv: thread.id, links, rev: draft.base.rev, writer: draft.base.writer });
      if (store.isActive()) {
        setDraft(null);
        setOpen(false);
        store.toast("Group links saved.", "ok");
        void store.refetch();
      }
    } catch (e) {
      if (store.isActive()) setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const field = "min-h-12 w-full min-w-0 rounded-xl stroke bg-surface px-3.5 text-[16px] text-ink outline-none focus-visible:outline-3 focus-visible:outline-agent-ink";
  return <>
    <Button variant="ghost" size="sm" className="shrink-0" icon={<IconLink size={19} />} onClick={() => setOpen(true)}>Links</Button>
    <Sheet open={open} onOpenChange={close} title="Group links" description="Named links shared with group members. Guests don’t receive them."
      footer={<div className="flex flex-col gap-2">
        {error && <p role="alert" className="text-[14px] font-semibold text-danger">{error}</p>}
        {draft ? <>
          {changed && <p role="status" className="text-[14px] text-text-2">The links changed while you were editing. Your draft is kept; saving this revision will fail. Load the latest links to edit them instead.</p>}
          {changed && <Button variant="outline" disabled={busy} onClick={edit}>Discard draft and load latest links</Button>}
          <Button disabled={busy || !editable} onClick={() => void save()}>{busy ? "Saving…" : "Save links"}</Button>
          <Button variant="ghost" disabled={busy} onClick={() => { setDraft(null); setError(""); }}>Cancel edit</Button>
        </> : editable && <Button onClick={edit}>Edit links</Button>}
        <Button variant="ghost" disabled={busy} onClick={() => close(false)}>Close</Button>
      </div>}>
      {draft ? <div className="flex flex-col gap-4">
        {draft.links.map((link, index) => <fieldset key={index} disabled={busy || !editable} className="min-w-0 rounded-2xl bg-sunken p-3">
          <legend className="px-1 text-[14px] font-bold">Link {index + 1}</legend>
          <label className="block text-[14px] font-bold">Name<input className={field + " mt-1"} value={link.name} autoComplete="off" onChange={e => update(index, "name", e.target.value)} /></label>
          <label className="mt-3 block text-[14px] font-bold">URL<input className={field + " mt-1"} type="url" inputMode="url" autoComplete="off" spellCheck={false} value={link.url} onChange={e => update(index, "url", e.target.value)} /></label>
          <Button variant="ghost" size="sm" className="mt-2" onClick={() => setDraft(current => current && ({ ...current, links: current.links.filter((_, i) => i !== index) }))}>Remove link {index + 1}</Button>
        </fieldset>)}
        {!draft.links.length && <p className="text-text-2">No links. Save to clear the group’s links, or add one.</p>}
        <Button variant="outline" disabled={busy || !editable} onClick={() => setDraft(current => current && ({ ...current, links: [...current.links, { name: "", url: "" }] }))}>Add link</Button>
      </div> : currentLinks.length ? <ul className="flex flex-col gap-2">
        {currentLinks.map((link, index) => <li key={index} className="min-w-0"><a href={link.url} target="_blank" rel="noopener noreferrer" className="block min-h-11 rounded-xl px-2 py-2 font-bold underline [overflow-wrap:anywhere] hover:bg-sunken">{link.name}<span className="mt-0.5 block text-[13px] font-normal text-text-2">{link.url}</span></a></li>)}
      </ul> : <p className="text-text-2">No links yet.</p>}
    </Sheet>
  </>;
}
