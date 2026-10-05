export function mountFileStorageOptions(container,{
  provider=daemonStorageSetupProvider()
}){
  const root=document.createElement('section');
  root.className='drive-space';
  root.setAttribute('aria-label','File storage options');
  container.append(root);
  let view=null,error='',busy=false;
  const node=(tag,text,attrs={
  })=>{
    const n=document.createElement(tag);
    if(text)n.textContent=text;
    for(const[k,v]of Object.entries(attrs))n.setAttribute(k,v);
    return n;
  };
  const input=(label,value='',type='text')=>{
    const l=node('label',label),i=node('input',null,{
      type
    });
    if(type==='checkbox')i.checked=!!value;
    else i.value=value;
    l.append(i);
    root.append(l);
    return i;
  };
  const button=(text,fn)=>{
    const b=node('button',text,{
      type:'button'
    });
    b.disabled=busy;
    b.onclick=fn;
    root.append(b);
    return b;
  };
  async function run(r={
    action:'status'
  }){
    if(busy)return;
    busy=true;
    try{
      view=await provider.storageSetup(r);
      error='';
    }catch(e){
      error=e.message;
    }finally{
      busy=false;
      render();
    }
  }
  function render(){
    root.replaceChildren(node('h3','File storage options'),node('p','Native AgentNet attachments stay end-to-end encrypted and need no Google setup. Google Drive optional, off by default.'));
    if(error)root.append(node('p',error,{
      role:'alert'
    }));
    if(!view){
      button('Load storage options',()=>run());
      return;
    }
    root.append(node('p',view.settings.config.enabled?'Google Drive enabled for workspace':'Google Drive off for workspace'),node('p',view.notice));
    if(!view.settings.can_admin){
      root.append(node('p','Workspace admin configures Google project and public OAuth client IDs. Each person separately connects their own Google account in a conversation.'));
      localSecret();
      return;
    }
    const d=view.draft,c=d.config;
    const enabled=input('Enable optional Google Drive',c.enabled,'checkbox'),existing=input('Use existing Google Cloud project',d.existing_project,'checkbox'),account=input('Google Cloud account verified by you (local admin draft only)',d.cloud_account||''),project=input('Google Cloud project ID',c.project||''),desktop=input('Public Desktop OAuth client ID',c.desktop_client_id||''),web=input('Public Web OAuth client ID (browser mode)',c.browser_client_id||''),origins=input('Exact browser authorized origins (comma separated)',(c.browser_origins||[]).join(', '));
    const complete={
    };
    for(const step of view.steps){
      const detail=node('details'),sum=node('summary',step.title);
      detail.append(sum,node('p',step.detail));
      if(step.url)detail.append(node('a','Official setup instructions',{
        href:step.url,target:'_blank',rel:'noopener noreferrer'
      }));
      for(const cmd of step.commands||[])detail.append(node('pre',cmd));
      root.append(detail);
      complete[step.id]=input('Admin-confirmed: '+step.title,d.completed?.[step.id]||false,'checkbox');
    }
    const draft=()=>({
      config:{
        enabled:enabled.checked,project:project.value.trim(),desktop_client_id:desktop.value.trim(),browser_client_id:web.value.trim(),browser_origins:origins.value.split(',').map(s=>s.trim()).filter(Boolean),api_confirmed:complete.api.checked,consent_confirmed:complete.consent.checked,clients_confirmed:complete.clients.checked
      },cloud_account:account.value.trim(),existing_project:existing.checked,completed:Object.fromEntries(Object.entries(complete).map(([id,i])=>[id,i.checked]))
    });
    button('Save local setup progress',()=>run({
      action:'save-draft',draft:draft()
    }));
    const confirm=input('Confirm saving public workspace provider configuration; no account consent or Google mutation occurs here',false,'checkbox');
    button('Save workspace storage option',()=>{
      if(!confirm.checked){
        error='Confirm public workspace configuration change';
        render();
        return;
      }run({
        action:'publish',draft:draft(),expect:view.settings.revision,confirm:true
      });
    });
    root.append(node('p','Supported gcloud commands create/select a project and enable Drive API. OAuth consent and Desktop/Web client creation use Google Cloud console; do not use IAP/IAM OAuth client commands for Drive. Checklist marks are admin confirmation, not live provider verification.'));
    localSecret();
  }
  function localSecret(){
    if(view.runtime==='browser')return;
    if(!view.settings.config.enabled||!view.settings.config.desktop_client_id)return;
    const details=node('details');
    details.append(node('summary','This client: optional Desktop OAuth client secret'),node('p','Stored privately on this client only. Never saved in Hub settings or conversation history. Disconnect Google before changing.'));
    const label=node('label','Desktop client secret'),secret=node('input',null,{
      type:'password'
    });
    label.append(secret);
    details.append(label);
    const b=node('button','Save private client secret',{
      type:'button'
    });
    b.onclick=()=>run({
      action:'local-secret',desktop_client_secret:secret.value
    });
    details.append(b);
    root.append(details);
  }  render();
  run();
  return {
    destroy(){
      root.remove();
    },refresh(){
      return run();
    }
  };
}
// The host binds call to one Engine instance and draft callbacks to that
// workspace's local encrypted store. Switching workspace never retargets them.
