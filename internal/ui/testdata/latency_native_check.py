"""Run only an owned, disposable copy of the installed Tauri shell."""
import json, os, pathlib, shutil, signal, subprocess, sys, tempfile, time, urllib.request

shell, url, token = sys.argv[1:]
root = pathlib.Path(tempfile.mkdtemp(prefix='agentnet-ui-latency-native-'))
app = root / 'agentnet-app'
shutil.copy2(shell, app)
sidecar = root / 'agentnet'
sidecar.write_text('#!/usr/bin/python3\nimport json,os,sys\nprint(json.dumps({"event":"page","mode":"daemon","url":os.environ["AGENTNET_FIXTURE_URL"]}),flush=True)\nfor line in sys.stdin:\n if line.strip()=="quit":break\n')
sidecar.chmod(0o700)
env = os.environ.copy()
env.update(HOME=str(root/'home'),XDG_CONFIG_HOME=str(root/'config'),XDG_DATA_HOME=str(root/'data'),XDG_CACHE_HOME=str(root/'cache'),AGENTNET_HOME=str(root/'agentnet-home'),AGENTNET_FIXTURE_URL=url+('&' if '?' in url else '?')+'token='+token)
for name in ['home','config','data','cache','agentnet-home']:(root/name).mkdir(mode=0o700)
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
try:
 with (root/'shell.log').open('w') as log:
  proc=subprocess.Popen(['dbus-run-session','--',str(app)],env=env,stdout=log,stderr=log,start_new_session=True)
  try:
   report=None
   for _ in range(300):
    if proc.poll() is not None:raise RuntimeError('Owned fixture shell exited: '+(root/'shell.log').read_text()[-3000:])
    with opener.open(url.rsplit('/',1)[0]+'/latency-result?run=native',timeout=2) as r:
     if r.status==200:report=json.load(r);break
    time.sleep(.1)
   if report is None:raise RuntimeError('Native fixture timeout: '+(root/'shell.log').read_text()[-3000:])
   print(json.dumps({'engine':'installed Tauri WebKitGTK',**report}))
   if not report.get('ok'):raise RuntimeError('Native responsiveness fixture failed')
  finally:
   # This process group contains only the disposable shell/sidecar we launched.
   if proc.poll() is None:
    os.killpg(proc.pid,signal.SIGTERM)
    try:proc.wait(timeout=5)
    except subprocess.TimeoutExpired:os.killpg(proc.pid,signal.SIGKILL);proc.wait()
finally:shutil.rmtree(root)
