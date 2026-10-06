// Desktop-only actions use the shell's host methods, never membership APIs.
import { useEffect, useState } from "react";
import { useApp } from "../context";
import { errorText } from "../api";
import type { AppStatus } from "../host";
import { Button } from "../ui/Button";
import { Card, Fact, Hint } from "./Settings.parts";
import { Confirm } from "./Message.actions";

export function AppControls({ command = false }: { command?: boolean }) {
  const store = useApp(), host = store.host;
  const [status, setStatus] = useState<AppStatus | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [message, setMessage] = useState("");
  const [replace, setReplace] = useState(false);
  useEffect(() => {
    let alive = true;
    host.appStatus?.().then((v) => { if (alive) setStatus(v); }, (e) => {
      if (alive && !/404|not found/i.test(errorText(e))) setError(errorText(e));
    });
    return () => { alive = false; };
  }, [host]);
  const act = async (command: boolean) => {
    if (busy) return;
    setBusy(true); setError("");
    try {
      if (command) { const v = await host.appReplaceCommand?.(); if (v && store.isActive()) setStatus((previous) => previous ? { ...previous, ...v } : previous); }
      else { const v = await host.appUpdate?.(); if (v && store.isActive()) setMessage(v.message); }
    } catch (e) { if (store.isActive()) setError(errorText(e)); }
    finally { if (store.isActive()) setBusy(false); }
  };
  if (!status) return error ? <p role="alert" className="text-danger">{error}</p> : null;
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
      <Button variant="act" disabled={busy || !status.app_update_supported || !host.appUpdate} onClick={() => void act(false)}>{busy ? "Updating…" : "Update AgentNet"}</Button>
      {!status.app_update_supported && <Hint>{status.problem || "This installation is updated by its package manager."}</Hint>}
      {(message || status.update_result) && <p role="status">{message || status.update_result}</p>}
    </>}
    {error && <p role="alert" className="text-danger">{error}</p>}
  </Card>;
}
