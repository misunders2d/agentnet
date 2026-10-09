// Private read-only runtime metadata, never an availability or settings claim.
import type { T } from "../api";
export function AgentModelReport({ overview, host, agentId = "" }: { overview: T.Overview; host: string; agentId?: string }) {
  const report = overview.model_reports?.find(r => r.host === host && r.agent_id === agentId);
  const date = report ? new Date(report.at * 1000) : null;
  const knownTime = date !== null && Number.isFinite(date.getTime());
  return <p className="pt-1 text-[13px] text-muted [overflow-wrap:anywhere]" data-agent-model={host + "#" + agentId}>
    {report ? <>Last reported model: {report.model} · <time dateTime={knownTime ? date!.toISOString() : undefined}>{knownTime ? date!.toLocaleString() : "Timestamp unavailable"}</time></> : "Model unknown"}
  </p>;
}
