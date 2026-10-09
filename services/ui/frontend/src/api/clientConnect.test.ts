import { describe, expect, it } from "vitest";

import { clientConnectSnippet, serverAuthInfo, type ServerSummary } from "./types";

const headerServer = {
  name: "example",
  namespace: "mcp-team-tools",
  ready: "1/1",
  status: "Ready",
  endpoint: "https://mcp.example.com/example/mcp",
  authMode: "header",
  authHeaders: ["X-Example-Credential", "Private-Token"],
  credentialPresence: "any",
  access_json: {
    mcpServers: {
      example: {
        type: "http",
        url: "https://mcp.example.com/example/mcp",
        headers: {
          "X-Example-Credential": "",
          "Private-Token": "",
        },
      },
    },
  },
} satisfies ServerSummary;

describe("header auth connect config", () => {
  it("copies configured header names into Claude and VS Code snippets", () => {
    const claude = clientConnectSnippet(headerServer, "mcpServers") as {
      mcpServers: { example: { headers: Record<string, string> } };
    };
    expect(claude.mcpServers.example.headers["X-Example-Credential"]).toBe("");
    expect(claude.mcpServers.example.headers["Private-Token"]).toBe("");

    const vscode = clientConnectSnippet(headerServer, "servers") as {
      servers: { example: { headers: Record<string, string> } };
    };
    expect(vscode.servers.example.headers["Private-Token"]).toBe("");
  });

  it("tells the caller to remove empty headers when any one credential is enough", () => {
    const auth = serverAuthInfo(headerServer);
    expect(auth.label).toBe("Header");
    expect(auth.detail).toContain("X-Example-Credential");
    expect(auth.detail).toContain("delete the other empty headers");
  });

  it("keeps OAuth servers free of a header requirement", () => {
    expect(serverAuthInfo().label).toBe("OAuth optional");
  });
});
