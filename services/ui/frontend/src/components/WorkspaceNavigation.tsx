import type { IconName } from "../ui/Icon";
import type { WorkspaceId } from "../routing/route";
import type { AuthStatus } from "../api/types";
import { hasUserIdentity, isAdmin, isTenantUser } from "../api/types";

export type { WorkspaceId };

export type WorkspaceTab = {
  id: Exclude<WorkspaceId, "signin">;
  label: string;
  description: string;
  icon: IconName;
  // Optional visibility gate, evaluated against the authenticated principal.
  visible?: (auth: AuthStatus) => boolean;
};

export const WORKSPACE_TABS: WorkspaceTab[] = [
  {
    id: "servers",
    label: "Servers",
    description: "Deployed MCP servers and their governed tool catalog.",
    icon: "server",
  },
  {
    id: "agents",
    label: "Agents",
    description: "Agent identities, access, sessions, and connection steps.",
    icon: "users",
    visible: (auth) => auth.authenticated,
  },
  {
    id: "access",
    label: "Access control",
    description: "Grants and agent sessions enforced by the MCP gateway.",
    icon: "shield",
    // Any authenticated principal, not only admins: the backend registers
    // /runtime/grants and /runtime/sessions with rr.auth, not rr.adminOnly.
    visible: (auth) => auth.authenticated,
  },
  {
    id: "activity",
    label: "Activity",
    description: "Your MCP usage and team membership.",
    icon: "activity",
    visible: isTenantUser,
  },
  {
    id: "keys",
    label: "API keys",
    description: "Personal API keys for agents and CI jobs.",
    icon: "key",
    visible: hasUserIdentity,
  },
  {
    id: "admin",
    label: "Administration",
    description: "Access control, teams, operations, and platform health.",
    icon: "gauge",
    visible: isAdmin,
  },
];

// Navigation fails closed: a tab is offered only when its server-backed
// principal is allowed to read it. Hiding a tab is presentation, not
// authorization - every panel behind one re-checks the principal and the
// backend enforces it again.
export function visibleWorkspaceTabs(auth: AuthStatus): WorkspaceTab[] {
  return WORKSPACE_TABS.filter((tab) => !tab.visible || tab.visible(auth));
}
