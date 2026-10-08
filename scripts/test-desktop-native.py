#!/usr/bin/env python3
import tempfile,pathlib,os,shutil,subprocess,time,http.server,threading,json,signal
import argparse
args=argparse.ArgumentParser(description='Isolated native Tauri download/folder qualification; no installed state.')
args.add_argument('--shell',default='desktop/src-tauri/target/debug/agentnet-app')
args.add_argument('--folder',action='store_true')
args.add_argument('--controls',metavar='GO_BINARY',help='Real Go sidecar to exercise attached controls')
args.add_argument('--external-link',action='store_true',help='Verify release links dispatch through the native external opener')
args=args.parse_args()
repo=pathlib.Path(__file__).resolve().parents[1]
os.chdir(repo)
root=pathlib.Path(tempfile.mkdtemp(prefix='agentnet-real-shell-'));home=root/'home';(home/'Downloads').mkdir(parents=True);config=home/'.config';config.mkdir()
(config/'user-dirs.dirs').write_text('XDG_DOWNLOAD_DIR="'+str(home/'Downloads')+'"\n')
shutil.copy2(args.shell,root/'agentnet-app')
reports=[]
external='https://github.com/misunders2d/agentnet/releases/tag/v0.8.5'
HTML='''<!doctype html><html><meta charset="utf-8"><body>AgentNet disposable shell download fixture<script>
window.downloadExecuted=false;window.foreignRejected=false;
function save(name,body,type){const a=document.createElement('a');a.href=URL.createObjectURL(new Blob([body],{type}));a.download=name;document.body.append(a);a.click();a.remove();}
setTimeout(async()=>{save('agent-result.txt','fixture UTF-8 — exact','text/plain');save('unsafe.html','<script>window.downloadExecuted=true;<'+ '/script>','text/html');save('unsafe.svg','<svg xmlns="http://www.w3.org/2000/svg" onload="window.downloadExecuted=true"></svg>','image/svg+xml');try{await window.__TAURI_INTERNALS__.invoke('agentnet_download_blob',{url:'blob:http://127.0.0.1:1/foreign',name:'foreign.txt'});}catch{window.foreignRejected=true;}const unmarked=document.createElement('a');unmarked.href=URL.createObjectURL(new Blob(['<script>window.downloadExecuted=true;<'+ '/script>'],{type:'text/html'}));document.body.append(unmarked);unmarked.click();unmarked.remove();setTimeout(()=>fetch('/report',{method:'POST',body:JSON.stringify({executed:window.downloadExecuted,foreignRejected:window.foreignRejected,page:location.pathname,native:typeof window.__agentnetNativeSkinFolder==='function'})}),1000);},500);
</script></body></html>'''
if args.external_link:
 HTML='''<!doctype html><meta charset="utf-8"><title>Isolated release link fixture</title><body><div id="skin"></div><script>
const root=document.getElementById('skin').attachShadow({mode:'open'});
const errors=[];window.addEventListener('unhandledrejection',e=>errors.push(String(e.reason)));
const link=document.createElement('a');link.href='''+json.dumps(external)+''';link.target='_blank';link.rel='noopener noreferrer';link.textContent='What’s new';root.append(link);
setTimeout(()=>{link.click();setTimeout(()=>fetch('/report',{method:'POST',body:JSON.stringify({page:location.pathname,href:link.href,label:link.textContent,errors})}),500);},500);
</script>'''
 # Observe the supported opener in a disposable PATH, rather than opening
 # the person's browser or changing their desktop associations.
 tools=root/'tools';tools.mkdir()
 recorder=tools/'xdg-open'
 recorder.write_text('#!/usr/bin/python3\nimport os,sys,pathlib\nwith pathlib.Path(os.environ["AGENTNET_EXTERNAL_EVIDENCE"]).open("a") as f:f.write(sys.argv[1]+"\\n")\n')
 recorder.chmod(0o700)
 import shlex
 flags=shlex.split(subprocess.check_output(['pkg-config','--cflags','--libs','webkit2gtk-4.1'],text=True))
 subprocess.run(['cc','-shared','-fPIC',str(repo/'scripts/native-link-fixture.c'),'-o',str(root/'popup.so'),*flags,'-ldl'],check=True)
if args.controls:
 HTML='<!doctype html><meta charset="utf-8"><body>Attached old-page fixture<script>\nsetTimeout(async()=>{try{const status=await fetch(\'/api/app/status\').then(r=>r.json());const refused=await fetch(\'/api/app/cli\',{method:\'POST\',headers:{\'Content-Type\':\'application/json\'},body:\'{"replace":false}\'});let arbitrary=false;try{await window.__TAURI_INTERNALS__.invoke(\'agentnet_app_controls\',{action:\'arbitrary\',body:\'{}\'});}catch{arbitrary=true;}await fetch(\'/report\',{method:\'POST\',body:JSON.stringify({status,cli:refused.status,arbitrary,page:location.pathname})});}catch(e){await fetch(\'/report\',{method:\'POST\',body:JSON.stringify({error:String(e)})});}},500);\n</script>'
if args.folder:
 package=root/'skin-package';(package/'nested').mkdir(parents=True)
 (package/'skin.json').write_text('{"api":1,"id":"native-fixture","name":"Native fixture","entry":"nested/entry.mjs","files":["nested/entry.mjs"]}')
 (package/'nested/entry.mjs').write_text('export function mount(root){root.textContent="fixture"}')
 HTML='''<!doctype html><meta charset="utf-8"><body>Native folder fixture<script type="module">
 try {const files=await window.__agentnetNativeSkinFolder();const {prepare}=await import('/assets/local-skins.mjs');const result=await prepare(files);await fetch('/report',{method:'POST',body:JSON.stringify({id:result.item.id,paths:files.map(f=>f.webkitRelativePath),bytes:result.assets[0].bytes.length})});}catch(e){await fetch('/report',{method:'POST',body:JSON.stringify({error:String(e)})});}
 </script>'''
 import shlex
 flags=shlex.split(subprocess.check_output(['pkg-config','--cflags','--libs','gtk+-3.0'],text=True))
 subprocess.run(['cc','-shared','-fPIC',str(repo/'scripts/native-folder-fixture.c'),'-o',str(root/'chooser.so'),*flags,'-ldl'],check=True)
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200)
  if args.folder and self.path.startswith('/assets/'):
   self.send_header('Content-Type','text/javascript');self.end_headers();self.wfile.write(pathlib.Path('internal/ui/static',self.path.split('/')[-1]).read_bytes())
  else:self.send_header('Content-Type','text/html; charset=utf-8');self.end_headers();self.wfile.write(HTML.encode())
 def do_POST(self):
  reports.append(json.loads(self.rfile.read(int(self.headers['Content-Length']))));self.send_response(200);self.end_headers()
 def log_message(self,*a):pass
server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler);threading.Thread(target=server.serve_forever,daemon=True).start()
(root/'agentnet').write_text('#!/usr/bin/python3\nimport json,os,sys\nprint(json.dumps({"event":"page","mode":"daemon","url":os.environ["AGENTNET_FIXTURE_URL"]}),flush=True)\nfor line in sys.stdin:\n if line.strip()=="quit":break\n')
(root/'agentnet').chmod(0o700)
if args.controls:
 shutil.copy2(args.controls,root/'agentnet')
 import fcntl
 agenthome=home/'.agentnet';agenthome.mkdir(mode=0o700)
 lock=open(agenthome/'daemon.lock','w');os.chmod(agenthome/'daemon.lock',0o600);fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
 (agenthome/'ui-url').write_text('http://127.0.0.1:'+str(server.server_port)+'/?t=synthetic');os.chmod(agenthome/'ui-url',0o600)

env=os.environ.copy();env.update(HOME=str(home),XDG_CONFIG_HOME=str(config),XDG_DATA_HOME=str(home/'.local/share'),XDG_CACHE_HOME=str(home/'.cache'),AGENTNET_HOME=str(home/'.agentnet'),AGENTNET_FIXTURE_URL='http://127.0.0.1:'+str(server.server_port)+'/?t=synthetic')
if args.external_link:
 env.update(PATH=str(tools)+os.pathsep+env.get('PATH',''),BROWSER=str(recorder),AGENTNET_EXTERNAL_EVIDENCE=str(root/'external-links.txt'),LD_PRELOAD=str(root/'popup.so'),AGENTNET_FIXTURE_POPUP_EVIDENCE=str(root/'popup-settings.txt'))
 # The fixture has no human interaction; use the supported hidden launch.
 # Preserve an explicit disabled startup choice in its disposable config.
 appconfig=config/'io.github.misunders2d.agentnet';appconfig.mkdir()
 (appconfig/'autostart-chosen').touch()
if args.folder:env.update(LD_PRELOAD=str(root/'chooser.so'),GTK_USE_PORTAL='0',AGENTNET_FIXTURE_FOLDER=str(package),AGENTNET_FIXTURE_NATIVE_EVIDENCE=str(root/'native-chooser.txt'))
with open(root/'shell.log','w') as log:
 p=subprocess.Popen(['dbus-run-session','--',str(root/'agentnet-app'),*(['--autostart'] if args.external_link else [])],env=env,stdout=log,stderr=log,start_new_session=True)
 try:
  end=time.monotonic()+15
  while time.monotonic()<end:
   if reports and (args.external_link or args.folder or args.controls or len(list((home/'Downloads').iterdir()))>=3):break
   time.sleep(.1)
  if args.external_link:
   observed=(root/'external-links.txt').read_text().splitlines() if (root/'external-links.txt').exists() else []
   print(json.dumps({'reports':reports,'external':observed,'root':str(root)}))
   assert (root/'popup-settings.txt').read_text().startswith('automatic_popups_before=0 fixture_enabled=1'), 'timer-click fixture alone permits automatic popups'
   assert reports and reports[0]['page']=='/' and reports[0]['href']==external, 'release link retained in shadow root, app page unchanged'
   assert reports[0]['errors']==[], 'release link opens without rejected native commands'
   assert observed==[external], 'exact release URL dispatched once through supported native opener'
   assert p.poll() is None, 'app remains running'
   print('real Tauri release link dispatch PASS')
  elif args.controls:
   print(json.dumps({'reports':reports,'root':str(root)}))
   assert reports and 'error' not in reports[0], 'actual native/Go bridge response'
   assert 'cli_state' in reports[0]['status'] and reports[0]['cli']==400 and reports[0]['arbitrary'] and reports[0]['page']=='/', 'existing handler/error fences on attached page'
   assert p.poll() is None and (agenthome/'app-ui-url').exists(), 'live shell, separate app control listener'
   print('real Tauri + real Go attached controls PASS')
  elif args.folder:
   evidence=(root/'native-chooser.txt').read_text() if (root/'native-chooser.txt').exists() else ''
   print(json.dumps({'reports':reports,'chooser':evidence,'root':str(root)}))
   assert reports and reports[0].get('id')=='local:native-fixture', 'existing full package validation'
   assert reports[0]['paths']==['skin.json','nested/entry.mjs'], 'nested package preserved'
   assert 'action=2 visible=1' in evidence, 'actual native SELECT_FOLDER shown and accepted'
   print('real Tauri native skin folder PASS')
  else:
   files={f.name:f.read_text() for f in (home/'Downloads').iterdir() if f.is_file()}
   print(json.dumps({'reports':reports,'files':files,'root':str(root)}))
   assert reports and all(r['executed'] is False and r['foreignRejected'] and r['page']=='/' and r['native'] for r in reports), 'native app page unchanged/non-executing, foreign blob denied'
   assert files.get('agent-result.txt')=='fixture UTF-8 — exact', 'original filename/bytes'
   assert set(files)=={'agent-result.txt','unsafe.html','unsafe.svg'}, 'all exact filenames saved'
   print('real Tauri shell downloads PASS')
 finally:
  os.killpg(p.pid,signal.SIGTERM)
  try:p.wait(timeout=4)
  except subprocess.TimeoutExpired:os.killpg(p.pid,signal.SIGKILL);p.wait()
  server.shutdown()
