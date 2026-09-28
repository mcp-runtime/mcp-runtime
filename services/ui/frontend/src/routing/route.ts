// Hash routing. The Go static server (services/ui/main.go) serves exactly the
// files embedded under static/ and returns 404 for anything else, so path
// routes would need an SPA fallback that also swallows unknown API and asset
// URLs. A hash keeps every deep link working against the unmodified server and
// keeps browser back/forward honest.

export type WorkspaceId = "servers" | "agents" | "access" | "activity" | "keys" | "admin" | "signin";

export type Route = {
  workspace: WorkspaceId;
  section: string;
  params: Record<string, string>;
};

const WORKSPACES: WorkspaceId[] = ["servers", "agents", "access", "activity", "keys", "admin", "signin"];

export const HOME: Route = { workspace: "servers", section: "", params: {} };

export function parseRoute(hash: string): Route {
  const raw = hash.replace(/^#\/?/, "");
  if (!raw) {
    return HOME;
  }
  const [pathPart, queryPart = ""] = raw.split("?");
  const [workspace, section = ""] = pathPart.split("/").map((part) => decodeURIComponent(part));
  if (!WORKSPACES.includes(workspace as WorkspaceId)) {
    return HOME;
  }
  const params: Record<string, string> = {};
  for (const [key, value] of new URLSearchParams(queryPart)) {
    if (value) {
      params[key] = value;
    }
  }
  return { workspace: workspace as WorkspaceId, section, params };
}

export function formatRoute(route: Route): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(route.params)) {
    if (value) {
      search.set(key, value);
    }
  }
  const query = search.toString();
  const path = route.section
    ? `${route.workspace}/${encodeURIComponent(route.section)}`
    : route.workspace;
  return `#/${path}${query ? `?${query}` : ""}`;
}

export function sameRoute(a: Route, b: Route): boolean {
  return formatRoute(a) === formatRoute(b);
}
