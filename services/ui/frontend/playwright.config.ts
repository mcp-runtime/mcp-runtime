import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.pw.ts",
  workers: process.env.CI ? 1 : 2,
  globalTimeout: 5 * 60 * 1000,
  use: { baseURL: "http://127.0.0.1:4189", trace: "retain-on-failure", screenshot: "only-on-failure" },
  webServer: { command: "npm run dev -- --host 127.0.0.1 --port 4189 --strictPort", url: "http://127.0.0.1:4189", reuseExistingServer: !process.env.CI },
});
