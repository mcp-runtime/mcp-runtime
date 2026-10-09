import { uiPath, docsPath } from "../api/config";
import { useEffect, useRef, useState, type ReactNode } from "react";

import { visibleWorkspaceTabs, type WorkspaceId } from "./WorkspaceNavigation";
import { AccountMenu } from "./AccountMenu";
import { Icon } from "../ui/Icon";
import type { AuthStatus } from "../api/types";

type AppShellProps = {
  auth: AuthStatus;
  authBusy: boolean;
  theme: "dark" | "light";
  onToggleTheme: () => void;
  workspace: WorkspaceId;
  onSelectWorkspace: (id: WorkspaceId) => void;
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
  onSignIn,
  onSignOut,
  children,
}: AppShellProps) {
  const [menuOpen, setMenuOpen] = useState(false);
  const [navFilter, setNavFilter] = useState("");
  const menuToggleRef = useRef<HTMLButtonElement>(null);
  const tabs = visibleWorkspaceTabs(auth);
  const currentLabel = tabs.find((tab) => tab.id === workspace)?.label || "Sign in";
  const groups = [
    { label: "Runtime", ids: ["servers", "agents", "access"] },
    { label: "Workspace", ids: ["activity", "keys"] },
    { label: "Platform", ids: ["admin"] },
  ];
  const nextTheme = theme === "dark" ? "light" : "dark";
  // The compact menu is a navigation affordance, not state worth keeping: any
  // route change closes it.
  useEffect(() => {
    setMenuOpen(false);
  }, [workspace]);

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

  function navItem(id: WorkspaceId, label: string, icon: Parameters<typeof Icon>[0]["name"], title: string) {
    const active = id === workspace;
    return (
      <li key={id}>
        <button
          type="button"
          className="nav-item"
          aria-current={active ? "page" : undefined}
          title={title}
          data-testid={`workspace-tab-${id}`}
          onClick={() => { setMenuOpen(false); onSelectWorkspace(id); }}
        >
          <Icon name={icon} size={15} />
          {label}
        </button>
      </li>
    );
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
          {groups.map((group) => {
            const items = tabs.filter((tab) => group.ids.includes(tab.id) &&
              tab.label.toLowerCase().includes(navFilter.trim().toLowerCase()));
            if (!items.length) return null;
            return <div className="sidebar-group" key={group.label}>
              <h2>{group.label}</h2>
              <ul className="primary-nav-list">
                {items.map((tab) => navItem(tab.id, tab.label, tab.icon, tab.description))}
              </ul>
            </div>;
          })}
          {!tabs.some((tab) => tab.label.toLowerCase().includes(navFilter.trim().toLowerCase())) ?
            <p className="sidebar-empty" role="status">No matching pages.</p> : null}
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
        <ul className="mobile-nav-list">
          {tabs.map((tab) => (
            <li key={tab.id}>
              <button
                type="button"
                className="nav-item"
                aria-current={tab.id === workspace ? "page" : undefined}
                data-testid={`mobile-tab-${tab.id}`}
                onClick={() => { setMenuOpen(false); onSelectWorkspace(tab.id); }}
              >
                <Icon name={tab.icon} size={15} />
                {tab.label}
              </button>
            </li>
          ))}
        </ul>
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
