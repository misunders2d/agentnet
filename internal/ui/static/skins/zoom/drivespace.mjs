// Presentation only: the captured public host supplies the Drive provider.
// Google access and credentials remain provider-owned.
export const NOTICE='Google Drive files are outside AgentNet end-to-end encryption. Google permissions apply independently.';
const folderMIME='application/vnd.google-apps.folder';
const validID=id=>typeof id==='string'&&/^[A-Za-z0-9_-]{1,256}$/.test(id);
const driveURL=f=>f.mimeType===folderMIME?'https://drive.google.com/drive/folders/'+f.id:'https://drive.google.com/file/d/'+f.id+'/view';
export function mountDriveSpace(container,{
  conv,provider,agents=[],openSettings
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
