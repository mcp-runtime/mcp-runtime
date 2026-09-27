import { describe, expect, it } from "vitest";

import { serverPromptDetails, serverResourceDetails, serverTaskDetails, type ServerSummary } from "./types";

const baseServer: ServerSummary = {
  name: "assistant",
  namespace: "team-a",
  ready: "1/1",
  status: "Running",
};

describe("server protocol inventory", () => {
  it("merges declared and live prompt metadata by name without losing labels", () => {
    const server: ServerSummary = {
      ...baseServer,
      prompts: [
        { name: "summarize", description: "Declared description", labels: { kind: "writing" } },
        { name: "handoff_note", description: "Write a handoff note" },
      ],
      liveInventory: {
        prompts: [
          { name: "summarize", description: "Live description", arguments: [{ name: "topic", required: true }] },
          { name: "hello", description: "Say hello" },
          { name: "  " },
        ],
      },
    };

    expect(serverPromptDetails(server)).toEqual([
      { name: "handoff_note", description: "Write a handoff note", source: "declared" },
      { name: "hello", description: "Say hello", source: "live", arguments: undefined, uri: undefined, mimeType: undefined },
      { name: "summarize", description: "Live description", labels: { kind: "writing" }, source: "both", arguments: [{ name: "topic", required: true }], uri: undefined, mimeType: undefined },
    ]);
    expect(server.prompts?.[0].description).toBe("Declared description");
  });

  it("keeps declared resource descriptions and uses a URI when a live resource has no name", () => {
    const server: ServerSummary = {
      ...baseServer,
      resources: [{ name: "readme", description: "Project README" }],
      liveInventory: {
        resources: [
          { name: "readme", uri: "file:///README.md", mimeType: "text/markdown" },
          { uri: "file:///guide.md", description: "Guide" },
        ],
      },
    };

    expect(serverResourceDetails(server)).toEqual([
      { name: "file:///guide.md", description: "Guide", source: "live", arguments: undefined, uri: "file:///guide.md", mimeType: undefined },
      { name: "readme", description: "Project README", source: "both", arguments: undefined, uri: "file:///README.md", mimeType: "text/markdown" },
    ]);
  });

  it("shows declared tasks when the live inventory is unavailable", () => {
    const server: ServerSummary = {
      ...baseServer,
      tasks: [{ name: "create_task", description: "Create a task" }],
      liveInventory: null,
    };

    expect(serverTaskDetails(server)).toEqual([
      { name: "create_task", description: "Create a task", source: "declared" },
    ]);
  });
});
