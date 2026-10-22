import { describe, expect, it } from "vitest";

import { serverInstallLinks } from "./client-install";

const server = {
  name: "example",
  endpoint: "http://internal-service:8080/mcp",
  access_json: { mcpServers: { example: { type: "http", url: "https://mcp.example.com/team/example/mcp" } } },
};

describe("server install links", () => {
  it("offers the five client actions using the published public endpoint", () => {
    const links = serverInstallLinks(server);
    expect(links.map((link) => link.id)).toEqual(["claude", "claude-code", "codex", "cursor", "vscode"]);
    const claude = new URL(links[0].href);
    expect(claude.searchParams.get("connectorName")).toBe("example");
    expect(claude.searchParams.get("connectorUrl")).toBe("https://mcp.example.com/team/example/mcp");
    const cursor = new URL(links.find((link) => link.id === "cursor")!.href);
    expect(JSON.parse(atob(cursor.searchParams.get("config")!))).toEqual(server.access_json.mcpServers.example);
    const vscode = new URL(links.find((link) => link.id === "vscode")!.href);
    expect(JSON.parse(decodeURIComponent(vscode.search.slice(1)))).toEqual({ name: "example", ...server.access_json.mcpServers.example });
    expect(links.map((link) => link.href).join(" ")).not.toContain("internal-service");
  });

  it("preserves required headers in native install payloads and setup prompts", () => {
    const entry = { ...server.access_json.mcpServers.example, headers: { "Private-Token": "" } };
    const links = serverInstallLinks({ ...server, access_json: { mcpServers: { example: entry } } });
    // Claude's web connector cannot accept these required headers.
    expect(links.some((link) => link.id === "claude")).toBe(false);
    const cursor = new URL(links.find((link) => link.id === "cursor")!.href);
    expect(JSON.parse(atob(cursor.searchParams.get("config")!)).headers).toEqual(entry.headers);
    const code = new URL(links.find((link) => link.id === "claude-code")!.href);
    expect(code.searchParams.get("q")).toContain("claude mcp add-json");
    expect(code.searchParams.get("q")).toContain('"Private-Token":""');
    const codex = new URL(links.find((link) => link.id === "codex")!.href);
    expect(codex.searchParams.get("prompt")).toContain('"Private-Token": ""');
  });

  it("encodes Unicode and URL query characters without corrupting the config", () => {
    const url = "https://mcp.example.com/日本/mcp?team=a&next=b+c";
    const links = serverInstallLinks({ name: "demo", endpoint: url });
    const cursor = new URL(links.find((link) => link.id === "cursor")!.href);
    const bytes = Uint8Array.from(atob(cursor.searchParams.get("config")!), (char) => char.charCodeAt(0));
    expect(JSON.parse(new TextDecoder().decode(bytes))).toEqual({ type: "http", url });
  });

  it("quotes server names and endpoints in generated shell commands", () => {
    const links = serverInstallLinks({ name: "demo'$(touch nope)", endpoint: "https://mcp.example.com/mcp?x=$(touch%20nope)&y=2" });
    const prompt = new URL(links.find((link) => link.id === "codex")!.href).searchParams.get("prompt");
    expect(prompt).toContain("'demo'\"'\"'$(touch nope)'");
    expect(prompt).toContain("--url 'https://mcp.example.com/mcp?x=$(touch%20nope)&y=2'");
  });

  it.each(["", "javascript:alert(1)", "/relative/mcp", "https://user:password@mcp.example.com/mcp", "file:///tmp/mcp"])("does not offer install links for an unusable endpoint: %s", (endpoint) => {
    expect(serverInstallLinks({ name: "demo", endpoint })).toEqual([]);
  });
});
