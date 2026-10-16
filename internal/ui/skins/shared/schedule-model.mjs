                                     
                                                  

                                  
                                              
                        
                               
 

export function scheduleReason(s                                                                                                                                                                             ) {
  if (!s.supported) return "Scheduled sending is unavailable on this host.";
  if (!s.conv || !s.ordinary || s.followup) return "Only ordinary messages to a human chat can be scheduled; questions, tasks, answers and follow-ups cannot.";
  if (s.frozen) return "Sending is paused in this chat.";
  if (s.visitor) return "Visitors cannot schedule ordinary messages here.";
  if (s.newTopic) return "Open an existing topic or Main before scheduling.";
  if (s.missing) return "The selected topic or reply is unavailable. Your draft is kept.";
  return s.fileReason || "";
}

export function localScheduleTime(date = new Date(Date.now() + 3600000)) {
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0,16);
}
export function scheduleDue(local        , now = Date.now()) {
  const time = new Date(local).getTime();
  if (!Number.isFinite(time) || time <= now) throw Error("Choose a date and time in the future.");
  return new Date(time).toISOString();
}
export function captureSchedule(id        , conv        , body        , draft       , due        , timezone        , audience                     )                  {
  scheduleDue(due);
  if (!body.trim() && !draft.files?.length) throw Error("Write a message or choose a file first.");
  const approved = Object.freeze({...audience, persons:Object.freeze([...(audience.persons || [])]), pids:Object.freeze([...(audience.pids || [])])})                                  ;
  const files = Object.freeze((draft.files || []).map(f => Object.freeze({...f})))                           ;
  const captured = Object.freeze({...draft, files});
  const payload = Object.freeze({id,conv,body,topic:draft.topic || "",reply_to:draft.replyTo,quote:draft.replyTo,due,timezone,audience:approved});
  return Object.freeze({payload,draft:captured,files});
}

// Staging handles are single-use even when POST refuses or its response is lost.
// Keep actual Files and restage them for every same-ID confirmation. Browser
// hosts use in-process handles, as in the existing ordinary Composer send.
export async function submitSchedule(api                                              , attempt                 ) {
  const staged            = [];
  try { for (const f of attempt.files) staged.push(await api.stage(f.file)); }
  catch (e) {
    const ids = staged.filter((s)              => typeof s === "string");
    if (ids.length) await api.discard(ids).catch(() => {});
    throw e;
  }
  const row = await api.createSchedule({...attempt.payload, ...(staged.length ? {files:staged            } : {})});
  if (row.id !== attempt.payload.id) throw Error("The saved schedule could not be confirmed.");
  return row;
}

// Invoke the existing clearSent guard only for the captured topic and actual
// unchanged File objects. Its text/reply guards preserve edits made meanwhile.
export function unchangedScheduledFiles(current       , attempt                 ) {
  if ((current.topic || "") !== (attempt.draft.topic || "") || !!current.newTopic !== !!attempt.draft.newTopic) return null;
  return attempt.files.filter(f => current.files?.some(now => now.key === f.key && now.file === f.file));
}
export function scheduleEdit(row                 , body        , due        , timezone        , removeFiles         , audience                      )                  {
  scheduleDue(due);
  if (!body.trim() && (removeFiles || !row.files?.length)) throw Error("Include a message or keep an attachment.");
  return {id:row.id,rev:row.rev,conv:row.conv,topic:row.topic,body,reply_to:row.reply_to,quote:row.quote,due,timezone,
    ...(removeFiles ? {files:[]} : {}), ...(audience ? {audience} : {})};
}
export function scheduleRefused(error         ) {
  const e = error                                  ;
  return ["scheduled_id_conflict","scheduled_stale","scheduled_audience"].includes(e?.code || "") ||
    /^(The scheduled message (ID already names different content|changed; review it again|is unavailable or could not be saved)|The approved audience)/i.test(e?.message || "");
}

export function clearScheduledDraft(current       , attempt                 )        {
  const unchanged=unchangedScheduledFiles(current,attempt);
  if(unchanged===null)return current;
  const keys=new Set(unchanged.map(f=>f.key));
  const next={...current,files:(current.files || []).filter(f=>!keys.has(f.key))};
  if(current.text===attempt.draft.text){next.text="";if(current.agent===attempt.draft.agent)next.agent=undefined;}
  if(current.replyTo===attempt.draft.replyTo)next.replyTo=undefined;
  return next;
}
