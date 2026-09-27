import { useCallback, useMemo, useState } from "react";

import {
  EMPTY_SERVER_FILTERS,
  EMPTY_TOOL_FILTERS,
  countToolsByServer,
  filterServers,
  filterTools,
  hasDrift,
  toolsInScope,
  visibleServerKeySet,
  type ServerFilters,
  type ServerStatusFilter,
  type ToolFilters,
} from "./filters";
import { ServerDetail } from "./ServerDetail";
import { ServerList } from "./ServerList";
import { SignedOutServers } from "./SignedOutServers";
import { ToolCatalog } from "./ToolCatalog";
import { ToolDetail } from "./ToolDetail";
import { Button } from "../../ui/Button";
import { ConfirmDialog, type ConfirmRequest } from "../../ui/ConfirmDialog";
import { SelectField, TextField } from "../../ui/Field";
import { FilterBar, FilterSummary, type FilterChip } from "../../ui/FilterBar";
import { MetricGrid } from "../../ui/MetricCard";
import { PageHeader } from "../../ui/PageHeader";
import { ErrorState, LoadingState } from "../../ui/States";
import { PublicServersPreview } from "./PublicServersPreview";
import { Icon } from "../../ui/Icon";
import { useCatalog } from "../../hooks/useCatalog";
import { retireServer } from "../../api/catalog";
import { readRuntimeConfig } from "../../api/config";
import {
  formatPublishQuota,
  isServerReady,
  isTenantUser,
  serverKey,
  toolKey,
  type AuthStatus,
  type ServerSummary,
} from "../../api/types";

type ServersWorkspaceProps = {
  auth: AuthStatus;
  onSignIn: () => void;
  // Supplied by the shell so a selected server or tool is shareable and
  // survives back/forward. Standalone renders fall back to local state.
  params?: Record<string, string>;
  onParamsChange?: (params: Record<string, string | undefined>) => void;
};

const STATUS_OPTIONS: Array<{ value: ServerStatusFilter; label: string }> = [
  { value: "all", label: "All" },
  { value: "ready", label: "Ready" },
  { value: "attention", label: "Needs attention" },
];

export function ServersWorkspace({
  auth,
  onSignIn,
  params,
  onParamsChange,
}: ServersWorkspaceProps) {
  const authenticated = auth.authenticated;
  const [localParams, setLocalParams] = useState<Record<string, string>>({});
  const activeParams = params ?? localParams;
  const setParams = useCallback(
    (next: Record<string, string | undefined>) => {
      if (onParamsChange) {
        onParamsChange(next);
        return;
      }
      setLocalParams((current) => {
        const merged = { ...current };
        for (const [key, value] of Object.entries(next)) {
          if (value) {
            merged[key] = value;
          } else {
            delete merged[key];
          }
        }
        return merged;
      });
    },
    [onParamsChange]
  );

  const [serverFilters, setServerFilters] = useState<ServerFilters>(EMPTY_SERVER_FILTERS);
  const [toolFilters, setToolFilters] = useState<ToolFilters>(EMPTY_TOOL_FILTERS);
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null);
  const [retiringKey, setRetiringKey] = useState("");
  const [actionError, setActionError] = useState("");
  const [actionNotice, setActionNotice] = useState("");

  const catalog = useCatalog(authenticated, serverFilters.namespace);

  const visibleServers = useMemo(
    () => filterServers(catalog.servers, serverFilters),
    [catalog.servers, serverFilters]
  );
  const visibleServerKeys = useMemo(() => visibleServerKeySet(visibleServers), [visibleServers]);
  const toolCounts = useMemo(() => countToolsByServer(catalog.tools), [catalog.tools]);
  const scopedTools = useMemo(
    () => toolsInScope(catalog.tools, visibleServerKeys),
    [catalog.tools, visibleServerKeys]
  );
  const visibleTools = useMemo(
    () => filterTools(catalog.tools, toolFilters, visibleServerKeys),
    [catalog.tools, toolFilters, visibleServerKeys]
  );

  const inspectedServerKey = activeParams.server || "";
  const selectedToolKey = activeParams.tool || "";
  const inspectedServer = useMemo(
    () => catalog.servers.find((server) => serverKey(server) === inspectedServerKey),
    [catalog.servers, inspectedServerKey]
  );
  const selectedTool = useMemo(
    () => visibleTools.find((tool) => toolKey(tool) === selectedToolKey),
    [visibleTools, selectedToolKey]
  );
  const inspectedServerTools = useMemo(
    () =>
      inspectedServer
        ? catalog.tools.filter(
            (tool) => `${tool.namespace}/${tool.server_name}` === serverKey(inspectedServer)
          )
        : [],
    [catalog.tools, inspectedServer]
  );

  const scopedServerName = useMemo(() => {
    if (!toolFilters.serverKey) {
      return "";
    }
    const match = catalog.servers.find((server) => serverKey(server) === toolFilters.serverKey);
    return match ? match.name : toolFilters.serverKey;
  }, [catalog.servers, toolFilters.serverKey]);

  const changeServerFilters = useCallback(
    (next: ServerFilters) => {
      setServerFilters(next);
      // A scope change can invalidate the tool scope and the open inspectors.
      setToolFilters((current) => ({ ...current, serverKey: "" }));
      setParams({ server: undefined, tool: undefined });
    },
    [setParams]
  );

  const clearAll = useCallback(() => {
    setServerFilters(EMPTY_SERVER_FILTERS);
    setToolFilters(EMPTY_TOOL_FILTERS);
    setParams({ server: undefined, tool: undefined });
  }, [setParams]);

  if (!authenticated) {
    // A public-mode deployment shows its anonymous catalog instead of a
    // sign-in wall (services/ui/public_catalog_proxy.go).
    if (readRuntimeConfig().platformMode === "public") {
      return <PublicServersPreview onSignIn={onSignIn} />;
    }
    return <SignedOutServers onSignIn={onSignIn} />;
  }

  if (catalog.status === "loading") {
    return (
      <>
        <PageHeader title="Servers" description="Deployed MCP servers and their governed tool catalog." />
        <LoadingState
          label="Loading namespaces, servers, and tools…"
          testId="catalog-loading"
          variant="cards"
          rows={3}
        />
      </>
    );
  }

  if (catalog.status === "unauthorized") {
    return (
      <>
        <PageHeader title="Servers" />
        <ErrorState
          title="Your session expired."
          detail="Sign in again to reload the server catalog."
          onRetry={onSignIn}
          retryLabel="Sign in"
          testId="catalog-unauthorized"
        />
      </>
    );
  }

  if (catalog.status === "error") {
    return (
      <>
        <PageHeader title="Servers" />
        <ErrorState
          title="The server catalog could not be loaded."
          detail={catalog.error}
          onRetry={catalog.reload}
          testId="catalog-error"
        />
      </>
    );
  }

  function askRetire(server: ServerSummary) {
    const key = serverKey(server);
    setConfirm({
      title: `Retire ${server.name}?`,
      body: (
        <>
          The MCPServer object in <strong>{server.namespace}</strong> is deleted and its endpoint stops
          answering. Tools it publishes leave the catalog. This cannot be undone from here; the server
          would have to be published again.
        </>
      ),
      confirmLabel: "Retire server",
      destructive: true,
      onConfirm: async () => {
        setRetiringKey(key);
        setActionError("");
        setActionNotice("");
        try {
          await retireServer(server.namespace, server.name);
          setActionNotice(`${server.name} retired.`);
          // The retired server may have been the active tool scope. Leaving
          // that key set would filter every remaining tool out and leave an
          // apparently empty catalog.
          setToolFilters((current) =>
            current.serverKey === key ? { ...current, serverKey: "" } : current
          );
          setParams({ server: undefined, tool: undefined });
          catalog.reload();
        } catch (error) {
          setActionError(
            error instanceof Error ? error.message : `${server.name} could not be retired.`
          );
        } finally {
          setRetiringKey("");
          setConfirm(null);
        }
      },
    });
  }

  const readyCount = visibleServers.filter(isServerReady).length;
  const driftCount = scopedTools.filter(hasDrift).length;
  const scopeLabel = serverFilters.namespace ? `namespace ${serverFilters.namespace}` : "all namespaces you can read";
  const serversFiltered =
    serverFilters.search.trim() !== "" || serverFilters.status !== "all" || serverFilters.namespace !== "";

  const serverChips: FilterChip[] = [];
  if (serverFilters.search.trim()) {
    serverChips.push({
      id: "search",
      label: "Search",
      value: serverFilters.search.trim(),
      onRemove: () => changeServerFilters({ ...serverFilters, search: "" }),
    });
  }
  if (serverFilters.namespace) {
    serverChips.push({
      id: "namespace",
      label: "Namespace",
      value: serverFilters.namespace,
      onRemove: () => changeServerFilters({ ...serverFilters, namespace: "" }),
    });
  }
  if (serverFilters.status !== "all") {
    serverChips.push({
      id: "status",
      label: "Status",
      value: serverFilters.status === "ready" ? "Ready" : "Needs attention",
      onRemove: () => changeServerFilters({ ...serverFilters, status: "all" }),
    });
  }

  const sheetOpen = Boolean(inspectedServer || selectedTool);

  return (
    <>
      <PageHeader
        title="Servers"
        description={`Deployed MCP servers and their governed tool catalog for ${scopeLabel}.`}
        actions={
          <Button
            variant="secondary"
            icon="refresh"
            onClick={catalog.reload}
            busy={catalog.refreshing}
            data-testid="catalog-refresh"
          >
            {catalog.refreshing ? "Refreshing…" : "Refresh"}
          </Button>
        }
      />

      <MetricGrid
        label="Server catalog summary"
        testId="server-stats"
        metrics={[
          {
            label: "Servers",
            value: visibleServers.length,
            icon: "server",
            hint: serversFiltered ? "Matching the filters below" : `In ${scopeLabel}`,
          },
          {
            label: "Ready",
            value: readyCount,
            icon: "check",
            hint: "All replicas up",
            tone: readyCount === visibleServers.length ? "default" : "warning",
          },
          { label: "Tools", value: scopedTools.length, icon: "tool", hint: "Published by these servers" },
          {
            label: "Tools with drift",
            value: driftCount,
            icon: "alert",
            hint: "Missing or ungoverned",
            tone: driftCount > 0 ? "warning" : "default",
          },
          // The runtime does not cap admin publishing, so the quota is only
          // meaningful for a tenant principal.
          ...(isTenantUser(auth)
            ? [
                {
                  label: "Publish quota",
                  value: formatPublishQuota(catalog.publishPolicy),
                  icon: "inbox" as const,
                  hint: "Active servers against your limit",
                  testId: "server-quota",
                },
              ]
            : []),
        ]}
      />

      {actionNotice ? (
        <p className="notice notice-success" role="status" data-testid="servers-action-notice">
          <Icon name="check" />
          <span className="notice-body">{actionNotice}</span>
        </p>
      ) : null}


      <div className={sheetOpen ? "servers-layout with-sheet" : "servers-layout"}>
        <div>
          <section className="section">
            <div className="section-head">
              <h2 className="section-title" id="server-list-title">
                Server fleet
              </h2>
            </div>

            <FilterBar label="Filter servers">
              <TextField
                label="Search servers"
                type="search"
                fieldClassName="grow"
                leadingIcon
                placeholder="Name, namespace, description, image, or endpoint"
                value={serverFilters.search}
                data-testid="server-search"
                onChange={(event) =>
                  changeServerFilters({ ...serverFilters, search: event.target.value })
                }
              />
              <SelectField
                label="Namespace"
                value={serverFilters.namespace}
                data-testid="namespace-filter"
                options={[
                  { value: "", label: "All namespaces" },
                  ...catalog.namespaces.map((entry) => ({
                    value: entry.namespace,
                    label: entry.namespace,
                  })),
                ]}
                onChange={(event) =>
                  changeServerFilters({ ...serverFilters, namespace: event.target.value })
                }
              />
              <fieldset className="field">
                <legend className="field-label">Status</legend>
                <div className="segmented">
                  {STATUS_OPTIONS.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      className="segment"
                      aria-pressed={serverFilters.status === option.value}
                      data-testid={`server-status-${option.value}`}
                      onClick={() => changeServerFilters({ ...serverFilters, status: option.value })}
                    >
                      {option.label}
                    </button>
                  ))}
                </div>
              </fieldset>
            </FilterBar>

            <FilterSummary
              chips={serverChips}
              count={
                visibleServers.length === catalog.servers.length
                  ? `${catalog.servers.length} server${catalog.servers.length === 1 ? "" : "s"}`
                  : `${visibleServers.length} of ${catalog.servers.length} servers`
              }
              onClear={clearAll}
              testId="server-summary"
            />

            <ServerList
              servers={visibleServers}
              toolCounts={toolCounts}
              scopedServerKey={toolFilters.serverKey}
              inspectedServerKey={inspectedServerKey}
              filtered={serversFiltered}
              onClearFilters={clearAll}
              onScope={(key) => {
                setToolFilters((current) => ({ ...current, serverKey: key }));
                setParams({ tool: undefined });
              }}
              onInspect={(key) => setParams({ server: key, tool: undefined, inventory: undefined })}
              onInspectInventory={(key, item) => setParams({ server: key, tool: undefined, inventory: item })}
              onRetire={askRetire}
              retiringKey={retiringKey}
              retireError={actionError}
            />
          </section>

          <ToolCatalog
            tools={visibleTools}
            scopedCount={scopedTools.length}
            filters={toolFilters}
            scopedServerName={scopedServerName}
            selectedToolKey={selectedToolKey}
            onFiltersChange={setToolFilters}
            onClearFilters={() => {
              setToolFilters(EMPTY_TOOL_FILTERS);
              setParams({ tool: undefined });
            }}
            onSelectTool={(key) => setParams({ tool: key || undefined, server: undefined })}
          />
        </div>

        {selectedTool ? (
          <ToolDetail tool={selectedTool} onClose={() => setParams({ tool: undefined })} />
        ) : inspectedServer ? (
          <ServerDetail
            key={`${inspectedServerKey}:${activeParams.inventory || ""}`}
            server={inspectedServer}
            tools={inspectedServerTools}
            selectedInventory={activeParams.inventory}
            onClose={() => setParams({ server: undefined, inventory: undefined })}
            onShowTools={() => {
              setToolFilters((current) => ({ ...current, serverKey: serverKey(inspectedServer) }));
              setParams({ server: undefined, inventory: undefined });
            }}
            onSelectTool={(key) => setParams({ tool: key, server: undefined, inventory: undefined })}
          />
        ) : null}
      </div>

      {confirm ? (
        <ConfirmDialog
          {...confirm}
          busy={retiringKey !== ""}
          onCancel={() => setConfirm(null)}
          testId="server-retire-confirm"
          confirmTestId="server-retire-confirm-yes"
          cancelTestId="server-retire-confirm-cancel"
        />
      ) : null}
    </>
  );
}
