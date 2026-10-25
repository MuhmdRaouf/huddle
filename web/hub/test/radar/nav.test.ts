import { describe, expect, it } from "vite-plus/test";
import { RADAR_VIEWS } from "../../src/app/router.ts";
import { MORE_VIEWS, PHONE_VIEWS } from "../../src/radar/app/nav.ts";

describe("radar's view navigation", () => {
  it("puts Overview, Requests, Costs and Alerts in the phone's nav, in that order", () => {
    expect(PHONE_VIEWS).toEqual(["overview", "requests", "costs", "alerts"]);
  });

  it("lists every other view under More, in sub-tab order", () => {
    expect(MORE_VIEWS).toEqual(["models", "usage", "blocks", "tools", "agents", "routers", "timeline"]);
  });

  it("names each view exactly once between the two", () => {
    expect([...PHONE_VIEWS, ...MORE_VIEWS].toSorted()).toEqual([...RADAR_VIEWS].toSorted());
  });
});
