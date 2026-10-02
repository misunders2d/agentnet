import os,json,time,uuid,threading,subprocess,signal,pty,fcntl,termios,struct,select,re,queue,sys,shlex,socket
from pathlib import Path
from http.server import ThreadingHTTPServer,BaseHTTPRequestHandler
def download_sandbox(enabled, home):
 if not enabled:return 'sandbox_mode = "read-only"\n'
 # First Hub attachment fetch needs network; the caller enforces loopback-only
 # namespace. Only cwd and this exact synthetic enrolled home are writable.
 return 'sandbox_mode = "workspace-write"\n[sandbox_workspace_write]\nwritable_roots = '+json.dumps([str(home)])+'\nnetwork_access = true\nexclude_slash_tmp = true\nexclude_tmpdir_env_var = true\n'
def download_tool(gate, data, binary, home, work):
 users=[v for v in data.get('input',[]) if isinstance(v,dict) and v.get('role')=='user']
 text=json.dumps(users[-1].get('content',[])) if users else ''
 refs=re.findall(r'Authorized attachment references: use installed agentnet download for this exact input ([0-9a-f]{32})',text)
 if len(refs)!=1 or gate['body'] not in text or gate['reply_body'] not in text:raise ValueError('selected current native context/reference mismatch')
 rows=json.loads(subprocess.check_output([binary,'--home',str(home),'receivers','--json'],timeout=10))
 selected=[v for v in rows if v['request_ref']==gate['request_ref']]
 if len(selected)!=1 or len(selected[0].get('inputs',[]))!=1 or selected[0]['inputs'][0]['id']!=refs[0]:raise ValueError('attachment reference differs from sole locally admitted input')
 out=work/'downloaded';out.mkdir(mode=0o700,exist_ok=True)
 argv=[binary,'--home',str(home),'download','--dir',str(out),refs[0]]
 return {'cmd':shlex.join(argv),'yield_time_ms':10000},{'emitted':'download','thread':data['prompt_cache_key'],'request_ref':gate['request_ref'],'input_id':refs[0],'original_present':True,'reply_present':True,'local_reference_matches':True}
ROOT=Path(__file__).resolve().parent;R=Path(os.environ['AGENTNET_CODEX_RUNTIME']);R.mkdir(mode=0o700);[ (R/x).mkdir(mode=0o700) for x in ['home','codex','tmp','work','evidence'] ];B='/home/misunderstood/.local/share/mise/installs/codex/0.159.3/bin/codex'
requests=[];events=[];commands=[];children=[];turngate=threading.Event();gates={}
class Provider(BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def do_GET(self):self.send_response(404);self.end_headers()
 def do_POST(self):
  raw=self.rfile.read(int(self.headers.get('content-length',0)));d=json.loads(raw);serialized=json.dumps(d.get('input',[]));requests.append({'time':time.time(),'path':self.path,'thread':d.get('prompt_cache_key'),'title_request':'Generate a concise, single-line task title' in serialized,'selected_reply_present':'Verified remote clarification1003y' in serialized});(R/'evidence/requests.json').write_text(json.dumps(requests,indent=2))
  if not self.path.endswith('/responses'):self.send_response(404);self.end_headers();return
  rid='resp_'+uuid.uuid4().hex;mid='msg_'+uuid.uuid4().hex
  item={'id':mid,'type':'message','role':'assistant','content':[{'type':'output_text','text':'Synthetic native completion.','annotations':[]}],'status':'completed'}
  gate=gates.pop(d.get('prompt_cache_key'),None) if 'Generate a concise, single-line task title' not in serialized else None
  if gate:
   if isinstance(gate,dict):
    tool,metadata=download_tool(gate,d,os.environ['AGENTNET_CODEX_BINARY'],Path(os.environ['AGENTNET_CODEX_HOME']),R/'work')
   else:
    tool={'cmd':'/usr/bin/sleep 8','yield_time_ms':10000} if gate=='busy' else {'cmd':'/usr/bin/touch '+str(R/'work'/'must-not-exist'),'sandbox_permissions':'require_escalated','justification':'Synthetic private fixture: require explicit native approval; never approve automatically.'}
    metadata={'emitted':gate,'thread':d.get('prompt_cache_key')}
   code='const r = await tools.exec_command('+json.dumps(tool)+'); text(r);'
   item={'type':'custom_tool_call','call_id':'call_'+uuid.uuid4().hex,'namespace':'functions','name':'exec','input':code}
   (R/'evidence'/'gate.json').write_text(json.dumps(metadata))
  response={'id':rid,'object':'response','created_at':int(time.time()),'status':'completed','model':d.get('model','gpt-6.1-sol'),'output':[item],'usage':{'input_tokens':1,'output_tokens':1,'total_tokens':2}}
  ev=[('response.created',{'type':'response.created','response':{**response,'status':'in_progress','output':[]}}),('response.output_item.added',{'type':'response.output_item.added','output_index':0,'item':{**item,'status':'in_progress','content':[]}}),('response.content_part.added',{'type':'response.content_part.added','item_id':mid,'output_index':0,'content_index':0,'part':{'type':'output_text','text':'','annotations':[]}}),('response.output_text.delta',{'type':'response.output_text.delta','item_id':mid,'output_index':0,'content_index':0,'delta':'Synthetic native completion.'}),('response.output_text.done',{'type':'response.output_text.done','item_id':mid,'output_index':0,'content_index':0,'text':'Synthetic native completion.'}),('response.output_item.done',{'type':'response.output_item.done','output_index':0,'item':item}),('response.completed',{'type':'response.completed','response':response})]
  if gate:ev=[ev[0],('response.output_item.done',{'type':'response.output_item.done','output_index':0,'item':item}),ev[-1]]
  body=''.join('event: '+name+'\ndata: '+json.dumps(v)+'\n\n' for name,v in ev).encode();self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
provider=ThreadingHTTPServer(('127.0.0.1',0),Provider);threading.Thread(target=provider.serve_forever,daemon=True).start()
download_enabled=os.environ.get('AGENTNET_CODEX_DOWNLOAD')=='1'
if download_enabled and {name for _,name in socket.if_nameindex()}!={'lo'}:raise RuntimeError('download fixture requires loopback-only namespace')
config=f'''model = "gpt-6.1-sol"
model_provider = "synthetic"
approval_policy = "on-request"
web_search = "disabled"
{download_sandbox(download_enabled, Path(os.environ['AGENTNET_CODEX_HOME']))}
[model_providers.synthetic]
name = "Synthetic loopback"
base_url = "http://127.0.0.1:{provider.server_port}/v1"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
[projects."{R/'work'}"]
trust_level = "trusted"
[analytics]
enabled = false
''';(R/'codex/config.toml').write_text(config)
env={'HOME':str(R/'home'),'CODEX_HOME':str(R/'codex'),'TMPDIR':str(R/'tmp'),'PATH':'/usr/bin:/bin','TERM':'xterm-256color','AGENTNET_CODEX_BINARY':os.environ['AGENTNET_CODEX_BINARY'],'AGENTNET_CODEX_HOME':os.environ['AGENTNET_CODEX_HOME']}
if download_enabled and os.environ.get('SSL_CERT_FILE'):env['SSL_CERT_FILE']=os.environ['SSL_CERT_FILE']
command="/usr/bin/python3 "+str(ROOT/"codex_receiver_hook.py")
(R/'codex/hooks.json').write_text(json.dumps({'hooks':{name:[{'hooks':[{'type':'command','command':command,'timeout':10}]}]for name in ['SessionStart','Stop','PostToolUse','SessionEnd']}}))
tuis=[];masters=[];terminals=[];buffers=[];trusted=[];reviewed=[];prompted=[];trust_time=[]
def spawn():
 master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',36,120,0,0))
 p=subprocess.Popen([B,'--no-alt-screen','-C',str(R/'work')],cwd=R/'work',env=env,stdin=slave,stdout=slave,stderr=slave,start_new_session=True);os.close(slave)
 tuis.append(p);masters.append(master);buffers.append(b'');trusted.append(False);reviewed.append(False);prompted.append(False);trust_time.append(time.time() if len(tuis)>1 else 0);terminals.append((R/'evidence'/('terminal-'+str(len(tuis))+'.raw')).open('wb'));(R/'evidence/pids.json').write_text(json.dumps([p.pid for p in tuis]));return len(tuis)-1
commands=queue.Queue();close_requests=[];close_exits={}
def observe_closes():
 for i in close_requests:
  code=tuis[i].poll()
  if code is not None and str(i) not in close_exits:
   close_exits[str(i)]={'pid':tuis[i].pid,'exit_code':code,'other_alive':any(j!=i and p.poll()is None for j,p in enumerate(tuis)),'driver_loop_active':True,'provider_alive':provider.fileno()>=0}
 if close_requests:(R/'evidence/close-observation.json').write_text(json.dumps({'requests':close_requests,'exits':close_exits}))
def input_reader():
 for line in sys.stdin:
  try:commands.put(json.loads(line))
  except ValueError:continue
 commands.put({'finish':True})
threading.Thread(target=input_reader,daemon=True).start()
try:
 spawn();deadline=time.time()+(270 if os.environ.get('AGENTNET_CODEX_GATE')=='close' else 180)
 while time.time()<deadline:
  for i,master in enumerate(masters):
   if tuis[i].poll()is not None:continue
   if select.select([master],[],[],.03)[0]:
    try:chunk=os.read(master,65536)
    except OSError:continue
    terminals[i].write(chunk);terminals[i].flush();buffers[i]=(buffers[i]+chunk)[-80000:]
    if b'\x1b[6n'in chunk:os.write(master,b'\x1b[1;1R')
    text=re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]','',buffers[i].decode('utf8','replace'));(R/'evidence'/('screen-'+str(i)+'.txt')).write_text(text[-18000:])
    if not trusted[i] and 'Hooks need review'in text:
     trusted[i]=True;trust_time[i]=time.time();time.sleep(.3);os.write(master,b'2');time.sleep(.3);os.write(master,b'\r') # explicit synthetic private hook consent only
    if not reviewed[i] and 't trust all' in text:
     reviewed[i]=True;trust_time[i]=time.time();os.write(master,b't');time.sleep(.3);os.write(master,b'\x1b')
   if i>0 and not trusted[i] and time.time()-trust_time[i]>1:
    retained=re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]','',buffers[i].decode('utf8','replace'))
    if 'Ask Codex to do anything' in retained and 'Hooks need review' not in retained:
     trusted[i]=True;trust_time[i]=time.time() # unchanged profile already consented by native UI
   if trusted[i] and not prompted[i] and time.time()-trust_time[i]>1.2:
    prompted[i]=True;os.write(master,b'Synthetic initial local context; no tools required.');time.sleep(.3);os.write(master,b'\r')
  while not commands.empty():
   a=commands.get()
   if a.get('spawn'):spawn()
   if a.get('gate'):
    if a['gate']=='download':
     if not download_enabled or not re.fullmatch('[0-9a-f]{32}',a.get('request_ref','')) or not all(isinstance(a.get(k),str) and a[k] for k in ['body','reply_body']):raise ValueError('invalid fixed download fixture gate')
     gates[a['thread']]={k:a[k] for k in ['request_ref','body','reply_body']}
    else:gates[a['thread']]=a['gate']
    (R/'evidence'/'gate-ready.json').write_text(json.dumps(a))
   if a.get('reject')is not None:os.write(masters[a['reject']],b'\x1b') # explicit private fixture rejection only
   if a.get('delay_end'):
    d=a['delay_end']
    if not isinstance(d.get('session_id'),str) or not 0<=d.get('seconds',-1)<=30:raise ValueError('invalid owned End delay')
    (R/'evidence/end-delay.json').write_text(json.dumps(d))
   if a.get('close')is not None:
    i=a['close']
    if type(i)is not int or not 0<=i<len(tuis) or tuis[i].poll()is not None:raise ValueError('invalid owned native close')
    close_requests.append(i);os.write(masters[i],b'\x04')
   if a.get('finish'):deadline=time.time()
  observe_closes()
  if not close_requests and all(p.poll()is not None for p in tuis):break # explicit close witness stays alive until finish/owned deadline
except Exception as e:
 (R/'evidence/failure.txt').write_text(str(e));print('FAIL '+str(e),flush=True)
finally:
 for p in reversed(tuis):
  if p.poll()is None:
   try:os.killpg(p.pid,signal.SIGTERM);p.wait(timeout=4)
   except subprocess.TimeoutExpired:os.killpg(p.pid,signal.SIGKILL);p.wait(timeout=4)
 stop=subprocess.run([B,'app-server','daemon','stop'],cwd=R/'work',env=env,capture_output=True,timeout=20);(R/'evidence/daemon-stop.json').write_text(json.dumps({'code':stop.returncode,'stdout':stop.stdout.decode(),'stderr':stop.stderr.decode()}))
 provider.shutdown()
 for f in terminals:f.close()
 for m in masters:os.close(m)
 (R/'evidence/cleanup.json').write_text(json.dumps({'child_pids':[p.pid for p in tuis],'all_exited':all(p.poll()is not None for p in tuis)}))
