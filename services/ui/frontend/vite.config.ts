import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Dev-only proxy. `npm run dev` serves the candidate build on :5173 and
// forwards session, config, and API traffic to a running MCP Runtime ingress
// (the Kind contributor cluster at http://localhost:18080 by default), so the
// candidate can be exercised against real services without deploying it.
// Override with MCP_DEV_UPSTREAM. It has no effect on `npm run build`.
const upstream = process.env.MCP_DEV_UPSTREAM || "http://localhost:18080";
const proxied = ["/auth", "/api/ui/v1", "/api/v1", "/config.js", "/grafana", "/prometheus"];

export default defineConfig({
  plugins: [react()],
  base: "./",
  server: {
    proxy: Object.fromEntries(
      proxied.map((path) => [path, { target: upstream, changeOrigin: false }])
    ),
  },
  build: {
    outDir: "../static",
    emptyOutDir: true,
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: "./src/test/setup.ts",
  },
});
