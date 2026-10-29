// Nav.test.tsx — the five tabs in each layout's chrome: the phone's pinned bar, the tablet's icon
// rail and the desktop's sidebar with the brand over them, each the Main navigation with the
// needs-you count on Sessions, painted from the tokens by the one badge rule.
import { render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { HealthDot, BottomNav, IconRail, type NeedsBadge, needsBadge, SideNav } from "../../src/app/Nav.tsx";
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
    render(<SideNav tab="sessions" needs={NONE} healthy />);
    const brand = screen.getByText("Huddle").closest("a") as HTMLAnchorElement;
    expect(brand.getAttribute("href")).toBe("#/channels");
    expect(brand.closest("nav")).toBeNull();
    await flush();
    expect(screen.getByText("v1.2.3")).toBeDefined();
  });

  it("lays the five tabs out with the active one current", () => {
    render(<SideNav tab="radar" needs={NONE} healthy />);
    const tabs = links("side");
    expect(tabs.map((a) => a.dataset.tab)).toEqual(["sessions", "channels", "radar", "kanban", "settings"]);
    expect(tabs.map((a) => a.getAttribute("aria-current"))).toEqual([null, null, "page", null, null]);
    expect(tabs.map((a) => a.getAttribute("href"))).toEqual([
      "#/sessions",
      "#/channels",
      "#/radar/overview",
      "#/kanban",
      "#/settings",
    ]);
    // rows at the tap token, 44 px whatever the root font size
    for (const a of tabs) expect(a.className).toContain("min-h-[var(--ui-tap)]");
  });

  it("marks the tab the shell names, for each of the five", () => {
    for (const tab of ["sessions", "channels", "radar", "kanban", "settings"] as const) {
      const { unmount } = render(<SideNav tab={tab satisfies Tab} needs={NONE} healthy />);
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
      needsBadge({
        inbox: 0,
        sessions: 2,
        items: [item("turn"), item("turn"), item("question", "snoozed")],
      }),
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

describe("the needs-you count on Sessions", () => {
  it("rides on Sessions in the bar, the rail and the sidebar in its tone, and is gone at zero", () => {
    for (const [Nav, name] of [
      [BottomNav, "bottom"],
      [IconRail, "rail"],
      [SideNav, "side"],
    ] as const) {
      const { rerender, unmount } = render(
        <Nav tab="sessions" needs={{ n: 3, tone: "needs-you" }} healthy />,
      );
      expect(
        document.querySelector(`[data-nav='${name}'] a[data-tab='sessions'] .ui-badge-needs-you`)
          ?.textContent,
      ).toBe("3");
      rerender(<Nav tab="sessions" needs={{ n: 1, tone: "turn-done" }} healthy />);
      expect(
        document.querySelector(`[data-nav='${name}'] a[data-tab='sessions'] .ui-badge-turn-done`)
          ?.textContent,
      ).toBe("1");
      rerender(<Nav tab="sessions" needs={NONE} healthy />);
      expect(document.querySelector(`[data-nav='${name}'] .ui-badge`)).toBeNull();
      unmount();
    }
  });

  it("names the rail's Sessions tab with its count, so the name holds the number the tab shows", () => {
    const { rerender } = render(<IconRail tab="sessions" needs={{ n: 3, tone: "needs-you" }} healthy />);
    const sessions = () => document.querySelector("[data-nav='rail'] a[data-tab='sessions']");
    expect(sessions()?.getAttribute("aria-label")).toBe("Sessions, 3 need you");
    rerender(<IconRail tab="sessions" needs={{ n: 1, tone: "turn-done" }} healthy />);
    expect(sessions()?.getAttribute("aria-label")).toBe("Sessions, 1 turn done");
    rerender(<IconRail tab="sessions" needs={NONE} healthy />);
    expect(sessions()?.getAttribute("aria-label")).toBe("Sessions");
    expect(
      document.querySelector("[data-nav='rail'] a[data-tab='channels']")?.getAttribute("aria-label"),
    ).toBe("Channels");
  });
});

describe("the tokens", () => {
  it("paints the bar, the rail and the sidebar with no daisyUI or legacy class", async () => {
    for (const Nav of [BottomNav, IconRail, SideNav]) {
      const { unmount } = render(<Nav tab="sessions" needs={{ n: 2, tone: "needs-you" }} healthy />);
      await flush();
      expect(legacyClasses(), Nav.name).toEqual([]);
      unmount();
    }
  });

  it("keeps the sidebar's version as a neutral badge", async () => {
    render(<SideNav tab="sessions" needs={NONE} healthy />);
    await flush();
    expect(screen.getByText("v1.2.3").className).toBe("ui-badge ui-badge-neutral");
  });
});

describe("the phone's bar", () => {
  it("is the pinned tab bar of five 52 px targets", () => {
    render(<BottomNav tab="kanban" needs={NONE} healthy />);
    const nav = document.querySelector("[data-nav='bottom']") as HTMLElement;
    expect(nav.className).toContain("ui-tabbar");
    expect(nav.getAttribute("aria-label")).toBe("Main");
    expect(links("bottom").map((a) => a.textContent)).toEqual([
      "Sessions",
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

describe("the health dot", () => {
  it("renders nothing while every part is well", () => {
    const { container } = render(<HealthDot healthy />);
    expect(container.querySelector("[data-health-dot]")).toBeNull();
  });

  it("renders the dot and the hidden word once a part fails", () => {
    const { container } = render(<HealthDot healthy={false} />);
    const dot = container.querySelector("[data-health-dot]") as HTMLElement;
    expect(dot).not.toBeNull();
    expect(dot.getAttribute("aria-hidden")).toBe("true");
    expect(dot.className).toContain("bg-[var(--ui-fail)]");
    expect(screen.getByText("A part of the hub is failing")).toBeDefined();
  });

  it("rides on the bar's settings tab, inside its link", () => {
    render(<BottomNav tab="settings" needs={NONE} healthy={false} />);
    const tab = document.querySelector("[data-nav='bottom'] a[data-tab='settings']");
    expect(tab?.querySelector("[data-health-dot]")).not.toBeNull();
    expect(tab?.textContent).toContain("A part of the hub is failing");
  });

  it("is gone from the bar's settings tab while the hub is well", () => {
    render(<BottomNav tab="settings" needs={NONE} healthy />);
    expect(document.querySelector("[data-nav='bottom'] [data-health-dot]")).toBeNull();
  });
});
