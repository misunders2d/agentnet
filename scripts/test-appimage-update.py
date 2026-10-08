#!/usr/bin/env python3
"""Linux package replacement/restart smoke; run inside private Xvfb + D-Bus.

Uses the published v0.8.2 package and CLI (verified against its SHA256SUMS),
then the real candidate package's helper to replace a disposable AppImage.
No enrollment, relay, GUI button automation, or live installation is involved.
All processes/files belong to one temporary fixture; no logs are uploaded.
"""

import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


RELEASE = "https://github.com/misunders2d/agentnet/releases/download/v0.8.2/"
APP = "AgentNet-linux-x86_64.AppImage"
CLI = "agentnet-linux-amd64"


class HTTPSRedirectOnly(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, target):
        require(target.startswith("https://"), "release redirect left HTTPS")
        return super().redirect_request(request, response, code, message, headers, target)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, target):
        return None


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def checksum(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def download(name, destination, limit):
    request = urllib.request.Request(RELEASE + name, headers={"User-Agent": "AgentNet-package-lifecycle-CI"})
    opener = urllib.request.build_opener(HTTPSRedirectOnly())
    with opener.open(request, timeout=60) as response, destination.open("wb") as output:
        require(response.url.startswith("https://"), "release download left HTTPS")
        count = 0
        while chunk := response.read(1 << 20):
            count += len(chunk)
            require(count <= limit, "release asset exceeds size limit")
            output.write(chunk)


def published_sums(text):
    sums = {}
    for line in text.splitlines():
        fields = line.split()
        if len(fields) != 2:
            continue
        digest, name = fields[0].lower(), fields[1].lstrip("*")
        if not re.fullmatch(r"[0-9a-f]{64}", digest):
            continue
        require(name not in sums, "duplicate published checksum")
        sums[name] = digest
    require(APP in sums and CLI in sums, "published release lacks required checksums")
    return sums


def version(program, env):
    output = subprocess.run([str(program), "version"], env=env, capture_output=True, timeout=15, check=True)
    lines = output.stdout.decode().splitlines()
    match = re.fullmatch(r"agentnet (\S+) \(protocol \d+\)", lines[0] if lines else "")
    require(match is not None, "packaged CLI did not report its actual version")
    return match.group(1)


def live_group(pgid):
    # AppImage extract-and-run and WebKit may outlive the immediate shell PID.
    # Check only groups created by this fixture, ignoring unreaped zombies.
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            fields = (entry / "stat").read_text().rsplit(")", 1)[1].split()
            if fields[0] != "Z" and int(fields[2]) == pgid:
                return True
        except (OSError, ValueError, IndexError):
            continue
    return False


def stop_group(process):
    for sig, seconds in ((signal.SIGTERM, 12), (signal.SIGKILL, 3)):
        try:
            os.killpg(process.pid, sig)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + seconds
        while live_group(process.pid) and time.monotonic() < deadline:
            process.poll()
            time.sleep(0.1)
        if not live_group(process.pid):
            process.wait(timeout=3)
            return
    raise RuntimeError("fixture process group did not stop")


def protected_endpoint(home):
    # Setup deliberately keeps its token in Tauri's private event channel,
    # not ui-url. A guarded 401 proves this actual backend is listening;
    # startup's durable result provides the authenticated internal proof.
    address = (home / "ui-addr").read_text().strip()
    host, port = address.rsplit(":", 1)
    require(ipaddress.ip_address(host).is_loopback and port.isdigit(), "app endpoint is not numeric loopback")
    require(0 < int(port) < 65536, "app endpoint port is invalid")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        opener.open("http://" + address + "/api/app/status", timeout=2).close()
    except urllib.error.HTTPError as error:
        return error.code == 401
    return False


def wait_for(description, probe, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if probe():
                return
        except (OSError, ValueError, urllib.error.URLError, subprocess.SubprocessError):
            pass
        time.sleep(0.2)
    raise RuntimeError("timed out: " + description)


def smoke(candidate):
    require(os.environ.get("DISPLAY"), "run this smoke under xvfb-run")
    require(os.environ.get("DBUS_SESSION_BUS_ADDRESS"), "run this smoke under dbus-run-session")
    require(candidate.is_file(), "candidate AppImage is missing")
    with tempfile.TemporaryDirectory(prefix="agentnet-appimage-update-") as fixture:
        root = Path(fixture)
        root.chmod(0o700)
        user, home, terminal_dir = root / "user", root / "agent-home", root / "terminal"
        for directory in (user, home, terminal_dir, root / "tmp", root / "runtime"):
            directory.mkdir(mode=0o700)
        env = {key: value for key, value in os.environ.items() if key in (
            "DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS", "LANG", "LC_ALL"
        )}
        env.update({
            "HOME": str(user), "USERPROFILE": str(user), "AGENTNET_HOME": str(home),
            "XDG_CONFIG_HOME": str(user / ".config"), "XDG_DATA_HOME": str(user / ".local/share"),
            "XDG_CACHE_HOME": str(user / ".cache"), "XDG_STATE_HOME": str(user / ".local/state"),
            "XDG_RUNTIME_DIR": str(root / "runtime"), "TMPDIR": str(root / "tmp"),
            "PATH": str(terminal_dir) + ":" + str(user / ".local/bin") + ":/usr/bin:/bin",
            "SHELL": "/bin/sh", "APPIMAGE_EXTRACT_AND_RUN": "1", "GDK_BACKEND": "x11",
        })
        processes = []
        with (root / "private-process.log").open("wb") as log:
            try:
                sums_file = root / "SHA256SUMS"
                download("SHA256SUMS", sums_file, 1 << 20)
                sums = published_sums(sums_file.read_text())
                installed, terminal = root / APP, terminal_dir / "agentnet"
                for name, destination in ((APP, installed), (CLI, terminal)):
                    download(name, destination, 512 << 20)
                    require(checksum(destination) == sums[name], "published asset failed checksum: " + name)
                    destination.chmod(0o755)
                require(version(terminal, env) == "v0.8.2", "published CLI version is not v0.8.2")

                # Extract the CI package to obtain its genuine bundled helper,
                # not a freshly-built substitute or a fixture executable.
                extract = root / "candidate-extract"
                extract.mkdir()
                package = root / "candidate.AppImage"
                shutil.copyfile(candidate, package)
                package.chmod(0o755)
                candidate_sum = checksum(package)
                subprocess.run([str(package), "--appimage-extract"], cwd=extract, env=env,
                               stdout=log, stderr=log, timeout=90, check=True)
                bundle = extract / "squashfs-root/usr/bin/agentnet"
                require(bundle.is_file(), "candidate AppImage lacks its bundled backend")
                candidate_version, bundle_sum = version(bundle, env), checksum(bundle)
                private, canonical = home / "bin/agentnet", user / ".local/bin/agentnet"

                old = subprocess.Popen([str(installed), "--autostart"], env=env,
                                       stdout=log, stderr=log, start_new_session=True)
                processes.append(old)
                wait_for("published packaged backend startup", lambda: protected_endpoint(home)
                         and private.is_file() and canonical.is_file())
                require(version(private, env) == "v0.8.2" and version(canonical, env) == "v0.8.2",
                        "published app did not install matching commands")
                require(checksum(private) == checksum(canonical), "published app command copies differ")
                print("Published v0.8.2 AppImage/CLI verified; real packaged backend started.", flush=True)

                # The old shell AND sidecar must stop before helper EOF permits
                # replacement. This tests the helper lifecycle, not a GUI click.
                stop_group(old)
                require(not live_group(old.pid), "old shell/sidecar still running before helper")
                try:
                    require(not protected_endpoint(home), "old backend remained reachable after shutdown")
                except (OSError, urllib.error.URLError):
                    pass

                staging = home / "app-update-ci"
                staging.mkdir(mode=0o700)
                asset = staging / APP
                shutil.copyfile(package, asset)
                require(checksum(asset) == candidate_sum, "candidate changed during staging")
                plan = staging / "plan.json"
                plan.write_text(json.dumps({"app": str(installed), "asset": str(asset), "kind": "appimage",
                                            "sum": candidate_sum, "version": candidate_version}))
                plan.chmod(0o600)
                helper = subprocess.Popen([str(bundle), "--home", str(home), "app-update-helper", str(plan)],
                                          env=env, stdin=subprocess.PIPE, stdout=log, stderr=log,
                                          start_new_session=True)
                processes.append(helper)
                helper.stdin.close()  # all old package processes are already gone
                require(helper.wait(timeout=90) == 0, "real packaged update helper failed")
                require(checksum(installed) == candidate_sum, "installed package differs from checked candidate")
                require(checksum(Path(str(installed) + ".old")) == sums[APP], "old package recovery copy was lost")

                def complete():
                    result = json.loads((home / "app-update-result").read_text())
                    require(result.get("state") not in ("partial", "failed"),
                            "restarted package verification failed: " + result.get("problem", "incomplete commands"))
                    return protected_endpoint(home) and result.get("state") == "complete" and result.get("version") == candidate_version

                wait_for("candidate Tauri/sidecar restart and durable verification", complete)
                require(live_group(helper.pid), "candidate app exited before lifecycle proof")
                stable = user / ".local/share/agentnet/AgentNet.AppImage"
                require((home / "app-exe").read_text().strip() == str(stable), "restarted app did not register its stable installation")
                require(checksum(stable) == candidate_sum, "stable package differs from checked candidate")
                require((stable.stat().st_mode & 0o777) == 0o700, "stable package is not owner-only executable")
                require(str(stable) in (user / ".local/share/applications/agentnet.desktop").read_text(), "launcher still depends on download")
                require(str(stable) in (user / ".config/autostart/AgentNet.desktop").read_text(), "login startup still depends on download")
                installed.unlink()
                require(checksum(stable) == candidate_sum, "removing download affected stable app")
                for command in (private, canonical, terminal):
                    require(checksum(command) == bundle_sum, "restarted app command bytes differ: " + command.name)
                    require(version(command, env) == candidate_version, "restarted command version differs")
                status = subprocess.run([str(terminal), "--home", str(home), "update", "--status"],
                                        env=env, capture_output=True, timeout=15, check=True).stdout.decode()
                require("Updated app and CLI to " + candidate_version + "." in status,
                        "terminal status did not report durable verified completion")
                require(not staging.exists(), "successful helper left its staging directory")
                print("PASS: real AppImage replacement + Tauri/sidecar restart; protected loopback endpoint; "
                      "private/canonical/PATH bytes and versions; durable complete at " + candidate_version + ".", flush=True)
            finally:
                for process in reversed(processes):
                    stop_group(process)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", type=Path, default=Path("desktop/out") / APP)
    args = parser.parse_args()
    def interrupted(_signal, _frame):
        raise RuntimeError("fixture interrupted; cleaning up owned processes")

    signal.signal(signal.SIGTERM, interrupted)
    try:
        smoke(args.candidate.resolve())
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as error:
        # Never emit the fixture's private process log or a page token.
        print("FAIL: AppImage lifecycle smoke: " + str(error), flush=True)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
