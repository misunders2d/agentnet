# Private fixture callback witness; no owner credential enters native transcript.
import json,os,subprocess,sys,time
from pathlib import Path
raw=sys.stdin.buffer.read();d=json.loads(raw)
event={k:d.get(k)for k in ['session_id','transcript_path','hook_event_name','source','reason']};event['time_ns']=time.time_ns()
out=Path(os.environ['CODEX_HOME']).parent/'evidence/hooks.jsonl'
with out.open('a')as f:f.write(json.dumps(event)+'\n')
# Owned synthetic timeout gate only; absent control means no delay. Vendor's
# unchanged ten-second command timeout must kill this exact process group.
control=out.parent/'end-delay.json'
if event['hook_event_name']=='SessionEnd' and control.exists():
 delay=json.loads(control.read_text())
 if delay.get('session_id')==event['session_id']:
  seconds=delay.get('seconds',0)
  if type(seconds)not in (int,float) or not 0<=seconds<=30:raise ValueError('invalid private End delay')
  if seconds:
   child=subprocess.Popen(['/usr/bin/sleep','60'])
   (out.parent/'end-delay-started.json').write_text(json.dumps({'session_id':event['session_id'],'pid':os.getpid(),'pgid':os.getpgrp(),'child_pid':child.pid,'seconds':seconds,'forwarded':False}))
   time.sleep(seconds);child.terminate();child.wait(timeout=3)
p=subprocess.run([os.environ['AGENTNET_CODEX_BINARY'],'--home',os.environ['AGENTNET_CODEX_HOME'],'hook','codex'],input=raw,capture_output=True)
with (out.parent/'hook-results.jsonl').open('a')as f:f.write(json.dumps({'time_ns':time.time_ns(),'session_id':event['session_id'],'hook_event_name':event['hook_event_name'],'returncode':p.returncode})+'\n')
sys.stdout.buffer.write(p.stdout)
