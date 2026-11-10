import { defineConfig, devices } from "@playwright/test";

/**
 * Smoke tests against the real binary (`make e2e` builds it first). One server and one user,
 * so the tests run in order in a single worker; e2e/server.ts starts it on a fresh data dir.
 */
export const port = Number(process.env["PM_E2E_PORT"] ?? 18234);

export default defineConfig({
  testDir: "e2e",
  globalSetup: "./e2e/server.ts",
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env["CI"],
  retries: 0,
  reporter: process.env["CI"] ? "github" : "list",
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
