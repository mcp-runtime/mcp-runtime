# Follow an MCP request

This walkthrough is for platform operators who need to explain an allowed and a
denied tool call. Use an existing demo server, active agent, grant and enrolled
adapter. See [Client Connections](connect-clients.md) for enrollment and
[Teams and Access](teams-and-access.md) for policy setup.

## What each view answers

| View | Question it answers |
| --- | --- |
| MCPSnoop at the client boundary | What JSON-RPC request and response did the client actually exchange? |
| Tempo trace | Which instrumented hops and gateway stages ran, and where was time spent? |
| Gateway/tool metrics | How many calls were allowed or denied, and how does latency change over time? |
| Correlated logs | What completed or failed for this trace ID? |
| Read-only kubectl | Which pod, container, configuration revision and rollout handled the call? |

The path is client → local adapter → Traefik → gateway sidecar → MCP server.
The platform API handles session enrollment separately. Gateway audit delivery
runs asynchronously: its span can end after the client's response. A trace ID
links timing and metadata; it does not retrieve request-body snapshots at every
hop. MCPSnoop sees the exchange at the point where you insert it.

## Prepare the connection

Replace the four values below with your demo's existing identifiers. Do not
create or change grants just to make a denied call succeed.

```bash
export DEMO_SERVER=workspace-assistant
export DEMO_NAMESPACE=mcp-team-acme
export DEMO_AGENT=agt_REPLACE_WITH_ACTIVE_AGENT
export DEMO_RUNTIME_URL=https://mcp.mcpruntime.org/workspace-assistant/mcp

mcp-runtime adapter proxy --help
mcp-runtime adapter proxy \
  --runtime-url "$DEMO_RUNTIME_URL" \
  --platform-url https://platform.mcpruntime.org \
  --server "$DEMO_SERVER" \
  --namespace "$DEMO_NAMESPACE" \
  --agent "$DEMO_AGENT" \
  --auto-refresh \
  --listen 127.0.0.1:8765
```

Keep this process running. An OAuth-enabled server also needs the local MCP
client's OAuth flow and bearer header, as described in Client Connections.
The adapter keeps the enrolled certificate on its upstream TLS connection.

In another terminal, start [MCPSnoop](https://github.com/kerlenton/mcpsnoop)
in front of the local HTTP adapter (upstream CLI reviewed 2026-10-13):

```bash
mcpsnoop http --target http://127.0.0.1:8765/mcp --listen 127.0.0.1:7000
```

Point your Streamable HTTP MCP client at `http://127.0.0.1:7000/mcp`. Let the
client perform initialization and maintain any negotiated MCP transport session.
An MCP transport session ID is separate from the platform's `MCPAgentSession`.
Keep captures local and use artificial demo arguments: captures contain payloads
and can expose credentials. Inspect the actual client exchange rather than
replacing its TLS connection between Traefik and the gateway.

## Compare one allowed and one denied call

For the shipped Go example, a read-only grant can allow `add` while withholding
`create_task`. First call `add` with `a=2, b=3`, then ask the client to call
`create_task` without changing that grant. Confirm the policy and advertised
tools in your deployment; clients may refuse to invoke a tool omitted from
`tools/list`. In that case use a protocol-capable test client that can issue the
explicit request on its initialized connection. See the hosted acceptance
workflow for reproducible governed call tests.

Observe the JSON-RPC frame and HTTP status in MCPSnoop. Find the corresponding
gateway response's `X-Trace-ID` header in the HTTP exchange. Runtime derives
this header from its active span; an upstream correlation header cannot replace
it. Copy that ID into Grafana Explore's Tempo trace-ID search, or match it to the
completion log's `trace_id`. When no valid span context exists, the header is
absent. An ID alone does not guarantee a retained trace: unsampled requests still
have an ID, and export failures or retention expiry can make Tempo lookup empty. A request with propagated
W3C context shares the ingress trace; without incoming context, an instrumented
hop creates a new root. Do not assume a MCPSnoop-exported span automatically
shares the same trace until its propagation is verified.

For an allowed call, expect the gateway's `inspect`, `policy`, `auth`, `authz`
and `upstream` stage spans. A denial ends at the rejecting stage and has no
upstream request. Inside-server MCP spans require an instrumented server: the
shipped Go example supports them, but an arbitrary Buddy or other server does
not gain SDK spans merely from OTEL environment variables. External OAuth
providers and optional Nginx likewise need their own instrumentation. Inspect
[Services and Observability](platform-services.md) for deployment prerequisites.

The gateway completion record contains `event=gateway.request`, `service`,
`namespace`, `server`, `tool`, `policy_version`, `method`, `status`, `decision`,
`reason`, `duration_ms`, `trace_id` and `span_id`. Tool names come from the
configured inventory and are length-bounded; unrecognized names use `__other__`.
The `policy_version` is the decision's configured version, not a hash proving
identical ConfigMap contents.
HTTP services emit `event=http.request` with a registered route template in
`operation`; unmatched routes do not log their raw URL. Healthy probes are
quiet; failed probes remain visible. A gateway HTTP success can still contain
an MCP tool result with `isError=true`; inspect the instrumented server span
and MCPSnoop result to distinguish these cases.

## Logs and metrics in Grafana

Open the reference platform's authenticated Grafana at
`https://platform.mcpruntime.org/grafana`. In Loki Explore select the demo
namespace and container, then filter by the trace ID:

```logql
{namespace="mcp-team-acme", container="mcp-gateway"} |= "trace_id=REPLACE_TRACE_ID"
```

Completion records can be parsed using `| logfmt` (the standard Go logger's
leading timestamp is not an application field). The TraceID derived-field link
opens Tempo. From Tempo, **Logs for this span** searches collected namespaces
by the same trace ID within a short time window. The service metrics links
open request-rate and latency queries; the gateway tool-outcomes link applies
to gateway root spans with namespace/server attributes. The linked metrics are
aggregate observations around the request, not a per-request metric lookup.
Platform service HTTP histograms also attach sampled trace exemplars.
Inspect the server container in the same namespace for its SDK
completion record; its span ID differs while the trace ID matches.

In Prometheus Explore, use:

```promql
sum by (tool, decision) (increase(mcp_gateway_tool_call_total{namespace="mcp-team-acme",server="workspace-assistant"}[5m]))
```

```promql
histogram_quantile(0.95, sum by (le, tool) (rate(mcp_gateway_tool_call_duration_seconds_bucket{namespace="mcp-team-acme",server="workspace-assistant",decision="allow"}[5m])))
```

Two calls demonstrate counters, not a meaningful p95. These series count
inspected tool calls and HTTP/policy outcomes. Tool labels use the bounded
configured inventory; unknown names collapse to `__other__`. Sampled histogram
exemplars can link a measurement to Tempo when OpenMetrics scraping and
Prometheus exemplar storage are enabled. Scrape and log ingestion take time;
refresh after the next scrape rather than assuming zero means a lost request.

## Retention and sampling limits

The bundled stack keeps data for a limited time: Tempo compacts blocks after
24 hours (`k8s/16-tempo.yaml`) and Loki retains logs for 168 hours
(`k8s/17-loki.yaml`); Prometheus retention is set by its own flags. A trace ID
copied after those windows will not resolve, although it may still be present
in a client capture. The Traefik ingress tracer samples every request
(`--tracing.sampleRate=1`); other services follow their OpenTelemetry SDK
configuration, and an unsampled request has a trace ID and log lines but no
retained trace or histogram exemplar. Grafana's Tempo-to-Loki link searches
from 5 seconds before the span start to 5 seconds after its end, and its
Tempo-to-Prometheus links use a two-minute window around the span.

## Confirm Kubernetes state without changing it

Use an explicit kubeconfig for the intended cluster. Set `DEMO_KUBECONFIG` to
your existing read-only diagnostic credential file, then select the pod that
handled the trace (if replicas exist, inspect logs for all candidate pods).

```bash
export DEMO_KUBECONFIG=/path/to/explicit-kubeconfig
kubectl --kubeconfig "$DEMO_KUBECONFIG" -n "$DEMO_NAMESPACE" get pods -l "app=$DEMO_SERVER" -o wide
export DEMO_POD=REPLACE_WITH_POD_NAME
export DEMO_TRACE_ID=REPLACE_WITH_32_HEX_TRACE_ID
kubectl --kubeconfig "$DEMO_KUBECONFIG" -n "$DEMO_NAMESPACE" get pod "$DEMO_POD" -o jsonpath='{range .spec.containers[*]}{.name}{" "}{.image}{"\n"}{end}'
kubectl --kubeconfig "$DEMO_KUBECONFIG" -n "$DEMO_NAMESPACE" logs "$DEMO_POD" -c mcp-gateway --since=10m --timestamps | rg --fixed-strings "$DEMO_TRACE_ID"
kubectl --kubeconfig "$DEMO_KUBECONFIG" -n "$DEMO_NAMESPACE" logs "$DEMO_POD" -c "$DEMO_SERVER" --since=10m --timestamps | rg --fixed-strings "$DEMO_TRACE_ID"
kubectl --kubeconfig "$DEMO_KUBECONFIG" -n "$DEMO_NAMESPACE" get events --field-selector "involvedObject.name=$DEMO_POD" --sort-by=.lastTimestamp
```

For direct metric inspection, port-forward the gateway's metrics port in one
terminal, then query from another (the reconciled default is 9103):

```bash
kubectl --kubeconfig "$DEMO_KUBECONFIG" -n "$DEMO_NAMESPACE" port-forward "pod/$DEMO_POD" 19103:9103
```

```bash
curl --fail --silent --show-error http://127.0.0.1:19103/metrics | rg '^mcp_gateway_tool_call_'
```

Events describe pod lifecycle and scheduling, not individual MCP requests.
No event archive is introduced here. If a span is missing, verify the deployed
images, Traefik v3 tracing configuration, OTLP collector reachability, sampling
and the server's SDK support before claiming full end-to-end coverage.

## Explain the platform API path separately

Enrollment is a platform operation before the MCP call. Follow its response
`X-Trace-ID` through `mcp_request_total` / `mcp_request_duration_seconds` and
`event=http.request` records for the registered API route. Shared bootstrap
services use the same service identifier in logs and HTTP metrics, including
an explicit `OTEL_SERVICE_NAME` override. Queries must use the configured name. Internal
HTTP client spans connect downstream services when W3C propagation is supported.
An enrollment trace and a later tool-call trace normally have different IDs;
the grant/agent-session relationship explains why the tool call is authorized,
not why the two independent HTTP operations share a trace.

Correlation provisioning follows [Grafana's Tempo provisioning documentation](https://grafana.com/docs/grafana/latest/datasources/tempo/configure-tempo-data-source/provision/).
The Grafana data sources remain behind the existing administrative access boundary.
