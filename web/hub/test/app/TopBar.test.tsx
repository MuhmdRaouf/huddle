// TopBar.test.tsx — the tablet's and the desktop's top bar: the brand, the five tabs as the Main
// navigation with the needs-you count on Home, the avatar at the right end, and the machines line
// the Sessions list draws.
import { render, type RenderResult, screen } from "@testing-library/preact";
import { beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { NeedsBadge } from "../../src/app/Nav.tsx";
import { MachinesLine, TopBar } from "../../src/app/TopBar.tsx";
import { legacyClasses, makeCtx, makeState, renderIn, stubMatchMedia } from "../helpers.tsx";

const NONE: NeedsBadge = { n: 0, tone: "needs-you" };

beforeEach(() => {
  stubMatchMedia(false);
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve({}) })),
  );
});

/** Renders the bar on `tab`. */
function mount(
  tab: Parameters<typeof TopBar>[0]["tab"],
  needs: NeedsBadge = NONE,
  healthy = true,
): RenderResult {
  return renderIn(
    <TopBar tab={tab} needs={needs} healthy={healthy} />,
    makeCtx(makeState(), { who: { kind: "owner" } }),
  );
}

const tabs = (): HTMLAnchorElement[] =>
  [...document.querySelectorAll("[data-nav='top'] a")] as HTMLAnchorElement[];

describe("the top bar", () => {
  it("lists the five tabs in the Main navigation, the open one current", () => {
    mount("radar");
    expect(screen.getByRole("navigation", { name: "Main" })).toBe(document.querySelector("[data-nav='top']"));
    expect(tabs().map((a) => a.dataset.tab)).toEqual(["home", "sessions", "channels", "radar", "kanban"]);
    expect(tabs().map((a) => a.textContent)).toEqual(["Home", "Sessions", "Channels", "Radar", "Boards"]);
    expect(tabs().map((a) => a.getAttribute("href"))).toEqual([
      "#/home",
      "#/sessions",
      "#/channels",
      "#/radar/overview",
      "#/kanban",
    ]);
    expect(tabs().map((a) => a.getAttribute("aria-current"))).toEqual([null, null, null, "page", null]);
    for (const a of tabs()) expect(a.className).toContain("min-h-[var(--ui-tap)]");
  });

  it("marks the tab the shell names, for each of the five", () => {
    for (const tab of ["home", "sessions", "channels", "radar", "kanban"] as const) {
      const { unmount } = mount(tab);
      expect(
        document.querySelector("[data-nav='top'] a[aria-current='page']")?.getAttribute("data-tab"),
      ).toBe(tab);
      unmount();
    }
  });

  it("puts the brand, linked to Home, before the tabs and the avatar after them", () => {
    mount("home");
    const brand = screen.getByText("huddle").closest("a") as HTMLAnchorElement;
    expect(brand.getAttribute("href")).toBe("#/home");
    expect(brand.closest("nav")).toBeNull();
    const avatar = screen.getByRole("button", { name: "Account menu" });
    const bar = document.querySelector("[data-top-bar]") as HTMLElement;
    const order = [...bar.querySelectorAll("a, button")];
    expect(order[0]).toBe(brand);
    expect(order.at(-1)).toBe(avatar);
  });

  it("carries the needs-you count on Home alone, in its tone, and none at zero", () => {
    const { unmount } = mount("sessions", { n: 2, tone: "turn-done" });
    expect(document.querySelectorAll("[data-nav='top'] .ui-badge")).toHaveLength(1);
    expect(
      document.querySelector("[data-nav='top'] a[data-tab='home'] .ui-badge-turn-done")?.textContent,
    ).toBe("2");
    unmount();
    mount("sessions");
    expect(document.querySelector("[data-nav='top'] .ui-badge")).toBeNull();
  });

  it("holds no Settings link, no jump field, no theme menu and no shortcuts key", () => {
    mount("home");
    expect(screen.queryByRole("link", { name: "Settings" })).toBeNull();
    expect(document.querySelector("#jump")).toBeNull();
    expect(screen.queryByLabelText(/^Theme/)).toBeNull();
    expect(screen.queryByLabelText("Keyboard shortcuts")).toBeNull();
    expect(screen.queryByLabelText("Machines")).toBeNull();
  });

  it("paints no daisyUI or legacy class", () => {
    mount("home", { n: 1, tone: "needs-you" });
    expect(legacyClasses()).toEqual([]);
  });
});

describe("the machines line", () => {
  it("answers its label and carries the live dot's state", () => {
    const { rerender } = render(<MachinesLine live="live" />);
    const line = screen.getByLabelText("Machines");
    const dot = (): string => line.querySelector("[data-live]")?.className ?? "";
    expect(dot()).toContain("bg-[var(--ui-ok)]");
    expect(line.textContent).toContain("This machine");
    rerender(<MachinesLine live="offline" />);
    expect(dot()).toContain("bg-[var(--ui-warn)]");
    rerender(<MachinesLine live="connecting" />);
    expect(dot()).toContain("bg-[var(--ui-idle)]");
  });
});
