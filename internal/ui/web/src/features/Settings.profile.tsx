// You and your devices: your name, setting up your person, devices that
// ask to join as you, adding one with a one-use link, removing one. Keys
// and addresses stay behind Details; a device becomes you only when you
// approve it here.
import { IconCheck, IconCopy, IconDeviceLaptop, IconDeviceMobile, IconLink, IconPlus, IconServer, IconTrash } from "@tabler/icons-react";
import { useEffect, useState, type FormEvent } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { deviceKind, niceDevice, timeOf } from "../model";
import { useStore } from "../store";
import { PersonAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Tag } from "../ui/Tag";
import { Card, copyText, Details, Fact, GroupLabel, Hint, input, PageHead, Skeleton, Tile } from "./Settings.parts";
import { QrCode } from "./Settings.qr";

/** The words for this device's own request to join a person (Overview.link). */
const ownLinkText: Record<string, string> = {
  pending: "Waiting for your other device to approve this one. Open AgentNet there and answer it.",
  refused: "Your other device refused this one, so it did not join as you.",
  expired: "That link expired before this device was approved. Make a new one on your other device (Your devices → Add a device).",
  stale: "That link is out of date because your devices changed meanwhile. Make a new one on your other device.",
  failed: "Joining as you did not work.",
};

export const pendingLinks = (o: T.Overview | null) => (o?.links || []).filter((l) => l.state === "pending");

export const devicesOf = (p: T.PersonView): T.DeviceView[] =>
  p.devices && p.devices.length ? p.devices : [{ address: p.address, name: p.address.split("/").pop() || p.address, fingerprint: p.fingerprint || "", this: true }];

/** DeviceGlyph: the device's kind as its name says it (model.deviceKind); a laptop when it says nothing. */
export function DeviceGlyph({ name, size = 20 }: { name: string; size?: number }) {
  const kind = deviceKind(name);
  const Icon = kind === "phone" ? IconDeviceMobile : kind === "server" ? IconServer : IconDeviceLaptop;
  return <Icon size={size} stroke={1.8} />;
}

// ---- Profile ---------------------------------------------------------------

export function ProfileSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  if (!o) return <><PageHead title="Profile" titleRef={titleRef} /><Skeleton /></>;
  const p = o.person;
  if (p) return <><PageHead title="Profile" titleRef={titleRef} lead="How you appear to people in your chats." /><Rename person={p} /></>;

  const own = o.link && ownLinkText[o.link.state];
  return (
    <>
      <PageHead title="Set up your person" titleRef={titleRef} lead="A person is you, the human others chat with. A service or bot, like a server, doesn’t need one." />
      {own ? (
        <Card className="p-4">
          <p className="font-semibold">{own}</p>
          {o.link?.detail && <Hint className="mt-1">{o.link.detail}</Hint>}
        </Card>
      ) : !o.persons ? (
        <Card className="p-4"><p>Chats between people aren’t available on this AgentNet.</p></Card>
      ) : o.role === "service" ? (
        <Card className="p-4"><p>This computer is a service or bot, so it has no person. Its own agent conversations are under Agents.</p></Card>
      ) : o.role === "unset" ? (
        <CreatePerson />
      ) : (
        <Card className="p-4"><p>This computer has no person yet.</p></Card>
      )}
    </>
  );
}

function Rename({ person }: { person: T.PersonView }) {
  const store = useApp();
  const [name, setName] = useState(person.label);
  const [busy, setBusy] = useState(false);
  useEffect(() => setName(person.label), [person.label]); // a rename from another device shows here
  const devices = devicesOf(person);
  const changed = name.trim() !== person.label && !!name.trim();
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!changed) return;
    setBusy(true);
    await store.run((a) => a.renamePerson(name.trim()), "Your name is now " + name.trim() + ".");
    setBusy(false);
  };
  return (
    <Card className="overflow-hidden">
      <div className="dots flex flex-col items-center gap-3 border-b-[1.5px] border-outline bg-sunken px-4 pb-5 pt-6 text-center lg:border-b">
        <PersonAvatar name={name.trim() || person.label} seed={person.person || person.address} size={72} />
        <div className="min-w-0 max-w-full">
          <p className="truncate font-display text-[28px] font-extrabold leading-tight">{name.trim() || person.label}</p>
		  {person.email && <p className="mt-1 break-all text-sm text-muted">{person.email}</p>}
          <p className="mt-1 flex flex-wrap justify-center gap-1.5">
            {devices.map((d) => (
              <span key={d.address} className="inline-flex items-center gap-1 rounded-full stroke bg-surface px-2.5 py-0.5 text-[13px] font-semibold">
                <DeviceGlyph name={d.name} size={14} />{niceDevice(d.name)}{d.this && <span className="font-normal text-muted">· this one</span>}
              </span>
            ))}
          </p>
          {person.published === false && <Hint className="mt-2">Not on your server yet: AgentNet adds you when it connects.</Hint>}
        </div>
      </div>
      {person.state === "self" && (
        <form onSubmit={save} className="space-y-2.5 p-4">
          <label htmlFor="settings-name" className="block font-semibold">Your name</label>
          <div className="flex gap-2">
            <input id="settings-name" className={input} value={name} maxLength={64} autoComplete="off" onChange={(e) => setName(e.target.value)} />
            {changed && <Button type="submit" variant="act" className="fade-in shrink-0" disabled={busy}>{busy ? "Saving…" : "Save"}</Button>}
          </div>
          <Hint>This is what people see in chats. Your chats, devices and permissions stay the same. Names aren’t checked, so someone else can pick the same one.</Hint>
        </form>
      )}
    </Card>
  );
}

function CreatePerson() {
  const store = useApp();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const create = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    const r = await store.run((a) => a.createPerson(name.trim()));
    if (r) store.toast(r.note || "You’re set up as " + r.person.label + ".", "ok");
    setBusy(false);
  };
  return (
    <div className="space-y-4">
      <Card className="p-4">
        <form onSubmit={create} className="space-y-3">
          <label htmlFor="settings-new-name" className="block font-semibold">Your name</label>
          <input id="settings-new-name" className={input} value={name} maxLength={64} autoComplete="name" placeholder="What people call you" onChange={(e) => setName(e.target.value)} />
          <Hint>It’s the name people see. It isn’t checked, so it proves nothing on its own.</Hint>
          <Button type="submit" variant="act" size="lg" disabled={!name.trim() || busy}>{busy ? "Setting up…" : "Set up my person"}</Button>
        </form>
      </Card>
      <Hint className="px-1">Already use AgentNet on another device? Add this one from there instead (You → Your devices → Add a device), so you’re one person everywhere.</Hint>
    </div>
  );
}

// ---- Devices -----------------------------------------------------------------

export function DevicesSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const [removing, setRemoving] = useState<T.DeviceView | null>(null);
  if (!o) return <><PageHead title="Your devices" titleRef={titleRef} /><Skeleton /></>;
  const p = o.person;
  if (!p) return (
    <>
      <PageHead title="Your devices" titleRef={titleRef} />
      <Card className="p-4"><p>Set up your person first (You → Profile). Then you can use AgentNet as yourself on your phone and other computers.</p></Card>
    </>
  );
  const devices = devicesOf(p);
  const requests = pendingLinks(o);
  return (
    <>
      <PageHead title="Your devices" titleRef={titleRef} lead="You’re one person on each of these. A new one joins with a link from here, and becomes you only when you approve it." />
      {requests.length > 0 && <div className="mb-5 space-y-3">{requests.map((l) => <DeviceRequest key={l.id} link={l} />)}</div>}
      <GroupLabel>{devices.length === 1 ? "1 device" : devices.length + " devices"}</GroupLabel>
      <ul className="space-y-3">
        {devices.map((d) => {
          const copy = (o.history || []).find((h) => h.device === d.address);
          const canRemove = !d.this && devices.length > 1;
          return (
            <li key={d.address}>
              <Card className="px-4 pb-1 pt-4">
                <div className="flex items-center gap-3">
                  <Tile color={d.this ? "#B5E3C4" : "#A8D8FF"} size={44}><DeviceGlyph name={d.name} size={24} /></Tile>
                  <div className="min-w-0 flex-1">
                    <p className="flex flex-wrap items-center gap-2 font-semibold">{niceDevice(d.name)} {d.this && <Tag tone="ok">This one</Tag>}</p>
                    <p className="text-[13px] text-muted">{d.this ? "You’re using it now" : copy ? copyWords(copy) : "One of your devices"}</p>
                  </div>
                </div>
                {copy && copy.state !== "done" && copy.state !== "ended" && copy.total > 0 && (
                  <div className="mt-3" role="progressbar" aria-label={"Copying your chats to " + niceDevice(d.name)} aria-valuemin={0} aria-valuemax={copy.total} aria-valuenow={copy.done}>
                    <div className="h-2.5 overflow-hidden rounded-full stroke bg-sunken"><div className="h-full bg-agent-fill transition-[width] duration-300" style={{ width: Math.round((copy.done / copy.total) * 100) + "%" }} /></div>
                  </div>
                )}
                <div className="mt-1 flex flex-wrap items-start justify-between gap-x-3">
                  <Details>
                    <Fact name="Address">{d.address}</Fact>
                    <Fact name="Key">{d.fingerprint || "not available"}</Fact>
                  </Details>
                  {canRemove && (
                    <button type="button" onClick={() => setRemoving(d)} aria-label={"Remove " + niceDevice(d.name)}
                      className="-mr-2 inline-flex min-h-11 items-center gap-1.5 rounded-full px-3 text-[13px] font-semibold text-danger hover:bg-danger-bg">
                      <IconTrash size={16} aria-hidden="true" />Remove
                    </button>
                  )}
                </div>
              </Card>
            </li>
          );
        })}
      </ul>
      {o.role === "person" && <AddDevice />}
      <RemoveDevice device={removing} onClose={() => setRemoving(null)} />
    </>
  );
}

function copyWords(h: T.HistoryCopy): string {
  if (h.state === "done") return "Has your chats";
  if (h.state === "ended") return "Copying your chats stopped: it’s no longer your device";
  if (!h.total) return "Getting your chats…";
  return "Copying your chats · " + h.done + " of " + h.total + " · while AgentNet runs here";
}

/** DeviceRequest: a new device asking to join as you. It is an OK only you give. */
function DeviceRequest({ link }: { link: T.LinkRequest }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  const decide = async (accept: boolean) => {
    setBusy(true);
    const r = await store.run((a) => a.decideDevice(link.id, accept));
    if (r) store.toast(r.note || (accept ? niceDevice(link.name) + " is now one of your devices." : "Refused."), "ok");
    setBusy(false);
  };
  return (
    <section aria-label={"New device " + link.name} className="overflow-hidden rounded-2xl stroke bg-surface shadow-pop">
      <div className="flex items-center justify-between gap-3 border-b-[1.5px] border-outline bg-act px-4 py-2 text-act-ink lg:border-b">
        <span className="rounded-md bg-surface px-2 py-0.5 text-[12px] font-bold uppercase tracking-[0.08em] text-ink stroke">Needs your OK</span>
        <span className="text-[13px] font-semibold tnum">asked {timeOf(link.requested_at)}</span>
      </div>
      <div className="p-4">
        <div className="flex items-center gap-3">
          <Tile color="#A8D8FF" size={44}><DeviceGlyph name={link.name} size={24} /></Tile>
          <h3 className="font-display text-[20px] font-bold leading-tight">“{link.name}” asks to join as you</h3>
        </div>
        <p className="mt-3 text-[15px]">Approve it only if you just opened your link on it yourself. Once approved it is you: it sends and gets your chats, and your chats are copied to it.</p>
        <Details>
          <Fact name="Address">{link.address}</Fact>
          <Fact name="Key">{link.fingerprint}</Fact>
          <Fact name="Ends">{new Date(link.expires).toLocaleString()}</Fact>
        </Details>
        <div className="mt-3 flex flex-col gap-2 @md:flex-row">
          <Button variant="act" className="@md:flex-1" disabled={busy} icon={<IconCheck size={18} />} onClick={() => decide(true)}>Yes, it’s mine</Button>
          <Button variant="outline" className="@md:flex-1" disabled={busy} onClick={() => decide(false)}>No, refuse it</Button>
        </div>
      </div>
    </section>
  );
}

function AddDevice() {
  const store = useApp();
  const [link, setLink] = useState<T.DeviceLink | null>(null);
  const [busy, setBusy] = useState(false);
  const make = async () => {
    setBusy(true);
    const l = await store.run((a) => a.deviceLink());
    if (l) setLink(l);
    setBusy(false);
  };
  return (
    <div className="mt-5">
      <Button variant="act" size="lg" disabled={busy} icon={<IconPlus size={20} />} onClick={make}>{busy ? "Making a link…" : "Add a device"}</Button>
      <LinkSheet link={link} onClose={() => setLink(null)} />
    </div>
  );
}

function LinkSheet({ link, onClose }: { link: T.DeviceLink | null; onClose: () => void }) {
  const store = useApp();
  const [copied, setCopied] = useState(false);
  useEffect(() => setCopied(false), [link?.url]);
  const page = !!link && /^https:\/\//.test(link.url);
  const copy = async () => {
    if (!link) return;
    const ok = await copyText(link.url);
    setCopied(ok);
    if (!ok) store.toast("Copy didn’t work here. Select the link and copy it by hand.", "error");
  };
  return (
    <Sheet open={!!link} onOpenChange={(open) => { if (!open) onClose(); }} title="Add a device"
      description="It joins as you only after you approve it here."
      footer={<div className="flex gap-2"><Button variant="act" className="flex-1" icon={copied ? <IconCheck size={18} /> : <IconCopy size={18} />} onClick={copy}>{copied ? "Copied" : page ? "Copy link" : "Copy code"}</Button><Button variant="outline" onClick={onClose}>Done</Button></div>}>
      {link && (
        <div className="space-y-4">
          <ol className="space-y-3">
            <Step n={1}>{page ? "On your new device, scan this code with its camera, or open the link below in its browser." : "On your new device, run AgentNet’s join with this code."}</Step>
            <Step n={2}>Come back here: it shows up under Your devices. Approve it.</Step>
          </ol>
          {page && <div className="flex justify-center"><QrCode text={link.url} label="QR code of the link for your new device" /></div>}
          <div className="flex items-start gap-3 rounded-2xl stroke bg-surface p-3">
            <IconLink size={20} className="mt-0.5 shrink-0 text-muted" aria-hidden="true" />
            <code className="line-clamp-3 min-w-0 flex-1 select-all break-all font-mono text-[13px] leading-5" aria-label={page ? "Link" : "Code"}>{link.url}</code>
          </div>
          <Hint>Works once, until {timeOf(link.expires)}. Anyone with it can ask to be you, so give it only to your own device. You still approve it here.</Hint>
        </div>
      )}
    </Sheet>
  );
}

function Step({ n, children }: { n: number; children: React.ReactNode }) {
  return (
    <li className="flex items-start gap-3">
      <span aria-hidden="true" className="grid size-7 shrink-0 place-items-center rounded-full stroke bg-surface font-display text-[14px] font-bold tnum">{n}</span>
      <span className="pt-0.5">{children}</span>
    </li>
  );
}

function RemoveDevice({ device, onClose }: { device: T.DeviceView | null; onClose: () => void }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  const name = device ? niceDevice(device.name) : "";
  const remove = async () => {
    if (!device) return;
    setBusy(true);
    const r = await store.run((a) => a.removeDevice(device.address));
    setBusy(false);
    if (r) { store.toast(name + " is no longer one of your devices.", "ok"); onClose(); }
  };
  return (
    <Sheet open={!!device} onOpenChange={(open) => { if (!open) onClose(); }} title={"Remove " + name + "?"}
      footer={<div className="flex flex-col gap-2 sm:flex-row-reverse"><Button variant="danger" className="sm:flex-1" disabled={busy} onClick={remove}>{busy ? "Removing…" : "Remove " + name}</Button><Button variant="outline" className="sm:flex-1" onClick={onClose}>Keep it</Button></div>}>
      <p>{name} stops being you: new chats no longer reach it. If you added it with a link, your server stops letting it in too.</p>
      <Hint className="mt-2">What it already has stays on it. Removing can’t erase that.</Hint>
    </Sheet>
  );
}
