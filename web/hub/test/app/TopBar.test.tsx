// TopBar.test.tsx — the desktop top bar: the brand with its version, the five text tabs with the
// needs-you count, the jump field with its ⌘K hint, the machines line with its live dot, the theme
// menu, and the shortcuts key.
import { act, render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { Tab } from "../../src/app/router.ts";
import { DesktopBar, MachinesLine, ThemeMenu, useVersion } from "../../src/app/TopBar.tsx";
import { flush, stubMatchMedia } from "../helpers.tsx";

beforeEach(() => {
  localStorage.clear();
  stubMatchMedia(false);
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve({ version: "1.2.3" }) })),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the brand and its version", () => {
  it("links the channels page, names the word and keeps a version badge", () => {
    render(<DesktopBar tab="now" n={0} live="live" />);
    expect(screen.getByLabelText("Huddle").getAttribute("href")).toBe("#/channels");
    expect(screen.getByText("v0.0.1")).toBeDefined();
  });

  it("shows the server's version once /health answers, and the placeholder when it fails", async () => {
    const probe = render(<VersionProbe />);
    await flush();
    expect(probe.container.textContent).toContain("1.2.3");
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new Error("down"))),
    );
    const fallback = render(<VersionProbe />);
    await flush();
    expect(fallback.container.textContent).toContain("0.0.1");
  });
});

/** The hook alone, so the /health read is testable without the whole bar. */
function VersionProbe() {
  return <span>{useVersion()}</span>;
}

describe("the five text tabs", () => {
  it("lays the tabs out with the active one current", () => {
    render(<DesktopBar tab="radar" n={0} live="live" />);
    const tabs = [...document.querySelectorAll("[data-nav='top'] a")] as HTMLAnchorElement[];
    expect(tabs.map((a) => a.dataset.tab)).toEqual(["now", "channels", "radar", "kanban", "settings"]);
    expect(tabs.map((a) => a.getAttribute("aria-current"))).toEqual([null, null, "page", null, null]);
    expect(tabs[1]?.getAttribute("href")).toBe("#/channels");
    expect(tabs[2]?.getAttribute("href")).toBe("#/radar/overview");
    expect(tabs[3]?.getAttribute("href")).toBe("#/kanban");
  });

  it("carries the needs-you count beside Now, and nothing when none needs the owner", () => {
    const { rerender } = render(<DesktopBar tab="now" n={3} live="live" />);
    expect(document.querySelector("[data-nav='top'] .badge-warning")?.textContent).toBe("3");
    rerender(<DesktopBar tab="now" n={0} live="live" />);
    expect(document.querySelector("[data-nav='top'] .badge-warning")).toBeNull();
  });
});

describe("the jump field", () => {
  it("names what it does, spells the shortcut and opens the palette", () => {
    const onPalette = vi.fn();
    render(<DesktopBar tab="now" n={0} live="live" onPalette={onPalette} />);
    const jump = screen.getByLabelText("Jump to a session");
    expect(jump.getAttribute("aria-keyshortcuts")).toBe("Meta+K Control+K /");
    expect(jump.querySelector("kbd")?.textContent).toMatch(/⌘K|Ctrl K/);
    void act(() => {
      (jump as HTMLElement).click();
    });
    expect(onPalette).toHaveBeenCalledTimes(1);
  });
});

describe("the machines line", () => {
  it("answers its label and carries the live dot's state", () => {
    const { rerender } = render(<MachinesLine live="live" />);
    const line = screen.getByLabelText("Machines");
    expect(line.querySelector(".status-success")).not.toBeNull();
    expect(line.textContent).toContain("This machine");
    rerender(<MachinesLine live="offline" />);
    expect(line.querySelector(".status-warning")).not.toBeNull();
    rerender(<MachinesLine live="connecting" />);
    expect(line.querySelector(".status-neutral")).not.toBeNull();
  });
});

describe("the theme menu", () => {
  it("opens with the three modes, checks the current one, and keeps a pick", () => {
    localStorage.setItem("huddle:theme", '{"family":"catppuccin","mode":"dark"}');
    render(<ThemeMenu />);
    void act(() => {
      (screen.getByLabelText("Theme: Dark") as HTMLButtonElement).click();
    });
    const menu = document.querySelector('[role="menu"]') as HTMLElement;
    expect(menu.textContent).toContain("System");
    expect(menu.textContent).toContain("Dark");
    expect(menu.textContent).toContain("Light");
    expect(menu.querySelector('[aria-checked="true"]')?.textContent).toContain("Dark");
    void act(() => {
      [...menu.querySelectorAll("button")].find((b) => b.textContent?.includes("Light"))?.click();
    });
    expect(localStorage.getItem("huddle:theme")).toBe('{"family":"catppuccin","mode":"light"}');
    expect(document.documentElement.dataset.theme).toBe("latte");
    expect(screen.getByLabelText("Theme: Light")).toBeDefined();
    expect(document.querySelector('[role="menu"]')).toBeNull();
  });

  it("reopens on the kept choice after a reload and migrates a legacy pick", () => {
    localStorage.setItem("huddle:theme", '"light"');
    render(<ThemeMenu />);
    expect(document.documentElement.dataset.theme).toBe("latte");
    expect(localStorage.getItem("huddle:theme")).toBe('{"family":"catppuccin","mode":"light"}');
    expect(screen.getByLabelText("Theme: Light")).toBeDefined();
  });

  it("applies the system's variant, the default, when nothing is kept", () => {
    stubMatchMedia(false);
    render(<ThemeMenu />);
    expect(document.documentElement.dataset.theme).toBe("latte");
    localStorage.clear();
    stubMatchMedia(true);
    render(<ThemeMenu />);
    expect(document.documentElement.dataset.theme).toBe("mocha");
  });

  it("follows the system while System is the mode", () => {
    localStorage.setItem("huddle:theme", '{"family":"catppuccin","mode":"system"}');
    stubMatchMedia(true);
    render(<ThemeMenu />);
    expect(document.documentElement.dataset.theme).toBe("mocha");
  });

  it("does not follow the system once a mode is picked", () => {
    const mq = stubMatchMedia(false);
    render(<ThemeMenu />);
    expect(document.documentElement.dataset.theme).toBe("latte"); // the default follows the light system
    void act(() => {
      (screen.getByLabelText("Theme: System") as HTMLButtonElement).click();
    });
    [...(document.querySelector('[role="menu"]')?.querySelectorAll("button") ?? [])]
      .find((b) => b.textContent?.includes("Dark"))
      ?.click();
    expect(document.documentElement.dataset.theme).toBe("mocha");
    void act(() => {
      mq.fire();
    });
    expect(document.documentElement.dataset.theme).toBe("mocha");
  });
});

describe("the bar's keys", () => {
  it("runs the shortcuts key", () => {
    const onHelp = vi.fn();
    render(<DesktopBar tab="now" n={0} live="live" onHelp={onHelp} />);
    void act(() => {
      screen.getByLabelText("Keyboard shortcuts").click();
    });
    expect(onHelp).toHaveBeenCalledTimes(1);
  });
});

/** One bar over every tab, so the type is used the way the shell uses it. */
describe("every tab paints the bar", () => {
  it("renders the same bar over each of the five tabs", () => {
    for (const tab of ["now", "channels", "radar", "kanban", "settings"] as const) {
      const { unmount } = render(<DesktopBar tab={tab satisfies Tab} n={0} live="connecting" />);
      expect(
        (document.querySelector("[data-nav='top'] a[aria-current='page']") as HTMLElement).dataset.tab,
      ).toBe(tab);
      unmount();
    }
  });
});
