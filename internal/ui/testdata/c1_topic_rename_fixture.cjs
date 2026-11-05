// Synthetic transport for the actual Conversation/Store fixture. No real store.
function topicRenameFixture(config) {
 const need=(ok,message)=>{if(!ok)throw Error('C1 rename fixture: '+message);};
 need(config&&['topicbar','person'].includes(config.placement),'explicit placement required');
 need(['held','normal','failing'].includes(config.mode),'explicit backend mode required');
 need(Number.isSafeInteger(config.history)&&config.history>0,'explicit history size required');
 const clone=x=>structuredClone(x),conv='a'.repeat(64),topic='3'.repeat(32),at='2026-11-05T00:00:00Z';
 const self={person:'1'.repeat(32),label:'Self',address:'self/laptop',state:'self',devices:[]};
 const peer={person:'2'.repeat(32),label:'Peer',address:'peer/laptop',state:'pinned',devices:[]};
 const t={id:topic,conv,title:'Original renamed topic',auto_title:'Automatic topic title',renamed:true,state:'active',count:config.history,last:'Source history',last_at:at,unread:0,review:0,running:0};
 const view={id:conv,kind:config.placement==='person'?'dm':'group',title:'Synthetic group',created:at,peer,mine:true,role:'member',members:[self,peer],agents:[],guests:[],topics:[t],frozen:'',
  messages:Array.from({length:config.history},(_,i)=>({id:(i+10).toString(16).padStart(32,'0'),lid:(i+10).toString(16).padStart(32,'0'),topic,body:'Source message '+i,dir:'in',from:peer.address,kind:'message',at,state:'stored',unread:false,actions:[],reactions:[]}))};
 const overview={seq:1,version:'synthetic-c1-rename',role:'person',groups:true,persons:true,topic_list:true,me:{address:self.address},person:self,people:[self,peer],threads:[],needs_you:[],dms:[]};
 const sync=()=>{overview.dms=[{...view,messages:undefined,count:config.history,last_at:at,unread:0}];};sync();
 const records=[],pending=new Map(),unexpected=[];
 function settle(seq,kind){
  const item=pending.get(seq);need(item,'unknown/completed request');need(['resolve','reject'].includes(kind),'unknown settlement');pending.delete(seq);
  item.record.state=kind==='resolve'?'resolved':'rejected';
  if(kind==='reject'){item.reject(Error('Synthetic rename refused'));return;}
  t.title=item.record.title||t.auto_title;t.renamed=!!item.record.title;overview.seq++;sync();item.resolve({note:'Synthetic rename confirmed'});
 }
 async function api(p,b){
  const u=new URL(p,'http://fixture.invalid');
  if(u.pathname==='/api/overview')return clone(overview);
  if(u.pathname==='/api/dm'){need(u.searchParams.get('id')===conv,'wrong conversation read');return clone(view);}
  if(u.pathname==='/api/topic/rename'){
   need(b&&b.conv===conv&&b.id===topic&&!b.root&&typeof b.title==='string','exact named topic required');
   const record={seq:records.length+1,title:b.title,conv:b.conv,id:b.id,state:'pending'};records.push(record);
   const result=new Promise((resolve,reject)=>pending.set(record.seq,{record,resolve,reject}));
   if(config.mode!=='held')queueMicrotask(()=>settle(record.seq,config.mode==='failing'?'reject':'resolve'));
   return result;
  }
  if(!b&&u.pathname==='/api/agents')return{host:self.address,local:true,agents:[],sessions:[]};
  if(!b&&u.pathname==='/api/groups/invitations')return[];
  if(!b&&u.pathname==='/api/typing/status')return{send:false,scopes:[]};
  if(!b&&u.pathname==='/api/schedules')return[];
  if(u.pathname==='/api/refresh'||u.pathname==='/api/topic/transfer/location')return{};
  unexpected.push({path:u.pathname,mutation:!!b});throw Error('Unexpected C1 rename request '+u.pathname);
 }
 return{config:clone(config),conv,topic,api,settle,
  witness:()=>({mode:config.mode,placement:config.placement,history:config.history,requests:clone(records),pending:pending.size,unexpected:clone(unexpected),confirmed_title:t.title})};
}
module.exports={topicRenameFixture};
