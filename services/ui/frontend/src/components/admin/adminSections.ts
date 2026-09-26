export type AdminSectionId = "teams" | "agents" | "operations" | "platform" | "analytics";

export type AdminSection = {
  id: AdminSectionId;
  label: string;
  group: string;
  description: string;
};

// Grouped so the rail reads as the organisation first, then the platform
// itself. Access control is deliberately not here: the backend serves
// /runtime/grants and /runtime/sessions to any authenticated principal, so it
// is a top-level workspace rather than an admin section.
export const ADMIN_SECTIONS: AdminSection[] = [
  {
    id: "teams",
    label: "Teams",
    group: "Organization",
    description: "Tenant teams, their namespaces, and membership.",
  },
  {
    id: "agents",
    label: "Agents",
    group: "Organization",
    description: "Team-owned governance identities and lifecycle.",
  },
  {
    id: "operations",
    label: "Operations",
    group: "Organization",
    description: "Platform users, the audit trail, and image activity.",
  },
  {
    id: "platform",
    label: "Platform health",
    group: "Platform",
    description: "Operator, Sentinel services, and observability components.",
  },
  {
    id: "analytics",
    label: "Usage analytics",
    group: "Platform",
    description: "Gateway events aggregated across the platform.",
  },
];

export function adminSection(id: string): AdminSection {
  return ADMIN_SECTIONS.find((section) => section.id === id) ?? ADMIN_SECTIONS[0];
}

export const ADMIN_GROUPS = ["Organization", "Platform"];
