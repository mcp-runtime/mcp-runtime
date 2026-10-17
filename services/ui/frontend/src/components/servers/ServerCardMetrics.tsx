import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { getServerObservabilityMetrics } from "../../api/catalog";
import type { ServerSummary } from "../../api/types";

export function ServerCardMetrics({ server }: { server: ServerSummary }) {
  const elementRef = useRef<HTMLDivElement>(null);
  const [nearViewport, setNearViewport] = useState(false);

  useEffect(() => {
    const element = elementRef.current;
    if (!element) return;
    if (typeof IntersectionObserver === "undefined") {
      setNearViewport(true);
      return;
    }

    const observer = new IntersectionObserver(([entry]) => {
      if (entry?.isIntersecting) {
        setNearViewport(true);
        observer.disconnect();
      }
    }, { rootMargin: "200px" });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const metricsQuery = useQuery({
    queryKey: ["server-metrics", server.namespace, server.name, server.uid || ""],
    queryFn: () => getServerObservabilityMetrics(server.namespace, server.name),
    enabled: nearViewport,
    staleTime: 30_000,
  });

  return (
    <div ref={elementRef} className="server-card-metrics" data-testid="server-card-metric-summary" role="group" aria-label={`One-hour metrics for ${server.name}`}>
      {nearViewport && metricsQuery.isPending ? <span className="section-note">Loading one-hour metrics…</span> : null}
      {nearViewport && metricsQuery.error ? <span className="section-note">Metrics unavailable</span> : null}
      {metricsQuery.data ? (
        <>
          <span>tools/call <b>{metricsQuery.data.tools_call_rate_per_second == null ? "—" : `${metricsQuery.data.tools_call_rate_per_second.toFixed(2)}/s`}</b></span>
          <span>p95 <b>{metricsQuery.data.tools_call_p95_seconds == null ? "—" : `${(metricsQuery.data.tools_call_p95_seconds * 1000).toFixed(0)} ms`}</b></span>
          <span>5xx <b>{metricsQuery.data.upstream_5xx_rate_per_second == null ? "—" : `${metricsQuery.data.upstream_5xx_rate_per_second.toFixed(3)}/s`}</b></span>
          <span>Denied <b>{metricsQuery.data.policy_deny_count == null ? "—" : Math.round(metricsQuery.data.policy_deny_count)}</b></span>
          {metricsQuery.data.slowest_tool ? <span>Slowest tool <b>{metricsQuery.data.slowest_tool}</b></span> : null}
        </>
      ) : null}
    </div>
  );
}
