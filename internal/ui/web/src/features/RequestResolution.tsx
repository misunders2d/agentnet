import { useEffect, useRef, useState } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { niceDevice } from "../model";
import { sendID } from "../optimistic.mjs";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { terminalStopLabel } from "./Message.model";

/** An explicit exact-request decision. Only the host's returned status closes
 * the request; sending this control never means the job has been resolved. */
export function ResolveOwnRequest({ request, disabled }: { request: T.ContinuationAction; disabled?: boolean }) {
  const store = useApp();
  const [choice, setChoice] = useState<{ request: T.ContinuationAction; send: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const sending = useRef(false);
  const generation = useRef(0);
  const identity = [request.host, request.id, request.key, request.attempt].join("/");
  const current = useRef(identity); current.current = identity;
  useEffect(() => () => { generation.current++; }, []);
  if (!request.host) return null;
  const prepare = async () => {
    if (sending.current) return;
    const c = {...request}, token = ++generation.current, key = identity;
    sending.current = true; setBusy(true); setProblem("");
    try {
      // This protected route checks the signed host capability only. It queues
      // no decision, and the actual send still rechecks after confirmation.
      await store.api.decide({host:c.host!,id:c.id,key:c.key,attempt:c.attempt,action:"resolve",expect:"needs_human",report:"",check:true});
      if (token === generation.current && current.current === key) setChoice({request:c,send:sendID()});
    } catch (e) {
      if (token === generation.current && current.current === key) setProblem(errorText(e).replace(/^(peer_update|server_update|server_unavailable): /, ""));
    } finally {
      sending.current = false;
      if (token === generation.current) setBusy(false);
    }
  };
  const submit = async () => {
    if (!choice || sending.current) return;
    sending.current = true; setBusy(true);
    try {
      const c = choice.request;
      const result = await store.run(api => api.decide({host:c.host!,id:c.id,key:c.key,attempt:c.attempt,action:"resolve",expect:"needs_human",report:"",send_id:choice.send}));
      if (result) { store.toast(result.note || "Decision queued. Waiting for the host to confirm."); setChoice(null); }
    } finally { sending.current = false; setBusy(false); }
  };
  return <>
    <Button size="sm" variant="ghost" disabled={disabled || busy} onClick={() => void prepare()}>{busy && !choice ? "Checking host…" : problem ? "Check host again" : "Mark as handled"}</Button>
    {problem && <p role="status" className="pt-1 text-[13px] text-text-2">{problem} You can open the original request on {niceDevice(request.host)}.</p>}
    {!problem && request.handled?.refused && <p role="status" className="pt-1 text-[13px] text-danger">Not applied: {request.handled.refused}</p>}
    <Sheet open={!!choice} onOpenChange={open => { if (!open && !busy) setChoice(null); }} title={"Mark as handled on " + niceDevice(choice?.request.host || request.host) + "?"}
      footer={<Button size="lg" disabled={busy} onClick={() => void submit()}>{busy ? "Sending…" : "Mark as handled"}</Button>}>
      <p>If a separate follow-up finished the work, you can close this exact waiting request on its host. It keeps the messages and does not run the task again or mark the topic done. The request stays pending here until the host confirms.</p>
    </Sheet>
  </>;
}

/** Stop uses the durable signed decision path. Receipt/queueing never means
 * the process stopped; only the host's status says that. */
export function StopOwnRequest({request}:{request:T.StopAction}) {
  const store=useApp();
  const [busy,setBusy]=useState(false),[queued,setQueued]=useState(false);
  const send=useRef(sendID());
  const identity=[request.host,request.id,request.key,request.attempt].join("/");
  const current=useRef(identity);current.current=identity;
  useEffect(()=>{send.current=sendID();setQueued(false);setBusy(false);},[identity]);
  const terminal=terminalStopLabel(request),acknowledged=!!request.result&&!request.result.refused;
  const submit=async()=>{
    if(busy||request.pending||terminal||acknowledged||queued&&!request.result)return;setBusy(true);
    const selected=identity;
    try {
      const result=await store.run(api=>api.decide({host:request.host!,id:request.id,key:request.key,attempt:request.attempt,action:"cancel",expect:"running",report:"",send_id:send.current}));
      if(result&&current.current===selected){setQueued(true);store.toast("Stop queued. Waiting for the host to confirm; completed actions are not undone.");}
    }finally{if(current.current===selected)setBusy(false);}
  };
  return <div className="shrink-0">
    <Button size="sm" variant="ghost" disabled={busy||!!terminal||request.pending||acknowledged||queued&&!request.result} onClick={()=>void submit()}>{terminal|| (busy?"Sending…":request.pending||queued&&!request.result?"Stop queued":acknowledged?"Stopping…":"Stop")}</Button>
    {request.result?.refused&&<p role="status" className="max-w-48 text-[12px] text-text-2">{request.result.refused}</p>}
  </div>;
}
