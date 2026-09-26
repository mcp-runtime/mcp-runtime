import { describe, expect, it } from "vitest";

import { observabilitySessionProxyURL } from "./observabilityLinks";

describe("observability session proxy links", () => {
  it("routes server dashboard links through the same-origin authenticated proxy", () => {
    expect(observabilitySessionProxyURL(
      "https://platform.example.com/api/v1/runtime/observability/grafana/dashboard?namespace=mcp-team-a&server=demo",
      "grafana/dashboard"
    )).toBe("/api/ui/v1/runtime/observability/grafana/dashboard?namespace=mcp-team-a&server=demo");
  });

  it("routes Prometheus query links through the same-origin authenticated proxy", () => {
    expect(observabilitySessionProxyURL(
      "https://platform.example.com/api/v1/runtime/observability/prometheus/query?namespace=mcp-team-a&server=demo&id=request-rate",
      "prometheus/query"
    )).toBe("/api/ui/v1/runtime/observability/prometheus/query?namespace=mcp-team-a&server=demo&id=request-rate");
  });

  it("preserves configured Grafana dashboard URLs outside the scoped API", () => {
    const configured = "https://grafana.example.com/d/server?var-namespace=mcp-team-a";
    expect(observabilitySessionProxyURL(configured, "grafana/dashboard")).toBe(configured);
  });
});
