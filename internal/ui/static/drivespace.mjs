// Drive is a client-side service. Tokens stay in memory in browser mode; only
// authenticated folder metadata uses the conversation's encrypted transport.
export const FILE_SCOPE='https://www.googleapis.com/auth/drive.file';
export const FULL_SCOPE='https://www.googleapis.com/auth/drive';
export const NOTICE='Google Drive files are outside AgentNet end-to-end encryption. Google permissions apply independently.';
const folderMIME='application/vnd.google-apps.folder';
const validID=id=>typeof id==='string'&&/^[A-Za-z0-9_-]{1,256}$/.test(id);
const api='https://www.googleapis.com/drive/v3';
const fileFields='id,name,mimeType,size,parents,trashed,capabilities(canAddChildren,canShare,canListChildren,canDownload)';
const driveURL=f=>f.mimeType===folderMIME?'https://drive.google.com/drive/folders/'+f.id:'https://drive.google.com/file/d/'+f.id+'/view';
export function daemonDriveProvider(fetchJSON, fetchRaw) {
  // The host supplies both callbacks captured to one immutable workspace.
  // Defaults are only for a standalone single-workspace daemon page.
  if (fetchJSON && !fetchRaw) {
    throw Error('Workspace Drive transport requires both JSON and raw upload callbacks');
  }
  const raw = fetchRaw || globalThis.fetch.bind(globalThis);
  const json = fetchJSON || (async function requestJSON(path, body) {
    const response = await raw(path, body === undefined ? {
    } : {
      method: 'POST',       headers: {
        'Content-Type': 'application/json'
      },       body: JSON.stringify(body),
    });
    if (!response.ok) throw Error(await response.text());
    return response.json();
  });
  async function drive(request) {
    if (request.action === 'status') {
      return json('/api/drive?conv=' + encodeURIComponent(request.conv));
    }
    return json('/api/drive', request);
  }
  async function driveUpload(conv, file, confirm) {
    if (!confirm) throw Error('Confirm plaintext Google upload');
    const query = new URLSearchParams({
      conv,       name: file.name,       size: String(file.size),       confirm_outside_e2ee: 'true',
    });
    const response = await raw('/api/drive/upload?' + query, {
      method: 'POST',       headers: {
        'Content-Type': 'application/octet-stream'
      },       body: file,
    });
    if (!response.ok) throw Error(await response.text());
    return response.json();
  }
  return {
    drive, driveUpload
  };
}
// A public Drive facade retains its captured membership. Consent is forwarded
// synchronously so a browser's click gesture reaches Google without an await.
export function boundDriveProvider(provider, check = () => {}) {
  const drive = {
    drive(request) { check(); return provider.drive(request); },
    driveUpload(conv, file, confirm) { check(); return provider.driveUpload(conv, file, confirm); },
  };
  if (provider.prepareGoogle) drive.prepareGoogle = () => { check(); return provider.prepareGoogle(); };
  if (provider.beginGoogleConsent) drive.beginGoogleConsent = request => { check(); return provider.beginGoogleConsent(request); };
  return Object.freeze(drive);
}
let gisLoading;
async function loadGIS(){
  if(globalThis.google?.accounts?.oauth2)return;
  if(!gisLoading)gisLoading=new Promise((resolve,reject)=>{
    const s=document.createElement('script');
    s.src='https://accounts.google.com/gsi/client';
    s.onload=resolve;
    s.onerror=()=>{
      gisLoading=null;
      reject(Error('Google consent script blocked. Configure OAuth origin and Google CSP access.'));
    };
    document.head.append(s);
  });
  await gisLoading;
}
// hooks are the browser engine's verified encrypted conversation-space store.
// getOwner resolves the local current member person; receive metadata must be
// authenticated independently by engine. Never infer Google emails from names.
export function browserDriveProvider({
  clientID='',getConfig,getSpace,publishSpace,getOwner,getGrant,setGrant,participation,getPending,setPending,fetchImpl=fetch,gis=loadGIS
}){
  let token=null,account='',configured=clientID,mutation=Promise.resolve(),cachedConfig=null;
  const serialize=fn=>{
    const p=mutation.then(fn,fn);
    mutation=p.catch(()=>{
    });
    return p;
  };
  async function request(path,{
    method='GET',body,headers={
    }
  }={
  }){
    if(!token||Date.now()>=token.expiry){
      token=null;
      throw Error('Google consent expired; connect Google again');
    }
    let r;
    try{
      r=await fetchImpl(path,{
        method,headers:{
          ...headers,Authorization:'Bearer '+token.access
        },body,redirect:'error',credentials:'omit'
      });
    }catch{
      throw Error('Google Drive unavailable; retry when online');
    }
    if(!r.ok){
      if(r.status===401){
        token=null;
        throw Error('Google consent expired or revoked; reconnect');
      }
      if(r.status===403||r.status===404)throw Error('Google access denied; check account, consent and folder permissions');
      throw Error('Google Drive request failed (HTTP '+r.status+')');
    }
    if(r.status===204)return null;
    try{
      return await r.json();
    }catch{
      throw Error('Invalid Google Drive response');
    }
  }
  async function folder(id){
    if(!validID(id))throw Error('Invalid Drive folder ID');
    const f=await request(api+'/files/'+id+'?supportsAllDrives=true&fields='+encodeURIComponent(fileFields));
    if(f.mimeType!==folderMIME||f.trashed)throw Error('Selected item is not an accessible Drive folder');
    return f;
  }
  async function activeGrants(conv,s){
    const raw=await getGrant?.(conv)||{
    },out={
    };
    for(const[pid,g]of Object.entries(raw)){
      if(s&&!s.disconnected&&g.folder===s.folder&&g.revision===s.revision&&g.consent===token?.consent)out[pid]=g.level;
    }
    return out;
  }
  async function view(conv){
    const settings=await getConfig?.();
 cachedConfig=settings;
    if(!settings?.enabled)return {
      enabled:false,configured:false,connected:false,notice:'Google Drive off. Admin may enable it in Settings > File storage options. Native encrypted attachments remain available.'
    };
    if(!settings.browser_client_id)throw Error('Workspace admin has not configured browser Google OAuth client');
    const requiresReconnect=!!token&&configured!==settings.browser_client_id;
    if(!token)configured=settings.browser_client_id;
    const owner=await getOwner(conv);
    if(!owner)throw Error('Current person is not a verified conversation member');
    const s=await getSpace(conv);
    return {
      enabled:true,requires_reconnect:requiresReconnect,configured:!!configured,connected:!!token&&Date.now()<token.expiry,full:token?.scopes?.includes(FULL_SCOPE)||false,account,space:s?.disconnected?undefined:s,pending:await getPending?.(conv),notice:NOTICE,grants:await activeGrants(conv,s)
    };
  }
  function prepare(){
    return gis();
  }
  function beginGoogleConsent({conv,full=false,confirm_account=false}){
 if(!confirm_account)throw Error("Confirm connecting your Google account on this browser");
 if(!cachedConfig?.enabled||!cachedConfig.browser_client_id)throw Error("Load enabled workspace Google setup first");
 if(!globalThis.google?.accounts?.oauth2)throw Error("Prepare Google consent, then click Connect again");
 if(token&&configured!==cachedConfig.browser_client_id)throw Error("Disconnect and explicitly reconnect after workspace Google client change");
 configured=cachedConfig.browser_client_id;
 const started=consent(full); // requestAccessToken starts synchronously in the actual click handler
 return started.then(()=>view(conv));
 }
 async function consent(full){
    if(!configured)throw Error('Configure a Google Web OAuth client ID and authorized origin first');
    if(!globalThis.google?.accounts?.oauth2)throw Error("Prepare Google consent, then click Connect again");
    const response=await new Promise((resolve,reject)=>{
      const c=google.accounts.oauth2.initTokenClient({
        client_id:configured,scope:full?FULL_SCOPE:FILE_SCOPE,include_granted_scopes:false,callback:r=>r.error?reject(Error('Google consent declined')):resolve(r),error_callback:()=>reject(Error('Google consent window closed or unavailable'))
      });
      c.requestAccessToken({
        prompt:'consent select_account'
      });
    });
    if(!response.access_token||!Number.isFinite(Number(response.expires_in))||Number(response.expires_in)<=0||Number(response.expires_in)>86400)throw Error('Invalid Google token response');
    const scopes=String(response.scope||'').split(' ');
    if(!scopes.includes(full?FULL_SCOPE:FILE_SCOPE))throw Error('Required Google scope was not granted');
    token={
      access:response.access_token,expiry:Date.now()+Number(response.expires_in)*1000,scopes,consent:crypto.randomUUID()
    };
    const a=await request(api+'/about?fields=user(permissionId,emailAddress,displayName)');
    if(!a.user?.permissionId){
      token=null;
      throw Error('Cannot verify consenting Google account');
    }
    account=a.user.emailAddress||a.user.displayName||'Connected Google account';
  }
  const service={
    prepareGoogle:prepare,beginGoogleConsent,async drive(r){
      return serialize(async()=>{
        const v=await view(r.conv);
        if(v.requires_reconnect&&r.action!=='disconnect-account'&&r.action!=='status')throw Error('Workspace Google client changed; explicitly disconnect and reconnect');
        if(!v.enabled&&r.action!=='status'&&r.action!=='disconnect-account')throw Error('Optional Google Drive disabled by workspace admin');
        switch(r.action||'status'){
          case 'status':return v;
          case 'configure':throw Error('Workspace admin configures public Google client IDs in Settings > File storage options');
          case 'consent':if(!r.confirm_account)throw Error('Confirm connecting your Google account on this browser');
          await consent(r.full);
          return view(r.conv);
          case 'disconnect-account':{
            const access=token?.access;
            token=null;
            account='';
            if(access&&globalThis.google?.accounts?.oauth2)await new Promise(resolve=>google.accounts.oauth2.revoke(access,resolve));
            return view(r.conv);
          }  case 'create':case 'connect':case 'disconnect-space':case 'publish-pending':{
            if(!publishSpace||!getPending||!setPending)throw Error('Encrypted conversation Drive transport and recovery store unavailable');
            const owner=await getOwner(r.conv);
            const prior=await getSpace(r.conv);
            if(prior&&prior.owner!==owner)throw Error('Only conversation space owner may change folder');
            if(!r.confirm_outside_e2ee)throw Error('Confirm Drive storage outside AgentNet E2EE');
            const recovery=await getPending(r.conv);
            if(recovery&&r.action!=='publish-pending')return {
              ...v,pending:recovery,notice:NOTICE+' Folder already exists; retry publication instead of creating another.'
            };
            let f;
            if(r.action==='publish-pending'){
              if(!recovery||recovery.owner!==owner)throw Error('No saved folder publication owned here');
              f={
                id:recovery.folder,name:recovery.name
              };
            }else if(r.action==='create'){
              if(!r.name?.trim()||r.name.length>255)throw Error('Folder name required');
              f=await request(api+'/files?supportsAllDrives=true&fields='+encodeURIComponent(fileFields),{
                method:'POST',headers:{
                  'Content-Type':'application/json'
                },body:JSON.stringify({
                  name:r.name,mimeType:folderMIME
                })
              });
            }else if(r.action==='connect'){
              if(!v.full)throw Error('Existing folders require explicit full-folder consent; drive.file cannot promise all existing children');
              f=await folder(r.folder);
            }else{
              if(!v.space)throw Error('No conversation Drive space');
              f={
                id:v.space.folder,name:v.space.name
              };
            }
            const old=await getSpace(r.conv);
            let s={
              conv:r.conv,folder:f.id,name:f.name,owner,revision:(old?.revision||0)+1,...(old?{
                previous:old.folder
              }:{
              }),...(r.action==='disconnect-space'?{
                disconnected:true
              }:{
              })
            };
            if(r.action==='publish-pending')s=recovery;
            await setPending(r.conv,s);
            try{
              await publishSpace(s);
            }catch{
              return {
                ...v,pending:s,notice:NOTICE+' Google folder exists. Conversation sharing unconfirmed. Retry saved publication; do not create again.'
              };
            }await setPending(r.conv,null);
            return view(r.conv);
          }  case 'list':{
            if(!v.space)throw Error('Connect conversation Drive folder first');
            const f=await folder(v.space.folder);
            if(!f.capabilities?.canListChildren)throw Error('Folder permissions do not allow listing');
            const q=new URLSearchParams({
              q:"'"+f.id+"' in parents and trashed = false",fields:'nextPageToken,incompleteSearch,files('+fileFields+')',pageSize:'100',supportsAllDrives:'true',includeItemsFromAllDrives:'true'
            });
            if(r.page)q.set('pageToken',r.page);
            return {
              ...v,folder:f,page:await request(api+'/files?'+q)
            };
          }  case 'share':case 'remove-permission':case 'permissions':{
            if(!v.space||v.space.owner!==await getOwner(r.conv))throw Error('Only conversation space owner manages access here');
            const f=await folder(v.space.folder);
            if(!f.capabilities?.canShare)throw Error('Google folder permissions do not allow sharing');
            const base=api+'/files/'+f.id+'/permissions';
            if(r.action==='permissions'){
              const q=new URLSearchParams({
                supportsAllDrives:'true',fields:'nextPageToken,permissions(id,type,role,emailAddress,permissionDetails)'
              });
              if(r.page)q.set('pageToken',r.page);
              const p=await request(base+'?'+q);
              return {
                ...v,permissions:p.permissions,next:p.nextPageToken
              };
            }
            if(!r.confirm_access)throw Error('Confirm explicit Google permission change');
            if(r.action==='share'){
              if(!['reader','writer'].includes(r.role)||!r.email?.includes('@'))throw Error('Explicit Google email and reader/writer role required');
              await request(base+'?supportsAllDrives=true&sendNotificationEmail=true',{
                method:'POST',headers:{
                  'Content-Type':'application/json'
                },body:JSON.stringify({
                  type:'user',emailAddress:r.email,role:r.role
                })
              });
            }else{
              if(!validID(r.permission))throw Error('Invalid Google permission ID');
              await request(base+'/'+r.permission+'?supportsAllDrives=true',{
                method:'DELETE'
              });
            }
            return {
              ...v,notice:NOTICE+' Removing folder permission does not remove independent child-file or inherited access. Conversation membership never changes Google permissions.'
            };
          }  case 'grant':{
            if(!v.space)throw Error('Connect a Drive space before granting agent access');
            if(!token)throw Error('Connect Google before granting agent access');
            if(!setGrant||!participation)throw Error('Agent Drive broker unavailable');
            const p=await participation(r.pid);
            if(p.conv!==r.conv||p.state!=='active'||!p.host_here||p.held)throw Error('Active agent participation hosted here required');
            if(!['none','read','write'].includes(r.grant))throw Error('Grant must be none, read or write');
            await setGrant(r.conv,r.pid,{
              level:r.grant,folder:v.space.folder,revision:v.space.revision,consent:token.consent
            });
            return view(r.conv);
          }  default:throw Error('Unknown Drive action');
        }
      });
    },async driveUpload(conv,file,confirm){
      return serialize(async()=>{
        if(!confirm)throw Error('Confirm upload outside AgentNet E2EE');
        if(file.size>32*1024*1024)throw Error('Drive uploads limited to 32 MiB');
        const v=await view(conv);
        if(!v.enabled)throw Error('Optional Google Drive disabled by workspace admin');
        if(!v.space)throw Error('Connect conversation folder first');
        const f=await folder(v.space.folder);
        if(!f.capabilities?.canAddChildren)throw Error('Google folder does not allow upload');
        const boundary='agentnet-'+crypto.randomUUID();
        const metadata=JSON.stringify({
          name:file.name,parents:[f.id]
        });
        const body=new Blob(['--'+boundary+'\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n'+metadata+'\r\n--'+boundary+'\r\nContent-Type: application/octet-stream\r\n\r\n',file,'\r\n--'+boundary+'--\r\n']);
        return request('https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&supportsAllDrives=true&fields='+encodeURIComponent(fileFields),{
          method:'POST',headers:{
            'Content-Type':'multipart/related; boundary='+boundary
          },body
        });
      });
    },async brokerList(conv,pid,page=''){
      const p=await participation(pid),g=(await activeGrants(conv,await getSpace(conv)))[pid];
      if(p.conv!==conv||p.state!=='active'||!p.host_here||p.held||!['read','write'].includes(g))throw Error('Agent has no Drive read grant');
      return service.drive({
        conv,action:'list',page
      });
    },async brokerUpload(conv,pid,file){
      const p=await participation(pid),g=(await activeGrants(conv,await getSpace(conv)))[pid];
      if(p.conv!==conv||p.state!=='active'||!p.host_here||p.held||g!=='write')throw Error('Agent has no Drive write grant');
      return service.driveUpload(conv,file,true);
    }
  };
  return service;
}
// Mount separate pane in current conversation. Every Google action explicit.
export function mountDriveSpace(container,{
  conv,provider=daemonDriveProvider(),agents=[],openSettings
}){
  let destroyed=false,busy=false,view={
    notice:NOTICE
  },page=null;
  const root=document.createElement('section');
  root.className='drive-space';
  root.setAttribute('aria-label','Conversation Google Drive space');
  container.append(root);
  function el(tag,text,attrs={
  }){
    const n=document.createElement(tag);
    if(text)n.textContent=text;
    for(const[k,v]of Object.entries(attrs))n.setAttribute(k,v);
    return n;
  }
  function input(label,type='text'){
    const wrap=el('label',label),i=el('input',null,{
      type
    });
    wrap.append(i);
    return [wrap,i];
  }
  function button(text,fn){
    const b=el('button',text,{
      type:'button'
    });
    b.disabled=busy;
    b.onclick=fn;
    return b;
  }
  async function run(req,pending){
    if(busy)return;
    busy=true;
    render();
    try{
      view=await (pending||provider.drive({
        conv,...req
      }));
      page=view.page||null;
      error='';
    }catch(e){
      error=e.message;
    }finally{
      busy=false;
      if(!destroyed)render();
    }
  }
  function connectGoogle(full) {
    if (!provider.beginGoogleConsent) return run({action:'consent',confirm_account:true,full});
    try {
      // Invoke before await/render so GIS receives the original user gesture.
      const pending = provider.beginGoogleConsent({conv,full,confirm_account:true});
      return run({}, pending);
    } catch (failure) {
      error = failure.message;
      render();
      if (provider.prepareGoogle) {
        provider.prepareGoogle().then(() => {
          error = 'Google consent ready. Click Connect Google again.';
          render();
        }).catch(failure => { error = failure.message; render(); });
      }
    }
  }
  let error='';
  function render(){
    root.replaceChildren(el('h3','Project space · Google Drive'),el('p',NOTICE));
    root.append(el('p','Conversation joins or leaves do not grant or revoke Google access. Each person connects their own Google account.'));
    if(error){
      const e=el('p',error,{
        role:'alert'
      });
      e.className='drive-error';
      root.append(e);
    }
    if(view.enabled===false){
      root.append(el('p',view.notice||'Google Drive off. Native encrypted attachments remain available.'),button('File storage options',()=>openSettings?.()));
      return;
    }
    if(!view.configured){
      const [w,id]=input('Google OAuth client ID');
      const[w2,secret]=input('Desktop client secret (optional)','password');
      root.append(w,w2,button('Save local Google setup',()=>run({
        action:'configure',client_id:id.value.trim(),client_secret:secret.value
      })),el('p','Desktop: register Desktop app OAuth client. Browser: register Web client and this authorized origin. Enable Drive API. Credentials remain on this client.'));
      return;
    }
    root.append(el('p',view.connected?'Connected: '+(view.account||'Google consent on this client'):'Google disconnected on this client.'));
    root.append(button(view.connected?'Reconnect Google':'Connect Google',()=>connectGoogle(false)),button('Consent to full existing-folder access',()=>connectGoogle(true)));
    root.append(el('p','Full-folder consent permits access to all Drive files in your Google account; required here to list existing children. Default consent sees app-authorized files only.'));
    if(view.consent_url)root.append(el('a','Continue Google consent in browser',{
      href:view.consent_url,target:'_blank',rel:'noopener noreferrer'
    }));
    if(view.requires_reconnect)root.append(el('p','Workspace Google client changed. Disconnect and explicitly reconnect; existing token remains private until you do.'));
    if(view.connected)root.append(button('Disconnect Google on this client',()=>run({
      action:'disconnect-account'
    })));
    if(view.pending){
      root.append(el('p',view.notice),el('p','Saved folder ID: '+view.pending.folder),el('a',view.pending.name+' · not confirmed shared',{
        href:'https://drive.google.com/drive/folders/'+view.pending.folder,target:'_blank',rel:'noopener noreferrer'
      }),button('Retry sharing saved folder',()=>run({
        action:'publish-pending',confirm_outside_e2ee:true
      })));
      return;
    }
    if(!view.space){
      const[w,name]=input('New project folder name');
      const[w2,id]=input('Existing Google folder ID');
      const[w3,confirm]=input('I understand Google files are outside AgentNet E2EE','checkbox');
      root.append(w,w2,w3,button('Create folder',()=>run({
        action:'create',name:name.value,confirm_outside_e2ee:confirm.checked
      })),button('Connect existing folder',()=>run({
        action:'connect',folder:id.value.trim(),confirm_outside_e2ee:confirm.checked
      })));
      return;
    }
    root.append(el('a',view.space.name,{
      href:'https://drive.google.com/drive/folders/'+view.space.folder,target:'_blank',rel:'noopener noreferrer'
    }),button('List project files',()=>run({
      action:'list'
    })));
    const [uw,upload]=input('Add file to project space','file'),[cw,confirm]=input('Upload plaintext to Google Drive','checkbox');
    root.append(uw,cw,button('Upload file',async()=>{
      if(!upload.files[0]||!confirm.checked){
        error='Choose file and confirm Google plaintext storage';
        render();
        return;
      }busy=true;
      render();
      try{
        await provider.driveUpload(conv,upload.files[0],true);
        busy=false;
        await run({
          action:'list'
        });
      }catch(e){
        busy=false;
        error=e.message;
        render();
      }
    }));
    if(page){
      if(page.incompleteSearch)root.append(el('p','Google returned incomplete search results.'));
      if(!view.full)root.append(el('p','Only app-authorized files shown. Existing folder children may be omitted under drive.file.'));
      const ul=el('ul');
      for(const f of page.files||[]){
        if(!validID(f.id))continue;
        const li=el('li');
        li.append(el('a',f.name||f.id,{
          href:driveURL(f),target:'_blank',rel:'noopener noreferrer'
        }));
        ul.append(li);
      }
      root.append(ul);
      if(page.nextPageToken)root.append(button('Next file page',()=>run({
        action:'list',page:page.nextPageToken
      })));
    }  const[w,email]=input('Explicit Google email to share with');
    const role=el('select');
    role.append(el('option','Reader',{
      value:'reader'
    }),el('option','Writer',{
      value:'writer'
    }));
    const[cw2,shareConfirm]=input('Confirm direct Google permission change','checkbox');
    root.append(w,role,cw2,button('Share folder',()=>run({
      action:'share',email:email.value.trim(),role:role.value,confirm_access:shareConfirm.checked
    })),button('Inspect permissions',()=>run({
      action:'permissions'
    })));
    for(const p of view.permissions||[]){
      const row=el('p',(p.emailAddress||p.type)+' · '+p.role);
      const inherited=(p.permissionDetails||[]).some(d=>d.inherited);
      if(inherited)row.append(el('span',' · inherited; change at source'));
      else row.append(button('Remove direct grant',()=>run({
        action:'remove-permission',permission:p.id,confirm_access:shareConfirm.checked
      })));
      root.append(row);
    }
    if(view.next)root.append(button('Next permission page',()=>run({
      action:'permissions',page:view.next
    })));
    root.append(el('p','Removing a folder grant cannot revoke independently shared children. Agent grants control AgentNet broker only; existing harness Google tools retain their own permissions.'));
    for(const a of agents){
      const label=el('label','Agent '+a.pid+' Drive access'),select=el('select');
      for(const grant of ['none','read','write'])select.append(el('option',grant,{
        value:grant
      }));
      select.value=view.grants?.[a.pid]||'none';
      label.append(select);
      root.append(label,button('Save agent grant',()=>run({
        action:'grant',pid:a.pid,grant:select.value
      })));
    }  const[dw,disconnect]=input('Disconnect conversation folder (Google permissions unchanged)','checkbox');
    root.append(dw,button('Disconnect space',()=>run({
      action:'disconnect-space',confirm_outside_e2ee:disconnect.checked
    })));
  }  render();
  run({
    action:'status'
  });
  return {
    destroy(){
      destroyed=true;
      root.remove();
    },refresh(){
      return run({
        action:'status'
      });
    }
  };
}
// Attachment controls call this after their own explicit confirmation UI. The
// daemon validates conversation/direction and signed manifest before uploading.
export function saveAttachmentToDrive(provider,conv,message,index,confirm){
  if(!confirm)throw Error('Confirm saving attachment outside AgentNet E2EE');
  return provider.drive({
    conv,action:'save-attachment',dir:message.dir,message:message.id,index,confirm_outside_e2ee:true
  });
}
