import { useEffect, useRef, useState } from "react";

import type { AuthStatus } from "../api/types";
import { Button } from "../ui/Button";
import { Icon } from "../ui/Icon";

type AccountMenuProps = {
  auth: AuthStatus;
  authBusy: boolean;
  workspace: string;
  onSignIn: () => void;
  onSignOut: () => void;
};

function accountIdentity(auth: AuthStatus): string {
  const email = auth.principal?.email?.trim();
  if (email) return email;
  const subject = auth.principal?.subject?.trim();
  if (subject) return subject;
  return auth.principal?.auth_type === "ui_api_key" ? "Platform API key" : "Signed-in account";
}

function accountRole(auth: AuthStatus): string {
  const role = auth.principal?.role?.trim().toLowerCase();
  if (role === "admin") return "Administrator";
  if (role === "user") return "User";
  if (!role) return "Authenticated";
  return role.replace(/[_-]+/g, " ").replace(/^\w/, (letter) => letter.toUpperCase());
}

export function AccountMenu({
  auth,
  authBusy,
  workspace,
  onSignIn,
  onSignOut,
}: AccountMenuProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const authenticated = auth.authenticated === true;
  const identity = accountIdentity(auth);
  const role = accountRole(auth);

  useEffect(() => {
    setOpen(false);
  }, [workspace, authenticated]);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(event: PointerEvent) {
      if (event.target instanceof Node && !rootRef.current?.contains(event.target)) {
        setOpen(false);
      }
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setOpen(false);
        triggerRef.current?.focus();
      }
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  return (
    <div className="account-menu" ref={rootRef} data-testid="account-menu">
      <button
        ref={triggerRef}
        type="button"
        className="account-trigger"
        aria-label={open ? "Close account menu" : "Open account menu"}
        aria-expanded={open}
        aria-controls="account-menu-panel"
        data-testid="account-trigger"
        onClick={() => setOpen((current) => !current)}
      >
        <span className="account-avatar" aria-hidden="true">
          <Icon name="user" size={16} />
        </span>
        <span className="account-trigger-label">{authenticated ? identity : "Account"}</span>
        <Icon className="account-trigger-chevron" name="chevronDown" size={14} />
      </button>

      {open ? (
        <section className="account-popover" id="account-menu-panel" aria-label="Account details">
          {authenticated ? (
            <>
              <div className="account-popover-heading">
                <span className="account-popover-eyebrow">Signed in as</span>
                <strong className="account-identity" title={identity}>{identity}</strong>
                <div className="account-badges">
                  <span className={role === "Administrator" ? "account-role-badge is-admin" : "account-role-badge"}>
                    {role}
                  </span>
                </div>
                <p className="account-access-scope">
                  {role === "Administrator" ? "Access across all platform namespaces." : "Access scoped to your namespaces."}
                </p>
              </div>

              <div className="account-popover-footer">
                <a className="btn btn-secondary" href="#/workspace" onClick={() => setOpen(false)}>My workspace</a>
                <Button
                  variant="ghost"
                  size="sm"
                  icon="logout"
                  block
                  disabled={authBusy}
                  onClick={() => {
                    setOpen(false);
                    onSignOut();
                  }}
                  data-testid="logout-button"
                >
                  {authBusy ? "Signing out…" : "Sign out"}
                </Button>
              </div>
            </>
          ) : (
            <div className="account-signed-out">
              <span className="account-avatar account-avatar-large" aria-hidden="true"><Icon name="user" size={19} /></span>
              <h2>Sign in to your account</h2>
              <p>Access your workspace and platform tools.</p>
              <Button data-testid="account-menu-signin" variant="primary" icon="login" block onClick={() => { setOpen(false); onSignIn(); }}>
                Sign in
              </Button>
            </div>
          )}
        </section>
      ) : null}
    </div>
  );
}
