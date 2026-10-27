// Captured, reviewed inputs for one conversation's background invitations.
// These records describe local work, never membership or execution authority.
import type { T } from "./api";

export type InvitationTarget = { key: string; name: string } & (
  | { route: "agent"; input: T.AgentInvite; joinOwn: boolean; more: boolean }
  | { route: "guest"; input: T.GuestAction }
  | { route: "group"; input: T.GroupInviteDraft }
);

export interface InvitationSend {
  targets: InvitationTarget[];
  person: string;
  next: number;
  agents: Record<number, T.AgentView>;
  done?: boolean;
  error?: string;
}

export function invitationResult(p: InvitationSend): string {
  return p.targets.map((t, i) => t.route === "agent"
    ? t.more ? "Shared the selected messages with " + t.name + "."
      : p.agents[i]?.state === "active" ? t.name + " joined with only the reviewed access."
        : "Invited " + t.name + "; its owner decides when it joins."
    : "Invited " + t.name + "; they decide whether to join.").join(" ");
}
