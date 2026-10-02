// Optional provider setup belongs in Settings > File storage options only.
// Commands are guidance; this module never runs gcloud or creates cloud resources.
export function daemonStorageSetupProvider(fetchJSON, fetchRaw) {
  if (fetchJSON && !fetchRaw) {
    throw Error('Workspace storage setup requires one captured JSON and raw transport');
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
  async function storageSetup(request) {
    if (!request?.action || request.action === 'status') {
      return json('/api/drive/setup');
    }
    return json('/api/drive/setup', request);
  }
  return {
    storageSetup
  };
} export function mountFileStorageOptions(container,{
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
export function browserStorageSetupProvider({
  call, readDraft, writeDraft
}) {
  if (!call || !readDraft || !writeDraft) {
    throw Error('Browser storage setup needs captured signed Engine and local draft store');
  }
  async function storageSetup(request = {
    action: 'status'
  }) {
    let settings = await call('GET', '/v1/storage/drive');
    let draft = await readDraft() || {
      config: settings.config,       existing_project: true,       completed: {
      },
    };
    if (request.action === 'local-secret') {
      throw Error('Browser uses Google Web OAuth consent; no Desktop secret on browser');
    }     if (request.action === 'save-draft' || request.action === 'publish') {
      if (!settings.can_admin) throw Error('Workspace admin only');
      draft = request.draft;
      browserSetupGuide(draft);
      // validates safe account/project command arguments
      if (request.action === 'publish') {
        if (!request.confirm) throw Error('Confirm public workspace configuration');
        settings = await call('PUT', '/v1/admin/storage/drive', {
          config: draft.config,           expect: request.expect,
        });
      }       await writeDraft(draft);
    } else if (request.action !== 'status' && request.action) {
      throw Error('Unknown file storage setup action');
    }
    return {
      runtime: 'browser',       settings,       draft,       steps: browserSetupGuide(draft),       notice: 'Google Drive optional, off by default. Checklist records admin confirmation, not live Google verification. Only public client IDs reach workspace Hub. Each person separately consents; tokens stay on their client.',
    };
  }
  return {
    storageSetup
  };
}
function browserSetupGuide(draft) {
  const config = draft?.config;
  if (!config || typeof config !== 'object') throw Error('Setup configuration required');
  const allowed = new Set([     'enabled', 'project', 'desktop_client_id', 'browser_client_id',     'browser_origins', 'api_confirmed', 'consent_confirmed', 'clients_confirmed',   ]);
  if (Object.keys(config).some(key => !allowed.has(key))) {
    throw Error('Workspace config allows only public project/client IDs and origins');
  }
  const project = config.project || '';
  const account = draft.cloud_account || '';
  if (project && !/^[a-z][a-z0-9-]{4,28}[a-z0-9]$/.test(project)) {
    throw Error('Google project ID must be 6-30 lowercase letters, digits or hyphens');
  }   if (account && (!/^[A-Za-z0-9._+%-]+@[A-Za-z0-9.-]+$/.test(account))) {
    throw Error('Choose exact verified Google Cloud account email');
  }
  const completed = draft.completed || {
  };
  const projectCommands = [];
  const apiCommands = [];
  if (project && account) {
    if (!draft.existing_project) {
      projectCommands.push(`gcloud projects create ${project} --account='${account}'`);
    }     projectCommands.push(`gcloud projects describe ${project} --account='${account}' --format='table(projectId,name,lifecycleState)'`);
    apiCommands.push(`gcloud services enable drive.googleapis.com --project=${project} --account='${account}'`);
    apiCommands.push(`gcloud services list --enabled --project=${project} --account='${account}' --filter='config.name:drive.googleapis.com' --format='value(config.name)'`);
  }
  return [     {
    id: 'account', title: 'Choose Google Cloud account',       detail: 'Inspect signed-in gcloud accounts, then explicitly choose intended admin account. No CLI default changes.',       commands: ["gcloud auth list --format='table(account,status)'"],       url: 'https://docs.cloud.google.com/sdk/docs/install',
  },     {
    id: 'project', title: 'Create or select project',       detail: 'Choose existing project or new unique project ID. Verify account and project before cloud mutations. Organization restrictions may need your admin.',       commands: projectCommands,       url: 'https://docs.cloud.google.com/sdk/gcloud/reference/projects/create',
  },     {
    id: 'api', title: 'Enable Google Drive API',       detail: 'Review account and project, then explicitly run enable command and inspect enabled API result. A checkbox is not provider proof.',       commands: apiCommands,       url: 'https://docs.cloud.google.com/sdk/gcloud/reference/services/enable',
  },     {
    id: 'consent', title: 'Configure Google Auth platform',       detail: 'Use Cloud Console Branding, Audience and Data Access. Default drive.file; full existing-folder mode additionally needs explicit drive scope. Configure test users and scope verification.',       url: 'https://developers.google.com/workspace/guides/configure-oauth-consent',
  },     {
    id: 'clients', title: 'Create Google OAuth clients',       detail: 'Cloud Console Google Auth platform > Clients: Desktop app for daemon PKCE, Web application for browser GIS with exact authorized origins. Copy public IDs. Do not use IAP/IAM OAuth clients for Google Drive.',       url: 'https://developers.google.com/workspace/guides/create-credentials',
  },     {
    id: 'enable', title: 'Enable optional Google Drive',       detail: 'Save public workspace setup only. Each person separately consents. Checklist records admin confirmation, not live Google verification; native encrypted attachments work with Drive off.',
  },   ].map(step => ({
    ...step, complete: !!completed[step.id]
  }));
}
