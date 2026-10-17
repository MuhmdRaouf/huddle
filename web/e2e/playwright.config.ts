// playwright.config.ts — the e2e run's shape: chromium only, headless, one worker, no retries,
// thirty seconds a test. The global setup builds the binary and starts the hub on a temp home;
// the teardown stops it and removes the temp homes. Everything the run writes — test results
// included — stays under the repo's gitignored .agents/scratch.

import { defineConfig, devices } from "@playwright/test";
import path from "node:path";
import { here, repoRoot } from "./paths.ts";

export default defineConfig({
  testDir: here,
  globalSetup: path.join(here, "global-setup.ts"),
  globalTeardown: path.join(here, "global-teardown.ts"),
  timeout: 30_000,
  globalTimeout: 5 * 60_000,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  outputDir: path.join(repoRoot, ".agents", "scratch", "e2e", "test-results"),
  forbidOnly: true,
  use: {
    headless: true,
    // the app-shell worker serves cached pages mid-run and double-paints the shell; the tests
    // measure the app itself, so they run with no worker
    serviceWorkers: "block",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
