import { AdminGuard } from "./AdminGuard";
import { type AdminSectionId } from "./adminSections";
import { OperationsPanel } from "./OperationsPanel";
import { PlatformHealthPanel } from "./PlatformHealthPanel";
import { TeamsPanel } from "./TeamsPanel";
import { UsageAnalyticsPanel } from "./UsageAnalyticsPanel";
import type { AuthStatus } from "../../api/types";

export type { AdminSectionId };

type AdminWorkspaceProps = {
  auth: AuthStatus;
  onSignIn: () => void;
  // Navigation lives in the application sidebar; this renders only content.
  section?: AdminSectionId;
};

export function AdminWorkspace({ auth, onSignIn, section }: AdminWorkspaceProps) {
  const active = section ?? "teams";
  return (
    <AdminGuard auth={auth} onSignIn={onSignIn}>
      {active === "teams" ? <TeamsPanel onSignIn={onSignIn} /> :
        active === "operations" ? <OperationsPanel onSignIn={onSignIn} /> :
        active === "analytics" ? <UsageAnalyticsPanel onSignIn={onSignIn} /> :
        <PlatformHealthPanel onSignIn={onSignIn} />}
    </AdminGuard>
  );
}
