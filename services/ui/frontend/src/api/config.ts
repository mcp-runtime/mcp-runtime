export type RuntimeConfig = {
  apiBase: string;
  defaults: {
    namespace: string;
    policyVersion: string;
  };
  platformMode: string;
  googleClientId: string;
};

export function readRuntimeConfig(): RuntimeConfig {
  return {
    apiBase: window.MCP_API_BASE || "/api/v1",
    defaults: {
      namespace: window.MCP_DEFAULTS?.namespace || "",
      policyVersion: window.MCP_DEFAULTS?.policyVersion || "v1",
    },
    platformMode: window.MCP_PLATFORM_MODE || "tenant",
    googleClientId: window.MCP_GOOGLE_CLIENT_ID || "",
  };
}

// Public route names come from the UI's validated deployment configuration.
export function uiPath(path: string): string {
  return `${window.MCP_PUBLIC_ROUTES?.prefix || ""}${path}`;
}

export function docsPath(): string { return window.MCP_PUBLIC_ROUTES?.docs || "/docs"; }
export function grafanaPath(): string { return window.MCP_PUBLIC_ROUTES?.grafana || "/grafana"; }
