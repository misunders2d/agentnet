#!/usr/bin/env python3
"""Profile the real Comic bundle in a disposable native shell and X display."""
import argparse
import json
import os
from pathlib import Path
import selectors
import shutil
import signal
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--shell', required=True)
parser.add_argument('--xvfb', default='Xvfb')
parser.add_argument('--assets', help='Optional previous bundled assets for comparison')
parser.add_argument('--baseline', action='store_true')
args = parser.parse_args()
repo = Path(__file__).resolve().parents[1]
scratch = Path(tempfile.mkdtemp(prefix='agentnet-native-render-'))
test_home = scratch / 'home'
config = test_home / '.config'
(config / 'io.github.misunders2d.agentnet').mkdir(parents=True)
(config / 'io.github.misunders2d.agentnet/autostart-chosen').touch()
shutil.copy2(args.shell, scratch / 'agentnet-app')
(scratch / 'agentnet').write_text(
    '#!/usr/bin/python3\nimport json,os,sys\n'
    'print(json.dumps({"event":"page","mode":"daemon",'
    '"url":os.environ["AGENTNET_FIXTURE_URL"]}),flush=True)\n'
    'for line in sys.stdin:\n if line.strip()=="quit":break\n')
(scratch / 'agentnet').chmod(0o700)
env = os.environ.copy()
env.update(AGENTNET_NATIVE_FIXTURE='1', AGENTNET_SCREENSHOTS=str(scratch))
if args.assets:
    env['AGENTNET_RENDERED_ASSETS'] = str(Path(args.assets).resolve())
children = []
logs = []

def start(command, child_env, name, **kwargs):
    log = (scratch / name).open('w')
    logs.append(log)
    process = subprocess.Popen(command, cwd=repo, env=child_env, stdout=log,
                               stderr=log, start_new_session=True, **kwargs)
    children.append(process)
    return process

def wait_file(file, process, seconds=45):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if file.exists():
            return
        if process.poll() is not None:
            raise RuntimeError(f'Fixture exited early; evidence: {scratch}')
        time.sleep(.05)
    raise TimeoutError(f'Fixture timed out; evidence: {scratch}')

try:
    fixture = start(['node', 'internal/ui/testdata/group_guest_controls_rendered.cjs'], env, 'fixture.log')
    wait_file(scratch / 'native-url', fixture)
    read_fd, write_fd = os.pipe()
    try:
        start([args.xvfb, '-displayfd', str(write_fd), '-screen', '0', '1440x900x24', '-nolisten', 'tcp'],
              env, 'display.log', pass_fds=(write_fd,))
    finally:
        os.close(write_fd)
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(read_fd, selectors.EVENT_READ)
            if not selector.select(10):
                raise TimeoutError(f'No isolated display; evidence: {scratch}')
            raw_display = b''
            while not raw_display.endswith(b'\n'):
                part = os.read(read_fd, 64)
                if not part:
                    raise RuntimeError('Isolated X display exited before ready')
                raw_display += part
            display = raw_display.decode().strip()
        assert display.isdigit(), 'Xvfb must allocate a fresh display'
    finally:
        os.close(read_fd)
    native_env = env.copy()
    for key in ('WAYLAND_DISPLAY', 'DBUS_SESSION_BUS_ADDRESS', 'APPIMAGE', 'APPDIR'):
        native_env.pop(key, None)
    native_env.update(HOME=str(test_home), XDG_CONFIG_HOME=str(config),
                      XDG_DATA_HOME=str(test_home / '.local/share'),
                      XDG_CACHE_HOME=str(test_home / '.cache'),
                      AGENTNET_HOME=str(test_home / '.agentnet'),
                      AGENTNET_FIXTURE_URL=(scratch / 'native-url').read_text(),
                      GDK_BACKEND='x11', DISPLAY=':' + display)
    shell = start(['dbus-run-session', '--', str(scratch / 'agentnet-app')], native_env, 'shell.log')
    wait_file(scratch / 'native-result.json', shell)
    result = json.loads((scratch / 'native-result.json').read_text())
    print(json.dumps({'evidence': str(scratch), **result}), flush=True)
    assert 'error' not in result, 'Native fixture must complete'
    assert result['rows'] == 1201 and result['draft'] == 'Draft stays unsent'
    assert result['sent'] is False, 'Profiling never sends a message'
    if not args.baseline:
        assert result['typed']['formats'] < 8, 'Typing must not reformat retained history'
    print('Native WebKit Comic render profile PASS', flush=True)
finally:
    # Only process groups created by this fixture; no installed app/display.
    for process in reversed(children):
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
    for log in logs:
        log.close()
