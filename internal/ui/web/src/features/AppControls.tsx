// Installed-app actions use the shell's host methods, never membership APIs.
import { useEffect, useRef, useState } from "react";
import { useApp } from "../context";
import { useStore } from "../store";
import { errorText } from "../api";
import type { AppStatus, AppUpdateCheck } from "../host";
import { Button } from "../ui/Button";
import { Card, Fact, Hint } from "./Settings.parts";
import { Confirm } from "./Message.actions";

export function AppControls({ command = false }: { command?: boolean }) {
  const store = useApp(), host = store.host;
  const android = host.platform === "android";
  const app = useStore(store, s => s.overview?.app);
  const supportedHost = (host.platform === "daemon" || android) && (host.appControlsAvailable !== false || app === true);
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
    if (!supportedHost || (android && command)) return;
    let alive = true;
    let generation = 0;
    const refresh = () => {
      const request = ++generation;
      const current = () => alive && request === generation && store.isActive();
      host.appStatus?.().then((v) => {
        if (!current()) return;
        setStatus(v); setUnavailable(false); setError("");
        if (android && v.update_result) setMessage("");
      }, (e) => {
        if (!current()) return;
        if (/404|not found/i.test(errorText(e))) setUnavailable(true);
        else setError(errorText(e));
      });
    };
    const visible = () => {
      if (host.android?.visible() !== false) refresh();
    };
    refresh();
    const off = android ? host.android?.onVisibility(visible) : undefined;
    return () => {
      alive = false; generation++; checkGeneration.current++;
      off?.();
    };
  }, [host, command, supportedHost]);
  const check = async () => {
    if (checking || busy || !host.appCheckUpdate || !supportedHost) return;
    const generation = ++checkGeneration.current;
    const current = () => generation === checkGeneration.current && store.isActive();
    setChecking(true); setChecked(null); setCheckError("");
    try { const result = await host.appCheckUpdate(); if (current()) setChecked(result); }
    catch (e) { if (current()) setCheckError("Couldn’t check for updates: " + errorText(e)); }
    finally { if (current()) setChecking(false); }
  };
  const act = async (command: boolean) => {
    if (busy) return;
    setBusy(true); setError(""); setMessage("");
    try {
      if (command) { const v = await host.appReplaceCommand?.(); if (v && store.isActive()) setStatus((previous) => previous ? { ...previous, ...v } : previous); }
      else { const v = await host.appUpdate?.(); if (v && store.isActive()) setMessage(v.message); }
    } catch (e) { if (store.isActive()) setError(errorText(e)); }
    finally { if (store.isActive()) setBusy(false); }
  };
  if (!supportedHost || (android && command)) return null;
  if (!status) {
    if (unavailable) return <Card className="space-y-2 p-4">
      <h3 className="font-bold">{command ? "AgentNet command for your tools" : android ? "Update this phone" : "Update this computer"}</h3>
      <Hint>{android ? "This Android app does not expose update controls. Install the newer APK over this app to keep your chats and identity. Do not uninstall it." : "To update this computer, open the installed AgentNet app. Your chats and identity stay on this computer."}</Hint>
    </Card>;
    return error ? <p role="alert" className="text-danger">{error}</p> : null;
  }
  const available = !command && checked?.state === "available" ? checked.latest : "";
  const update = <Button variant="act" disabled={busy || checking || !status.app_update_supported || !host.appUpdate || (!!checked && checked.state !== "available")} onClick={() => void act(false)}>{busy ? "Updating…" : "Update AgentNet"}</Button>;
  return <Card tone={available ? "agent" : "surface"} className="space-y-2 p-4">
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
      {available ? <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 aria-live="polite" className="font-display text-[24px] font-extrabold leading-tight">Version {available} is available.</h3>
        {update}
      </div> : <h3 className="font-bold">{android ? "Update this phone" : "Update this computer"}</h3>}
      <p>{android ? "AgentNet checks the release APK before opening Android’s installer. Install it over this app to keep your chats and identity. Do not uninstall it." : "The app, its AgentNet command and connected tools update together. The app restarts when ready."}</p>
      <Hint>Installed app: {status.version}.</Hint>
      <div className="flex flex-wrap gap-3">
        {host.appCheckUpdate && <Button disabled={checking || busy} onClick={() => void check()}>{checking ? "Checking…" : "Check for updates"}</Button>}
        {!available && update}
      </div>
      {checked && !available && <p role="status">{checked.state === "current" ? `This app matches the latest stable release (${checked.latest}).` :
          `This app (${checked.version}) is ahead of the latest stable release (${checked.latest}).`}</p>}
      {checkError && <p role="alert" className="text-danger">{checkError}</p>}
      {!status.app_update_supported && <Hint>{status.problem || (android ? "Updates are unavailable in this installation." : "This installation is updated by its package manager.")}</Hint>}
      {message ? <p role="status">{message}</p> : status.update_result && (available && !android ?
        <Hint>Last update: {status.update_result}</Hint> : <p role="status">{status.update_result}</p>)}
    </>}
    {error && <p role="alert" className="text-danger">{error}</p>}
  </Card>;
}
