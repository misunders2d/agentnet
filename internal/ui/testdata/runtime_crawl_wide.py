# Run from internal/ui after sourcing the Playwright environment.
# Build: go test -c -o /tmp/agr-wide.test .
# Bounded batches retain JSON-line control evidence even on timeout.
# Existing output resumes completed final batches. Delete/move output to restart.
import os,subprocess,time,json,signal,hashlib
from pathlib import Path
harness_sha256=hashlib.sha256(b"".join(Path(p).read_bytes() for p in ["testdata/latency_fixture.mjs","testdata/latency_rendered_check.cjs"])).hexdigest()
output=os.environ.get('AGENTNET_CRAWL_OUTPUT','/tmp/agentnet-crawl-wide.json')
start=time.time(); results=json.load(open(output))['runs'] if os.path.exists(output) else []
suites='actions agents compose groups history media navigation outbox permissions profile review settings storage teams topics unread workspaces'.split()
for suite in suites:
 for mode in ['held','failing']:
  for viewport in ['1440x960','390x844']:
   if any(r.get('phase')=='final' and r.get('harness_sha256')==harness_sha256 and r['suite']==suite and r['mode']==mode and r['viewport']==viewport for r in results): continue
   if time.time()-start>float(os.environ.get('AGENTNET_CRAWL_SECONDS','3780')): break
   env=dict(os.environ,AGENTNET_RUNTIME_CRAWL='1',AGENTNET_TRUSTED_BACKEND=mode,AGENTNET_LATENCY_SUITES=suite,AGENTNET_LATENCY_WRITE_SUITES=suite,AGENTNET_LATENCY_VIEWPORTS=viewport,AGENTNET_LATENCY_TOUCH=str(int(viewport=='390x844')))
   command=['flock','/tmp/agr-browser.lock','nice','-n','10',os.environ.get('AGENTNET_CRAWL_BINARY','/tmp/agr-wide.test'),'-test.run','^TestUIResponsivenessRendered$','-test.v','-test.timeout=45s']
   p=subprocess.Popen(command,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,start_new_session=True)
   try:
    raw,_=p.communicate(timeout=60);code=p.returncode
   except subprocess.TimeoutExpired:
    os.killpg(p.pid,signal.SIGTERM);raw,_=p.communicate();code='bounded batch timeout'
   finally:
    try: os.killpg(p.pid,signal.SIGTERM)
    except ProcessLookupError: pass
   reports=[]
   for line in raw.splitlines():
    try: reports.append(json.loads(line.strip()))
    except: pass
   results.append(dict(phase='final',harness_sha256=harness_sha256,mode=mode,viewport=viewport,suite=suite,exit=code,reports=reports,log=raw[-3000:]))
   with open(output,'w') as f: json.dump(dict(commit=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),duration_seconds=time.time()-start,runs=results),f)
