import { afterEach, describe, expect, it, vi } from "vitest";

import {
  UnauthorizedError,
  apiURL,
  clearCSRFToken,
  fetchJSON,
  fetchUIJSON,
  setCSRFToken,
  withQuery,
} from "./client";
import { readRuntimeConfig } from "./config";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  clearCSRFToken();
  delete window.MCP_API_BASE;
});

describe("apiURL", () => {
  it("routes allowlisted catalog GETs through the UI session proxy", () => {
    expect(apiURL("/runtime/namespaces", "/api/v1")).toBe("/api/ui/v1/runtime/namespaces");
    expect(apiURL("/runtime/servers?namespace=mcp-servers", "/api/v1")).toBe(
      "/api/ui/v1/runtime/servers?namespace=mcp-servers"
    );
    expect(apiURL("/runtime/tools", "/api/v1")).toBe("/api/ui/v1/runtime/tools");
  });

  it("keeps unmigrated paths on the public API base", () => {
    expect(apiURL("/runtime/policy", "/api/v1")).toBe("/api/v1/runtime/policy");
    expect(apiURL("/runtime/servers/mcp-servers/demo", "/api/v1")).toBe(
      "/api/v1/runtime/servers/mcp-servers/demo"
    );
  });

  it("routes the Phase 4 admin reads through the session proxy", () => {
    for (const path of [
      "/runtime/grants",
      "/runtime/sessions",
      "/runtime/teams",
      "/runtime/components",
      "/admin/operations",
      "/admin/deployments",
      "/events",
    ]) {
      expect(apiURL(path, "/api/v1")).toBe(`/api/ui/v1${path}`);
    }
    expect(apiURL("/admin/operations?user=a%40b.c", "/api/v1")).toBe(
      "/api/ui/v1/admin/operations?user=a%40b.c"
    );
  });

  it("routes only concrete admin writes through the session proxy", () => {
    expect(apiURL("/runtime/grants/mcp-servers/demo", "/api/v1", "PATCH")).toBe(
      "/api/ui/v1/runtime/grants/mcp-servers/demo"
    );
    expect(apiURL("/runtime/sessions/mcp-servers/demo", "/api/v1", "DELETE")).toBe(
      "/api/ui/v1/runtime/sessions/mcp-servers/demo"
    );
    expect(apiURL("/runtime/grants", "/api/v1", "PATCH")).toBe("/api/v1/runtime/grants");
    expect(apiURL("/runtime/grants/mcp-servers/demo/extra", "/api/v1", "PATCH")).toBe(
      "/api/v1/runtime/grants/mcp-servers/demo/extra"
    );
  });

  it("does not route mutations through the GET-only session proxy", () => {
    expect(apiURL("/runtime/servers", "/api/v1", "POST")).toBe("/api/v1/runtime/servers");
  });
});

describe("readRuntimeConfig", () => {
  it("defaults apiBase to /api/v1", () => {
    expect(readRuntimeConfig().apiBase).toBe("/api/v1");
  });
});

describe("fetchJSON", () => {
  it("uses same-origin credentials and never sets an authorization header", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ servers: [] }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchJSON("/runtime/servers?namespace=mcp-servers")).resolves.toEqual({
      servers: [],
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/ui/v1/runtime/servers?namespace=mcp-servers");
    expect(init.credentials).toBe("same-origin");
    const headers = new Headers(init.headers);
    expect(headers.get("authorization")).toBeNull();
    expect(headers.get("x-api-key")).toBeNull();
  });

  it("strips caller-supplied credentials before fetch", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await fetchJSON("/runtime/namespaces", {
      headers: {
        Authorization: "Bearer leaked",
        "x-api-key": "leaked-key",
      },
    });

    const headers = new Headers((fetchMock.mock.calls[0] as [string, RequestInit])[1].headers);
    expect(headers.get("authorization")).toBeNull();
    expect(headers.get("x-api-key")).toBeNull();
  });

  it("throws UnauthorizedError on 401", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 401,
        text: async () => `{"error":"unauthorized"}`,
      })
    );

    await expect(fetchJSON("/runtime/tools")).rejects.toBeInstanceOf(UnauthorizedError);
  });

  it("adds the in-memory CSRF token only to allowed writes", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
    });
    vi.stubGlobal("fetch", fetchMock);
    setCSRFToken("session-csrf");

    await fetchJSON("/runtime/grants/mcp-servers/demo", {
      method: "PATCH",
      headers: { "X-CSRF-Token": "attacker-token", Authorization: "Bearer leaked" },
      body: JSON.stringify({ disabled: true }),
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/ui/v1/runtime/grants/mcp-servers/demo");
    const headers = new Headers(init.headers);
    expect(headers.get("x-csrf-token")).toBe("session-csrf");
    expect(headers.get("authorization")).toBeNull();
  });

  it("routes agent directory and revoke-all writes through the CSRF-protected session proxy", async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({ agent: {} }) });
    vi.stubGlobal("fetch", fetchMock);
    setCSRFToken("session-csrf");

    await fetchJSON("/runtime/agents/agt_01arz3ndektsv4rrffq69g5fav/deactivate", { method: "POST" });
    await fetchJSON("/runtime/grants/mcp-team-a/cross-team/revoke-sessions", { method: "POST" });

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/ui/v1/runtime/agents/agt_01arz3ndektsv4rrffq69g5fav/deactivate",
      "/api/ui/v1/runtime/grants/mcp-team-a/cross-team/revoke-sessions",
    ]);
    for (const [, init] of fetchMock.mock.calls) {
      expect(new Headers((init as RequestInit).headers).get("x-csrf-token")).toBe("session-csrf");
    }
  });
});

describe("withQuery", () => {
  it("appends only non-empty parameters", () => {
    expect(withQuery("/runtime/servers", { namespace: "mcp-servers" })).toBe(
      "/runtime/servers?namespace=mcp-servers"
    );
    expect(withQuery("/runtime/servers", { namespace: "  " })).toBe("/runtime/servers");
    expect(withQuery("/runtime/servers", { namespace: undefined })).toBe("/runtime/servers");
  });
});

describe("fetchUIJSON", () => {
  it("calls UI-origin session paths without the API base", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ authenticated: false }),
    });
    vi.stubGlobal("fetch", fetchMock);
    window.MCP_API_BASE = "/api/v1";

    await expect(fetchUIJSON("/auth/status")).resolves.toEqual({ authenticated: false });
    expect(fetchMock.mock.calls[0][0]).toBe("/auth/status");
    expect((fetchMock.mock.calls[0][1] as RequestInit).credentials).toBe("same-origin");
  });

  it("strips caller-supplied credential headers", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ authenticated: true }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await fetchUIJSON("/auth/login", {
      method: "POST",
      headers: { Authorization: "Bearer leaked", "x-api-key": "leaked-key" },
      body: JSON.stringify({ email: "a@b.c", password: "x" }),
    });

    const headers = new Headers((fetchMock.mock.calls[0] as [string, RequestInit])[1].headers);
    expect(headers.get("authorization")).toBeNull();
    expect(headers.get("x-api-key")).toBeNull();
  });

  it("refuses paths outside the UI session allowlist", async () => {
    await expect(fetchUIJSON("/auth/admin-check")).rejects.toThrow(
      "unsupported UI origin path: /auth/admin-check"
    );
  });

  it("maps 401 to UnauthorizedError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: false, status: 401, text: async () => "unauthorized" })
    );

    await expect(fetchUIJSON("/auth/status")).rejects.toBeInstanceOf(UnauthorizedError);
  });
});
