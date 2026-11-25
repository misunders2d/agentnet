// Delete your account (Google Play account deletion; docs/DECISIONS.md,
// November 25, 2026, and ACCOUNT_DELETION.md). The relay erases you; this
// sheet says plainly what goes, what stays and that it cannot be undone.
import { useState, type FormEvent } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Card, Hint, input } from "./Settings.parts";

export const accountDeletionURL = "https://github.com/misunders2d/agentnet/blob/main/ACCOUNT_DELETION.md";

type Phase = { state: "idle" } | { state: "deleting" } | { state: "deleted" } | { state: "failed"; error: string };

export function DeleteAccount({ person }: { person: T.PersonView }) {
  const store = useApp();
  const [open, setOpen] = useState(false), [typed, setTyped] = useState(""), [phase, setPhase] = useState<Phase>({ state: "idle" });
  const ready = typed.trim().toUpperCase() === "DELETE" && phase.state !== "deleting";
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!ready) return;
    setPhase({ state: "deleting" });
    void store.api.deleteAccount(person.person || "").then(() => setPhase({ state: "deleted" }), err => setPhase({ state: "failed", error: errorText(err) }));
  };
  const close = (value: boolean) => { if (!value && phase.state !== "deleting") { setOpen(false); if (phase.state !== "deleted") { setTyped(""); setPhase({ state: "idle" }); } } };
  return <>
    <Card className="mt-4 p-4">
      <p className="font-semibold">Delete your account</p>
      <Hint className="mt-1">Erases you from this workspace’s server, on all your devices. It can’t be undone.</Hint>
      <Button variant="outline" className="mt-3 text-danger" onClick={() => setOpen(true)} disabled={phase.state === "deleted"}>{phase.state === "deleted" ? "Account deleted" : "Delete account…"}</Button>
    </Card>
    <Sheet open={open} onOpenChange={close} title={phase.state === "deleted" ? "Your account was deleted" : "Delete your account?"}>
      {phase.state === "deleted" ? <div role="status" className="space-y-2">
        <p>This workspace’s server erased you. All your devices are signed out of it.</p>
        <Hint>Messages and files on this device stay until you remove the workspace or the app. Copies other people already received stay with them.</Hint>
      </div> : <form onSubmit={submit} className="space-y-3">
        <p>The server erases your picture, your files, its encrypted copies of your messages, your Google sign-in link and your invitations. All your devices and assistants lose access.</p>
        <p>Your name stays in the signed record that keeps past group history verifiable, marked deleted. Copies people already received stay with them.</p>
        <Hint><a href={accountDeletionURL} target="_blank" rel="noreferrer" className="underline">What is deleted and what is kept</a></Hint>
        <label className="block font-semibold">Type DELETE to confirm
          <input className={input + " mt-1"} value={typed} autoComplete="off" onChange={e => setTyped(e.target.value)} disabled={phase.state === "deleting"} />
        </label>
        {phase.state === "deleting" && <p role="status" className="text-muted">Deleting your account…</p>}
        {phase.state === "failed" && <p role="alert" className="text-danger">Your account was not deleted: {phase.error}</p>}
        <Button type="submit" variant="danger" disabled={!ready}>{phase.state === "failed" ? "Try again" : "Delete my account"}</Button>
      </form>}
    </Sheet>
  </>;
}
