// Nav.test.tsx — the five tabs in each layout's chrome: the phone's pinned bar, the tablet's icon
// rail and the desktop's sidebar with the brand over them, each the Main navigation with the
// needs-you count on Now, painted from the tokens by the one badge rule.
import { render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { BottomNav, IconRail, type NeedsBadge, needsBadge, SideNav } from "../../src/app/Nav.tsx";
import type { Tab } from "../../src/app/router.ts";
import type { NeedItem } from "../../src/registry.ts";
import { flush, legacyClasses } from "../helpers.tsx";

beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve({ version: "1.2.3" }) })),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Nothing waits on the owner. */
const NONE: NeedsBadge = { n: 0, tone: "needs-you" };

/** One open needs-you item of a kind. */
const item = (kind: string, state = "open"): NeedItem => ({
  id: 1,
  session: "s-1",
  kind,
  summary: kind,
  created_at: "2026-10-25T12:00:00Z",
  state,
});

const links = (nav: string): HTMLAnchorElement[] =>
  [...document.querySelectorAll(`[data-nav='${nav}'] a`)] as HTMLAnchorElement[];

describe("the sidebar", () => {
  it("puts the brand with the server's version over the tabs, outside the Main navigation", async () => {
    render(<SideNav tab="now" needs={NONE} />);
    const brand = screen.getByText("Huddle").closest("a") as HTMLAnchorElement;
    expect(brand.getAttribute("href")).toBe("#/channels");
    expect(brand.closest("nav")).toBeNull();
    await flush();
    expect(screen.getByText("v1.2.3")).toBeDefined();
  });

  it("lays the five tabs out with the active one current", () => {
    render(<SideNav tab="radar" needs={NONE} />);
    const tabs = links("side");
    expect(tabs.map((a) => a.dataset.tab)).toEqual(["now", "channels", "radar", "kanban", "settings"]);
    expect(tabs.map((a) => a.getAttribute("aria-current"))).toEqual([null, null, "page", null, null]);
    expect(tabs.map((a) => a.getAttribute("href"))).toEqual([
      "#/now",
      "#/channels",
      "#/radar/overview",
      "#/kanban",
      "#/settings",
    ]);
    // rows at the tap token, 44 px whatever the root font size
    for (const a of tabs) expect(a.className).toContain("min-h-[var(--ui-tap)]");
  });

  it("marks the tab the shell names, for each of the five", () => {
    for (const tab of ["now", "channels", "radar", "kanban", "settings"] as const) {
      const { unmount } = render(<SideNav tab={tab satisfies Tab} needs={NONE} />);
      expect(
        (document.querySelector("[data-nav='side'] a[aria-current='page']") as HTMLElement).dataset.tab,
      ).toBe(tab);
      unmount();
    }
  });
});

describe("the one badge rule", () => {
  it("is red for a blocking ask: a channel's inbox item, a permission or a question", () => {
    expect(needsBadge({ inbox: 2, sessions: 0, items: [] })).toEqual({ n: 2, tone: "needs-you" });
    expect(needsBadge({ inbox: 0, sessions: 1, items: [item("permission")] })).toEqual({
      n: 1,
      tone: "needs-you",
    });
    expect(needsBadge({ inbox: 0, sessions: 2, items: [item("turn"), item("question")] })).toEqual({
      n: 2,
      tone: "needs-you",
    });
  });

  it("is orange when every open item is a finished turn, a snoozed ask aside", () => {
    expect(
      needsBadge({ inbox: 0, sessions: 2, items: [item("turn"), item("turn"), item("question", "snoozed")] }),
    ).toEqual({ n: 2, tone: "turn-done" });
  });

  it("stays red while the items are unread or say nothing, so a blocking ask is never shown calm", () => {
    expect(needsBadge({ inbox: 0, sessions: 1, items: null })).toEqual({ n: 1, tone: "needs-you" });
    expect(needsBadge({ inbox: 0, sessions: 1, items: [] })).toEqual({ n: 1, tone: "needs-you" });
  });

  it("counts nothing when nothing waits", () => {
    expect(needsBadge({ inbox: 0, sessions: 0, items: [item("turn")] }).n).toBe(0);
  });
});

describe("the needs-you count on Now", () => {
  it("rides on Now in the bar, the rail and the sidebar in its tone, and is gone at zero", () => {
    for (const [Nav, name] of [
      [BottomNav, "bottom"],
      [IconRail, "rail"],
      [SideNav, "side"],
    ] as const) {
      const { rerender, unmount } = render(<Nav tab="now" needs={{ n: 3, tone: "needs-you" }} />);
      expect(
        document.querySelector(`[data-nav='${name}'] a[data-tab='now'] .ui-badge-needs-you`)?.textContent,
      ).toBe("3");
      rerender(<Nav tab="now" needs={{ n: 1, tone: "turn-done" }} />);
      expect(
        document.querySelector(`[data-nav='${name}'] a[data-tab='now'] .ui-badge-turn-done`)?.textContent,
      ).toBe("1");
      rerender(<Nav tab="now" needs={NONE} />);
      expect(document.querySelector(`[data-nav='${name}'] .ui-badge`)).toBeNull();
      unmount();
    }
  });
});

describe("the tokens", () => {
  it("paints the bar, the rail and the sidebar with no daisyUI or legacy class", async () => {
    for (const Nav of [BottomNav, IconRail, SideNav]) {
      const { unmount } = render(<Nav tab="now" needs={{ n: 2, tone: "needs-you" }} />);
      await flush();
      expect(legacyClasses(), Nav.name).toEqual([]);
      unmount();
    }
  });

  it("keeps the sidebar's version as a neutral badge", async () => {
    render(<SideNav tab="now" needs={NONE} />);
    await flush();
    expect(screen.getByText("v1.2.3").className).toBe("ui-badge ui-badge-neutral");
  });
});

describe("the phone's bar", () => {
  it("is the pinned tab bar of five 52 px targets", () => {
    render(<BottomNav tab="kanban" needs={NONE} />);
    const nav = document.querySelector("[data-nav='bottom']") as HTMLElement;
    expect(nav.className).toContain("ui-tabbar");
    expect(nav.getAttribute("aria-label")).toBe("Main");
    expect(links("bottom").map((a) => a.textContent)).toEqual([
      "Now",
      "Channels",
      "Radar",
      "Kanban",
      "Settings",
    ]);
    expect(links("bottom").map((a) => a.getAttribute("aria-current"))).toEqual([
      null,
      null,
      null,
      "page",
      null,
    ]);
    for (const a of links("bottom")) expect(a.className).toContain("min-h-[52px]");
  });
});
