import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { UsageSeries } from "./UsageSeries";
it("labels both time and rate axes and plots rates rather than bucket counts", () => {
  render(
    <UsageSeries
      points={[
        {
          bucket: "2026-10-14T10:00:00Z",
          events: 7200,
          allowed: 7200,
          denied: 0,
          requests_per_second: 2,
        },
        {
          bucket: "2026-10-14T11:00:00Z",
          events: 0,
          allowed: 0,
          denied: 0,
          requests_per_second: 0,
        },
      ]}
    />,
  );
  expect(screen.getByText("Time (local)")).toBeInTheDocument();
  expect(screen.getAllByText("Requests / second")).toHaveLength(2);
  expect(screen.getByRole("img")).toHaveAccessibleName(
    "2 time buckets; peak 2 requests per second",
  );
});
