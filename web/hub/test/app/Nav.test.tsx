// Nav.test.tsx — the phone's pinned bar of five tabs, the Main navigation with the needs-you count
// on Home, painted from the tokens by the one badge rule, and the health dot.
import { render, screen } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { BottomNav, HealthDot, type NeedsBadge, needsBadge } from "../../src/app/Nav.tsx";
import type { NeedItem } from "../../src/registry.ts";
import { legacyClasses } from "../helpers.tsx";

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

describe("the needs-you count on Home", () => {
  it("rides on Home in the bar in its tone, and is gone at zero", () => {
    const { rerender } = render(<BottomNav tab="home" needs={{ n: 3, tone: "needs-you" }} />);
    expect(
      document.querySelector("[data-nav='bottom'] a[data-tab='home'] .ui-badge-needs-you")?.textContent,
    ).toBe("3");
    rerender(<BottomNav tab="home" needs={{ n: 1, tone: "turn-done" }} />);
    expect(
      document.querySelector("[data-nav='bottom'] a[data-tab='home'] .ui-badge-turn-done")?.textContent,
    ).toBe("1");
    rerender(<BottomNav tab="home" needs={NONE} />);
    expect(document.querySelector("[data-nav='bottom'] .ui-badge")).toBeNull();
  });

  it("is on Home alone", () => {
    render(<BottomNav tab="sessions" needs={{ n: 2, tone: "needs-you" }} />);
    expect(document.querySelectorAll("[data-nav='bottom'] .ui-badge")).toHaveLength(1);
    expect(document.querySelector("[data-nav='bottom'] a[data-tab='home'] .ui-badge")).not.toBeNull();
  });
});

describe("the tokens", () => {
  it("paints the bar with no daisyUI or legacy class", () => {
    render(<BottomNav tab="home" needs={{ n: 2, tone: "needs-you" }} />);
    expect(legacyClasses()).toEqual([]);
  });
});

describe("the phone's bar", () => {
  it("is the pinned tab bar of five 52 px targets", () => {
    render(<BottomNav tab="kanban" needs={NONE} />);
    const nav = document.querySelector("[data-nav='bottom']") as HTMLElement;
    expect(nav.className).toContain("ui-tabbar");
    expect(nav.getAttribute("aria-label")).toBe("Main");
    expect(links("bottom").map((a) => a.dataset.tab)).toEqual([
      "home",
      "sessions",
      "channels",
      "radar",
      "kanban",
    ]);
    expect(links("bottom").map((a) => a.textContent)).toEqual([
      "Home",
      "Sessions",
      "Channels",
      "Radar",
      "Boards",
    ]);
    expect(links("bottom").map((a) => a.getAttribute("aria-current"))).toEqual([
      null,
      null,
      null,
      null,
      "page",
    ]);
    for (const a of links("bottom")) expect(a.className).toContain("min-h-[52px]");
  });
});

describe("the phone bar's marks", () => {
  it("draws the canvas glyph on each tab, one shape apiece", () => {
    render(<BottomNav tab="home" needs={NONE} />);
    const shapes = links("bottom").map((a) => a.querySelector("svg")?.innerHTML ?? "");
    expect(new Set(shapes).size).toBe(5);
    expect(shapes[0]).toContain("M3 11l9-8 9 8");
    expect(shapes[1]).toContain("M4 5h16v11H8l-4 4z");
    expect(shapes[4]).toContain("M4 4h5v16H4zM10 4h5v10h-5zM16 4h4v7h-4z");
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
});
