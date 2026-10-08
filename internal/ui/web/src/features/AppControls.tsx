// Desktop-only actions use the shell's host methods, never membership APIs.
import { useEffect, useRef, useState } from "react";
import { useApp } from "../context";
import { errorText } from "../api";
import type { AppStatus, AppUpdateCheck } from "../host";
import { Button } from "../ui/Button";
import { Card, Fact, Hint } from "./Settings.parts";
import { Confirm } from "./Message.actions";

export function AppControls({ command = false }: { command?: boolean }) {
  const store = useApp(), host = store.host;
  const [status, setStatus] = useState<AppStatus | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [message, setMessage] = useState("");
  const [replace, setReplace] = useState(false);
  const [unavailable, setUnavailable] = useState(false);
  const [checked, setChecked] = useState<AppUpdateCheck | null>(null);
  const [checking, setChecking] = useState(false), [checkError, setCheckError] = useState("");
  const checkGeneration = useRef(0);
  useEffect(() => {
    checkGeneration.current++;
    setChecked(null); setChecking(false); setCheckError("");
    if (host.platform === "browser") return;
    let alive = true;
    host.appStatus?.().then((v) => { if (alive) setStatus(v); }, (e) => {
      if (!alive) return;
      if (/404|not found/i.test(errorText(e))) setUnavailable(true);
      else setError(errorText(e));
    });
    return () => { alive = false; checkGeneration.current++; };
  }, [host, command]);
  const check = async () => {
    if (checking || busy || !host.appCheckUpdate || host.platform === "browser") return;
    const generation = ++checkGeneration.current;
    const current = () => generation === checkGeneration.current && store.isActive();
    setChecking(true); setChecked(null); setCheckError("");
    try { const result = await host.appCheckUpdate(); if (current()) setChecked(result); }
    catch (e) { if (current()) setCheckError("Couldn’t check for updates: " + errorText(e)); }
    finally { if (current()) setChecking(false); }
  };
  const act = async (command: boolean) => {
    if (busy) return;
    setBusy(true); setError("");
    try {
      if (command) { const v = await host.appReplaceCommand?.(); if (v && store.isActive()) setStatus((previous) => previous ? { ...previous, ...v } : previous); }
      else { const v = await host.appUpdate?.(); if (v && store.isActive()) setMessage(v.message); }
    } catch (e) { if (store.isActive()) setError(errorText(e)); }
    finally { if (store.isActive()) setBusy(false); }
  };
  if (host.platform === "browser") return null;
  if (!status) {
    if (unavailable) return <Card className="space-y-2 p-4">
      <h3 className="font-bold">{command ? "AgentNet command for your tools" : "Update this computer"}</h3>
      {!command && <Button variant="act" disabled>Update AgentNet</Button>}
      <Hint>This page does not expose the app’s update controls. If a separately managed daemon owns this home, stop it when idle using its normal service manager, then reopen the installed AgentNet app to update.</Hint>
    </Card>;
    return error ? <p role="alert" className="text-danger">{error}</p> : null;
  }
  return <Card className="space-y-2 p-4">
    {command ? <>
      <h3 className="font-bold">AgentNet command for your tools</h3>
      <Fact name="Location">{status.cli_path}</Fact>
      {status.cli_state === "installed" ? <Hint>Your tools can use this copy of AgentNet.</Hint> :
        status.cli_state === "custom" ? <><p>This command is your own build. Replace it with the app’s copy only if you choose.</p>
          <Button disabled={busy || !host.appReplaceCommand} onClick={() => setReplace(true)}>Replace command…</Button></> : <p role="alert" className="text-danger">{status.cli_problem || "The AgentNet command could not be installed."}</p>}
      <Confirm open={replace} onOpenChange={setReplace} title="Replace your AgentNet command?" ok="Replace command" onOk={() => void act(true)}>
        <p>Your tools will use the app’s version at {status.cli_path}. Your existing custom build at this location is replaced.</p>
      </Confirm>
    </> : <>
      <h3 className="font-bold">Update this computer</h3>
      <p>The app, its AgentNet command and connected tools update together. The app restarts when ready.</p>
      <Hint>Installed app: {status.version}.</Hint>
      <div className="flex flex-wrap gap-3">
        {host.appCheckUpdate && <Button disabled={checking || busy} onClick={() => void check()}>{checking ? "Checking…" : "Check for updates"}</Button>}
        <Button variant="act" disabled={busy || checking || !status.app_update_supported || !host.appUpdate || (!!checked && checked.state !== "available")} onClick={() => void act(false)}>{busy ? "Updating…" : "Update AgentNet"}</Button>
      </div>
      {checked && <p role="status">{checked.state === "available" ? `Version ${checked.latest} is available.` :
        checked.state === "current" ? `This app matches the latest stable release (${checked.latest}).` :
          `This app (${checked.version}) is ahead of the latest stable release (${checked.latest}).`}</p>}
      {checkError && <p role="alert" className="text-danger">{checkError}</p>}
      {!status.app_update_supported && <Hint>{status.problem || "This installation is updated by its package manager."}</Hint>}
      {(message || status.update_result) && <p role="status">{message || status.update_result}</p>}
    </>}
    {error && <p role="alert" className="text-danger">{error}</p>}
  </Card>;
}
