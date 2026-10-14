import { formatRoute } from "../../routing/route";
import type { SessionSummary } from "../../api/types";

export type AccessReference = { kind: "grant" | "session"; namespace: string; name: string; agent?: string };

export function accessHref(reference: AccessReference): string {
  return formatRoute({ workspace: "access", section: "", params: { kind: reference.kind, namespace: reference.namespace, name: reference.name, ...(reference.agent ? { agent: reference.agent } : {}) } });
}

export function AccessLink({ kind, namespace, name, agent }: AccessReference) {
  return <a href={accessHref({ kind, namespace, name, agent })}>{namespace}/{name}</a>;
}

export function AuthorizingGrant({ session }: { session: SessionSummary }) {
  return session.grantName && session.grantNamespace
    ? <AccessLink kind="grant" namespace={session.grantNamespace} name={session.grantName} agent={session.subject?.agentID} />
    : <span>Unknown — no grant linkage recorded</span>;
}

export function revocationLabel(session: SessionSummary): string {
  if (!session.revoked) return "—";
  switch (session.revocationCause) {
    case "agent_deactivated": return "Agent deactivation";
    case "access_revoked": return "Access revoked";
    case "grant_revoked": return "Grant sessions revoked";
    case "manual": return "Manually revoked";
    case "server_retired": return "Server retired";
    default: return "Unknown — no revocation cause recorded";
  }
}
