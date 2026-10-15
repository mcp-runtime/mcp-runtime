import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { AccessControlPanel, type AccessSelection } from "../admin/AccessControlPanel";
import { AccessDetail } from "../admin/AccessDetail";
import { Button } from "../../ui/Button";
import { PageHeader } from "../../ui/PageHeader";
import { EmptyState, ErrorState, LoadingState } from "../../ui/States";
import { getAgent, listGrants, listSessions } from "../../api/admin";
import { ADMIN_QUERY_KEY } from "../../hooks/useAdminData";
import { listNamespaces } from "../../api/catalog";
import { isAdmin, type AuthStatus } from "../../api/types";
import { CATALOG_QUERY_KEY } from "../../hooks/useCatalog";
import { useTeams } from "../../hooks/useAdminData";

type AccessWorkspaceProps = {
  auth: AuthStatus;
  onSignIn: () => void;
  params?: Record<string, string>;
  onClearReference?: () => void;
};

// Grants and agent sessions are any authenticated user's territory, not
// admin-only - the backend registers /runtime/grants and /runtime/sessions
// with its plain auth() middleware, not adminOnly().
export function AccessWorkspace({ auth, onSignIn, params = {}, onClearReference }: AccessWorkspaceProps) {
  const [namespace, setNamespace] = useState("");
  const [selection, setSelection] = useState<AccessSelection | null>(null);
  const linked = (params.kind === "grant" || params.kind === "session") && Boolean(params.namespace && params.name);
  const linkedQuery = useQuery({
    queryKey: [ADMIN_QUERY_KEY, "access-reference", params.kind, params.namespace, params.name, params.agent],
    enabled: auth.authenticated && linked,
    queryFn: async (): Promise<AccessSelection | null> => {
      const agent = params.agent ? await getAgent(params.agent) : null;
      if (params.kind === "grant") {
        const item = (agent?.grants ?? await listGrants(params.namespace)).find((row) => row.name === params.name && row.namespace === params.namespace);
        return item ? { kind: "grant", item } : null;
      }
      const item = (agent?.sessions ?? await listSessions(params.namespace)).find((row) => row.name === params.name && row.namespace === params.namespace);
      return item ? { kind: "session", item } : null;
    },
  });
  const teamsQuery = useTeams(auth.authenticated);
  const canManage = isAdmin(auth) || teamsQuery.data?.some((team) => team.namespace === namespace && team.role === "owner") === true;

  // Shares ServersWorkspace's query cache/key, so no extra request when that
  // catalog read has already happened this session.
  const namespacesQuery = useQuery({
    queryKey: [CATALOG_QUERY_KEY, "namespaces"],
    queryFn: listNamespaces,
    enabled: auth.authenticated,
  });

  // The runtime API resolves an empty namespace by role
  // (services/runtime-api/internal/runtimeapi/subject_binding.go,
  // scopedNamespaceForPrincipal): admin gets every namespace cluster-wide,
  // but a non-admin with no team namespace gets a 403 rather than a default
  // scope. There is no such thing as a non-admin who legitimately wants an
  // empty namespace, so this re-applies every time namespace goes back to ""
  // - not just once - otherwise clearing the filter strands them on a scope
  // that 403s both tables.
  useEffect(() => {
    if (isAdmin(auth) || namespace !== "") {
      return;
    }
    const first = namespacesQuery.data?.[0]?.namespace;
    if (first) {
      setNamespace(first);
    }
  }, [auth, namespace, namespacesQuery.data]);

  if (!auth.authenticated) {
    return (
      <>
        <PageHeader title="Access control" />
        <EmptyState
          icon="shield"
          title="Sign in to view access control."
          detail="Grants and agent sessions enforced by the MCP gateway are visible to any signed-in account."
          testId="access-signed-out"
          action={
            <Button variant="primary" icon="login" onClick={onSignIn}>
              Sign in
            </Button>
          }
        />
      </>
    );
  }

  if (linked && linkedQuery.isPending) return <LoadingState label="Loading access record…" />;
  if (linked && (linkedQuery.error || !linkedQuery.data)) return <ErrorState title="Access record is unavailable." detail="It may have been removed or may be outside your permitted scope." onRetry={() => void linkedQuery.refetch()} />;
  const activeSelection = linked ? linkedQuery.data : selection;
  if (activeSelection) {
    return (
      <AccessDetail
        selection={activeSelection}
        onBack={() => { setSelection(null); onClearReference?.(); }}
        sectionLabel="Access control"
      />
    );
  }

  // A non-admin with an empty namespace must wait for the default above to
  // settle before AccessControlPanel (and its grants/sessions queries) ever
  // mounts, otherwise the first request goes out empty, 403s, and immediately
  // refires once corrected. Give up and proceed once the namespaces read has
  // resolved with nothing to default to, so a principal with zero visible
  // namespaces still reaches a rendered (if empty) state.
  const hasDefaultToApply =
    namespacesQuery.isSuccess && Boolean(namespacesQuery.data[0]?.namespace);
  const waitingForNamespaceDefault =
    !isAdmin(auth) && namespace === "" && (namespacesQuery.isPending || hasDefaultToApply);
  if (waitingForNamespaceDefault) {
    return (
      <>
        <PageHeader title="Access control" />
        <LoadingState label="Loading access control…" testId="access-loading" />
      </>
    );
  }

  return (
    <AccessControlPanel
      namespace={namespace}
      onNamespaceChange={setNamespace}
      onSelect={setSelection}
      onSignIn={onSignIn}
      canManage={canManage}
    />
  );
}
