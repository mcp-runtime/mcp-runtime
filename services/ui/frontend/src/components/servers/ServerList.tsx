import { useEffect, useRef, useState } from "react";

import { Button } from "../../ui/Button";
import { CopyButton } from "../../ui/CopyButton";
import { StatusBadge } from "../../ui/Badge";
import { Icon } from "../../ui/Icon";
import { EmptyState } from "../../ui/States";
import { formatAbsolute, formatAge } from "../../lib/format";
import {
  serverAuthInfo,
  isServerReady,
  serverKey,
  serverPromptDetails,
  serverResourceDetails,
  serverTaskDetails,
  type ServerSummary,
} from "../../api/types";

type ServerListProps = {
  servers: ServerSummary[];
  toolCounts: Record<string, number>;
  scopedServerKey: string;
  inspectedServerKey: string;
  onScope: (key: string) => void;
  onInspect: (key: string) => void;
  onInspectInventory?: (key: string, item: string) => void;
  onClearFilters: () => void;
  filtered: boolean;
  // Retiring is offered to any authenticated principal: the runtime API checks
  // publish permission on the namespace, not the admin role. Omitted entirely
  // for the anonymous public catalog, where there is no session to retire with.
  onRetire?: (server: ServerSummary) => void;
  retiringKey?: string;
  retireError?: string;
};

export function ServerList({
  servers,
  toolCounts,
  scopedServerKey,
  inspectedServerKey,
  onScope,
  onInspect,
  onInspectInventory,
  onClearFilters,
  filtered,
  onRetire,
  retiringKey,
  retireError,
}: ServerListProps) {
  const [copiedKey, setCopiedKey] = useState("");
  const [copyError, setCopyError] = useState("");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => () => clearTimeout(timer.current), []);

  async function copyConnectConfig(server: ServerSummary) {
    const key = serverKey(server);
    setCopyError("");
    clearTimeout(timer.current);
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error("clipboard unavailable");
      }
      await navigator.clipboard.writeText(JSON.stringify(server.access_json || {}, null, 2));
      setCopiedKey(key);
      timer.current = setTimeout(() => setCopiedKey((current) => (current === key ? "" : current)), 2400);
    } catch {
      setCopyError(`The connect config for ${server.name} could not be copied. Open the server details to read it.`);
    }
  }

  if (servers.length === 0) {
    return (
      <EmptyState
        icon="server"
        title={filtered ? "No servers match these filters." : "No MCP servers in this scope."}
        detail={
          filtered
            ? "Widen the namespace, status, or search filters to see more servers."
            : "Publish a server with `mcp-runtime server deploy`, or switch to a namespace that has one."
        }
        testId="server-list-empty"
        action={
          filtered ? (
            <Button variant="secondary" onClick={onClearFilters}>
              Clear filters
            </Button>
          ) : undefined
        }
      />
    );
  }

  return (
    <>
      {retireError ? (
        <p className="notice notice-danger" role="alert" data-testid="server-retire-error">
          <Icon name="alert" />
          <span className="notice-body">{retireError}</span>
        </p>
      ) : null}
      {copyError ? (
        <p className="notice notice-danger" role="alert" data-testid="server-copy-error">
          <Icon name="alert" />
          <span className="notice-body">{copyError}</span>
        </p>
      ) : null}

      <ul className="server-grid" data-testid="server-list">
        {servers.map((server) => {
          const key = serverKey(server);
          const ready = isServerReady(server);
          const scoped = key === scopedServerKey;
          const inspected = key === inspectedServerKey;
          const toolCount = toolCounts[key] ?? 0;
          const prompts = serverPromptDetails(server);
          const resources = serverResourceDetails(server);
          const tasks = serverTaskDetails(server);
          const auth = serverAuthInfo(server);
          const hasConnectConfig = Boolean(
            server.access_json && Object.keys(server.access_json).length
          );
          const observability = server.observability;
          // Grafana links are shown only when runtime-api marks the dashboard
          // link available; panel links never render without it (issue #543).
          const grafanaAvailable = Boolean(observability?.grafana.available && observability.grafana.url);
          const grafanaPanelLinks = grafanaAvailable
            ? observability?.prometheus.queries.filter((query) => query.grafana_url) ?? []
            : [];
          const hasObservability = grafanaAvailable;

          return (
            <li key={key}>
              <article
                className={scoped || inspected ? "server-card is-selected" : "server-card"}
                data-testid="server-card"
                data-server-key={key}
              >
                <div className="server-card-head">
                  <div>
                    <h3 className="server-card-name">{server.name}</h3>
                    <p className="server-card-namespace">{server.namespace}</p>
                  </div>
                  <div className="server-card-badges">
                    {/* Kubernetes readiness only: it says the replicas are up, not
                        that the MCP endpoint answered. */}
                    <StatusBadge tone={ready ? "ready" : "attention"}>
                      {ready ? "Ready" : server.status || "Not ready"}
                    </StatusBadge>
                    {/* What a client must present to reach this server. */}
                    <StatusBadge tone={auth.tone} label={auth.detail}>
                      {auth.label}
                    </StatusBadge>
                  </div>
                </div>

                {server.description ? (
                  <p className="server-card-description clamp-2" title={server.description}>
                    {server.description}
                  </p>
                ) : null}

                <div className="server-card-facts">
                  <span>
                    Replicas <b>{server.ready || "—"}</b>
                  </span>
                  <span>
                    Tools <b>{toolCount}</b>
                  </span>
                  {prompts.length ? (
                    <span>
                      Prompts <b>{prompts.length}</b>
                    </span>
                  ) : null}
                  {resources.length ? (
                    <span>
                      Resources <b>{resources.length}</b>
                    </span>
                  ) : null}
                  {tasks.length ? (
                    <span>
                      Tasks <b>{tasks.length}</b>
                    </span>
                  ) : null}
                  {formatAge(server.age) ? (
                    <span title={formatAbsolute(server.age)}>
                      Age <b>{formatAge(server.age)}</b>
                    </span>
                  ) : null}
                </div>

                {server.endpoint ? (
                  <span className="copy-row">
                    <span className="copy-value" title={server.endpoint}>
                      {server.endpoint}
                    </span>
                    <CopyButton
                      value={server.endpoint}
                      label={`Copy the endpoint for ${server.name}`}
                      testId="server-copy-endpoint"
                    />
                  </span>
                ) : null}

                {prompts.length || resources.length || tasks.length ? (
                  <details className="server-card-inventory" data-testid="server-card-inventory">
                    <summary>Protocol inventory <span>{prompts.length + resources.length + tasks.length} items</span></summary>
                    <div className="server-card-inventory-groups">
                      {[
                        { label: "Prompts", items: prompts },
                        { label: "Resources", items: resources },
                        { label: "Tasks", items: tasks },
                      ].filter((group) => group.items.length).map((group) => (
                        <div className="server-card-inventory-group" key={group.label}>
                          <strong>{group.label}</strong>
                          <div className="server-card-inventory-items">
                            {group.items.map((item) => (
                              onInspectInventory ? (
                                <button
                                  key={item.name}
                                  type="button"
                                  className="server-card-inventory-item"
                                  onClick={() => onInspectInventory(key, `${group.label}:${item.name}`)}
                                  title={`View ${item.name} in server details`}
                                >
                                  <span>{item.name}</span>
                                  <small>{item.description || "No description published"}</small>
                                </button>
                              ) : (
                                <div key={item.name} className="server-card-inventory-item">
                                  <span>{item.name}</span>
                                  <small>{item.description || "No description published"}</small>
                                </div>
                              )
                            ))}
                          </div>
                        </div>
                      ))}
                    </div>
                  </details>
                ) : null}

                {hasObservability ? (
                  <div className="server-card-observability" data-testid="server-card-observability">
                    <span className="observability-label">Metrics</span>
                    {grafanaAvailable && observability?.grafana.url ? (
                      <a
                        className="quiet-link"
                        href={observability.grafana.url}
                        target="_blank"
                        rel="noreferrer"
                        data-testid="server-card-grafana-link"
                      >
                        Grafana <Icon name="external" size={11} />
                      </a>
                    ) : null}
                    {grafanaPanelLinks.map((query) => (
                      <a
                        key={query.id}
                        className="quiet-link"
                        href={query.grafana_url}
                        target="_blank"
                        rel="noreferrer"
                        title={query.description}
                      >
                        {query.name} <Icon name="external" size={11} />
                      </a>
                    ))}
                  </div>
                ) : null}

                <div className="server-card-actions">
                  <Button variant="secondary" size="sm" onClick={() => onInspect(key)} data-testid="server-card-details">
                    View details
                  </Button>
                  <Button
                    variant={scoped ? "primary" : "ghost"}
                    size="sm"
                    aria-pressed={scoped}
                    data-testid="server-card-select"
                    onClick={() => onScope(scoped ? "" : key)}
                  >
                    {scoped ? "Clear server filter" : "Show tools"}
                  </Button>
                  {hasConnectConfig ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      icon={copiedKey === key ? "check" : "copy"}
                      data-testid="server-card-copy-connect-config"
                      onClick={() => void copyConnectConfig(server)}
                    >
                      {copiedKey === key ? "Copied" : "Copy connect config"}
                    </Button>
                  ) : null}
                  {onRetire ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      icon="trash"
                      className="card-action-end"
                      busy={retiringKey === key}
                      data-testid="server-card-retire"
                      onClick={() => onRetire(server)}
                    >
                      Retire
                    </Button>
                  ) : null}
                </div>
              </article>
            </li>
          );
        })}
      </ul>
    </>
  );
}
