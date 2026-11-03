// open-session.test.ts — which session the address opens: the one after Home or Sessions, and none
// anywhere else.

import { describe, expect, it } from "vite-plus/test";
import { openSessionOf } from "../../src/app/open-session.ts";
import { parseHash } from "../../src/app/router.ts";

const route = (hash: string) => parseHash(hash, (_k, fb) => fb);

describe("openSessionOf", () => {
  it("names the session after Home and after Sessions", () => {
    expect(openSessionOf(route("#/home/s-1"))).toBe("s-1");
    expect(openSessionOf(route("#/sessions/s-2"))).toBe("s-2");
  });

  it("names none for a list with no pick or a tab with no session", () => {
    expect(openSessionOf(route("#/home"))).toBeNull();
    expect(openSessionOf(route("#/sessions"))).toBeNull();
    expect(openSessionOf(route("#/radar/costs"))).toBeNull();
    expect(openSessionOf(route("#/kanban/board-1"))).toBeNull();
  });
});
