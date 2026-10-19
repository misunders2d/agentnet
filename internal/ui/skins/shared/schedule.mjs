// Shared scheduled-message UI. Entries supply their existing draft/host binding;
// this never invokes ordinary Send, switches workspaces or installs a timer.
import {captureSchedule,clearScheduledDraft,localScheduleTime,scheduleDue,scheduleEdit,scheduleReason,scheduleRefused,scheduleSummary,submitSchedule} from './schedule-model.mjs';
import {scheduleSaves} from './schedule-saves.mjs';

const states=Object.freeze({pending:'Pending',dispatching:'Preparing to send',sent:'Queued to send',cancelled:'Cancelled',needs_review:'Needs review'});
const mutable=row=>row.state==='pending'||row.state==='needs_review';
const androidFiles='Scheduling attachments is unavailable on Android until its file bridge is qualified.';
const zone=()=>Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC';
const freezeAudience=a=>Object.freeze({...a,persons:Object.freeze([...(a.persons||[])]),pids:Object.freeze([...(a.pids||[])])});
const defaultAudience=a=>[...(a.persons||[]).map(id=>'Verified participant ('+id.slice(0,8)+')'),...(a.pids||[]).map(id=>'Invited participant ('+id.slice(0,8)+')')].join(', ')||'No recipients';

export function mountSchedule({root,host,owner,context,capture,getDraft,setDraft,audienceText=defaultAudience,subscribe,isCurrent=()=>true,onBlockingChange}) {
  if(typeof owner!=='string'||!owner)throw Error('Supply the creating skin draft namespace.');
  const doc=root.ownerDocument,win=doc.defaultView,handle=host.workspace.handle;
  const node=(tag,text='',props={})=>{const n=doc.createElement(tag);n.textContent=text;Object.assign(n,props);return n;};
  const button=(text,run,disabled=false)=>{const b=node('button',text,{type:'button',className:'an-hit',disabled,onclick:()=>{if(active()&&!b.disabled)run();}});return b;};
  const para=(text,role)=>{const p=node('p',text,{className:'hint'});if(role)p.setAttribute('role',role);return p;};
  const field=(label,type,value,change)=>{const box=node('label',label),input=node(type==='textarea'?'textarea':'input','',{...(type==='textarea'?{}:{type}),value,disabled:busy});input.setAttribute('aria-label',label);input.oninput=()=>change(input.value);box.append(input);return box;};
  const timeFields=(label,value,change)=>{
    const box=node('fieldset','',{className:'an-schedule-time'}),fields=node('div','',{className:'an-schedule-fields'}),summary=para(scheduleSummary(value));
    const update=()=>{const local=date.children[0].value+'T'+time.children[0].value;change(local);summary.textContent=scheduleSummary(local);};
    const date=field(label+' date','date',value.split('T')[0],update),time=field(label+' time','time',value.split('T')[1]||'',update);
    fields.append(date,time);box.append(node('legend',label==='Send'?'When to send':'Change send time'),fields,para('Local timezone: '+zone()),summary);return box;
  };
  let dead=false,pane=null,conv='',epoch=0,listRequest=0,busy=false,loading=false,notice='',snapshot=null,audience=null,due='',rows=[],selected=null,editBody='',editTime='',removeFiles=false,review=null,approved,found=null,forgetting=false;
  let lastBlocking,ledger,off;
  const menu=node('div','',{className:'topics-actions'}),hint=para(''),dialog=node('dialog','',{className:'topics-list an-schedule-dialog'}),content=node('div');
  const heading=node('h2'),runtime=para(host.platform==='browser'
    ?'This browser page must stay open. If it is offline, pending messages retry when it is online again.'
    :host.platform==='android'
    ?'AgentNet must be running and online at or after the scheduled time. Mobile background and process limits can delay sending; pending messages retry when the app is running online.'
    :'The AgentNet daemon must be running at or after the scheduled time. If it is off or offline, pending messages retry when it is running online again.');
  const createButton=button('Schedule message…',()=>void openCreate()),listButton=button('Scheduled messages',()=>void openList());
  menu.append(createButton,listButton,hint);root.append(menu);
  const bound=()=>host.workspace.handle===handle;
  const active=()=>!dead&&bound()&&root.isConnected&&isCurrent();
  const viewing=(version=epoch)=>active()&&!!pane&&version===epoch&&context().conv===conv&&dialog.isConnected;
  const block=()=>Object.values(ledger.snapshot()).some(s=>!!s.attempt&&(s.status==='saving'||s.status==='uncertain'));
  const eligibility=()=>{const c=context();return scheduleReason({...c,fileReason:c.fileReason||(host.platform==='android'&&c.conv&&getDraft(c.conv)?.files?.length?androidFiles:'')});};
  const slot=()=>ledger.snapshot()[conv];
  const api={
    stage:file=>{if(!active())throw Error('The view changed. Your draft is kept.');return host.stage(file);},
    discard:ids=>bound()?host.api('/api/upload/discard',{ids}):Promise.resolve(),
    createSchedule:async payload=>{
      if(!active()){
        const ids=(payload.files||[]).filter(f=>typeof f==='string');if(ids.length)await api.discard(ids).catch(()=>{});
        throw Error('The view changed. Your draft is kept.');
      }
      return host.api('/api/schedule/create',payload);
    },
  };
  function changed(){
    if(!active())return;
    for(const saved of ledger.takeConfirmed()){
      if(!active())return;
      setDraft(saved.conv,clearScheduledDraft(getDraft(saved.conv),saved.attempt));
    }
    if(!active())return;
    const blocking=block();if(blocking!==lastBlocking){lastBlocking=blocking;onBlockingChange?.(blocking);}
    update();if(viewing())paint();
  }
  ledger=scheduleSaves(host,changed,owner);
  function update(){
    if(!active())return;
    const c=context(),why=eligibility();createButton.disabled=!!why;createButton.title=why;hint.textContent=why;
    listButton.disabled=!c.supported||!c.conv||!c.ordinary||!!c.followup;
    listButton.title=listButton.disabled?(why||'Choose an ordinary human chat.') : '';
    if(pane&&conv!==c.conv)close();
  }
  function close(){
    epoch++;listRequest++;pane=null;selected=null;review=null;approved=undefined;rows=[];snapshot=null;audience=null;found=null;forgetting=false;loading=false;notice='';
    if(dialog.open)dialog.close();dialog.remove();content.replaceChildren();
  }
  function show(kind){
    close();conv=context().conv;pane=kind;heading.textContent=kind==='create'?'Schedule message':'Scheduled messages';dialog.setAttribute('aria-label',heading.textContent);
    dialog.replaceChildren(heading,runtime,button('Close',close),content);root.append(dialog);dialog.showModal();paint();
    return epoch;
  }
  async function openCreate(){
    if(!active())return;update();if(eligibility())return;
    const version=show('create');if(slot()&&slot().status!=='refused')return;
    // Freeze the draft now; changes while the preview is open remain in the entry.
    const captured=capture(conv);snapshot={body:captured.body,draft:Object.freeze({...captured.draft,files:Object.freeze((captured.draft.files||[]).map(f=>Object.freeze({...f})))})};due=localScheduleTime();loading=true;paint();
    try{const a=await host.api('/api/schedule/audience?'+new URLSearchParams({conv,topic:snapshot.draft.topic||''}));if(viewing(version))audience=freezeAudience(a);}
    catch{if(viewing(version))notice='The current audience could not be verified. Your draft is kept. Close and reopen to review it again.';}
    finally{if(viewing(version)){loading=false;paint();}}
  }
  async function openList(){
    if(!active())return;update();if(listButton.disabled)return;
    show('list');await refresh();
  }
  async function refresh(){
    if(!viewing()||pane!=='list')return;
    const version=epoch,request=++listRequest,chat=conv;loading=true;paint();
    try{const result=await host.api('/api/schedules?'+new URLSearchParams({conv:chat,all:'1'}));if(viewing(version)&&request===listRequest&&!selected)rows=result||[];}
    catch{if(viewing(version)&&request===listRequest)notice='Scheduled messages could not be loaded. Close and reopen to try again.';}
    finally{if(viewing(version)&&request===listRequest){loading=false;if(!selected)paint();}}
  }
  async function save(attempt){
    if(!viewing()||busy||eligibility())return;
    if(host.platform==='android'&&attempt.files.length){notice=androidFiles;paint();return;}
    const chat=conv,version=epoch;busy=true;notice='';
    try{ledger.begin(chat,attempt);}catch{busy=false;notice='Resolve the original unconfirmed save before starting another schedule.';paint();return;}
    try{
      await submitSchedule(api,attempt);ledger.settle(chat,attempt.payload.id,'confirmed');
      if(viewing(version)){await openList();if(viewing())notice='Scheduled on this device. It has not been sent yet.';}
    }catch(error){
      const refused=scheduleRefused(error);ledger.settle(chat,attempt.payload.id,refused?'refused':'uncertain',refused?'Save refused. Your draft is kept. Review the chat, audience, time and attachments before a changed save.':'Save not confirmed. Retry this original same-ID save; it may already be scheduled.');
    }finally{busy=false;if(viewing())paint();}
  }
  function start(){
    if(!snapshot||!audience||busy||eligibility()||slot()&&slot().status!=='refused')return;
    try{
      const bytes=win.crypto.getRandomValues(new Uint8Array(16)),id=Array.from(bytes,b=>b.toString(16).padStart(2,'0')).join('');
      void save(captureSchedule(id,conv,snapshot.body,snapshot.draft,scheduleDue(due),zone(),audience));
    }catch{notice='Choose a future time and include a message or file. Your draft is kept.';paint();}
  }
  async function lookup(s){
    if(!viewing()||busy||s.status==='saving')return;
    const chat=conv,version=epoch;busy=true;found=null;paint();
    try{
      const row=await host.api('/api/schedules?'+new URLSearchParams({id:s.id}));
      if(row.id!==s.id||row.conv!==chat)throw Error('Different saved state.');
      ledger.settle(chat,s.id,'uncertain',s.attempt?'Saved state found; confirm with same-ID save. The lookup alone cannot verify attachment contents and does not clear your draft.':'Saved state found. Compare it with your draft before explicitly finishing this review. Your draft is kept.');
      if(viewing(version))found=row;
    }catch{ledger.settle(chat,s.id,'uncertain','Saved state could not be confirmed. It may still be saved by an earlier request. This original intent is kept; no new ID was allocated.');}
    finally{busy=false;if(viewing(version))paint();}
  }
  async function action(run){
    if(!viewing()||busy)return;
    const version=epoch;busy=true;notice='';paint();
    try{await run();if(viewing(version)){selected=null;review=null;approved=undefined;await refresh();}}
    catch{if(viewing(version)){selected=null;review=null;approved=undefined;await refresh();notice='The action was not confirmed or the revision changed. Review the refreshed list before trying again. No audience was changed automatically.';}}
    finally{busy=false;if(viewing(version))paint();}
  }
  async function reviewAudience(row){
    if(!viewing()||busy)return;
    const version=epoch;busy=true;paint();
    try{const a=await host.api('/api/schedule/audience?'+new URLSearchParams({conv:row.conv,topic:row.topic||''}));if(viewing(version)&&selected===row)review=freezeAudience(a);}
    catch{if(viewing(version))notice='The current audience could not be verified. The saved audience is unchanged.';}
    finally{busy=false;if(viewing(version))paint();}
  }
  function paintSlot(s){
    const box=node('section');box.setAttribute('aria-label','Unconfirmed schedule save');
    box.append(para(s.status==='saving'?'Saving schedule…':s.status==='refused'?'Save refused':s.status==='confirmed'?'Confirmed save for another skin':'Unconfirmed save'));
    if(s.attempt)box.append(para(s.attempt.payload.body),para(new Date(s.attempt.payload.due).toLocaleString()+' · '+s.attempt.payload.timezone));
    for(const f of s.files)box.append(para(f.name+' · '+f.size+' bytes'));
    box.append(para(s.note||''));if(!s.durable)box.append(para('This unconfirmed save won’t be remembered if the page reloads. Keep AgentNet open until you resolve it.','status'));
    if(s.status==='confirmed')box.append(para('Reopen the creating skin to apply its confirmed draft clear. Your draft here is kept.'));
    else{
      if(s.attempt){const fileBlocked=host.platform==='android'&&s.attempt.files.length;if(fileBlocked)box.append(para(androidFiles));box.append(button('Retry same save',()=>void save(s.attempt),busy||s.status==='saving'||!!eligibility()||!!fileBlocked));}
      if(s.status!=='refused')box.append(button('Check saved state',()=>void lookup(s),busy||s.status==='saving'));
      box.append(button('Forget…',()=>{forgetting=true;paint();},busy||s.status==='saving'));
    }
    if(found?.id===s.id&&!s.attempt){
      box.append(para(found.body),para(new Date(found.due).toLocaleString()+' · '+(states[found.state]||'Unavailable')));
      for(const f of found.files||[])box.append(para(f.name+' · '+f.size+' bytes'));
      box.append(para('Compare this saved state with your draft. Attachment names and sizes do not prove identical contents.'),button('Keep draft and finish review',()=>{ledger.forget(conv,s.id);found=null;void openList();},busy));
    }
    if(forgetting)box.append(para('It may already be scheduled. Check Scheduled messages before saving again.'),button('Forget unconfirmed save',()=>{ledger.forget(conv,s.id);forgetting=false;found=null;void openCreate();},busy||s.status==='saving'));
    content.append(box);
  }
  function paintCreate(){
    const s=slot();if(s&&s.status!=='refused')return;
    if(!snapshot)return;
    const form=node('form'),why=eligibility();form.onsubmit=e=>{e.preventDefault();start();};
    form.append(para(snapshot.body||'Attachment message'));for(const f of snapshot.draft.files||[])form.append(para(f.name));
    form.append(timeFields('Send',due,v=>{due=v;}),para('Approved audience'),para(loading?'Verifying current audience…':audience?audienceText(audience,conv):'Audience unavailable'),para('This exact preview is saved as the audience ceiling. Membership and keys are checked again before sending.'));
    if(why)form.append(para(why,'alert'));
    form.append(button(s?'Save changed schedule':'Save schedule',start,busy||loading||!audience||!!why));content.append(form);
  }
  function paintEdit(row,article){
    article.append(field('Scheduled message text','textarea',editBody,v=>{editBody=v;}),timeFields('Reschedule',editTime,v=>{editTime=v;}));
    if(row.files?.length){const label=node('label','Remove all attachments'),check=node('input','',{type:'checkbox',checked:removeFiles,disabled:busy});check.setAttribute('aria-label','Remove all attachments');check.onchange=()=>{removeFiles=check.checked;};label.append(check);article.append(label);}
    article.append(para(approved?'New audience explicitly confirmed: '+audienceText(approved,conv):'Editing text or time keeps the saved audience ceiling and attachments unless explicitly removed.'),button('Review audience',()=>void reviewAudience(row),busy));
    if(review)article.append(para(audienceText(review,conv)),button('Confirm reviewed audience',()=>{approved=review;review=null;paint();},busy));
    article.append(button('Save schedule changes',()=>{
      try{const change=scheduleEdit(row,editBody,scheduleDue(editTime),zone(),removeFiles,approved);void action(()=>host.api('/api/schedule/edit',change));}
      catch{notice='Choose a future time and include a message or keep an attachment. No change was saved.';paint();}
    },busy),button('Keep saved schedule',()=>{selected=null;review=null;approved=undefined;void refresh();},busy));
  }
  function paint(){
    if(!viewing())return;
    content.replaceChildren();if(notice)content.append(para(notice,'status'));
    if(pane==='create')paintCreate();const s=slot();if(s)paintSlot(s);
    if(pane!=='list')return;
    if(loading)content.append(para('Loading scheduled messages…','status'));
    if(!loading&&!rows.length)content.append(para('No scheduled messages in this chat on this device.'));
    for(const row of rows){
      const article=node('article');article.setAttribute('aria-label','Scheduled message: '+(states[row.state]||'Unavailable'));
      article.append(para(states[row.state]||'Unavailable'),para(row.body),para('Send at: '+scheduleSummary(row.due)),para(zone()+' · saved timezone '+row.timezone),para(audienceText(row.audience||{},conv)));
      for(const f of row.files||[])article.append(para(f.name));
      if(row.state==='needs_review')article.append(para('The chat, audience or attachments need review before sending.'));
      if(row.state==='dispatching')article.append(para('AgentNet is preparing the ordinary send. A delivery receipt is separate.'));
      if(mutable(row)){
        if(selected?.id===row.id)paintEdit(selected,article);
        else article.append(button('Edit schedule',()=>{selected=row;editBody=row.body;editTime=localScheduleTime(new Date(row.due));removeFiles=false;review=null;approved=undefined;notice='';paint();},busy),button('Send now',()=>void action(()=>host.api('/api/schedule/send_now',{id:row.id,rev:row.rev})),busy),button('Cancel schedule',()=>void action(()=>host.api('/api/schedule/cancel',{id:row.id,rev:row.rev})),busy));
      }
      content.append(article);
    }
  }
  const cancel=e=>{e.preventDefault();close();};dialog.addEventListener('cancel',cancel);
  // Freeze the open editor and its original revision. Repainting on a host
  // push replaces native inputs/focus and used to discard the entire edit.
  // Save still uses CAS; closing the editor explicitly reloads current state.
  off=subscribe?.(()=>{if(viewing()&&pane==='list'&&!selected)void refresh();});changed();
  return {openCreate,openList,close,update,get blocking(){return block();},destroy(){if(dead)return;close();dead=true;off?.();ledger.dispose();dialog.removeEventListener('cancel',cancel);menu.remove();}};
}
