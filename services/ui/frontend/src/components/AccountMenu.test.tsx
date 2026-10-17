import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AppProviders } from "../providers/AppProviders";
import type { AuthStatus } from "../api/types";
import { AccountMenu } from "./AccountMenu";

const ADMIN: AuthStatus = {
  authenticated: true,
  principal: { role: "admin", email: "admin@example.test", auth_type: "platform_jwt" },
};

describe("AccountMenu", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("keeps the account popover focused on identity and does not fetch the server catalog", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    render(
      <AppProviders>
        <AccountMenu
          auth={ADMIN}
          authBusy={false}
          workspace="servers"
          onSignIn={vi.fn()}
          onSignOut={vi.fn()}
        />
      </AppProviders>
    );

    await user.click(screen.getByTestId("account-trigger"));

    expect(screen.getAllByText("admin@example.test")).toHaveLength(2);
    expect(screen.getByText("Administrator")).toBeInTheDocument();
    expect(screen.getByTestId("logout-button")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "My workspace" })).toHaveAttribute("href", "#/workspace");
    expect(screen.queryByText("Deployed MCP servers")).not.toBeInTheDocument();
    expect(screen.queryByText("Platform jwt")).not.toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
