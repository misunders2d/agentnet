import { useState } from "react";
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { ScheduleDialogs } from "./Composer.schedule";
import { DraftsView } from "./Outbox.drafts";
import { Reminders } from "./Reminders";

const sections = ["scheduled", "drafts", "reminders"] as const;
const titles = { scheduled: "Scheduled", drafts: "Drafts", reminders: "Reminders" };

/** A workspace's locally held work. Visiting this page never sends anything. */
export function Outbox() {
  const store = useApp();
  const section = useStore(store, s => s.section);
  const overview = useStore(store, s => s.overview);
  const saves = useStore(store, s => s.scheduleSaves);
  const [recovery, setRecovery] = useState("");
  const [refresh, setRefresh] = useState(0);
  const unresolved = Object.values(saves).filter(s=>s.status === "saving" || s.status === "uncertain");
  const current = sections.find(s => s === section) || "scheduled";
  return <section aria-label="Outbox" className="mx-auto w-full max-w-4xl px-4 py-5 sm:px-6">
    <h1 className="font-display text-3xl font-extrabold tracking-tight">Outbox</h1>
    <p className="mt-2 text-sm text-text-2">Scheduled messages, unfinished drafts and personal reminders on this device.</p>
    <nav aria-label="Outbox" className="my-4 flex flex-wrap gap-2">
      {sections.map(s => <Button key={s} variant={s === current ? "outline" : "ghost"} aria-current={s === current ? "page" : undefined} onClick={() => store.showTab("outbox", s)}>{titles[s]}</Button>)}
    </nav>
    {current === "scheduled" ? overview?.schedules
      ? <>
        {!!unresolved.length&&<section aria-label="Unconfirmed schedule saves" className="mb-4 rounded-xl bg-sunken p-3 text-sm">
          <p className="font-semibold">Some saves need confirmation</p>
          <p>Review the original save before scheduling the same message again.</p>
          {unresolved.map(s=><div key={s.id} className="mt-2 flex items-center justify-between gap-2"><span className="min-w-0 truncate">{overview.dms?.find(d=>d.id===s.conv)?.title || "Chat "+s.conv.slice(0,8)}</span><Button size="sm" disabled={s.status === "saving"} onClick={()=>setRecovery(s.conv)}>{s.status === "saving" ? "Saving…" : "Review save"}</Button></div>)}
        </section>}
        <ScheduleDialogs key={refresh} page pane="list" onPane={() => {}} conv="" reason="" capture={() => ({ draft: { text: "" }, body: "" })} />
        {recovery&&<ScheduleDialogs key={recovery} reviewOnly pane="create" onPane={pane=>{setRecovery("");if(pane==="list")setRefresh(n=>n+1);}} conv={recovery} reason="" capture={() => ({ draft: { text: "" }, body: "" })} />}
      </>
      : <p className="py-5 text-text-2">Scheduled sending is unavailable on this host.</p>
      : current === "drafts" ? <DraftsView /> : <Reminders />}
  </section>;
}
