import { uiPath, docsPath } from "../api/config";
import { useEffect, useRef, useState, type ReactNode } from "react";

import { visibleWorkspaceTabs, type WorkspaceId } from "./WorkspaceNavigation";
import { AccountMenu } from "./AccountMenu";
import { Icon } from "../ui/Icon";
import { ADMIN_GROUPS, ADMIN_SECTIONS, adminSection, type AdminSectionId } from "./admin/adminSections";
import { isAdmin } from "../api/types";
import type { AuthStatus } from "../api/types";

type AppShellProps = {
  auth: AuthStatus;
  authBusy: boolean;
  theme: "dark" | "light";
  onToggleTheme: () => void;
  workspace: WorkspaceId;
  onSelectWorkspace: (id: WorkspaceId) => void;
  section: string;
  onSelectAdminSection: (section: AdminSectionId) => void;
  onSignIn: () => void;
  onSignOut: () => void;
  children: ReactNode;
};

export function AppShell({
  auth,
  authBusy,
  theme,
  onToggleTheme,
  workspace,
  onSelectWorkspace,
  section,
  onSelectAdminSection,
  onSignIn,
  onSignOut,
  children,
}: AppShellProps) {
  const [menuOpen, setMenuOpen] = useState(false);
  const [navFilter, setNavFilter] = useState("");
  const menuToggleRef = useRef<HTMLButtonElement>(null);
  const tabs = visibleWorkspaceTabs(auth);
  const currentLabel = workspace === "admin" ? adminSection(section).label :
    tabs.find((tab) => tab.id === workspace)?.label || "Sign in";
  type NavItem = { id: string; label: string; icon: Parameters<typeof Icon>[0]["name"];
    description: string; active: boolean; select: () => void };
  const groups: { label: string; items: NavItem[] }[] = [
    { label: "Runtime", ids: ["servers", "agents", "access"] },
    { label: "Workspace", ids: ["activity", "keys"] },
  ].map((group) => ({ label: group.label, items: tabs.filter((tab) => group.ids.includes(tab.id))
    .map((tab) => ({ ...tab, active: tab.id === workspace, select: () => onSelectWorkspace(tab.id) })) }));
  if (isAdmin(auth)) {
    const icons = { teams: "users", operations: "activity", platform: "gauge", analytics: "chart" } as const;
    groups.push(...ADMIN_GROUPS.map((label) => ({ label, items: ADMIN_SECTIONS
      .filter((item) => item.group === label).map((item) => ({ ...item, id: `admin-${item.id}`,
        icon: icons[item.id], active: workspace === "admin" && adminSection(section).id === item.id,
        select: () => onSelectAdminSection(item.id) })) })));
  }
  const query = navFilter.trim().toLowerCase();
  const filteredGroups = groups.map((group) => ({ ...group, items: group.items.filter((item) =>
    `${group.label} ${item.label}`.toLowerCase().includes(query)) })).filter((group) => group.items.length);
  const nextTheme = theme === "dark" ? "light" : "dark";
  // The compact menu is a navigation affordance, not state worth keeping: any
  // route change closes it.
  useEffect(() => {
    setMenuOpen(false);
  }, [workspace, section]);

  useEffect(() => {
    if (!menuOpen) return;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setMenuOpen(false);
        menuToggleRef.current?.focus();
      }
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [menuOpen]);

  function navItem(item: NavItem, compact = false) {
    const testId = item.id.startsWith("admin-") ? `admin-section-${item.id.slice(6)}` : `workspace-tab-${item.id}`;
    return <li key={item.id}>
      <button type="button" className="nav-item" aria-current={item.active ? "page" : undefined}
        title={item.description} data-testid={compact ? (item.id.startsWith("admin-") ? `mobile-${testId}` : `mobile-tab-${item.id}`) : testId}
        onClick={() => { setMenuOpen(false); item.select(); }}>
        <Icon name={item.icon} size={15} />{item.label}
      </button>
    </li>;
  }

  return (
    <div className="shell">
      <a className="skip-link" href="#workspace-content">
        Skip to content
      </a>

      <aside className="sidebar" aria-label="Console sidebar">
        <div className="sidebar-brand">
          <img className={`brand-logo${theme === "dark" ? " brand-logo-dark" : ""}`}
            src={uiPath(theme === "dark" ? "/brand/mcp-runtime-logo-dark.png" : "/brand/mcp-runtime-logo.png")}
            alt="MCP Runtime" />
          <span className="console-tag">Console</span>
        </div>
        <div className="sidebar-context">
          <span className="sidebar-context-icon"><Icon name="server" size={18} /></span>
          <div><strong>MCP Runtime</strong><span>Control plane</span></div>
        </div>
        <label className="sidebar-filter">
          <Icon name="search" size={14} />
          <input type="search" aria-label="Filter navigation" placeholder="Filter navigation…"
            value={navFilter} onChange={(event) => setNavFilter(event.target.value)} />
        </label>
        <nav className="primary-nav" aria-label="Primary">
          {filteredGroups.map((group) => <div className="sidebar-group" key={group.label}>
            <h2>{group.label}</h2>
            <ul className="primary-nav-list">{group.items.map((item) => navItem(item))}</ul>
          </div>)}
          {!filteredGroups.length ? <p className="sidebar-empty" role="status">No matching pages.</p> : null}

        </nav>
        <div className="sidebar-footer">
          <a className="sidebar-docs" href={docsPath()} target="_blank"
            rel="noreferrer" aria-label="Documentation (opens in a new tab)" data-testid="docs-link">
            <Icon name="book" size={15} /> Documentation <Icon name="external" size={12} />
          </a>
        </div>
      </aside>

      <header className="topbar">
        <div className="topbar-inner">
          <span className="brand mobile-brand">
            <img
              className={`brand-logo${theme === "dark" ? " brand-logo-dark" : ""}`}
              src={uiPath(theme === "dark" ? "/brand/mcp-runtime-logo-dark.png" : "/brand/mcp-runtime-logo.png")}
              alt="MCP Runtime"
            />
          </span>

          <div className="console-breadcrumb" aria-label="Current workspace">
            <Icon name="gauge" size={14} /><span>Console</span>
            <Icon name="chevronRight" size={12} /><strong>{currentLabel}</strong>
          </div>

          <div className="topbar-actions">
            <span className={`session-indicator${auth.authenticated ? " is-authenticated" : ""}`}>
              <span aria-hidden="true" />{auth.authenticated ? "Authenticated" : "Public access"}
            </span>
            <AccountMenu
              auth={auth}
              authBusy={authBusy}
              workspace={workspace}
              onSignIn={onSignIn}
              onSignOut={onSignOut}
              onOpenCatalog={() => onSelectWorkspace("servers")}
            />

            <span className="topbar-divider" aria-hidden="true" />

            <button
              type="button"
              className="icon-btn"
              aria-label={`Switch to ${nextTheme} mode`}
              aria-pressed={theme === "light"}
              title={theme === "dark" ? "Dark" : "Light"}
              onClick={onToggleTheme}
              data-testid="theme-toggle"
            >
              <Icon name={theme === "dark" ? "moon" : "sun"} />
              <span className="visually-hidden">{theme === "dark" ? "Dark" : "Light"}</span>
            </button>

            <button
              type="button"
              ref={menuToggleRef}
              aria-label={menuOpen ? "Close navigation menu" : "Open navigation menu"}
              className="icon-btn nav-toggle"
              aria-expanded={menuOpen}
              aria-controls="mobile-nav"
              data-testid="nav-toggle"
              onClick={() => setMenuOpen((open) => !open)}
            >
              <Icon name={menuOpen ? "close" : "menu"} />
            </button>
          </div>
        </div>
      </header>

      <nav
        className={menuOpen ? "mobile-nav is-open" : "mobile-nav"}
        id="mobile-nav"
        aria-label="Primary (compact)"
        hidden={!menuOpen}
      >
        {groups.map((group) => <div className="sidebar-group" key={group.label}>
          <h2>{group.label}</h2>
          <ul className="mobile-nav-list">{group.items.map((item) => navItem(item, true))}</ul>
        </div>)}
        <a className="sidebar-docs" href={docsPath()} target="_blank" rel="noreferrer">
          <Icon name="book" size={15} /> Documentation <Icon name="external" size={12} />
          <span className="visually-hidden"> (opens in a new tab)</span>
        </a>
      </nav>

      <main className="main" id="workspace-content" tabIndex={-1}>
        {children}
      </main>
    </div>
  );
}
