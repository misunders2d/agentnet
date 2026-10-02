# Focused fixture-only regression: import no native driver/profile/runtime.
import ast,json,re,shlex,subprocess,tempfile,tomllib
from pathlib import Path
from unittest.mock import patch
source=Path(__file__).parents[2]/'client/testdata/codex_receiver_native.py'
tree=ast.parse(source.read_text())
helpers=ast.Module(body=[n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name in {'download_sandbox','download_tool'}],type_ignores=[])
ns={'json':json,'re':re,'shlex':shlex,'subprocess':subprocess};exec(compile(helpers,str(source),'exec'),ns)
legacy=tomllib.loads(ns['download_sandbox'](False,Path('/synthetic/alice')))
assert legacy=={'sandbox_mode':'read-only'}
config=tomllib.loads(ns['download_sandbox'](True,Path('/synthetic/alice')))
assert config['sandbox_mode']=='workspace-write'
assert config['sandbox_workspace_write']=={'writable_roots':['/synthetic/alice'],'network_access':True,'exclude_slash_tmp':True,'exclude_tmpdir_env_var':True}
ref='1'*32;input_id='2'*32;gate={'request_ref':ref,'body':'Original LOCAL','reply_body':'Verified reply'}
text='Original LOCAL; Verified reply; Authorized attachment references: use installed agentnet download for this exact input '+input_id
rows=[{'request_ref':ref,'inputs':[{'id':input_id}]}]
with tempfile.TemporaryDirectory(prefix='codex-fixture-check-') as tmp:
 work=Path(tmp)/'work with spaces';work.mkdir()
 data={'prompt_cache_key':'exact-thread','input':[{'role':'user','content':[{'text':text}]}]}
 with patch.object(subprocess,'check_output',return_value=json.dumps(rows).encode()) as call:
  tool,metadata=ns['download_tool'](gate,data,'/pinned binary/agentnet',Path('/synthetic Alice'),work)
  assert shlex.split(tool['cmd'])==['/pinned binary/agentnet','--home','/synthetic Alice','download','--dir',str(work/'downloaded'),input_id]
  assert metadata['input_id']==input_id and metadata['local_reference_matches']
  assert call.call_args.args[0]==['/pinned binary/agentnet','--home','/synthetic Alice','receivers','--json']
 for bad_text,bad_rows in [(text+' '+text,rows),(text,[{'request_ref':ref,'inputs':[{'id':'3'*32}]}]),('foreign reply',rows)]:
  with patch.object(subprocess,'check_output',return_value=json.dumps(bad_rows).encode()):
   try:ns['download_tool'](gate,{'input':[{'role':'user','content':[{'text':bad_text}]}]},'/pinned binary',Path('/synthetic'),work)
   except ValueError:pass
   else:raise AssertionError('unsafe reference admitted')
print('default/download fixture checks ok')
