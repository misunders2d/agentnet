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

export function mountSchedule({root,host,owner,context,capture,getDraft,setDraft,audienceText=defaultAudience,topicText=()=> 'Named topic',subscribe,isCurrent=()=>true,onBlockingChange,outboxRoot,onOutboxChange,chatText=()=> 'Chat',getDrafts=()=>[],resumeDraft,getReminders=()=>[],openOriginalSave}) {
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
    fields.append(date,time);box.append(node('legend',label==='Send'?'When to send':'Change send time'),fields);if(pane!=='list')box.append(para('Local timezone: '+zone()));box.append(summary);return box;
  };
  let dead=false,pane=null,conv='',epoch=0,listRequest=0,busy=false,loading=false,notice='',snapshot=null,audience=null,due='',rows=[],selected=null,editBody='',editTime='',removeFiles=false,review=null,approved,found=null,forgetting=false,historyOpen=false,inOutbox=false,editing=false;
  let lastBlocking,ledger,off,historyRows=[],historyLoaded=false,historyLoading=false,historyRequest=0,selectedReminder=null;
  const menu=node('div','',{className:'topics-actions'}),hint=para(''),dialog=node('dialog','',{className:'topics-list an-schedule-dialog'}),content=node('div');
  const heading=node('h2'),runtime=para(host.platform==='browser'
    ?'Keep this page open and online to send.'
    :host.platform==='android'
    ?'Sends while AgentNet is running and online. Background limits can delay sending.'
    :'Sends from this device while AgentNet is running and online.');
  const entryButton=button('Schedule…',()=>{
    const draft=getDraft(context().conv),prepared=!!draft?.text?.trim()||!!draft?.files?.length;
    void (!eligibility()&&prepared?openCreate():openList());
  });
  const createButton=button('Schedule message',()=>{if(pane!=='create')void openCreate();}),listButton=button(outboxRoot?'Open Outbox':'Scheduled messages',()=>{if(pane!=='list')void openList();}),navigation=node('nav','',{className:'an-schedule-nav'});
  navigation.setAttribute('aria-label','Scheduling');navigation.append(createButton,listButton);
  const tabs=node('nav','',{className:'an-schedule-nav'}),tabButtons=new Map();tabs.setAttribute('aria-label','Outbox');
  for(const [key,label]of [['list','Scheduled'],['drafts','Drafts'],['reminders','Reminders']]){const tab=button(label,()=>void openOutbox(key));tabButtons.set(key,tab);tabs.append(tab);}
  menu.append(entryButton,hint);root.append(menu);
  const bound=()=>host.workspace.handle===handle;
  const active=()=>!dead&&bound()&&root.isConnected&&isCurrent();
  const viewing=(version=epoch)=>active()&&!!pane&&version===epoch&&(inOutbox||context().conv===conv)&&dialog.isConnected;
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
  function update(repaint=true){
    if(!active())return;
    const c=context(),why=eligibility();createButton.disabled=busy||!!why;createButton.title=why;hint.textContent=why;
    listButton.disabled=busy||!c.supported||!c.conv||!c.ordinary||!!c.followup;
    listButton.title=listButton.disabled?(why||'Choose an ordinary human chat.') : '';
    entryButton.disabled=listButton.disabled;entryButton.title=listButton.title;
    if(pane&&!inOutbox&&conv!==c.conv)close();
    if(repaint&&viewing()&&inOutbox&&pane!=='list'&&!busy)paint();
  }
  function close(){
    const wasOutbox=inOutbox;inOutbox=false;
    epoch++;listRequest++;historyRequest++;pane=null;selected=null;selectedReminder=null;review=null;approved=undefined;rows=[];snapshot=null;audience=null;found=null;forgetting=false;loading=false;notice='';historyOpen=false;historyRows=[];historyLoaded=false;historyLoading=false;
    if(dialog.open)dialog.close();dialog.remove();content.replaceChildren();
    if(wasOutbox)onOutboxChange?.(false);
  }
  function show(kind,global=false){
    close();inOutbox=global;conv=global?'':context().conv;pane=kind;heading.textContent=global?'Outbox':kind==='create'?'Schedule message':'Scheduled messages';dialog.setAttribute('aria-label',heading.textContent);
    dialog.className='topics-list an-schedule-dialog'+(global?' an-outbox-view':'');
    dialog.replaceChildren(heading,global?tabs:navigation,runtime,button('Close',close),content);(global?outboxRoot||root:root).append(dialog);dialog.showModal();if(global)onOutboxChange?.(true);paint();
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
    if(outboxRoot)return openOutbox('list');
    if(!active())return;update();const c=context();if(!c.supported||!c.conv||!c.ordinary||c.followup)return;
    show('list');await refresh();
  }
  async function openOutbox(tab='list'){
    if(!active())return;
    const kind=tab==='scheduled'?'list':tab;if(!['list','drafts','reminders'].includes(kind))return;
    if(!inOutbox)show(kind,true);
    else{epoch++;listRequest++;historyRequest++;pane=kind;selected=null;editing=false;review=null;approved=undefined;historyOpen=false;historyRows=[];historyLoaded=false;historyLoading=false;notice='';loading=false;paint();}
    if(kind==='list'){
      if(!context().supported){notice='Scheduled messages are unavailable on this device. Update AgentNet to manage them here.';paint();return;}
      await refresh();
    }
  }
  async function refresh(){
    if(!viewing()||pane!=='list')return;
    const version=epoch,request=++listRequest,chat=conv;loading=true;paint();
    try{const result=await host.api('/api/schedules?'+new URLSearchParams({...(!inOutbox?{conv:chat}:{}),all:inOutbox?'0':'1'}));if(viewing(version)&&request===listRequest&&!selected)rows=result||[];}
    catch{if(viewing(version)&&request===listRequest)notice='Scheduled messages could not be loaded. Close and reopen to try again.';}
    finally{if(viewing(version)&&request===listRequest){loading=false;if(!selected)paint();}}
  }
  async function loadHistory(){
    if(!viewing()||!inOutbox||pane!=='list'||!historyOpen)return;
    const version=epoch,request=++historyRequest;historyLoading=true;paint();
    try{const result=await host.api('/api/schedules?all=1');if(viewing(version)&&request===historyRequest){historyRows=(result||[]).filter(row=>row.state==='sent'||row.state==='cancelled');historyLoaded=true;}}
    catch{if(viewing(version)&&request===historyRequest)notice='Schedule history could not be loaded. Close and reopen history to try again.';}
    finally{if(viewing(version)&&request===historyRequest){historyLoading=false;paint();}}
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
    article.append(para(approved?'New audience explicitly confirmed: '+audienceText(approved,row.conv):'Editing text or time keeps the saved audience ceiling and attachments unless explicitly removed.'),button('Review audience',()=>void reviewAudience(row),busy));
    if(review)article.append(para(audienceText(review,row.conv)),button('Confirm reviewed audience',()=>{approved=review;review=null;paint();},busy));
    article.append(button('Save schedule changes',()=>{
      try{const change=scheduleEdit(row,editBody,scheduleDue(editTime),zone(),removeFiles,approved);void action(()=>host.api('/api/schedule/edit',change));}
      catch{notice='Choose a future time and include a message or keep an attachment. No change was saved.';paint();}
    },busy),button('Keep saved schedule',()=>{selected=null;review=null;approved=undefined;void refresh();},busy));
  }
  async function resume(run){
    if(!viewing()||busy||!run)return;
    const version=epoch;busy=true;notice='';paint();
    try{await run();if(viewing(version))close();}
    catch{if(viewing(version))notice='That original destination could not be opened. The draft or reminder is kept.';}
    finally{busy=false;if(viewing())paint();}
  }
  function paintDrafts(){
    const drafts=getDrafts().filter(d=>d.text?.trim()||d.files?.length);
    content.append(para('Unsent on this device. Files held in this window are not a promise of attachment persistence after reload.'));
    if(!drafts.length)content.append(para('No unsent drafts on this device.'));
    for(const draft of drafts){
      const article=node('article','',{className:'an-outbox-row'}),open=button('',()=>void resume(()=>resumeDraft?.(draft.key,draft.record)),busy||!resumeDraft);
      open.className+=' an-outbox-summary an-outbox-draft';open.setAttribute('aria-label','Resume draft: '+draft.title);
      open.append(node('strong',draft.title,{className:'an-outbox-chat'}),node('span',draft.text?.trim().slice(0,140)||'Attachment draft',{className:'an-outbox-preview'}),node('span',draft.detail||'Unsent draft',{className:'hint an-outbox-meta'}));
      if(draft.files?.length)open.append(node('span',draft.files.map(f=>typeof f==='string'?f:f.name).join(', '),{className:'an-outbox-files'}));
      article.append(open);content.append(article);
    }
  }
  function paintReminders(){
    const reminders=getReminders();
    content.append(para('Personal reminders only. Nothing is sent.'));
    if(!reminders.length)content.append(para('No pending reminders on this device.'));
    for(const reminder of reminders){
      const article=node('article','',{className:'an-outbox-row'}),open=button('',()=>{selectedReminder=reminder.id;paint();},busy);
      open.className+=' an-outbox-summary';open.setAttribute('aria-label','View reminder: '+reminder.title);
      open.append(node('time',reminder.when,{className:'an-schedule-when'}),node('strong',reminder.chat||reminder.title,{className:'an-outbox-chat'}),node('span',reminder.title+(reminder.detail?' · '+reminder.detail:''),{className:'an-outbox-preview'}),node('span',reminder.overdue?'Due':'Pending',{className:'hint an-outbox-status'}));article.append(open);
      if(selectedReminder===reminder.id){
        const detail=node('section','',{className:'an-schedule-detail'});detail.setAttribute('aria-label','Reminder details');detail.append(para(reminder.title),button('Open message',()=>void resume(reminder.open),busy));
        for(const [label,run]of [['Change reminder',reminder.change],['Done',reminder.done],['Cancel reminder',reminder.cancel]])if(run)detail.append(button(label,()=>{if(!busy)Promise.resolve(run()).then(()=>{if(viewing())paint();},()=>{if(viewing()){notice='The reminder action was not confirmed. Refresh its current state before trying again.';paint();}});},busy));
        detail.append(button('Close details',()=>{selectedReminder=null;paint();},busy));article.append(detail);
      }
      content.append(article);
    }
  }
  function compactRow(row){
    const article=node('article','',{className:'an-outbox-row'}),open=button('',()=>{selected=row;editing=false;notice='';paint();},busy);
    open.className+=' an-outbox-summary';open.setAttribute('aria-label','View scheduled message: '+chatText(row.conv));open.setAttribute('data-schedule-id',row.id);
    open.append(node('time',new Date(row.due).toLocaleString([], {dateStyle:'medium',timeStyle:'short'}),{className:'an-schedule-when',dateTime:row.due}),node('strong',chatText(row.conv),{className:'an-outbox-chat'}),node('span',row.body?.slice(0,140)||'Attachment message',{className:'an-outbox-preview'}),node('span',states[row.state]||'Unavailable',{className:'hint an-outbox-status'}));article.append(open);
    if(selected?.id===row.id){
      const original=selected,detail=node('section','',{className:'an-schedule-detail'});detail.setAttribute('aria-label','Scheduled message details');
      detail.append(para(original.body||'Attachment message'),para(original.topic?topicText(original.topic,original.conv):'Main flow'),para(audienceText(original.audience||{},original.conv)));
      for(const file of original.files||[])detail.append(para(file.name));
      if(original.timezone&&original.timezone!==zone())detail.append(para('Saved timezone: '+original.timezone));
      if(original.state==='needs_review')detail.append(para('The chat, audience or attachments need review before sending.'));
      if(mutable(original)){
        if(editing)paintEdit(original,detail);
        else detail.append(button('Edit schedule',()=>{editing=true;editBody=original.body;editTime=localScheduleTime(new Date(original.due));removeFiles=false;review=null;approved=undefined;paint();},busy),button('Send now',()=>void action(()=>host.api('/api/schedule/send_now',{id:original.id,rev:original.rev})),busy),button('Cancel schedule',()=>void action(()=>host.api('/api/schedule/cancel',{id:original.id,rev:original.rev})),busy));
      }
      detail.append(button('Close details',()=>{selected=null;editing=false;review=null;approved=undefined;void refresh();},busy));article.append(detail);
    }
    return article;
  }
  function paint(){
    if(!viewing())return;
    update(false);createButton.setAttribute('aria-pressed',String(pane==='create'));listButton.setAttribute('aria-pressed',String(pane==='list'));for(const [key,b]of tabButtons)b.setAttribute('aria-pressed',String(pane===key));runtime.hidden=inOutbox&&pane!=='list';
    content.replaceChildren();if(notice)content.append(para(notice,'status'));
    if(inOutbox&&pane==='drafts'){paintDrafts();return;}
    if(inOutbox&&pane==='reminders'){paintReminders();return;}
    if(pane==='create')paintCreate();const s=slot();if(s)paintSlot(s);
    if(pane!=='list')return;
    if(loading)content.append(para('Loading scheduled messages…','status'));
    content.append(para('On this device · Local timezone: '+zone()));
    if(inOutbox)for(const pending of Object.values(ledger.snapshot())){
      const original=node('article','',{className:'an-outbox-row'});original.append(para(chatText(pending.conv)+' · '+(pending.status==='saving'?'Saving schedule':'Unconfirmed save')),button('Review original save',()=>void resume(()=>openOriginalSave?.(pending.conv)),busy||pending.status==='saving'||!openOriginalSave));content.append(original);
    }
    const history=inOutbox?historyRows:rows.filter(row=>row.state==='sent'||row.state==='cancelled'),current=rows.filter(row=>row.state!=='sent'&&row.state!=='cancelled');
    if(!loading&&!current.length)content.append(para('No scheduled messages pending here on this device.'));
    const renderRow=row=>{
      const article=node('article','',{className:'an-schedule-row'});article.setAttribute('aria-label','Scheduled message: '+(states[row.state]||'Unavailable'));
      article.append(node('time',new Date(row.due).toLocaleString([], {dateStyle:'medium',timeStyle:'short'}),{className:'an-schedule-when',dateTime:row.due}),node('p',row.body||'Attachment message',{className:'an-schedule-preview'}),para(states[row.state]||'Unavailable'),para(row.topic?topicText(row.topic):'Main flow'));
      for(const f of row.files||[])article.append(para(f.name));
      const recipients=node('details','',{className:'an-schedule-recipients'});recipients.append(node('summary','Recipients'),para(audienceText(row.audience||{},conv)));
      if(row.timezone&&row.timezone!==zone())recipients.append(para('Saved timezone: '+row.timezone));
      article.append(recipients);
      if(row.state==='needs_review')article.append(para('The chat, audience or attachments need review before sending.'));
      if(row.state==='dispatching')article.append(para('Preparing to send. Delivery is confirmed separately.'));
      if(mutable(row)){
        if(selected?.id===row.id)paintEdit(selected,article);
        else article.append(button('Edit schedule',()=>{selected=row;editBody=row.body;editTime=localScheduleTime(new Date(row.due));removeFiles=false;review=null;approved=undefined;notice='';paint();},busy),button('Send now',()=>void action(()=>host.api('/api/schedule/send_now',{id:row.id,rev:row.rev})),busy),button('Cancel schedule',()=>void action(()=>host.api('/api/schedule/cancel',{id:row.id,rev:row.rev})),busy));
      }
      return article;
    };
    const render=inOutbox?compactRow:renderRow;
    for(const row of current)content.append(render(row));
    if(history.length||inOutbox){
      const section=node('section','',{className:'an-schedule-history',hidden:!historyOpen});
      section.setAttribute('aria-label','Schedule history');
      if(historyOpen&&historyLoading)section.append(para('Loading schedule history…','status'));
      if(historyOpen||!inOutbox)for(const row of history)section.append(render(row));
      const toggle=button('',()=>{
        historyOpen=!historyOpen;if(inOutbox){if(historyOpen)void loadHistory();else{historyRequest++;historyLoading=false;paint();}return;}section.hidden=!historyOpen;labelHistory();
      });
      const labelHistory=()=>{const label=historyOpen?'Hide history':'Show history';toggle.textContent=label+(!inOutbox||historyLoaded?' ('+history.length+')':'');toggle.setAttribute('aria-label',label);toggle.setAttribute('aria-expanded',String(historyOpen));};
      labelHistory();content.append(toggle,section);
    }
  }
  const cancel=e=>{e.preventDefault();close();};dialog.addEventListener('cancel',cancel);
  // Freeze the open editor and its original revision. Repainting on a host
  // push replaces native inputs/focus and used to discard the entire edit.
  // Save still uses CAS; closing the editor explicitly reloads current state.
  off=subscribe?.(()=>{if(viewing()&&pane==='list'&&!selected)void refresh();else if(viewing()&&pane!=='create'&&pane!=='list')paint();});changed();
  return {openCreate,openList,openOutbox,close,update,get blocking(){return block();},destroy(){if(dead)return;close();dead=true;off?.();ledger.dispose();dialog.removeEventListener('cancel',cancel);menu.remove();}};
}
