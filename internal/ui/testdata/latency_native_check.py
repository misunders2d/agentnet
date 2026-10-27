"""Run an owned disposable Tauri shell/profile on a native GUI runner.

Usage: python3 latency_native_check.py SHELL URL TOKEN COMPILED_SIDECAR
Linux: display (or xvfb-run), dbus-run-session. macOS: logged-in GUI runner.
Windows: WebView2, disposable user without AgentNet processes or Run entry.
"""
import json, os, pathlib, platform, shutil, signal, subprocess, sys, tempfile, time, urllib.request
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

shell, url, token, sidecar_binary = sys.argv[1:]
system = platform.system()
assert system in ('Linux', 'Darwin', 'Windows'), 'Unsupported native runner'
assert urlsplit(url).hostname == '127.0.0.1', 'Only owned loopback fixtures'
if system in ('Darwin', 'Windows'):
    assert os.environ.get('AGENTNET_LATENCY_DISPOSABLE_GUI') == '1', 'Use an explicitly disposable native GUI runner'
# Profile variables cannot isolate HKCU or an existing native single instance.
if system == 'Windows':
    import winreg
    try:
        with winreg.OpenKey(winreg.HKEY_CURRENT_USER, r'Software\Microsoft\Windows\CurrentVersion\Run') as key:
            try:
                winreg.QueryValueEx(key, 'AgentNet')
            except FileNotFoundError:
                pass
            else:
                raise RuntimeError('Use a disposable Windows user without an AgentNet Run entry')
    except FileNotFoundError:
        pass
    tasks = subprocess.check_output(['tasklist', '/FI', 'IMAGENAME eq agentnet-app.exe', '/FO', 'CSV'], text=True)
    assert 'agentnet-app.exe' not in tasks.lower(), 'Existing AgentNet instance; do not control it'

root = pathlib.Path(tempfile.mkdtemp(prefix='agentnet-ui-latency-native-'))
suffix = '.exe' if system == 'Windows' else ''
app = root / 'bin' / ('agentnet-app' + suffix)
app.parent.mkdir()
source = pathlib.Path(shell).resolve()
bundle = next((p for p in source.parents if p.suffix == '.app'), None)
if bundle and system == 'Darwin':
    copied = root / bundle.name
    shutil.copytree(bundle, copied, symlinks=True)
    app = copied / source.relative_to(bundle)
else:
    shutil.copy2(source, app)
    # AppImage binaries use $ORIGIN/../lib. Keep the downloaded artifact's
    # read-only library tree available without copying or changing its files.
    if system == 'Linux' and (source.parent.parent/'lib').is_dir():
        (root/'lib').symlink_to(source.parent.parent/'lib', target_is_directory=True)
    if system == 'Windows' and source.with_name('WebView2Loader.dll').is_file():
        shutil.copy2(source.with_name('WebView2Loader.dll'), app.with_name('WebView2Loader.dll'))
shutil.copy2(sidecar_binary, app.with_name('agentnet' + suffix))
env = os.environ.copy()
fixture_cwd = root
artifact_environment = []
if system == 'Linux' and (source.parent.parent/'lib/x86_64-linux-gnu/webkit2gtk-4.1/WebKitNetworkProcess').is_file():
    # linuxdeploy's patched WebKit paths are relative to usr/, just as the
    # artifact's AppRun launcher arranges. Child helpers need its library path.
    fixture_cwd = source.parent.parent
    artifact_environment = ['LD_LIBRARY_PATH='+str(fixture_cwd/'lib'), 'WEBKIT_INJECTED_BUNDLE_PATH='+str(fixture_cwd/'lib/x86_64-linux-gnu/webkit2gtk-4.1/injected-bundle/libwebkit2gtkinjectedbundle.so')]
    plugins = fixture_cwd/'lib/gstreamer-1.0'
    if plugins.is_dir():
        # Match the bundled linuxdeploy GStreamer AppRun hook, rather than
        # mixing these libraries with the runner's system plugin/scanner ABI.
        scanner = fixture_cwd/'lib/gstreamer1.0/gstreamer-1.0/gst-plugin-scanner'
        assert scanner.is_file(), 'Bundled GStreamer scanner is missing'
        artifact_environment += ['GST_REGISTRY_REUSE_PLUGIN_SCANNER=no',
            'GST_PLUGIN_SYSTEM_PATH_1_0='+str(plugins), 'GST_PLUGIN_PATH_1_0='+str(plugins),
            'GST_PLUGIN_SCANNER_1_0='+str(scanner)]
for name in ('home', 'config', 'data', 'cache', 'agentnet-home', 'appdata', 'localappdata'):
    (root/name).mkdir(mode=0o700)
query = dict(parse_qsl(urlsplit(url).query)); query.update(token=token)
query.setdefault('run', 'native')
paint_diagnostic = os.environ.get('AGENTNET_LATENCY_PAINT_DIAGNOSTIC', '')
if paint_diagnostic:
    if paint_diagnostic not in ('baseline', 'motion', 'flat', 'no-background', 'no-shadow'):
        raise ValueError('Invalid paint diagnostic')
    query['paintDiagnostic'] = paint_diagnostic
parts = urlsplit(url)
fixture_url = urlunsplit((parts.scheme, parts.netloc, parts.path, urlencode(query), ''))
env.update(HOME=str(root/'home'), XDG_CONFIG_HOME=str(root/'config'),
           XDG_DATA_HOME=str(root/'data'), XDG_CACHE_HOME=str(root/'cache'),
           USERPROFILE=str(root/'home'), APPDATA=str(root/'appdata'),
           LOCALAPPDATA=str(root/'localappdata'), AGENTNET_HOME=str(root/'agentnet-home'),
           AGENTNET_FIXTURE_URL=fixture_url)
for key in ('APPIMAGE', 'APPDIR', 'DBUS_SESSION_BUS_ADDRESS'):
    env.pop(key, None)
identifier = 'io.github.misunders2d.agentnet'
config = (root/'home/Library/Application Support' if system == 'Darwin' else
          root/'appdata' if system == 'Windows' else root/'config') / identifier
config.mkdir(parents=True, exist_ok=True)
(config/'autostart-chosen').touch()  # suppress first-run login changes
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
proc = None
profile_dir = os.environ.get('AGENTNET_NATIVE_PROFILE_DIR') if system == 'Darwin' else None
def webcontent_pids():
    rows = subprocess.check_output(['ps', '-axo', 'pid=,comm='], text=True).splitlines()
    return {int(row.split(None, 1)[0]): row.split(None, 1)[1] for row in rows if 'WebKit.WebContent' in row}
active_samples = {}
profile_records = []
samplers = []
if profile_dir:
    pathlib.Path(profile_dir).mkdir(parents=True, exist_ok=True)
try:
    with (root/'shell.log').open('w') as log:
        # System D-Bus must use its system libdbus; apply bundled libraries
        # only to the owned app/helpers, as opposed to the session launcher.
        command = ['dbus-run-session', '--', 'env', *artifact_environment, str(app)] if system == 'Linux' else [str(app)]
        kwargs = {'start_new_session': True} if system != 'Windows' else {'creationflags': subprocess.CREATE_NEW_PROCESS_GROUP}
        proc = subprocess.Popen(command, cwd=fixture_cwd, env=env, stdout=log, stderr=log, **kwargs)
        report = None
        end = time.monotonic() + 30  # existing fixture bound
        next_profile_check = 0
        result_url = urlunsplit((parts.scheme, parts.netloc, '/latency-result', urlencode({'run':query['run']}), ''))
        while time.monotonic() < end:
            if profile_dir and time.monotonic() >= next_profile_check:
                next_profile_check = time.monotonic() + .5
                # WebKit may assign an already-prewarmed process to this page.
                # The explicit disposable-GUI guard excludes personal sessions.
                for pid, command_name in webcontent_pids().items():
                    if pid in active_samples and active_samples[pid].poll() is None:
                        continue
                    # Capture startup and the first interactions together. The
                    # one-second probe mostly sampled process initialization.
                    # Join this diagnostic before stopping its owned target.
                    destination = pathlib.Path(profile_dir) / (query.get('suite', 'fixture')+'-'+str(pid)+'-'+str(len(samplers))+'.txt')
                    sampler = subprocess.Popen(['sample', str(pid), '5', '10', '-file', str(destination)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    active_samples[pid] = sampler
                    samplers.append(sampler)
                    profile_records.append({'pid': pid, 'command': command_name, 'elapsedMs': round((time.monotonic()-(end-30))*1000), 'file': destination.name})
            if proc.poll() is not None:
                raise RuntimeError('Owned fixture shell exited: '+(root/'shell.log').read_text(errors='replace')[-3000:])
            with opener.open(result_url, timeout=2) as response:
                if response.status == 200:
                    report = json.load(response)
                    break
            time.sleep(.1)
        if report is None:
            raise RuntimeError('Native fixture timeout: '+(root/'shell.log').read_text(errors='replace')[-3000:])
        print(json.dumps({'engine': {'Linux':'Tauri WebKitGTK','Darwin':'Tauri WKWebView','Windows':'Tauri WebView2'}[system],
                          'nativeOS': system, 'machine': platform.machine(), 'profiled': bool(profile_dir), 'diagnostic': bool(profile_dir or paint_diagnostic), **report}))
        if not report.get('ok'):
            raise RuntimeError('Native responsiveness fixture failed')
finally:
    body_error = sys.exc_info()[1]
    try:
        profile_end = time.monotonic() + 6
        for sampler in samplers:
            if sampler.poll() is None:
                try:
                    sampler.wait(timeout=max(.01, profile_end-time.monotonic()))
                except subprocess.TimeoutExpired:
                    sampler.terminate()
                    sampler.wait(timeout=1)
        if profile_dir:
            for record, sampler in zip(profile_records, samplers):
                record['returncode'] = sampler.returncode
            (pathlib.Path(profile_dir)/(query.get('suite', 'fixture')+'-samples.json')).write_text(json.dumps(profile_records, indent=2))
        teardown_end = time.monotonic() + 5
        # Only terminate the owned process tree, never an image name/other instance.
        if proc is not None and proc.poll() is None:
            if system == 'Windows':
                subprocess.run(['taskkill', '/PID', str(proc.pid), '/T', '/F'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True, timeout=max(.01, teardown_end-time.monotonic()))
            else:
                os.killpg(proc.pid, signal.SIGTERM)
            try:
                proc.wait(timeout=min(1, max(.01, teardown_end-time.monotonic())))
            except subprocess.TimeoutExpired:
                if system != 'Windows':
                    os.killpg(proc.pid, signal.SIGKILL)
                proc.wait(timeout=max(.01, teardown_end-time.monotonic()))
        # WebView2 children release their profile handles asynchronously after
        # taskkill returns. Join that resource lifetime within the same deadline.
        def removed_owned_child(_function, path, error_info):
            error = error_info[1]
            child = pathlib.Path(path)
            # WebView2 can remove one of its lockfiles during this walk. Only
            # an already-absent descendant of this exact owned root is benign.
            if isinstance(error, FileNotFoundError) and child != root and root in child.parents:
                return
            raise error
        while True:
            try:
                shutil.rmtree(root, onerror=removed_owned_child)
                break
            except PermissionError:
                if system != 'Windows' or time.monotonic() >= teardown_end:
                    raise
                time.sleep(min(.05, max(0, teardown_end-time.monotonic())))
    except Exception as cleanup_error:
        if body_error is None:
            raise
        body_error.add_note('Owned native fixture cleanup also failed: '+repr(cleanup_error))
        print('Secondary cleanup failure: '+repr(cleanup_error), file=sys.stderr)
