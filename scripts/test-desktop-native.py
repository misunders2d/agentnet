#!/usr/bin/env python3
import tempfile,pathlib,os,shutil,subprocess,time,http.server,threading,json,signal
import argparse
args=argparse.ArgumentParser(description='Isolated native Tauri download/folder qualification; no installed state.')
args.add_argument('--shell',default='desktop/src-tauri/target/debug/agentnet-app')
args.add_argument('--folder',action='store_true')
args.add_argument('--controls',metavar='GO_BINARY',help='Real Go sidecar to exercise attached controls')
args=args.parse_args()
repo=pathlib.Path(__file__).resolve().parents[1]
os.chdir(repo)
root=pathlib.Path(tempfile.mkdtemp(prefix='agentnet-real-shell-'));home=root/'home';(home/'Downloads').mkdir(parents=True);config=home/'.config';config.mkdir()
(config/'user-dirs.dirs').write_text('XDG_DOWNLOAD_DIR="'+str(home/'Downloads')+'"\n')
shutil.copy2(args.shell,root/'agentnet-app')
reports=[]
HTML='''<!doctype html><html><meta charset="utf-8"><body>AgentNet disposable shell download fixture<script>
window.downloadExecuted=false;window.foreignRejected=false;
function save(name,body,type){const a=document.createElement('a');a.href=URL.createObjectURL(new Blob([body],{type}));a.download=name;document.body.append(a);a.click();a.remove();}
setTimeout(async()=>{save('agent-result.txt','fixture UTF-8 — exact','text/plain');save('unsafe.html','<script>window.downloadExecuted=true;<'+ '/script>','text/html');save('unsafe.svg','<svg xmlns="http://www.w3.org/2000/svg" onload="window.downloadExecuted=true"></svg>','image/svg+xml');try{await window.__TAURI_INTERNALS__.invoke('agentnet_download_blob',{url:'blob:http://127.0.0.1:1/foreign',name:'foreign.txt'});}catch{window.foreignRejected=true;}const unmarked=document.createElement('a');unmarked.href=URL.createObjectURL(new Blob(['<script>window.downloadExecuted=true;<'+ '/script>'],{type:'text/html'}));document.body.append(unmarked);unmarked.click();unmarked.remove();setTimeout(()=>fetch('/report',{method:'POST',body:JSON.stringify({executed:window.downloadExecuted,foreignRejected:window.foreignRejected,page:location.pathname,native:typeof window.__agentnetNativeSkinFolder==='function'})}),1000);},500);
</script></body></html>'''
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
if args.folder:env.update(LD_PRELOAD=str(root/'chooser.so'),GTK_USE_PORTAL='0',AGENTNET_FIXTURE_FOLDER=str(package),AGENTNET_FIXTURE_NATIVE_EVIDENCE=str(root/'native-chooser.txt'))
with open(root/'shell.log','w') as log:
 p=subprocess.Popen(['dbus-run-session','--',str(root/'agentnet-app')],env=env,stdout=log,stderr=log,start_new_session=True)
 try:
  end=time.monotonic()+15
  while time.monotonic()<end:
   if reports and (args.folder or args.controls or len(list((home/'Downloads').iterdir()))>=3):break
   time.sleep(.1)
  if args.controls:
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
