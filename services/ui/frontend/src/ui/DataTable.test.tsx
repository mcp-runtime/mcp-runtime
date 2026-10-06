import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { buildColumns, DataTable } from "./DataTable";

describe("DataTable column layout", () => {
  const rows = [
    { name: "workspace-cursor", subject: "agent-" + "a".repeat(120) },
    { name: "another-grant", subject: "human-123" },
  ];

  it("shares widths between headers and rows without losing long values or sorting", async () => {
    const user = userEvent.setup();
    render(<DataTable
      columns={buildColumns<typeof rows[number]>([
        { id: "name", header: "Grant", width: "40%", rowHeader: true, sortValue: r => r.name, cell: r => r.name },
        { id: "subject", header: "Subject", width: "60%", cell: r => r.subject },
      ])}
      rows={rows}
      rowKey={r => r.name}
      minWidth={1100}
      caption="Access grants"
      regionLabel="Access grants"
      emptyMessage="No grants"
      testId="layout-table"
    />);
    const table = screen.getByRole("table");
    expect(table).toHaveClass("data-table-fixed");
    expect(table).toHaveStyle({ minWidth: "1100px" });
    expect([...table.querySelectorAll("col")].map(col => col.style.width)).toEqual(["40%", "60%"]);
    expect(within(table).getByText(rows[0].subject)).toBeVisible();
    await user.click(screen.getByTestId("layout-table-sort-name"));
    expect(screen.getAllByRole("rowheader")[0]).toHaveTextContent("another-grant");
    expect(screen.getByRole("columnheader", { name: /Grant/ })).toHaveAttribute("aria-sort", "ascending");
  });

  it("keeps automatic layout for callers without column widths", () => {
    render(<DataTable
      columns={buildColumns<typeof rows[number]>([
        { id: "name", header: "Grant", cell: r => r.name },
      ])}
      rows={[]}
      rowKey={r => r.name}
      caption="Empty grants"
      regionLabel="Empty grants"
      emptyMessage="No grants"
      testId="empty-layout"
    />);
    expect(screen.getByRole("table")).not.toHaveClass("data-table-fixed");
    expect(screen.getByText("No grants")).toHaveAttribute("colspan", "1");
  });
});
