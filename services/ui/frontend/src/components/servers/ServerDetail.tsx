import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { StatusBadge, riskTone } from "../../ui/Badge";
import { Button, ButtonLink } from "../../ui/Button";
import { CopyButton } from "../../ui/CopyButton";
import { DetailSheet } from "../../ui/DetailSheet";
import { formatAbsolute, formatAge } from "../../lib/format";
import {
  clientConnectSnippet,
  serverAuthInfo,
  isServerReady,
  serverPromptDetails,
  serverResourceDetails,
  serverTaskDetails,
  toolKey,
  type InventoryDetail,
  type ServerSummary,
  type ToolRow,
} from "../../api/types";
import { getRuntimeServer, getServerObservabilityMetrics, listServerEvents, listServerLogs } from "../../api/catalog";

type ServerDetailProps = {
  server: ServerSummary;
  tools: ToolRow[];
  onClose: () => void;
  onShowTools: () => void;
  onSelectTool: (key: string) => void;
  selectedInventory?: string;
};

function parseLogTimestamp(value: string): Date | undefined {
  const milliseconds = Number(value) / 1_000_000;
  if (!Number.isFinite(milliseconds)) return undefined;
  const date = new Date(milliseconds);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

function InventoryGroup({ title, items, selectedInventory }: { title: string; items: InventoryDetail[]; selectedInventory?: string }) {
  if (items.length === 0) return null;

  return (
    <section className="protocol-group" aria-label={title}>
      <h3 className="protocol-group-title">{title} <span>{items.length}</span></h3>
      <div className="protocol-items">
        {items.map((item) => (
          <details key={item.name} className="protocol-item" data-testid="server-detail-inventory-item" open={selectedInventory === `${title}:${item.name}` || undefined}>
            <summary>
              <span className="protocol-item-heading">
                <span className="protocol-item-name">{item.name}</span>
                <span className="protocol-item-description">{item.description || "No description published"}</span>
              </span>
              <span className="protocol-item-cue">Details</span>
            </summary>
            <div className="protocol-item-body">
              <p><strong>Source:</strong> {item.source === "both" ? "Server metadata and latest probe" : item.source === "live" ? "Latest probe" : "Server metadata"}</p>
              {item.uri ? <p><strong>URI:</strong> <code>{item.uri}</code></p> : null}
              {item.mimeType ? <p><strong>Media type:</strong> {item.mimeType}</p> : null}
              {item.arguments?.length ? (
                <div>
                  <strong>Arguments</strong>
                  <ul className="protocol-item-arguments">
                    {item.arguments.map((argument) => (
                      <li key={argument.name}>
                        <code>{argument.name}</code>{argument.required ? " (required)" : ""}
                        {argument.description ? <span> — {argument.description}</span> : null}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
              {item.labels && Object.keys(item.labels).length ? (
                <div>
                  <strong>Labels</strong>
                  <div className="protocol-item-labels">
                    {Object.entries(item.labels).map(([key, value]) => (
                      <span key={key}>{key}: {value}</span>
                    ))}
                  </div>
                </div>
              ) : null}
            </div>
          </details>
        ))}
      </div>
    </section>
  );
}

export function ServerDetail({ server, tools, onClose, onShowTools, onSelectTool, selectedInventory }: ServerDetailProps) {
  const [configTab, setConfigTab] = useState<"claude" | "cursor" | "vscode" | "raw">("claude");
  const ready = isServerReady(server);
  const auth = serverAuthInfo(server);
  const prompts = serverPromptDetails(server);
  const resources = serverResourceDetails(server);
  const tasks = serverTaskDetails(server);

  // The runtime hands back a complete MCP client config for this server. It is
  // shown in full and selectable, so it is usable even if the clipboard is
  // blocked.
  const connectConfig = useMemo(
    () =>
      server.access_json && Object.keys(server.access_json).length
        ? JSON.stringify(server.access_json, null, 2)
        : "",
    [server.access_json]
  );

  // Prefer the catalog rows (they carry drift and the resolved risk); fall back
  // to the declared tools on the server record when the catalog read is scoped
  // elsewhere.
  const toolRows = useMemo(() => {
    if (tools.length > 0) {
      return tools.map((tool) => ({
        key: toolKey(tool),
        name: tool.tool_name,
        description: tool.description,
        trust: tool.required_trust,
        sideEffect: tool.side_effect,
        risk: tool.risk_level,
        selectable: true,
      }));
    }
    return (server.tools || []).map((tool) => ({
      key: `${server.namespace}/${server.name}/${tool.name || ""}`,
      name: tool.name || "—",
      description: tool.description,
      trust: tool.requiredTrust,
      sideEffect: tool.sideEffect,
      risk: tool.riskLevel,
      selectable: false,
    }));
  }, [tools, server]);

  const eventsQuery = useQuery({
    queryKey: ["server-events", server.namespace, server.name],
    queryFn: () => listServerEvents(server.namespace, server.name),
    staleTime: 30_000,
  });

  const runtimeStatusQuery = useQuery({
    queryKey: ["runtime-server-detail", server.namespace, server.name],
    queryFn: () => getRuntimeServer(server.namespace, server.name),
    staleTime: 30_000,
  });
  const runtimeStatus = runtimeStatusQuery.data?.runtimeStatus ?? server.runtimeStatus;

  // Observability is included by the API only for principals authorized to
  // inspect this gateway-backed server. The logs endpoint repeats that check.
  const logsQuery = useQuery({
    queryKey: ["server-logs", server.namespace, server.name],
    queryFn: () => listServerLogs(server.namespace, server.name),
    enabled: !!server.observability,
    staleTime: 15_000,
  });
  const metricsQuery = useQuery({
    queryKey: ["server-metrics", server.namespace, server.name, server.uid || ""],
    queryFn: () => getServerObservabilityMetrics(server.namespace, server.name),
    enabled: !!server.observability,
    staleTime: 30_000,
  });

  const configTabs = useMemo(() => {
    return {
      claude: { label: "Claude Desktop", hint: "~/Library/Application Support/Claude/claude_desktop_config.json", value: JSON.stringify(clientConnectSnippet(server, "mcpServers"), null, 2) },
      cursor: { label: "Cursor", hint: "~/.cursor/mcp.json or .cursor/mcp.json", value: JSON.stringify(clientConnectSnippet(server, "mcpServers"), null, 2) },
      vscode: { label: "VS Code", hint: ".vscode/mcp.json", value: JSON.stringify(clientConnectSnippet(server, "servers"), null, 2) },
      raw: { label: "Raw JSON", hint: "The server-provided access configuration", value: JSON.stringify(server.access_json || {}, null, 2) },
    };
  }, [server]);

  return (
    <DetailSheet
      title={server.name}
      eyebrow={server.namespace}
      onClose={onClose}
      testId="server-detail"
      closeLabel="Close server details"
      closeTestId="server-detail-close"
    >
      <div className="inline-actions">
        <StatusBadge tone={ready ? "ready" : "attention"}>
          {ready ? "Ready" : server.status || "Not ready"}
        </StatusBadge>
        {auth.label ? <StatusBadge tone={auth.tone}>{auth.label}</StatusBadge> : null}
        {server.gatewayEnabled === true ? (
          <StatusBadge tone="info" label="Runtime gateway enabled" testId="server-gateway-badge">Gateway</StatusBadge>
        ) : null}
      </div>

      {server.description ? <p className="muted">{server.description}</p> : null}

      <div>
        <p className="detail-label">Authentication</p>
        <p className="section-note" data-testid="server-detail-auth">
          {auth.detail}
        </p>
      </div>

      {connectConfig ? (
        <div>
          <div className="section-head">
            <p className="detail-label">Connect from an MCP client</p>
            <CopyButton
              value={connectConfig}
              label={`Copy the MCP client config for ${server.name}`}
              testId="server-detail-copy-config"
            />
          </div>
          <div className="detail-tabs" role="tablist" aria-label="MCP client configuration">
            {(Object.keys(configTabs) as Array<keyof typeof configTabs>).map((key) => (
              <button key={key} type="button" role="tab" aria-selected={configTab === key} className={configTab === key ? "detail-tab is-active" : "detail-tab"} onClick={() => setConfigTab(key)}>
                {configTabs[key].label}
              </button>
            ))}
          </div>
          <p className="section-note" style={{ marginBottom: "var(--space-2)" }}>
            Paste this into {configTabs[configTab].hint}.
          </p>
          <code className="code-block" data-testid="server-detail-config">
            {configTabs[configTab].value}
          </code>
        </div>
      ) : (
        <p className="section-note" data-testid="server-detail-no-config">
          The runtime did not resolve a public connect endpoint for this server, so there is no client
          config to copy.
        </p>
      )}

      <dl className="detail-rows">
        <div>
          <dt>Replicas ready</dt>
          <dd className="num">{server.ready || "—"}</dd>
        </div>
        <div>
          <dt>Status</dt>
          <dd>{server.status || "Unknown"}</dd>
        </div>
        {formatAge(server.age) ? (
          <div>
            <dt>Age</dt>
            <dd title={formatAbsolute(server.age)}>{formatAge(server.age)}</dd>
          </div>
        ) : null}
        {server.image ? (
          <div>
            <dt>Image</dt>
            <dd>
              <span className="copy-row">
                <span className="copy-value" title={server.image}>
                  {server.image}
                </span>
                <CopyButton value={server.image} label={`Copy the image reference for ${server.name}`} />
              </span>
            </dd>
          </div>
        ) : null}
        {server.endpoint ? (
          <div>
            <dt>Endpoint</dt>
            <dd>
              <span className="copy-row">
                <span className="copy-value" title={server.endpoint}>
                  {server.endpoint}
                </span>
                <CopyButton
                  value={server.endpoint}
                  label={`Copy the endpoint for ${server.name}`}
                  testId="server-detail-copy-endpoint"
                />
              </span>
            </dd>
          </div>
        ) : null}
        {prompts.length || resources.length || tasks.length ? (
          <div>
            <dt>Also serves</dt>
            <dd>
              {[
                prompts.length ? `${prompts.length} prompts` : "",
                resources.length ? `${resources.length} resources` : "",
                tasks.length ? `${tasks.length} tasks` : "",
              ]
                .filter(Boolean)
                .join(" · ")}
            </dd>
          </div>
        ) : null}
        {server.uid ? (
          <div>
            <dt>UID</dt>
            <dd className="cell-code">{server.uid}</dd>
          </div>
        ) : null}
      </dl>

      <section className="protocol-inventory" data-testid="server-detail-runtime-status">
        <div className="section-head">
          <p className="detail-label">Runtime status</p>
          <span className="section-note">Operator state and recent runtime observations</span>
        </div>
        {runtimeStatusQuery.error ? <p className="section-note">Some runtime details are unavailable.</p> : null}
        <dl className="detail-rows">
          <div><dt>Transport</dt><dd>{runtimeStatus?.transport || "Unknown"}</dd></div>
          <div><dt>Connection</dt><dd>{runtimeStatus?.connectionState || server.status || "Unknown"}</dd></div>
          <div><dt>Authentication source</dt><dd>{runtimeStatus?.authSource || auth.label || "Unknown"}</dd></div>
          {runtimeStatus?.lastProbeTime ? <div><dt>Last inventory probe</dt><dd>{new Date(runtimeStatus.lastProbeTime).toLocaleString()}</dd></div> : null}
          {runtimeStatus?.lastError ? <div><dt>Last error</dt><dd title={runtimeStatus.lastError.reason}>{runtimeStatus.lastError.message} · {new Date(runtimeStatus.lastError.observedAt).toLocaleString()}</dd></div> : null}
          {runtimeStatus?.lastSession ? <div><dt>Last session</dt><dd title={runtimeStatus.lastSession.namespace || undefined}>{runtimeStatus.lastSession.id}{runtimeStatus.lastSession.toolName ? ` · ${runtimeStatus.lastSession.toolName}` : ""} · {new Date(runtimeStatus.lastSession.usedAt).toLocaleString()}</dd></div> : null}
          {!runtimeStatus?.lastSession && runtimeStatus?.lastSessionState === "none" ? <div><dt>Last session</dt><dd>No session-backed call was recorded.</dd></div> : null}
          {!runtimeStatus?.lastSession && runtimeStatus?.lastSessionState === "unavailable" ? <div><dt>Last session</dt><dd>Activity access or analytics is unavailable.</dd></div> : null}
        </dl>
      </section>

      <div>
        <div className="section-head">
          <p className="detail-label">Tools ({toolRows.length})</p>
          <Button variant="ghost" size="sm" onClick={onShowTools} data-testid="server-detail-show-tools">
            Filter catalog to this server
          </Button>
        </div>
        {toolRows.length === 0 ? (
          <p className="section-note">This server publishes no tools in the current catalog read.</p>
        ) : (
          <div className="tool-scroll-list" data-testid="server-detail-tools" tabIndex={0}>
            {toolRows.map((tool) => {
              const body = (
                <>
                  <span className="tool-scroll-name">
                    {tool.name}
                    {tool.risk ? (
                      <StatusBadge tone={riskTone(tool.risk)} dot={false}>
                        {tool.risk}
                      </StatusBadge>
                    ) : null}
                    {tool.trust ? (
                      <StatusBadge tone="neutral" dot={false}>
                        {`trust: ${tool.trust}`}
                      </StatusBadge>
                    ) : null}
                    {tool.sideEffect ? (
                      <StatusBadge tone="neutral" dot={false}>
                        {tool.sideEffect}
                      </StatusBadge>
                    ) : null}
                  </span>
                  {tool.description ? <span className="tool-scroll-desc">{tool.description}</span> : null}
                </>
              );
              return tool.selectable ? (
                <button
                  key={tool.key}
                  type="button"
                  className="tool-scroll-item"
                  data-testid="server-detail-tool"
                  onClick={() => onSelectTool(tool.key)}
                >
                  {body}
                </button>
              ) : (
                <div key={tool.key} className="tool-scroll-item" data-testid="server-detail-tool">
                  {body}
                </div>
              );
            })}
          </div>
        )}
      </div>

      {prompts.length || resources.length || tasks.length ? (
        <section className="protocol-inventory" data-testid="server-detail-inventory">
          <div className="section-head">
            <p className="detail-label">Protocol inventory</p>
            <span className="section-note">Select an item to see its details</span>
          </div>
          <InventoryGroup title="Prompts" items={prompts} selectedInventory={selectedInventory} />
          <InventoryGroup title="Resources" items={resources} selectedInventory={selectedInventory} />
          <InventoryGroup title="Tasks" items={tasks} selectedInventory={selectedInventory} />
        </section>
      ) : null}

      {server.observability ? (
        <section className="protocol-inventory" data-testid="server-detail-observability">
          <div className="section-head">
            <p className="detail-label">Server observability</p>
            <span className="section-note">Scoped to {server.namespace}/{server.name}</span>
          </div>
          <div className="inline-actions" aria-label="Server metrics">
            {server.observability.prometheus.queries.map((query) => (
              <ButtonLink key={query.id} size="sm" href={query.url} target="_blank" rel="noreferrer">
                {query.name}
              </ButtonLink>
            ))}
            {server.observability.grafana.available ? (
              <ButtonLink size="sm" href={server.observability.grafana.url} target="_blank" rel="noreferrer">Grafana dashboard</ButtonLink>
            ) : null}
          </div>
          <div data-testid="server-detail-metric-summary" aria-label="Server metrics summary">
            {metricsQuery.isPending ? <p className="section-note">Loading one-hour metrics…</p> : null}
            {metricsQuery.error ? <p className="section-note">Server metrics are unavailable.</p> : null}
            {metricsQuery.data ? (
              <>
                <dl className="detail-rows">
                  <div><dt>tools/call rate</dt><dd>{metricsQuery.data.tools_call_rate_per_second == null ? "No data" : `${metricsQuery.data.tools_call_rate_per_second.toFixed(2)} / sec`}</dd></div>
                  <div><dt>tools/call p95</dt><dd>{metricsQuery.data.tools_call_p95_seconds == null ? "No data" : `${(metricsQuery.data.tools_call_p95_seconds * 1000).toFixed(0)} ms`}</dd></div>
                  <div><dt>Upstream HTTP 5xx rate</dt><dd>{metricsQuery.data.upstream_5xx_rate_per_second == null ? "No data" : `${metricsQuery.data.upstream_5xx_rate_per_second.toFixed(3)} / sec`}</dd></div>
                  <div><dt>Policy denies</dt><dd>{metricsQuery.data.policy_deny_count == null ? "No data" : Math.round(metricsQuery.data.policy_deny_count)}</dd></div>
                  <div><dt>Slowest tool p95</dt><dd>{metricsQuery.data.slowest_tool || "Not enough calls"}</dd></div>
                </dl>
                <p className="section-note">Window: last {metricsQuery.data.window}. The slowest tool is shown after at least five calls.</p>
              </>
            ) : null}
          </div>
          <div>
            <div className="section-head">
              <p className="detail-label">Recent server logs</p>
              <span className="section-note">Last hour · up to 100 lines</span>
            </div>
            {logsQuery.isPending ? <p className="section-note">Loading server logs…</p> : null}
            {logsQuery.error ? <p className="section-note">Server logs are unavailable.</p> : null}
            {!logsQuery.isPending && !logsQuery.error && (logsQuery.data || []).length === 0 ? <p className="section-note">No recent server logs.</p> : null}
            {(logsQuery.data || []).length > 0 ? (
              <div className="event-list" data-testid="server-detail-logs" role="log" aria-label={`Recent logs for ${server.name}`}>
                {(logsQuery.data || []).map((entry, index) => (
                  <div className="event-row" key={`${entry.timestamp}-${index}`}>
                    <time dateTime={parseLogTimestamp(entry.timestamp)?.toISOString()}>{parseLogTimestamp(entry.timestamp)?.toLocaleString() ?? "Unknown time"}</time>
                    <span className="wrap-anywhere" title={entry.container ? `${entry.container}${entry.pod ? ` · ${entry.pod}` : ""}` : undefined}>{entry.line}</span>
                  </div>
                ))}
              </div>
            ) : null}
          </div>
        </section>
      ) : null}

      <div>
        <div className="section-head">
          <p className="detail-label">Recent activity</p>
          <span className="section-note">Last 20 events</span>
        </div>
        {eventsQuery.isPending ? <p className="section-note">Loading activity…</p> : null}
        {eventsQuery.error ? <p className="section-note">Activity is unavailable for this server.</p> : null}
        {!eventsQuery.isPending && !eventsQuery.error && (eventsQuery.data || []).length === 0 ? <p className="section-note">No recent activity recorded.</p> : null}
        {(eventsQuery.data || []).length > 0 ? (
          <div className="event-list" data-testid="server-detail-events">
            {(eventsQuery.data || []).map((event, index) => (
              <div className="event-row" key={`${event.timestamp || "event"}-${index}`}>
                <span>{event.tool_name || event.event_type || "Gateway event"}</span>
                <StatusBadge tone={event.decision === "deny" ? "warning" : "ready"}>{event.decision || "unknown"}</StatusBadge>
                <time dateTime={event.timestamp}>{event.timestamp ? new Date(event.timestamp).toLocaleString() : "—"}</time>
              </div>
            ))}
          </div>
        ) : null}
      </div>
    </DetailSheet>
  );
}
