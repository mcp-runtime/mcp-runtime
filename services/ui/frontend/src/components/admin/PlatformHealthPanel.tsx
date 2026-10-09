import { grafanaPath } from "../../api/config";
import { useState } from "react";

import { AsyncSection } from "./AsyncSection";
import { StatusBadge } from "../../ui/Badge";
import { Button, ButtonLink } from "../../ui/Button";
import { ConfirmDialog, type ConfirmRequest } from "../../ui/ConfirmDialog";
import { Icon } from "../../ui/Icon";
import { MetricGrid } from "../../ui/MetricCard";
import { PageHeader } from "../../ui/PageHeader";
import { EmptyState } from "../../ui/States";
import { useAdminReload, useComponents } from "../../hooks/useAdminData";
import { restartComponent } from "../../api/admin";
import type { ComponentStatus } from "../../api/types";

type PlatformHealthPanelProps = {
  onSignIn: () => void;
};

function componentReady(component: ComponentStatus): boolean {
  return (component.status || "").toLowerCase() === "ready";
}

export function PlatformHealthPanel({ onSignIn }: PlatformHealthPanelProps) {
  const reload = useAdminReload();
  const componentsQuery = useComponents(true);
  const components = componentsQuery.data ?? [];
  const loaded = !componentsQuery.isPending && !componentsQuery.error;
  const readyCount = components.filter(componentReady).length;
  const [busyKey, setBusyKey] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null);

  async function restart(key: string, label: string, component?: string) {
    setBusyKey(key);
    setError("");
    setNotice("");
    try {
      await restartComponent(component);
      setNotice(`${label} restarting. Readiness updates as the rollout progresses.`);
      reload();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "The restart could not be started.");
    } finally {
      setBusyKey("");
      setConfirm(null);
    }
  }

  return (
    <>
      <PageHeader
        title="Platform health"
        breadcrumb={[{ label: "Administration" }, { label: "Platform health" }]}
        description="Operator, platform services, and observability components reported by the runtime API."
        actions={
          <Button
            variant="secondary"
            icon="refresh"
            onClick={reload}
            busy={componentsQuery.isFetching && !componentsQuery.isPending}
            data-testid="platform-refresh"
          >
            Refresh
          </Button>
        }
      />

      <MetricGrid
        label="Platform summary"
        testId="platform-stats"
        metrics={[
          { label: "Components", value: loaded ? components.length : undefined, icon: "server" },
          { label: "Ready", value: loaded ? readyCount : undefined, icon: "check", tone: "success" },
          {
            label: "Not ready",
            value: loaded ? components.length - readyCount : undefined,
            icon: "alert",
            tone: loaded && components.length - readyCount > 0 ? "danger" : "default",
          },
        ]}
      />

      <nav className="observability-links" aria-label="Observability tools" style={{ marginBottom: "var(--space-5)" }}>
        {/* Keep the link direct so the platform ingress forward-auth still applies. */}
        <ButtonLink
          variant="secondary"
          href={grafanaPath()}
          target="_blank"
          rel="noreferrer"
          trailingIcon="external"
          data-testid="grafana-link"
        >
          Grafana
        </ButtonLink>
      </nav>

      {notice ? (
        <p className="notice notice-success" role="status" data-testid="platform-action-notice">
          <Icon name="check" />
          <span className="notice-body">{notice}</span>
        </p>
      ) : null}
      {error ? (
        <p className="notice notice-danger" role="alert" data-testid="platform-action-error">
          <Icon name="alert" />
          <span className="notice-body">{error}</span>
        </p>
      ) : null}

      <AsyncSection
        query={componentsQuery}
        loadingLabel="Loading platform components…"
        errorTitle="Platform components could not be loaded."
        onRetry={reload}
        onSignIn={onSignIn}
        testId="platform"
        loadingVariant="cards"
      >
        {components.length === 0 ? (
          <EmptyState
            icon="server"
            title="No platform components reported."
            detail="The runtime API returned an empty component list for this cluster."
            testId="platform-empty"
          />
        ) : (
          <ul className="component-grid" data-testid="platform-components">
            {components.map((component) => {
              const ready = componentReady(component);
              const name = component.display || component.key;
              return (
                <li key={component.key}>
                  <article className="component-card" data-testid="platform-component">
                    <div className="component-card-head">
                      <div>
                        <p className="component-name">{name}</p>
                        <p className="component-resource">
                          {component.namespace} / {component.resource}
                        </p>
                      </div>
                      <StatusBadge tone={ready ? "ready" : "attention"}>
                        {component.status || "Unknown"}
                      </StatusBadge>
                    </div>

                    {component.message ? (
                      <p className="component-message">{component.message}</p>
                    ) : null}

                    <div className="component-card-foot">
                      <span>
                        {component.kind} · ready <span className="num">{component.ready || "—"}</span>
                      </span>
                      <Button
                        variant="ghost"
                        size="sm"
                        icon="power"
                        busy={busyKey === component.key}
                        disabled={busyKey !== "" && busyKey !== component.key}
                        data-testid={`restart-${component.key}`}
                        onClick={() =>
                          setConfirm({
                            title: `Restart ${name}?`,
                            body: `This rolls ${component.namespace}/${component.resource}. Traffic through this component may be interrupted while the new pods become ready.`,
                            confirmLabel: `Restart ${name}`,
                            destructive: true,
                            onConfirm: () => restart(component.key, name, component.key),
                          })
                        }
                      >
                        Restart
                      </Button>
                    </div>
                  </article>
                </li>
              );
            })}
          </ul>
        )}
      </AsyncSection>

      <section className="danger-zone" aria-labelledby="platform-danger-title">
        <h2 id="platform-danger-title">
          <Icon name="alert" /> Disruptive actions
        </h2>
        <p>
          Restarting everything rolls every platform component at once. Expect the dashboard, gateway, and
          analytics to be briefly unavailable. Refreshing this page is a safe read and does not restart
          anything.
        </p>
        <Button
          variant="danger"
          icon="power"
          busy={busyKey === "all"}
          disabled={busyKey !== "" && busyKey !== "all"}
          data-testid="restart-all"
          onClick={() =>
            setConfirm({
              title: "Restart every platform component?",
              body: `This rolls all ${components.length} components reported above at the same time. In-flight MCP calls may fail until the rollout completes.`,
              confirmLabel: "Restart all components",
              destructive: true,
              onConfirm: () => restart("all", "All components"),
            })
          }
        >
          Restart all
        </Button>
      </section>

      {confirm ? (
        <ConfirmDialog
          {...confirm}
          busy={busyKey !== ""}
          onCancel={() => setConfirm(null)}
          testId="platform-confirm"
          confirmTestId="platform-confirm-yes"
          cancelTestId="platform-confirm-cancel"
        />
      ) : null}
    </>
  );
}
