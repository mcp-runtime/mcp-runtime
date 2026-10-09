/// <reference types="vite/client" />

interface Window {
  MCP_PUBLIC_ROUTES?: { prefix?: string; docs?: string; grafana?: string; registry?: string };
  MCP_API_BASE?: string;
  MCP_DEFAULTS?: {
    namespace?: string;
    policyVersion?: string;
  };
  MCP_PLATFORM_MODE?: "tenant" | "org" | "public" | string;
  MCP_GOOGLE_CLIENT_ID?: string;
  // Attached by the Google Identity Services script
  // (https://accounts.google.com/gsi/client) once it loads.
  google?: {
    accounts: {
      id: {
        initialize: (config: {
          client_id: string;
          callback: (response: { credential?: string }) => void;
        }) => void;
        renderButton: (
          container: HTMLElement,
          options: {
            theme?: string;
            size?: string;
            shape?: string;
            text?: string;
            width?: number;
          }
        ) => void;
      };
    };
  };
}
