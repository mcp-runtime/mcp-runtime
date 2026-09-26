import { readRuntimeConfig } from "./config";

// Same-origin dashboard client. Auth is the HttpOnly mcp_ui_session cookie.
// This module does not read credentials from window, localStorage, or
// sessionStorage and never sends Authorization or x-api-key.

export const SESSION_PROXY_PREFIX = "/api/ui/v1";

export const SESSION_PROXY_GET_PATHS = new Set([
  "/runtime/namespaces",
  "/runtime/servers",
  "/runtime/tools",
  // Phase 4 admin reads. Each is already GET-allowlisted server-side in
  // sessionProxyRuntimePrefixes / sessionProxyAnalyticsPrefixes.
  "/runtime/grants",
  "/runtime/sessions",
  "/runtime/teams",
  "/runtime/agents",
  "/runtime/components",
  "/admin/operations",
  "/admin/deployments",
  "/events",
  "/analytics/usage",
  "/user/api-keys",
  "/user/analytics/usage",
]);

const SESSION_PROXY_GET_PREFIXES = ["/runtime/teams/", "/runtime/agents/"];

export const SESSION_PROXY_WRITE_PATHS: Array<{
  path: string;
  methods: string[];
  segments?: number;
  suffixes?: string[];
  suffixIndex?: number;
}> = [
  { path: "/user/api-keys", methods: ["POST"] },
  { path: "/user/api-keys/", methods: ["DELETE"], segments: 1 },
  { path: "/runtime/grants/", methods: ["PATCH", "DELETE"], segments: 2 },
  { path: "/runtime/grants/", methods: ["POST"], segments: 3, suffixes: ["revoke-sessions"] },
  { path: "/runtime/grants", methods: ["POST"] },
  { path: "/runtime/sessions/", methods: ["PATCH", "DELETE"], segments: 2 },
  { path: "/runtime/sessions", methods: ["POST"] },
  { path: "/runtime/teams", methods: ["POST"] },
  { path: "/runtime/teams/", methods: ["DELETE"], segments: 1 },
  { path: "/runtime/teams/", methods: ["POST"], segments: 2, suffixes: ["members", "users"] },
  { path: "/runtime/teams/", methods: ["POST"], segments: 2, suffixes: ["agents"] },
  { path: "/runtime/agents/", methods: ["PATCH"], segments: 1 },
  { path: "/runtime/agents/", methods: ["POST"], segments: 2, suffixes: ["deactivate", "reactivate"] },
  { path: "/runtime/teams/", methods: ["PUT", "DELETE"], segments: 3, suffixes: ["members"], suffixIndex: 1 },
  { path: "/runtime/actions/restart", methods: ["POST"] },
  { path: "/runtime/servers/", methods: ["DELETE"], segments: 2 },
];

export const CSRF_HEADER = "X-CSRF-Token";
let csrfToken = "";

export function setCSRFToken(token: string): void {
  csrfToken = typeof token === "string" ? token.trim() : "";
}

export function clearCSRFToken(): void {
  csrfToken = "";
}

export function hasCSRFToken(): boolean {
  return csrfToken !== "";
}

export function isUnsafeMethod(method: string): boolean {
  return !["GET", "HEAD", "OPTIONS", "TRACE"].includes(method.toUpperCase());
}

function sessionProxyWriteAllowed(method: string, pathname: string): boolean {
  return SESSION_PROXY_WRITE_PATHS.some((route) => {
    const upperMethod = method.toUpperCase();
    if (!route.methods.includes(upperMethod)) return false;
    if (route.segments === undefined) return pathname === route.path;
    const rest = pathname.startsWith(route.path) ? pathname.slice(route.path.length) : "";
    if (!rest || rest.startsWith("/") || rest.endsWith("/")) return false;
    const parts = rest.split("/");
    if (parts.length !== route.segments) return false;
    if (!route.suffixes) return true;
    const index = route.suffixIndex ?? parts.length - 1;
    return route.suffixes.includes(parts[index]);
  });
}

function sessionProxyGetAllowed(pathname: string): boolean {
  return SESSION_PROXY_GET_PATHS.has(pathname) ||
    SESSION_PROXY_GET_PREFIXES.some((prefix) => pathname.startsWith(prefix));
}

export class CSRFError extends Error {
  readonly status = 403;

  constructor(message = "csrf_failed") {
    super(message);
    this.name = "CSRFError";
  }
}

export class UnauthorizedError extends Error {
  readonly status = 401;

  constructor(message = "unauthorized") {
    super(message);
    this.name = "UnauthorizedError";
  }
}

// A 403 that isn't the CSRF-token case above - e.g. RequireRole rejecting a
// non-admin principal (pkg/platformauth/middleware.go), which the gateway
// reports as {"error":"forbidden","message":"insufficient permissions"}.
export class ForbiddenError extends Error {
  readonly status = 403;

  constructor(message = "forbidden") {
    super(message);
    this.name = "ForbiddenError";
  }
}

export function apiURL(
  path: string,
  apiBase = readRuntimeConfig().apiBase,
  method = "GET"
): string {
  const normalized = path.startsWith("/") ? path : `/${path}`;
  const pathname = normalized.split("?")[0];
  if (method.toUpperCase() === "GET" && sessionProxyGetAllowed(pathname)) {
    return `${SESSION_PROXY_PREFIX}${normalized}`;
  }
  if (sessionProxyWriteAllowed(method, pathname)) {
    return `${SESSION_PROXY_PREFIX}${normalized}`;
  }
  const base = apiBase.replace(/\/$/, "") || "/api/v1";
  return `${base}${normalized}`;
}

// sameOriginInit builds a request that carries only the session cookie. Any
// caller-supplied credential header is dropped before it can reach the wire.
function sameOriginInit(options: RequestInit): RequestInit {
  const headers = new Headers(options.headers);
  headers.delete("Authorization");
  headers.delete("authorization");
  headers.delete("X-API-Key");
  headers.delete("x-api-key");
  headers.delete(CSRF_HEADER);
  headers.delete(CSRF_HEADER.toLowerCase());

  const method = options.method?.toString() || "GET";
  if (isUnsafeMethod(method) && csrfToken) {
    headers.set(CSRF_HEADER, csrfToken);
  }

  return { ...options, credentials: "same-origin", headers };
}

async function readJSON(response: Response): Promise<unknown> {
  if (response.status === 401) {
    throw new UnauthorizedError();
  }
  if (!response.ok) {
    const text = await response.text();
    if (response.status === 403) {
      if (text.includes("csrf_failed")) {
        throw new CSRFError();
      }
      throw new ForbiddenError(text || "forbidden");
    }
    throw new Error(text || `Request failed: ${response.status}`);
  }
  return response.json();
}

export async function fetchJSON(path: string, options: RequestInit = {}): Promise<unknown> {
  const url = apiURL(
    path,
    readRuntimeConfig().apiBase,
    options.method?.toString() || "GET"
  );
  return readJSON(await fetch(url, sameOriginInit(options)));
}

// Paths served by the UI origin itself (session lifecycle), not by the
// upstream runtime API. They intentionally bypass apiBase.
const UI_ORIGIN_PATHS = new Set(["/auth/status", "/auth/login", "/auth/logout"]);

export async function fetchUIJSON(path: string, options: RequestInit = {}): Promise<unknown> {
  if (!UI_ORIGIN_PATHS.has(path)) {
    throw new Error(`unsupported UI origin path: ${path}`);
  }
  return readJSON(await fetch(path, sameOriginInit(options)));
}

// Anonymous public-mode catalog reads (services/ui/public_catalog_proxy.go).
// Unlike every other fetch* helper here, these never carry the session
// cookie or a CSRF token - there is no session to carry, since the visitor
// isn't signed in. The runtime API authenticates the request as a synthetic
// public principal instead.
export const PUBLIC_CATALOG_PREFIX = "/api/public/v1";

export async function fetchPublicJSON(path: string): Promise<unknown> {
  return readJSON(await fetch(`${PUBLIC_CATALOG_PREFIX}${path}`, { credentials: "omit" }));
}

export function withQuery(path: string, params: Record<string, string | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    const trimmed = value?.trim();
    if (trimmed) {
      search.set(key, trimmed);
    }
  }
  const query = search.toString();
  return query ? `${path}?${query}` : path;
}
