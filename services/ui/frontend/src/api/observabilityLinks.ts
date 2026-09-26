import { SESSION_PROXY_PREFIX } from "./client";

type ScopedObservabilityRoute = "grafana/dashboard" | "prometheus/query";

// API-generated observability links are absolute because they also serve CLI
// and API clients. When opened from the authenticated UI, route scoped reads
// through the same-origin session proxy so the user's session is forwarded
// and the runtime API rechecks namespace/server visibility.
export function observabilitySessionProxyURL(url: string, route: ScopedObservabilityRoute): string {
  try {
    const parsed = new URL(url, window.location.origin);
    const apiPath = `/api/v1/runtime/observability/${route}`;
    if (!parsed.pathname.endsWith(apiPath)) return url;
    return `${SESSION_PROXY_PREFIX}/runtime/observability/${route}${parsed.search}`;
  } catch {
    return url;
  }
}
