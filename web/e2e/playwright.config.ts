// playwright.config.ts — the e2e run's shape: chromium only, headless, one worker, no retries,
// thirty seconds a test, in five projects: `desktop` (1440x900) runs every spec, `iphone` (the
// iPhone 15's viewport, touch and user agent over chromium) runs the specs of the pages built
// phone first, and the screenshot suite runs on its own in `screens-desktop`, `screens-iphone`
// and `tablet` (an iPad at 820x1180, touch, over chromium), one CI job each under the 10-minute
// cap. The global setup builds the binary and starts the hub on a temp home;
// the teardown stops it and removes the temp homes. Everything the run writes — test results
// included — stays under the repo's gitignored .agents/scratch.

import { defineConfig, devices } from "@playwright/test";
import path from "node:path";
import { here, repoRoot } from "./paths.ts";
import { chromiumTlsArgs } from "./tls.ts";

/** The specs that also run on the phone: the pages built on the web/ui design system. */
const PHONE_SPECS = [
  "tokens.spec.ts",
  "phone-perf.spec.ts",
  "shell.spec.ts",
  "channel.spec.ts",
  "now.spec.ts",
  "kanban.spec.ts",
  "kanban-panel.spec.ts",
  "settings.spec.ts",
  "landing.spec.ts",
  "signin.spec.ts",
  "radar.spec.ts",
];
/** The specs that run on the phone only: the phone's load budget under throttled CDP. */
const PHONE_ONLY = ["phone-perf.spec.ts"];
/** The screenshot suite: its own project on each width, so no CI job passes its cap. */
const SCREENS = ["screens.spec.ts"];

export default defineConfig({
  testDir: here,
  globalSetup: path.join(here, "global-setup.ts"),
  globalTeardown: path.join(here, "global-teardown.ts"),
  timeout: 30_000,
  // every project in one local run; CI runs one project per job, each well inside its own cap
  globalTimeout: 20 * 60_000,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  outputDir: path.join(repoRoot, ".agents", "scratch", "e2e", "test-results"),
  forbidOnly: true,
  use: {
    headless: true,
    // the reserved name hub.example.test mapped onto loopback, and the run's own CA pinned by
    // its key hash — a pin, not a blanket certificate ignore: minted here (the module runs
    // once, before any browser launches) so the pair the setup imports is the one pinned
    launchOptions: { args: chromiumTlsArgs() },
    // the app-shell worker serves cached pages mid-run and double-paints the shell; the tests
    // measure the app itself, so they run with no worker
    serviceWorkers: "block",
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "desktop",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } },
      testIgnore: [...PHONE_ONLY, ...SCREENS],
    },
    {
      name: "iphone",
      use: { ...devices["iPhone 15"], browserName: "chromium" },
      testMatch: PHONE_SPECS,
    },
    {
      name: "screens-desktop",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } },
      testMatch: SCREENS,
    },
    {
      name: "screens-iphone",
      use: { ...devices["iPhone 15"], browserName: "chromium" },
      testMatch: SCREENS,
    },
    {
      name: "tablet",
      use: {
        ...devices["iPad Pro 11"],
        viewport: { width: 820, height: 1180 },
        screen: { width: 820, height: 1180 },
        browserName: "chromium",
      },
      testMatch: SCREENS,
    },
  ],
});
