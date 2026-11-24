import { useEffect, useRef, useState } from "react";
import { type NotificationSound, errorText } from "../api";
import { useApp } from "../context";
import { Card, GroupLabel, Hint, Toggle } from "./Settings.parts";
import { Button } from "../ui/Button";

const maxSoundFile = 1 << 20;

function encodeWav(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result).split(",", 2)[1]);
    reader.onerror = () => reject(new Error("Could not read this WAV file. Choose it again."));
    reader.readAsDataURL(file);
  });
}

export function WindowsNotificationSound() {
  const { api } = useApp();
  const [saved, setSaved] = useState<NotificationSound | null>(null);
  const [draft, setDraft] = useState<NotificationSound | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [pending, setPending] = useState(false);
  const [status, setStatus] = useState(""), [error, setError] = useState("");
  const working = useRef(false);
  const picker = useRef<HTMLInputElement>(null);
  useEffect(() => {
    let mounted = true;
    api.notificationSound().then(value => {
      if (mounted) { setSaved(value); setDraft(value); }
    }, e => { if (mounted) setError(errorText(e)); });
    return () => { mounted = false; };
  }, [api]);

  const update = (patch: Partial<NotificationSound>) => {
    setDraft(value => value && { ...value, ...patch });
    setStatus("");
    setError("");
  };
  const choose = (picked: File | undefined) => {
    if (!picked) return;
    if (picked.size === 0 || picked.size > maxSoundFile || !/\.wav$/i.test(picked.name)) {
      setError("Choose a WAV file up to 1 MiB.");
      return;
    }
    setFile(picked);
    update({ source: "custom" });
  };
  const submit = async (preview: boolean) => {
    if (!draft || working.current) return;
    working.current = true;
    setPending(true);
    setError("");
    setStatus(preview ? "Starting preview…" : "Saving sound settings…");
    try {
      const payload = { source: draft.source, volume: draft.volume, revision: draft.revision, respect_dnd: draft.respect_dnd };
      const attachment = file && draft.source === "custom" ? { file_name: file.name, file: await encodeWav(file) } : {};
      const value = await api.changeNotificationSound(preview, { ...payload, ...attachment });
      if (!preview) { setSaved(value); setDraft(value); setFile(null); }
      setStatus(preview ? "Preview started." : "Sound settings saved on this device.");
    } catch (e) {
      setStatus("");
      setError(errorText(e));
    } finally {
      working.current = false;
      setPending(false);
    }
  };
  const customName = file?.name ?? draft?.file_name;
  const usable = draft?.source !== "custom" || !!customName;
  const changed = draft && (draft.source !== saved?.source || draft.volume !== saved?.volume ||
    draft.respect_dnd !== saved?.respect_dnd || (draft.source === "custom" && !!file));

  return <section aria-labelledby="windows-sound-title">
    <GroupLabel id="windows-sound-title">Notification sound</GroupLabel>
    <Card className="space-y-3 p-4">
      {draft ? <>
        <label className="block font-semibold">Sound
          <select className="mt-2 block min-h-11 w-full rounded-lg border border-hairline bg-surface px-3" value={draft.source}
            onChange={e => update({ source: e.currentTarget.value as NotificationSound["source"] })} disabled={pending}>
            <option value="system">Windows default</option>
            <option value="chime">AgentNet Chime</option>
            <option value="custom">Custom WAV</option>
          </select>
        </label>
        {draft.source === "custom" && <>
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" disabled={pending} onClick={() => picker.current?.click()}>Choose WAV file</Button>
            <input ref={picker} type="file" accept=".wav,audio/wav,audio/x-wav" hidden
              onChange={e => { choose(e.currentTarget.files?.[0]); e.currentTarget.value = ""; }} />
            {customName && <span className="break-all text-sm" aria-live="polite">{customName}</span>}
          </div>
          <Hint>16-bit PCM, mono or stereo, 8–48 kHz. Up to 5 seconds and 1 MiB.</Hint>
        </>}
        {draft.source !== "system" && <label className="block font-semibold">AgentNet volume <output>{draft.volume}%</output>
          <input type="range" aria-label="AgentNet volume" className="block h-8 w-full accent-agent-ink" min="0" max="100" value={draft.volume}
            onChange={e => update({ volume: e.currentTarget.valueAsNumber })} disabled={pending} />
        </label>}
        <div className="flex items-center gap-3">
          <div className="min-w-0 flex-1">
            <p className="font-semibold">Respect Windows Do Not Disturb</p>
            <Hint>Silence AgentNet notification sounds when Windows is quiet or can’t report its state.</Hint>
          </div>
          <Toggle label="Respect Windows Do Not Disturb" checked={draft.respect_dnd} disabled={pending}
            onChange={respect_dnd => update({ respect_dnd })} />
        </div>
        <div className="flex flex-wrap gap-2">
          {draft.source !== "system" && <Button variant="outline" disabled={pending || !usable} onClick={() => void submit(true)}>Play preview</Button>}
          <Button disabled={pending || !changed || !usable} onClick={() => void submit(false)}>Save sound settings</Button>
        </div>
      </> : !error && <p role="status" className="text-sm">Loading sound settings…</p>}
      {status && <p role="status" className="text-sm text-muted">{status}</p>}
      {error && <p role="alert" className="text-sm text-danger">{error}</p>}
    </Card>
  </section>;
}
