import { screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vite-plus/test";
import type { BlocksReport, UsageBlock } from "../../../src/radar/shared/usage.ts";
import {
  activeBlockOf,
  ActiveBlock,
  blockElapsed,
  blockTokens,
  noBlockHint,
} from "../../../src/radar/app/ActiveBlock.tsx";
import { blockWhen, BlocksView } from "../../../src/radar/app/views/Blocks.tsx";
import type { ClientState } from "../../../src/radar/state.ts";
import { legacyClasses } from "../../helpers.tsx";
import { renderApp } from "./render.tsx";
import { expectTokensOnly } from "./tokens.ts";

// every render in this file paints only token classes and labels its table cells for the phone
afterEach(() => expectTokensOnly());

const NOW = 1_793_000_000_000;
const HOUR = 3_600_000;

const start = NOW - 2 * HOUR;
const end = start + 5 * HOUR;

const block = (over: Partial<UsageBlock> = {}): UsageBlock => ({
  start,
  end,
  firstTs: NOW - 90 * 60_000,
  lastTs: NOW - 30 * 60_000,
  active: true,
  requests: 3,
  input: 1_200_000,
  output: 300_000,
  cacheRead: 5_000_000,
  cacheWrite: 2_000_000,
  costUsd: 5.4,
  savingsUsd: 0.25,
  burnTokensPerHour: 4_250_000,
  burnCostPerHour: 2.7,
  projectedTokens: 12_750_000,
  projectedCostUsd: 8.1,
  ...over,
});

const PAST = block({
  start: start - 24 * HOUR,
  end: start - 24 * HOUR + 5 * HOUR,
  firstTs: start - 24 * HOUR + 25 * 60_000,
  lastTs: start - 24 * HOUR + 26 * 60_000,
  active: false,
  requests: 1,
  input: 10,
  output: 5,
  cacheRead: 0,
  cacheWrite: 0,
  costUsd: 0,
  savingsUsd: 0,
  burnTokensPerHour: 15 / 5,
  burnCostPerHour: 0,
  projectedTokens: 0,
  projectedCostUsd: 0,
});

const REPORT: BlocksReport = { blocks: [block(), PAST], approximate: false, unpriced: 0 };

function blocksState(over: Partial<ClientState> = {}): Partial<ClientState> {
  return { blocks: { key: "7|claude", report: REPORT }, blocksDays: 7, ...over };
}

describe("activeBlockOf", () => {
  it("answers the window the clock sits in, when there is one", () => {
    expect(activeBlockOf(REPORT)).toEqual(block());
    expect(activeBlockOf({ ...REPORT, blocks: [PAST] })).toBeNull();
  });
});

describe("blockTokens", () => {
  it("sums every token kind the window counts", () => {
    expect(blockTokens(block())).toBe(8_500_000);
  });
});

describe("blockElapsed", () => {
  it("answers the share of the window behind the clock, clamped at both ends", () => {
    expect(blockElapsed(block(), NOW)).toBeCloseTo(0.4);
    expect(blockElapsed(block(), start - 1)).toBe(0);
    expect(blockElapsed(block(), end + 1)).toBe(1);
    expect(blockElapsed({ ...block(), start: end, end }, NOW)).toBe(0); // a degenerate window
  });
});

describe("noBlockHint", () => {
  it("names the last end when a window ran, and says what the scope counted when none did", () => {
    const hour = new Date(PAST.end).toTimeString().slice(0, 5);
    expect(noBlockHint(REPORT, "claude")).toBe(
      `The last block ended ${hour}. The next request starts a new one.`,
    );
    const empty: BlocksReport = { blocks: [], approximate: false, unpriced: 0 };
    expect(noBlockHint(empty, "claude")).toContain("No Claude-model requests");
    expect(noBlockHint(empty, "all")).toBe("No requests in the window. The next one starts a block.");
  });
});

describe("ActiveBlock", () => {
  it("renders the live window: the time left, the tokens, the cost, the burn and the projection", () => {
    const { container } = renderApp(<ActiveBlock />, blocksState(), NOW);
    const card = container.querySelector("[data-block-card]")?.textContent ?? "";
    expect(card).toContain("3h00m");
    expect(card).toContain("8.5M");
    expect(card).toContain("$5.40");
    expect(card).toContain("4.25M/h");
    expect(card).toContain("$2.70/h");
    expect(card).toContain("12.8M");
    expect(card).toContain("$8.10");
    const bar = container.querySelector("[data-active-block] [role='img']");
    expect(bar?.getAttribute("aria-label")).toBe("The window is 40% through");
  });

  it("marks the estimates once the report says a model went unpriced", () => {
    const rough = renderApp(
      <ActiveBlock />,
      blocksState({ blocks: { key: "7|claude", report: { ...REPORT, approximate: true } } }),
      NOW,
    );
    const card = rough.container.querySelector("[data-block-card]")?.textContent ?? "";
    expect(card).toContain("~$5.40");
    expect(card).toContain("~$2.70/h");
  });

  it("says there is no live window, and what would start one", () => {
    const ended = renderApp(
      <ActiveBlock />,
      blocksState({ blocks: { key: "7|claude", report: { ...REPORT, blocks: [PAST] } } }),
      NOW,
    );
    expect(ended.container.querySelector("[data-block-none]")?.textContent).toContain("No active block");
    const empty = renderApp(
      <ActiveBlock />,
      blocksState({ blocks: { key: "7|claude", report: { blocks: [], approximate: false, unpriced: 0 } } }),
      NOW,
    );
    expect(empty.container.querySelector("[data-block-none]")?.textContent).toContain(
      "No Claude-model requests",
    );
  });

  it("waits while the report is on its way, and names a failure", () => {
    const loading = renderApp(<ActiveBlock />, {}, NOW);
    expect(loading.container.querySelector("[data-block-loading]")?.textContent).toContain(
      "Reading the metering blocks",
    );
    const failed = renderApp(<ActiveBlock />, { blocksError: "GET /api/blocks?days=7 → 503" }, NOW);
    expect(failed.container.querySelector("[data-block-loading]")?.textContent).toContain("503");
  });

  it("paints the card and its scope toggle on the tokens, in every state", () => {
    const live = renderApp(<ActiveBlock />, blocksState(), NOW);
    expect(legacyClasses(live.container)).toEqual([]);
    const buttons = [...live.container.querySelectorAll('[role="group"][aria-label="Model scope"] button')];
    expect(buttons.map((b) => [b.classList.contains("ui-btn"), b.getAttribute("aria-pressed")])).toEqual([
      [true, "true"],
      [true, "false"],
    ]);
    expect(
      live.container.querySelector("[data-active-block] [role='img'] > div")?.getAttribute("style"),
    ).toContain("var(--radar-series-1)");
    live.unmount();
    const loading = renderApp(<ActiveBlock />, {}, NOW);
    expect(legacyClasses(loading.container)).toEqual([]);
    loading.unmount();
    const ended = renderApp(
      <ActiveBlock />,
      blocksState({ blocks: { key: "7|claude", report: { ...REPORT, blocks: [PAST] } } }),
      NOW,
    );
    expect(legacyClasses(ended.container)).toEqual([]);
  });

  it("offers the model-scope toggle and asks the controller for the wider one", async () => {
    const view = renderApp(<ActiveBlock />, blocksState(), NOW);
    await userEvent.click(screen.getByRole("button", { name: "All models" }));
    expect(view.act).toHaveBeenCalledWith("blocks-models", "all");
  });
});

describe("blockWhen", () => {
  it("spells a window's clock ends, the date only when it did not start today", () => {
    const day = new Date(PAST.start).toLocaleDateString("en-US", { month: "short", day: "numeric" });
    expect(blockWhen(block(), NOW)).toBe(
      `${new Date(start).toTimeString().slice(0, 5)} – ${new Date(end).toTimeString().slice(0, 5)}`,
    );
    expect(blockWhen(PAST, NOW)).toBe(
      `${day} ${new Date(PAST.start).toTimeString().slice(0, 5)} – ${new Date(PAST.end).toTimeString().slice(0, 5)}`,
    );
  });
});

describe("BlocksView", () => {
  it("lists every window newest first, the live one marked", () => {
    const { container } = renderApp(<BlocksView />, blocksState(), NOW);
    const rows = container.querySelectorAll("[data-blocks-table] tbody tr");
    expect(rows).toHaveLength(2);
    expect(rows[0]?.getAttribute("class")).toContain(
      "bg-[color-mix(in_oklab,var(--ui-accent)_10%,transparent)]",
    );
    const first = rows[0]?.textContent ?? "";
    expect(first).toContain("3");
    expect(first).toContain("8.5M");
    expect(first).toContain("$5.40");
    expect(first).toContain("4.25M/h");
    expect(first).toContain("12.8M");
    const second = rows[1]?.textContent ?? "";
    expect(second).toContain("–"); // an ended window has no burn and no projection
    expect(container.textContent).toContain("2 in the last 7 days");
  });

  it("offers the 7 and 30-day windows and asks the controller for the other one", async () => {
    const view = renderApp(<BlocksView />, blocksState(), NOW);
    await userEvent.click(
      screen.getByRole("group", { name: "Window" }).querySelector("button[data-value='30']") ?? document.body,
    );
    expect(view.act).toHaveBeenCalledWith("blocks-days", "30");
  });

  it("says what an empty window needs and what the estimate is worth", () => {
    const empty = renderApp(
      <BlocksView />,
      blocksState({ blocks: { key: "7|claude", report: { blocks: [], approximate: false, unpriced: 0 } } }),
      NOW,
    );
    expect(screen.getByText("No blocks in this window")).toBeTruthy();
    expect(empty.container.textContent).toContain("No Claude-model requests");
    expect(empty.container.textContent).toContain("Estimates at API list price");
  });

  it("waits with a skeleton and names a failure when nothing else can show", () => {
    const loading = renderApp(<BlocksView />, {}, NOW);
    expect(loading.container.querySelector("[role='status']")).toBeTruthy();
    const failed = renderApp(<BlocksView />, { blocksError: "GET /api/blocks?days=7 → 503" }, NOW);
    expect(failed.container.querySelector("[role='alert']")?.textContent).toContain("503");
  });
});
