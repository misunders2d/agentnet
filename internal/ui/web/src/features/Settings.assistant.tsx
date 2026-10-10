// This computer's agent (the program that answers approved questions
// and runs accepted tasks), and who may use it. Choosing a program grants
// nobody anything: approvals stay where they are, and the person's own
// tool permissions stay the authority.
import { IconBolt, IconLock, IconMessageQuestion } from "@tabler/icons-react";
import { type ReactNode } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Card, Command, Details, Fact, Failed, GroupLabel, Hint, PageHead, Skeleton, useLoad } from "./Settings.parts";
import { Permissions, useGrants } from "./AgentsView.grants";
import { AppControls } from "./AppControls";
import { AssistantSetup } from "./AssistantSetup";
import { browserDevice as isBrowser } from "./AssistantSetup.model";
import { DefaultAgentChoice, harnessName, MANUAL } from "./DefaultAgent";

export { harnessName };

// What a question may use, per program, in plain words. The server's exact
// wording stays under Details.
const questionLimits: Record<string, string> = {
  claude: "Uses your Claude tools, skills and permissions unchanged.",
  codex: "Uses your Codex tools, skills, sandbox and permissions unchanged.",
  pi: "Uses your Pi tools, skills and permissions unchanged.",
};

export function AssistantSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const browser = isBrowser(store.host.platform, o);
  const r = useLoad(() => store.api.responder(), [o?.me.responder, o?.me.responder_dir, browser]);
  const head = <PageHead title="Your agent" titleRef={titleRef} lead="The program on this computer that answers questions and does tasks for you, with your own setup." />;
  // Named-agent setup is primary; the legacy default routing stays subordinate.
  const setup = <div className="space-y-4"><AssistantSetup /></div>;
  if (browser) return <>{head}{setup}<AppControls command /></>;
  const defaults = r.error && !r.data ? <Failed text={r.error} retry={r.reload} /> : r.data ? <AssistantForm view={r.data} saved={r.reload} /> : <Skeleton lines={4} />;
  return <>{head}{setup}<div className="mt-6 space-y-4"><Details label="Default agent">{defaults}</Details><AppControls command /></div></>;

}

function AssistantForm({ view, saved }: { view: T.ResponderView; saved: () => void }) {
  const store = useApp();
  const me = useStore(store, (s) => s.overview?.person);
  return (
    <div className="space-y-6">
      <Status view={view} me={me} />
      <DefaultAgentChoice view={view} saved={saved} extra={(choice) => <Limits harness={choice !== MANUAL ? choice : ""} view={view} />} />
    </div>
  );
}

function Status({ view, me }: { view: T.ResponderView; me?: T.PersonView }) {
  const myFace = <PersonAvatar name={me?.label || "Me"} seed={me?.person || "me"} size={56} />;
  const h = view.harness || "";
  const found = (view.harnesses || []).some((x) => x.name === h && x.found);
  let face: ReactNode, title: string, sub: string, tone: "ok" | "danger" | "surface";
  if (!view.chosen) { face = myFace; title = "Nobody answers yet"; sub = "Questions and tasks wait for you until you choose below."; tone = "surface"; }
  else if (view.manual) { face = myFace; title = "You answer yourself"; sub = "Questions and tasks wait for you in OKs."; tone = "surface"; }
  else if (view.ready) { face = <AgentAvatar seed={h} size={56} mood="done" device="laptop" />; title = harnessName(h) + " answers for you"; sub = "Installed here, and its folder exists. That doesn’t check its sign-in."; tone = "ok"; }
  else { face = <AgentAvatar seed={h} size={56} mood="waiting" device="laptop" />; title = harnessName(h) + " can’t start"; sub = !found ? harnessName(h) + " isn’t installed where AgentNet can find it. Until it is, questions and tasks fail." : "Its folder doesn’t exist. Until it does, questions and tasks fail."; tone = "danger"; }
  return (
    <Card tone={tone} className="p-4">
      <div className="flex items-center gap-4">
        {face}
        <div className="min-w-0">
          <p className={"font-display text-[20px] font-bold leading-tight " + (tone === "danger" ? "text-danger" : "")}>{title}</p>
          <p className="text-[15px] text-text-2">{sub}</p>
        </div>
      </div>
      {view.problem && <Details className="mt-1"><p>{view.problem}</p></Details>}
    </Card>
  );
}

function Limits({ harness, view }: { harness: string; view: T.ResponderView }) {
  const h = (view.harnesses || []).find((x) => x.name === harness);
  return (
    <section aria-labelledby="assistant-limits">
      <GroupLabel id="assistant-limits">What it may do</GroupLabel>
      <Card className="divide-y divide-hairline">
        <Rule icon={<IconMessageQuestion size={20} />} title="Questions">
          Answered automatically only for people you approve. {harness ? questionLimits[harness] || "" : ""} Tools your setup already allows keep their effects.
        </Rule>
        <Rule icon={<IconBolt size={20} />} title="Tasks">Run only after you OK them, with your normal permissions.</Rule>
        <Rule icon={<IconLock size={20} />} title="Nothing more">Choosing a program doesn’t approve anyone or change any permission.</Rule>
      </Card>
      {(h || view.chosen) && (
        <Details className="mt-1 px-1">
          {h?.question_mode && <p className="whitespace-pre-line">{h.question_mode}</p>}
          {h?.path && <Fact name="Program">{h.path}</Fact>}
          {h?.tested_live && <Fact name="Tested">{h.tested_live}</Fact>}
          {!!view.timeout_seconds && <Fact name="Time limit">{view.timeout_seconds + " seconds per job"}</Fact>}
          {view.context && view.context.length > 0 && <Fact name="Context">{view.context.join(", ")}</Fact>}
          <Hint>Change the time limit and context files with agentnet responder in a terminal.</Hint>
        </Details>
      )}
    </section>
  );
}

function Rule({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <div className="flex gap-3 p-4">
      <span aria-hidden="true" className="mt-0.5 grid size-9 shrink-0 place-items-center rounded-xl bg-sunken text-ink">{icon}</span>
      <div className="min-w-0">
        <p className="font-semibold">{title}</p>
        <p className="text-[15px] text-text-2">{children}</p>
      </div>
    </div>
  );
}

// ---- Permissions -----------------------------------------------------------

export function PermissionsSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const grants = useGrants();
  const copied = (ok: boolean) => store.toast(ok ? "Copied." : "Copy didn’t work here. Select the words and copy them by hand.", ok ? "ok" : "error");
  return (
    <>
      <PageHead title="Permissions" titleRef={titleRef} lead="Who may use your agent without asking you first." />
      <div className="space-y-6">
        <Card className="divide-y divide-hairline">
          <Rule icon={<IconMessageQuestion size={20} />} title="Questions">Your agent answers by itself only for people you’ve approved. Everyone else’s questions wait for you in OKs.</Rule>
          <Rule icon={<IconBolt size={20} />} title="Tasks">A task runs only after you OK it. If you chose Always allow for someone, their current and future verified devices can give tasks without asking. Removing a device ends its person access; key changes and frozen people block it.</Rule>
          <Rule icon={<IconLock size={20} />} title="Chat messages">Nothing anyone types in a chat can approve anything. Only you can, in OKs.</Rule>
        </Card>
        {isBrowser(store.host.platform, o) ? (
          <Hint className="px-1">This browser runs nothing. Permissions belong to the computer that runs your agent.</Hint>
        ) : (
          <section aria-labelledby="permissions-now">
            <GroupLabel id="permissions-now">Approved right now</GroupLabel>
            <Permissions grants={grants} />
            <Hint className="mt-2 px-1">Saved person and advanced device permissions appear here, including those granted in a terminal.</Hint>
            <Details label="In a terminal" className="px-1">
              <div className="space-y-2 pt-1">
                <Command cmd="agentnet approvals" what="Who’s approved, and whether it still holds" onCopied={copied} />
                <Command cmd="agentnet unapprove PERSON-or-ADDRESS" what="Stop answering their questions by itself" onCopied={copied} />
                <Command cmd="agentnet unapprove --tasks PERSON-or-ADDRESS" what="Turn off Always allow for their tasks" onCopied={copied} />
              </div>
            </Details>
          </section>
        )}
      </div>
    </>
  );
}
