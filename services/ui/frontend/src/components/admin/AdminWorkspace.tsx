import { useCallback, useState } from "react";

import { AdminGuard } from "./AdminGuard";
import { ADMIN_GROUPS, ADMIN_SECTIONS, adminSection, type AdminSectionId } from "./adminSections";
import { OperationsPanel } from "./OperationsPanel";
import { PlatformHealthPanel } from "./PlatformHealthPanel";
import { TeamsPanel } from "./TeamsPanel";
import { UsageAnalyticsPanel } from "./UsageAnalyticsPanel";
import { SelectField } from "../../ui/Field";
import type { AuthStatus } from "../../api/types";

export type { AdminSectionId };

type AdminWorkspaceProps = {
  auth: AuthStatus;
  onSignIn: () => void;
  // Supplied by the shell so the section is part of the URL. Standalone
  // renders fall back to local state.
  section?: AdminSectionId;
  onSectionChange?: (section: AdminSectionId) => void;
};

export function AdminWorkspace({ auth, onSignIn, section, onSectionChange }: AdminWorkspaceProps) {
  const [localSection, setLocalSection] = useState<AdminSectionId>("teams");
  const active = section ?? localSection;

  const select = useCallback(
    (next: AdminSectionId) => {
      if (onSectionChange) {
        onSectionChange(next);
      } else {
        setLocalSection(next);
      }
    },
    [onSectionChange]
  );

  return (
    <AdminGuard auth={auth} onSignIn={onSignIn}>
      <div className="admin-layout">
        <nav className="admin-rail" aria-label="Administration sections">
          {ADMIN_GROUPS.map((group) => {
            const items = ADMIN_SECTIONS.filter((item) => item.group === group);
            if (items.length === 0) {
              return null;
            }
            return (
              <div className="admin-rail-group" key={group}>
                <h2>{group}</h2>
                <ul className="admin-rail-list">
                  {items.map((item) => (
                    <li key={item.id}>
                      <button
                        type="button"
                        className="admin-rail-item"
                        aria-current={item.id === active ? "page" : undefined}
                        data-testid={`admin-section-${item.id}`}
                        onClick={() => select(item.id)}
                      >
                        {item.label}
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            );
          })}
        </nav>

        <div className="admin-section-select">
          <SelectField
            label="Administration section"
            value={active}
            data-testid="admin-section-select"
            options={ADMIN_SECTIONS.map((item) => ({
              value: item.id,
              label: `${item.group} · ${item.label}`,
            }))}
            onChange={(event) => select(event.target.value as AdminSectionId)}
          />
        </div>

        <div>
          {active === "teams" ? (
            <TeamsPanel onSignIn={onSignIn} />
          ) : active === "operations" ? (
            <OperationsPanel onSignIn={onSignIn} />
          ) : active === "analytics" ? (
            <UsageAnalyticsPanel onSignIn={onSignIn} />
          ) : (
            <PlatformHealthPanel onSignIn={onSignIn} />
          )}
        </div>
      </div>
    </AdminGuard>
  );
}
