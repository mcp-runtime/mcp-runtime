import { useCallback, useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { AppShell } from "./components/AppShell";
import { ActivityWorkspace } from "./components/user/ActivityWorkspace";
import { ApiKeysWorkspace } from "./components/user/ApiKeysWorkspace";
import { SignInPanel } from "./components/SignInPanel";
import { ServersWorkspace } from "./components/servers/ServersWorkspace";
import { AdminWorkspace } from "./components/admin/AdminWorkspace";
import { AgentsPanel } from "./components/admin/AgentsPanel";
import { AccessWorkspace } from "./components/access/AccessWorkspace";
import { adminSection, type AdminSectionId } from "./components/admin/adminSections";
import { visibleWorkspaceTabs } from "./components/WorkspaceNavigation";
import { useHashRoute } from "./routing/useHashRoute";
import type { WorkspaceId } from "./routing/route";
import { login, logout, readAuthStatus, type LoginInput } from "./api/auth";
import { isAdmin, type AuthStatus } from "./api/types";

type ThemeMode = "dark" | "light";
const THEME_STORAGE_KEY = "mcp-sentinel-theme";

function initialTheme(): ThemeMode {
  try {
    return window.localStorage.getItem(THEME_STORAGE_KEY) === "light" ? "light" : "dark";
  } catch {
    return "dark";
  }
}

function loginErrorMessage(err: unknown): string {
  const raw = err instanceof Error ? err.message : "";
  if (!raw || raw.includes("unauthorized") || raw.includes("401")) {
    return "That email, password, or API key was not accepted.";
  }
  if (raw.includes("too_many_requests")) {
    return "Too many sign-in attempts. Wait a moment and try again.";
  }
  if (raw.includes("missing_credentials")) {
    return "Enter an email and password, or an API key.";
  }
  return "Sign-in failed. Try again.";
}

export function App() {
  const queryClient = useQueryClient();
  const { route, navigate, setParams } = useHashRoute();
  const [auth, setAuth] = useState<AuthStatus>({ authenticated: false });
  const [authReady, setAuthReady] = useState(false);
  const [authBusy, setAuthBusy] = useState(false);
  const [loginError, setLoginError] = useState("");
  const [theme, setTheme] = useState<ThemeMode>(initialTheme);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      window.localStorage.setItem(THEME_STORAGE_KEY, theme);
    } catch {
      // The theme still applies when storage is unavailable (for example in a
      // locked-down browser profile); persistence is a progressive enhancement.
    }
  }, [theme]);

  useEffect(() => {
    let cancelled = false;
    readAuthStatus()
      .then((status) => {
        if (!cancelled) {
          setAuth(status);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setAuth({ authenticated: false });
        }
      })
      .finally(() => {
        if (!cancelled) {
          setAuthReady(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const allowedTabs = useMemo(() => visibleWorkspaceTabs(auth), [auth]);

  // A workspace that role gating no longer permits must not stay rendered, and
  // a shared deep link into one must not either. Both fall back to Servers.
  useEffect(() => {
    if (!authReady || route.workspace === "signin") {
      return;
    }
    const allowed = allowedTabs.some((tab) => tab.id === route.workspace);
    if (!allowed) {
      navigate({ workspace: "servers" }, { replace: true });
    }
  }, [authReady, allowedTabs, route.workspace, navigate]);

  // Second line of defence behind AdminGuard.
  useEffect(() => {
    if (authReady && route.workspace === "admin" && !isAdmin(auth)) {
      navigate({ workspace: "servers" }, { replace: true });
    }
  }, [authReady, route.workspace, auth, navigate]);

  const handleSignIn = useCallback(() => {
    setLoginError("");
    queryClient.clear();
    setAuth({ authenticated: false });
    navigate({ workspace: "signin" });
  }, [queryClient, navigate]);

  const handleSubmit = useCallback(
    async (input: LoginInput) => {
      setAuthBusy(true);
      setLoginError("");
      try {
        const status = await login(input);
        queryClient.clear();
        setAuth(status);
        navigate({ workspace: "servers" }, { replace: true });
      } catch (err) {
        setLoginError(loginErrorMessage(err));
      } finally {
        setAuthBusy(false);
      }
    },
    [queryClient, navigate]
  );

  const handleSignOut = useCallback(async () => {
    setAuthBusy(true);
    try {
      await logout();
    } finally {
      queryClient.clear();
      setAuth({ authenticated: false });
      navigate({ workspace: "servers" }, { replace: true });
      setAuthBusy(false);
    }
  }, [queryClient, navigate]);

  const selectWorkspace = useCallback(
    (id: WorkspaceId) => {
      navigate({ workspace: id });
    },
    [navigate]
  );

  let content;
  if (!authReady) {
    content = (
      <div className="state state-loading" role="status" aria-live="polite">
        <span className="skeleton" style={{ width: 18, height: 18, borderRadius: "50%" }} />
        <p className="state-title">Checking your session…</p>
      </div>
    );
  } else if (route.workspace === "signin") {
    content = (
      <SignInPanel
        onSubmit={handleSubmit}
        onCancel={() => navigate({ workspace: "servers" })}
        error={loginError}
        busy={authBusy}
      />
    );
  } else if (route.workspace === "admin") {
    content = (
      <AdminWorkspace
        auth={auth}
        onSignIn={handleSignIn}
        section={adminSection(route.section).id}
        onSectionChange={(section: AdminSectionId) => navigate({ workspace: "admin", section })}
      />
    );
  } else if (route.workspace === "agents") {
    content = <AgentsPanel auth={auth} onSignIn={handleSignIn} initialTeam={route.params.team} initialAgent={route.params.agent} onTeamChange={(team) => setParams({ team })} onAgentChange={(agent) => setParams({ agent })} />;
  } else if (route.workspace === "access") {
    content = <AccessWorkspace auth={auth} onSignIn={handleSignIn} />;
  } else if (route.workspace === "activity") {
    content = <ActivityWorkspace auth={auth} onSignIn={handleSignIn} />;
  } else if (route.workspace === "keys") {
    content = <ApiKeysWorkspace auth={auth} onSignIn={handleSignIn} />;
  } else {
    content = (
      <ServersWorkspace
        auth={auth}
        onSignIn={handleSignIn}
        params={route.params}
        onParamsChange={setParams}
      />
    );
  }

  return (
    <AppShell
      auth={auth}
      authBusy={authBusy}
      theme={theme}
      onToggleTheme={() => setTheme((current) => (current === "dark" ? "light" : "dark"))}
      workspace={route.workspace}
      onSelectWorkspace={selectWorkspace}
      onSignIn={handleSignIn}
      onSignOut={handleSignOut}
    >
      {content}
    </AppShell>
  );
}
