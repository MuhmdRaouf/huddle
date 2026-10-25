// hermetic.spec.ts — the run touches nothing of the user's: the two hubs the setup starts, and
// every command run against their homes, carry a huddle-router port of this run's own, picked
// free for the run and never the installed daemon's 18786, so a hub that comes to host the
// router (once a provider is enabled) and a hook or MCP child that probes it never meet the
// user's router.

import { expect, test } from "@playwright/test";
import { theSetup } from "./helpers.ts";

/** The port the installed daemon's router listens on; a test never speaks to it. */
const DAEMON_ROUTER_PORT = 18786;

test("each hub of the run carries its own router port, never the daemon's", () => {
  const { routerPort, hubRouterPort } = theSetup();
  for (const port of [routerPort, hubRouterPort]) {
    expect(port, "the setup names the router port").toEqual(expect.any(Number));
    expect(port).not.toBe(DAEMON_ROUTER_PORT);
    expect(port).toBeGreaterThan(1024);
  }
  expect(routerPort).not.toBe(hubRouterPort);
});
