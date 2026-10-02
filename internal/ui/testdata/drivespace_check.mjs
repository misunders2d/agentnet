import assert from 'node:assert/strict';
import {
  browserDriveProvider,FILE_SCOPE,FULL_SCOPE
} from '../static/drivespace.mjs';
import {
  webcrypto
} from 'node:crypto';
if(!globalThis.crypto)Object.defineProperty(globalThis,"crypto",{
  value:webcrypto
});
let currentUser='alice',expire=false,deny=false,offline=false,revoked=0;
globalThis.google={
  accounts:{
    oauth2:{
      initTokenClient(c){
        return {
          requestAccessToken(opts){
            assert.match(opts.prompt,/select_account/);
            c.callback({
              access_token:currentUser+'-fixture-token',expires_in:3600,scope:c.scope
            });
          }
        };
      },revoke(token,cb){
        revoked++;
        cb();
      }
    }
  }
};
const space=new Map(),grants=new Map(),pending=new Map();
let mutations=0,shares=0,uploads=0,publicationFailures=1;
const folder={
  id:'project-folder',name:'Project <script>',mimeType:'application/vnd.google-apps.folder',capabilities:{
    canAddChildren:true,canShare:true,canListChildren:true
  }
};
async function fetchImpl(path,opts){
  assert.equal(opts.credentials,'omit');
  assert.equal(opts.redirect,'error');
  assert.match(opts.headers.Authorization,/Bearer (alice|bob)-fixture-token/);
  if(offline)throw Error('NETWORK SECRET');
  if(expire)return new Response('PROVIDER SECRET',{
    status:401
  });
  if(deny)return new Response('PROVIDER SECRET',{
    status:403
  });
  const u=new URL(path);
  let body;
  if(u.pathname.endsWith('/about'))body={
    user:{
      permissionId:currentUser+'-permission',emailAddress:currentUser+'@example.test'
    }
  };
  else if(u.pathname.endsWith('/permissions')&&opts.method==='POST'){
    shares++;
    const b=JSON.parse(opts.body);
    assert.equal(b.emailAddress,'bob@example.test');
    body={
      id:'p1',role:b.role
    };
  } else if(u.pathname.endsWith('/permissions'))body={
    permissions:[{
      id:'p1',role:'writer',permissionDetails:[{
        inherited:true,inheritedFrom:'parent'
      }]
    }],nextPageToken:'acl-next'
  };
  else if(u.pathname.endsWith('/p1')&&opts.method==='DELETE')return new Response(null,{
    status:204
  });
  else if(u.pathname.includes('/upload/')){
    uploads++;
    assert(opts.body instanceof Blob);
    const b=await opts.body.text();
    assert.match(b,/plaintext fixture/);
    body={
      id:'file1',name:'test.txt',parents:['project-folder']
    };
  } else if(u.pathname.endsWith('/files')&&opts.method==='POST'){
    mutations++;
    body=folder;
  } else if(u.pathname.endsWith('/files')){
    assert.equal(u.searchParams.get('q'),"'project-folder' in parents and trashed = false");
    body={
      files:[{
        id:'file1',name:'test.txt'
      }],...(u.searchParams.get('pageToken')?{
      }:{
        nextPageToken:'next'
      })
    };
  } else body=folder;
  return new Response(JSON.stringify(body),{
    status:200,headers:{
      'Content-Type':'application/json'
    }
  });
}
const participation=async pid=>({
  pid,conv:'conv1',state:'active',host_here:true,held:0
});
function provider(owner){
  return browserDriveProvider({
    clientID:'fixture-web-client',getConfig:async()=>({
      enabled:true,browser_client_id:'fixture-web-client'
    }),getPending:async conv=>pending.get(owner+'/'+conv),setPending:async(conv,s)=>s?pending.set(owner+'/'+conv,s):pending.delete(owner+'/'+conv),gis:async()=>{
    },fetchImpl,getSpace:async conv=>space.get(conv),getOwner:async()=>owner,publishSpace:async s=>{
      if(publicationFailures){
        publicationFailures--;
        throw Error("synthetic E2EE publication unavailable");
      }
      const old=space.get(s.conv);
      if(old&&old.owner!==owner)throw Error('owner mismatch');
      assert.equal(s.owner,owner);
      space.set(s.conv,s);
    },getGrant:async conv=>grants.get(owner+'/'+conv)||{
    },setGrant:async(conv,pid,g)=>{
      const key=owner+'/'+conv;
      grants.set(key,{
        ...(grants.get(key)||{
        }),[pid]:g
      });
    },participation
  });
}
const alice=provider('alice-person'),bob=provider('bob-person');
let vRecovery;
const disabled=browserDriveProvider({
  getConfig:async()=>({
    enabled:false
  })
});
assert.equal((await disabled.drive({
  conv:'conv1',action:'status'
})).enabled,false);
await assert.rejects(disabled.drive({
  conv:'conv1',action:'create'
}),/disabled/);
await assert.rejects(alice.drive({
  conv:'conv1',action:'consent'
}),/Confirm/);
await alice.drive({
  conv:'conv1',action:'consent',confirm_account:true
});
await assert.rejects(alice.drive({
  conv:'conv1',action:'connect',folder:'project-folder',confirm_outside_e2ee:true
}),/full-folder consent/);
vRecovery=await alice.drive({
  conv:'conv1',action:'create',name:'Project',confirm_outside_e2ee:true
});
assert.equal(vRecovery.pending.folder,'project-folder');
assert(!vRecovery.space);
assert.equal(mutations,1);
await alice.drive({
  conv:'conv1',action:'create',name:'Project',confirm_outside_e2ee:true
});
assert.equal(mutations,1);
const reopened=provider('alice-person');
vRecovery=await reopened.drive({
  conv:'conv1',action:'publish-pending',confirm_outside_e2ee:true
});
assert(!vRecovery.pending);
assert.equal(mutations,1);
assert.equal(space.get('conv1').owner,'alice-person');
assert(!JSON.stringify(space.get('conv1')).includes('fixture-token'));
let v=await alice.drive({
  conv:'conv1',action:'list'
});
assert.equal(v.page.nextPageToken,'next');
v=await alice.drive({
  conv:'conv1',action:'list',page:'next'
});
assert(!v.page.nextPageToken);
const file=new Blob(['plaintext fixture']);
file.name='test.txt';
await assert.rejects(alice.driveUpload('conv1',file,false),/Confirm/);
await alice.driveUpload('conv1',file,true);
assert.equal(uploads,1);
await assert.rejects(alice.drive({
  conv:'conv1',action:'share',email:'bob@example.test',role:'writer'
}),/Confirm/);
await alice.drive({
  conv:'conv1',action:'share',email:'bob@example.test',role:'writer',confirm_access:true
});
assert.equal(shares,1);
v=await alice.drive({
  conv:'conv1',action:'permissions'
});
assert.equal(v.permissions[0].permissionDetails[0].inherited,true);
currentUser='bob';
await bob.drive({
  conv:'conv1',action:'consent',confirm_account:true,full:true
});
v=await bob.drive({
  conv:'conv1',action:'list'
});
assert.equal(v.account,'bob@example.test');
assert.equal(v.full,true);
await assert.rejects(bob.drive({
  conv:'conv1',action:'connect',folder:'other-folder',confirm_outside_e2ee:true
}),/owner/);
await assert.rejects(alice.brokerList('conv1','pid'),/no Drive read grant/);
await alice.drive({
  conv:'conv1',action:'grant',pid:'pid',grant:'read'
});
await alice.brokerList('conv1','pid');
await assert.rejects(alice.brokerUpload('conv1','pid',file),/no Drive write grant/);
await alice.drive({
  conv:'conv1',action:'grant',pid:'pid',grant:'write'
});
await alice.brokerUpload('conv1','pid',file);
await assert.rejects(bob.brokerList('conv1','pid'),/no Drive read grant/);
await alice.drive({
  conv:'conv1',action:'grant',pid:'pid',grant:'none'
});
await assert.rejects(alice.brokerList('conv1','pid'),/no Drive read grant/);
deny=true;
await assert.rejects(bob.drive({
  conv:'conv1',action:'list'
}),e=>/access denied/.test(e.message)&&!e.message.includes('SECRET'));
deny=false;
offline=true;
await assert.rejects(bob.drive({
  conv:'conv1',action:'list'
}),/unavailable/);
offline=false;
expire=true;
await assert.rejects(bob.drive({
  conv:'conv1',action:'list'
}),/expired/);
expire=false;
assert.equal((await bob.drive({
  conv:'conv1',action:'status'
})).connected,false);
currentUser='alice';
await alice.drive({
  conv:'conv1',action:'consent',confirm_account:true,full:true
});
await alice.drive({
  conv:'conv1',action:'disconnect-space',confirm_outside_e2ee:true
});
assert.equal((await alice.drive({
  conv:'conv1',action:'status'
})).space,undefined);
await assert.rejects(bob.drive({
  conv:'conv1',action:'connect',folder:'project-folder',confirm_outside_e2ee:true
}),/owner/);
await alice.drive({
  conv:'conv1',action:'disconnect-account'
});
assert.equal(revoked,1);
assert.equal((await alice.drive({
  conv:'conv1',action:'status'
})).connected,false);
console.log('Drive browser provider: create/connect/list/upload, two accounts, explicit permissions, grants, denied/expiry/offline/disconnect PASS');

// All daemon JSON/setup/raw-upload calls stay bound to their originating
// workspace even when another provider becomes active while A is pending.
const { daemonDriveProvider } = await import('../static/drivespace.mjs');
const { daemonStorageSetupProvider, browserStorageSetupProvider } = await import('../static/drivespace-setup.mjs');
const originalFetch = globalThis.fetch;
globalThis.fetch = () => { throw Error('Global default workspace transport used'); };
const calls = [];
let releaseA;
const pendingA = new Promise(resolve => { releaseA = resolve; });
function boundTransport(workspace) {
  async function json(path, body) {
    calls.push({ workspace, kind: 'json', path, body });
    if (workspace === 'A' && path.startsWith('/api/drive?')) {
      await pendingA;
    }
    return { workspace };
  }
  async function raw(path) {
    calls.push({ workspace, kind: 'raw', path });
    if (workspace === 'A') await pendingA;
    return new Response(JSON.stringify({ workspace }));
  }
  return { json, raw };
}
const transportA = boundTransport('A');
const transportB = boundTransport('B');
const driveA = daemonDriveProvider(transportA.json, transportA.raw);
const driveB = daemonDriveProvider(transportB.json, transportB.raw);
const setupA = daemonStorageSetupProvider(transportA.json, transportA.raw);
const setupB = daemonStorageSetupProvider(transportB.json, transportB.raw);
assert.throws(() => daemonDriveProvider(transportA.json), /both JSON and raw/);
assert.throws(() => daemonStorageSetupProvider(transportA.json), /captured JSON and raw/);
const pendingRead = driveA.drive({ conv: 'conv-A', action: 'status' });
const pendingUpload = driveA.driveUpload('conv-A', file, true);
await setupA.storageSetup({ action: 'save-draft', draft: { project: 'A' } });
await driveB.drive({ conv: 'conv-B', action: 'create', name: 'B' });
await setupB.storageSetup({ action: 'status' });
releaseA();
assert.equal((await pendingRead).workspace, 'A');
assert.equal((await pendingUpload).workspace, 'A');
assert(calls.some(c => c.workspace === 'A' && c.kind === 'raw' && c.path.includes('conv=conv-A')));
assert(calls.some(c => c.workspace === 'A' && c.path === '/api/drive/setup' && c.body.action === 'save-draft'));
globalThis.fetch = originalFetch;

// Browser setup calls captured signed Engine, stores drafts in workspace-local
// state, and publishes only public provider configuration under admin authority.
function browserSetupFixture(canAdmin) {
  let draft;
  let settings = { config: { enabled: false }, revision: 0, can_admin: canAdmin };
  const requests = [];
  const provider = browserStorageSetupProvider({
    call: async (method, path, body) => {
      requests.push({ method, path, body });
      if (method === 'PUT') {
        assert(canAdmin);
        assert.equal(path, '/v1/admin/storage/drive');
        assert(!JSON.stringify(body).includes('cloud_account'));
        settings = { config: body.config, revision: settings.revision + 1, can_admin: canAdmin };
      }
      return settings;
    },
    readDraft: async () => draft,
    writeDraft: async value => { draft = value; },
  });
  return { provider, requests };
}
const adminSetup = browserSetupFixture(true);
const memberSetup = browserSetupFixture(false);
const setupDraft = {
  config: { enabled: false, project: 'agentnet-fixture' },
  cloud_account: 'admin@example.test',
  existing_project: true,
  completed: { account: true },
};
await adminSetup.provider.storageSetup({ action: 'save-draft', draft: setupDraft });
assert.equal((await adminSetup.provider.storageSetup()).draft.cloud_account, 'admin@example.test');
assert.equal((await memberSetup.provider.storageSetup()).draft.cloud_account, undefined);
await assert.rejects(memberSetup.provider.storageSetup({ action: 'publish', draft: setupDraft, confirm: true }), /admin only/);
await assert.rejects(adminSetup.provider.storageSetup({ action: 'local-secret' }), /no Desktop secret/);
await adminSetup.provider.storageSetup({ action: 'publish', draft: setupDraft, confirm: true, expect: 0 });
assert.equal(adminSetup.requests.filter(r => r.method === 'PUT').length, 1);
assert.equal((await adminSetup.provider.storageSetup()).runtime, 'browser');
console.log('Drive captured workspace transports and browser admin setup isolation PASS');
