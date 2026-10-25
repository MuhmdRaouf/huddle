// Nav.test.tsx — the five tabs in each layout's chrome: the phone's pinned bar, the tablet's icon
// rail and the desktop's sidebar with the brand over them, each the Main navigation with the
// needs-you count on Now.
import { render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { BottomNav, IconRail, SideNav } from "../../src/app/Nav.tsx";
import type { Tab } from "../../src/app/router.ts";
import { flush } from "../helpers.tsx";

beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve({ version: "1.2.3" }) })),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const links = (nav: string): HTMLAnchorElement[] =>
  [...document.querySelectorAll(`[data-nav='${nav}'] a`)] as HTMLAnchorElement[];

describe("the sidebar", () => {
  it("puts the brand with the server's version over the tabs, outside the Main navigation", async () => {
    render(<SideNav tab="now" n={0} />);
    const brand = screen.getByText("Huddle").closest("a") as HTMLAnchorElement;
    expect(brand.getAttribute("href")).toBe("#/channels");
    expect(brand.closest("nav")).toBeNull();
    await flush();
    expect(screen.getByText("v1.2.3")).toBeDefined();
  });

  it("lays the five tabs out with the active one current", () => {
    render(<SideNav tab="radar" n={0} />);
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
    // 44 px rows
    for (const a of tabs) expect(a.className).toContain("min-h-11");
  });

  it("marks the tab the shell names, for each of the five", () => {
    for (const tab of ["now", "channels", "radar", "kanban", "settings"] as const) {
      const { unmount } = render(<SideNav tab={tab satisfies Tab} n={0} />);
      expect(
        (document.querySelector("[data-nav='side'] a[aria-current='page']") as HTMLElement).dataset.tab,
      ).toBe(tab);
      unmount();
    }
  });
});

describe("the needs-you count on Now", () => {
  it("rides on Now in the bar, the rail and the sidebar, and is gone at zero", () => {
    for (const [Nav, name] of [
      [BottomNav, "bottom"],
      [IconRail, "rail"],
      [SideNav, "side"],
    ] as const) {
      const { rerender, unmount } = render(<Nav tab="now" n={3} />);
      expect(
        document.querySelector(`[data-nav='${name}'] a[data-tab='now'] .badge-warning`)?.textContent,
      ).toBe("3");
      rerender(<Nav tab="now" n={0} />);
      expect(document.querySelector(`[data-nav='${name}'] .badge-warning`)).toBeNull();
      unmount();
    }
  });
});

describe("the phone's bar", () => {
  it("is the pinned tab bar of five 52 px targets", () => {
    render(<BottomNav tab="kanban" n={0} />);
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
