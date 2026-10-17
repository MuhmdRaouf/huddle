import { fireEvent, screen, within } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { describe, expect, it } from "vite-plus/test";
import type { UsageDay, UsageReport, UsageSlice, UsageTotals } from "../../src/shared/usage.ts";
import {
  dayRows,
  processedTokens,
  stepDay,
  unpricedModels,
  UsageView,
  usageSeries,
} from "../../src/ui/app/views/Usage.tsx";
import type { ClientState } from "../../src/ui/state.ts";
import { renderApp } from "./render.tsx";

const NOW = 1_793_000_000_000;

const DATES = [
  "2026-10-11",
  "2026-10-12",
  "2026-10-13",
  "2026-10-14",
  "2026-10-15",
  "2026-10-16",
  "2026-10-17",
];

const day = (date: string, over: Partial<Omit<UsageDay, "date">> = {}): UsageDay => ({
  date,
  requests: 0,
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  costUsd: 0,
  savingsUsd: 0,
  ...over,
});

const FLASH_DAY = day("2026-10-12", { requests: 1, input: 10, output: 5 });
const GLM_DAY = day("2026-10-17", {
  requests: 3,
  input: 2400,
  output: 600,
  cacheRead: 5000,
  cacheWrite: 2000,
  costUsd: 5.4,
  savingsUsd: 0.25,
});

const TOTALS: UsageTotals = {
  requests: 4,
  input: 2410,
  output: 605,
  cacheRead: 5000,
  cacheWrite: 2000,
  costUsd: 5.4,
  savingsUsd: 0.25,
};

const GLM: UsageSlice = {
  key: "glm-5.3",
  requests: 3,
  input: 2400,
  output: 600,
  cacheRead: 5000,
  cacheWrite: 2000,
  costUsd: 5.4,
  savingsUsd: 0.25,
  days: DATES.map((date) => day(date)).with(6, GLM_DAY),
};

const FLASH: UsageSlice = {
  key: "glm-5.3-flash",
  requests: 1,
  input: 10,
  output: 5,
  cacheRead: 0,
  cacheWrite: 0,
  costUsd: 0,
  savingsUsd: 0,
  days: DATES.map((date) => day(date)).with(1, FLASH_DAY),
};

const REPORT_DAYS: UsageDay[] = DATES.map((date) =>
  day(date, date === "2026-10-12" ? { requests: 1, input: 10, output: 5 } : {}),
).with(6, GLM_DAY);

const PROJECT: UsageSlice = { key: "/w/e2e", ...TOTALS, days: REPORT_DAYS };

const REPORT: UsageReport = {
  from: "2026-10-11",
  to: "2026-10-17",
  days: REPORT_DAYS,
  totals: TOTALS,
  byModel: [GLM, FLASH],
  byProject: [PROJECT],
  byKind: [{ key: "main", ...TOTALS, days: REPORT_DAYS }],
  topSessions: {
    "/w/e2e": [
      {
        sessionId: "s-1",
        label: "seed the dashboard",
        project: "/w/e2e",
        lastTs: NOW,
        requests: 3,
        input: 2400,
        output: 600,
        cacheRead: 5000,
        cacheWrite: 2000,
        costUsd: 5.4,
        savingsUsd: 0.25,
      },
      {
        sessionId: "i-23456789",
        label: "",
        project: "/w/e2e",
        lastTs: NOW - 60_000,
        requests: 1,
        input: 10,
        output: 5,
        cacheRead: 0,
        cacheWrite: 0,
        costUsd: 0,
        savingsUsd: 0,
      },
    ],
  },
  approximate: true,
  unpriced: 1,
  pricesRetrieved: "2026-10-01",
};

/** A window where every model is priced: no wave, no note. */
const PRICED: UsageReport = {
  ...REPORT,
  approximate: false,
  unpriced: 0,
  byModel: [GLM],
  totals: {
    requests: 3,
    input: 2400,
    output: 600,
    cacheRead: 5000,
    cacheWrite: 2000,
    costUsd: 5.4,
    savingsUsd: 0.25,
  },
};

const EMPTY: UsageReport = {
  from: "2026-10-11",
  to: "2026-10-17",
  days: DATES.map((date) => day(date)),
  totals: { requests: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, costUsd: 0, savingsUsd: 0 },
  byModel: [],
  byProject: [],
  byKind: [],
  topSessions: {},
  approximate: false,
  unpriced: 0,
  pricesRetrieved: "2026-10-01",
};

function usageState(over: Partial<ClientState> = {}): Partial<ClientState> {
  return { usage: { key: 7, report: REPORT }, usageDays: 7, ...over };
}

describe("processedTokens", () => {
  it("sums every token kind the report counts", () => {
    expect(processedTokens({ input: 1, output: 2, cacheRead: 3, cacheWrite: 4 })).toBe(10);
  });
});

describe("usageSeries", () => {
  it("builds one zero-filled series per visible model, in the report's order", () => {
    const data = usageSeries(REPORT, "tokens", []);
    expect(data.keys).toEqual(["glm-5.3", "glm-5.3-flash"]);
    expect(data.days.map((entry) => entry.date)).toEqual(DATES);
    expect(data.values[0]).toEqual([0, 0, 0, 0, 0, 0, 10_000]);
    expect(data.values[1]).toEqual([0, 15, 0, 0, 0, 0, 0]);
  });

  it("drops the hidden series and switches its values with the metric", () => {
    expect(usageSeries(REPORT, "tokens", ["glm-5.3"]).keys).toEqual(["glm-5.3-flash"]);
    const cost = usageSeries(REPORT, "cost", []);
    expect(cost.values[0]).toEqual([0, 0, 0, 0, 0, 0, 5.4]);
    expect(cost.values[1]).toEqual([0, 0, 0, 0, 0, 0, 0]);
  });
});

describe("dayRows", () => {
  it("splits a picked day over the models that ran, biggest share first", () => {
    const twin: UsageReport = {
      ...REPORT,
      byModel: [
        { ...GLM, days: DATES.map((date) => day(date)).with(6, day("2026-10-17", { input: 7500 })) },
        { ...FLASH, days: DATES.map((date) => day(date)).with(6, day("2026-10-17", { input: 2500 })) },
      ],
    };
    expect(dayRows(twin, "2026-10-17", "tokens", [])).toEqual([
      { key: "glm-5.3", value: 7500, share: 75 },
      { key: "glm-5.3-flash", value: 2500, share: 25 },
    ]);
    expect(dayRows(twin, "2026-10-17", "tokens", ["glm-5.3-flash"])).toEqual([
      { key: "glm-5.3", value: 7500, share: 100 },
    ]);
  });

  it("skips models with nothing on the day and answers nothing for an unpicked one", () => {
    expect(dayRows(REPORT, "2026-10-17", "tokens", [])).toEqual([
      { key: "glm-5.3", value: 10_000, share: 100 },
    ]);
    expect(dayRows(REPORT, "2026-10-12", "tokens", [])).toEqual([
      { key: "glm-5.3-flash", value: 15, share: 100 },
    ]);
    expect(dayRows(REPORT, "2026-10-14", "tokens", [])).toEqual([]);
  });

  it("reads a model with no recorded days as zero on every day", () => {
    const sparse: UsageReport = { ...REPORT, byModel: [GLM, { ...FLASH, days: [] }] };
    expect(dayRows(sparse, "2026-10-17", "tokens", [])).toEqual([
      { key: "glm-5.3", value: 10_000, share: 100 },
    ]);
  });
});

describe("unpricedModels", () => {
  it("names the models the window ran without a price", () => {
    expect(unpricedModels(REPORT)).toEqual(["glm-5.3-flash"]);
    expect(unpricedModels(PRICED)).toEqual([]);
  });
});

describe("stepDay", () => {
  it("steps within the window, starts at its nearer end and stops at both", () => {
    expect(stepDay(DATES, null, 1)).toBe("2026-10-11");
    expect(stepDay(DATES, null, -1)).toBe("2026-10-17");
    expect(stepDay(DATES, "2026-10-16", 1)).toBe("2026-10-17");
    expect(stepDay(DATES, "2026-10-17", 1)).toBe("2026-10-17");
    expect(stepDay(DATES, "2026-10-12", -1)).toBe("2026-10-11");
    expect(stepDay(DATES, "2026-10-11", -1)).toBe("2026-10-11");
    expect(stepDay(DATES, "nope", 1)).toBeNull();
    expect(stepDay([], null, 1)).toBeNull();
  });
});

describe("UsageView", () => {
  it("renders the five tiles from the report, the cache share among them", () => {
    const { container } = renderApp(<UsageView />, usageState(), NOW);
    const tiles = container.querySelector("[data-usage-tiles]")?.textContent ?? "";
    expect(tiles).toContain("Processed tokens");
    expect(tiles).toContain("10,015");
    expect(tiles).toContain("Cached input");
    expect(tiles).toContain("5,000");
    expect(tiles).toContain("53% of prompt tokens");
    expect(tiles).toContain("Uncached input");
    expect(tiles).toContain("2,410");
    expect(tiles).toContain("Output");
    expect(tiles).toContain("605");
    expect(tiles).toContain("Cache savings");
    expect(tiles).toContain("$0.25");
  });

  it("marks the estimates honestly: * for list price, ~ and the named unpriced models when approximate", () => {
    const { container } = renderApp(<UsageView />, usageState(), NOW);
    expect(screen.getAllByText("*").length).toBeGreaterThanOrEqual(2); // the cost tile's mode and the savings tile
    expect(container.querySelector("[data-usage-tiles]")?.textContent).toContain("~");
    const note = container.querySelector("[data-usage-note]")?.textContent ?? "";
    expect(note).toContain("1 request ran on a model without a price");
    expect(note).toContain("glm-5.3-flash");
    const clean = renderApp(<UsageView />, usageState({ usage: { key: 7, report: PRICED } }), NOW);
    expect(clean.container.textContent).not.toContain("without a price");
  });

  it("names the unpriced models even when the count did not reach the report, and reads a cache-free window as 0%", () => {
    const uncounted: UsageReport = { ...PRICED, approximate: true, unpriced: 0, byModel: [GLM, FLASH] };
    const note = renderApp(<UsageView />, usageState({ usage: { key: 7, report: uncounted } }), NOW);
    expect(note.container.querySelector("[data-usage-note]")?.textContent).toContain(
      "Some requests ran on models without a price: glm-5.3-flash",
    );
    const nameless: UsageReport = { ...PRICED, approximate: true, unpriced: 0 };
    const plain = renderApp(<UsageView />, usageState({ usage: { key: 7, report: nameless } }), NOW);
    expect(plain.container.querySelector("[data-usage-note]")?.textContent).toContain(
      "Some requests ran without a price",
    );
    const fresh: UsageReport = {
      ...PRICED,
      totals: { requests: 1, input: 0, output: 500, cacheRead: 0, cacheWrite: 0, costUsd: 0, savingsUsd: 0 },
    };
    const tiles = renderApp(<UsageView />, usageState({ usage: { key: 7, report: fresh } }), NOW);
    expect(tiles.container.querySelector("[data-usage-tiles]")?.textContent).toContain("0% of prompt tokens");
  });

  it("reads an all-unpriced window in cost mode honestly: no share, no percent", () => {
    const unpriced: UsageReport = {
      ...REPORT,
      approximate: true,
      unpriced: 4,
      byModel: [FLASH],
      totals: {
        requests: 4,
        input: 2410,
        output: 605,
        cacheRead: 5000,
        cacheWrite: 2000,
        costUsd: 0,
        savingsUsd: 0,
      },
    };
    const view = renderApp(
      <UsageView />,
      usageState({ usage: { key: 7, report: unpriced }, usageMetric: "cost" }),
      NOW,
    );
    expect(view.container.textContent).toContain("–");
    expect(view.container.querySelector(".chart-ymax")).toBeNull();
  });

  it("switches the tiles and the chart between tokens and cost", async () => {
    const view = renderApp(<UsageView />, usageState(), NOW);
    await userEvent.click(screen.getByRole("button", { name: "Cost" }));
    expect(view.act).toHaveBeenCalledWith("usage-metric", "cost");

    const cost = renderApp(<UsageView />, usageState({ usageMetric: "cost" }), NOW);
    const tiles = cost.container.querySelector("[data-usage-tiles]")?.textContent ?? "";
    expect(tiles).toContain("Est. cost");
    expect(tiles).toContain("$5.40");
    expect(tiles).toContain("10,015"); // the token count moves to the tile's subline
    expect(cost.container.querySelector(".chart-ymax")?.textContent).toBe("$5.40 per day");
    expect(view.container.querySelector(".chart-ymax")?.textContent).toBe("10k per day");
  });

  it("offers the 7/30/90-day windows and asks the controller for the chosen one", async () => {
    const view = renderApp(<UsageView />, usageState(), NOW);
    await userEvent.click(
      within(screen.getByRole("group", { name: "Window" })).getByRole("button", { name: "30 days" }),
    );
    expect(view.act).toHaveBeenCalledWith("usage-days", "30");
    const seven = renderApp(<UsageView />, usageState({ usageDays: 7 }), NOW);
    expect(
      seven.container
        .querySelector("[role='group'][aria-label='Window']")
        ?.querySelector("[aria-pressed='true']")?.textContent,
    ).toContain("7 days");
  });

  it("shows the picked day's split and clears it again", () => {
    const picked = renderApp(<UsageView />, usageState({ usageDay: "2026-10-17" }), NOW);
    const card = picked.container.querySelector("[data-usage-day]")?.textContent ?? "";
    expect(card).toContain("17 Oct");
    expect(card).toContain("10,000");
    expect(card).toContain("GLM 5.3");
    expect(picked.container.querySelector("[data-usage-day]")).toBeTruthy();
    const bare = renderApp(<UsageView />, usageState(), NOW);
    expect(bare.container.querySelector("[data-usage-day]")).toBeNull();
  });

  it("splits a day in cost mode, and says when nothing ran on the picked date", () => {
    const cost = renderApp(<UsageView />, usageState({ usageDay: "2026-10-17", usageMetric: "cost" }), NOW);
    const card = cost.container.querySelector("[data-usage-day]")?.textContent ?? "";
    expect(card).toContain("$5.40");
    expect(card).toContain("100%");
    const missing = renderApp(<UsageView />, usageState({ usageDay: "1999-01-01" }), NOW);
    expect(missing.container.querySelector("[data-usage-day]")?.textContent).toContain(
      "Nothing ran that day",
    );
  });

  it("moves the picked day with the arrow keys and clears with Escape", async () => {
    const view = renderApp(<UsageView />, usageState({ usageDay: "2026-10-16" }), NOW);
    const plot = screen.getByRole("img", { name: /daily usage/i });
    await userEvent.click(plot);
    await userEvent.keyboard("{Escape}");
    expect(view.act).toHaveBeenCalledWith("usage-day", "");
    await userEvent.keyboard("{ArrowRight}");
    expect(view.act).toHaveBeenCalledWith("usage-day", "2026-10-17");
    await userEvent.keyboard("{ArrowLeft}");
    expect(view.act).toHaveBeenCalledWith("usage-day", "2026-10-15");
    // any other key leaves the pick alone
    const before = view.act.mock.calls.length;
    await userEvent.keyboard("a");
    expect(view.act.mock.calls.length).toBe(before);
  });

  it("lists the series as share rows that hide on click, and refuses to hide the last visible one", async () => {
    const view = renderApp(<UsageView />, usageState(), NOW);
    const rows = screen.getAllByRole("button", { name: /^Hide GLM/ });
    expect(rows).toHaveLength(2);
    expect(rows[0]?.textContent).toContain("10,000");
    await userEvent.click(rows[0] as HTMLElement);
    expect(view.act).toHaveBeenCalledWith("usage-hide", "glm-5.3");

    const single = renderApp(<UsageView />, usageState({ usage: { key: 7, report: PRICED } }), NOW);
    const only = single.container.querySelector<HTMLButtonElement>("button[aria-label='Hide GLM 5.3']");
    expect(only?.disabled).toBe(true);

    const hidden = renderApp(<UsageView />, usageState({ usageHidden: ["glm-5.3"] }), NOW);
    const shown = hidden.container.querySelector<HTMLButtonElement>("button[aria-label='Show GLM 5.3']");
    expect(shown?.getAttribute("aria-pressed")).toBe("true");
    expect(shown?.getAttribute("aria-label")).toBe("Show GLM 5.3");
  });

  it("opens a model's drill-down from its table row, and closes it again", async () => {
    const view = renderApp(<UsageView />, usageState(), NOW);
    await userEvent.click(screen.getByRole("cell", { name: "GLM 5.3 Flash" }));
    expect(view.act).toHaveBeenCalledWith(
      "usage-drill",
      JSON.stringify({ kind: "model", key: "glm-5.3-flash" }),
    );

    const open = renderApp(
      <UsageView />,
      usageState({ usageDrill: { kind: "model", key: "glm-5.3-flash" } }),
      NOW,
    );
    expect(open.container.textContent).toContain("GLM 5.3 Flash");
    const drill = open.container.querySelector("[data-usage-drill]");
    expect(drill?.querySelectorAll("path.chart-line")).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Close the drill-down" }));
    expect(open.act).toHaveBeenCalledWith("usage-drill-close");

    // an all-zero window (the flash model in cost mode) draws the line without a peak label
    const flat = renderApp(
      <UsageView />,
      usageState({ usageMetric: "cost", usageDrill: { kind: "model", key: "glm-5.3-flash" } }),
      NOW,
    );
    expect(flat.container.querySelector("[data-usage-drill] .chart-ymax")).toBeNull();
  });

  it("drills into a project and copies the resume command for its top session", () => {
    const texts: string[] = [];
    Object.defineProperty(window.navigator, "clipboard", {
      value: { writeText: (text: string) => (texts.push(text), Promise.resolve()) },
      configurable: true,
    });
    const view = renderApp(
      <UsageView />,
      usageState({ usageDrill: { kind: "project", key: "/w/e2e" } }),
      NOW,
    );
    expect(view.container.textContent).toContain("seed the dashboard");
    expect(view.container.textContent).toContain("i-234567"); // a session with no label reads by its id prefix
    fireEvent.click(screen.getByRole("button", { name: "Copy the resume command for i-234567" }));
    expect(texts).toEqual(["claude --resume i-23456789"]);
    Object.defineProperty(window.navigator, "clipboard", { value: undefined, configurable: true });
  });

  it("closes a drill-down whose slice left the report, and shows a sessionless project without a table", () => {
    const gone = renderApp(<UsageView />, usageState({ usageDrill: { kind: "model", key: "gpt-9" } }), NOW);
    expect(gone.container.querySelector("[data-usage-drill]")).toBeNull();
    const bare = renderApp(
      <UsageView />,
      usageState({
        usage: { key: 7, report: { ...REPORT, topSessions: {} } },
        usageDrill: { kind: "project", key: "/w/e2e" },
      }),
      NOW,
    );
    expect(bare.container.querySelector("[data-usage-drill]")).toBeTruthy();
    expect(bare.container.querySelector("table[data-key='usage-sessions']")).toBeNull();
  });

  it("says loading while the report is on its way, and names a failure", () => {
    const loading = renderApp(<UsageView />, {}, NOW);
    expect(loading.container.querySelector("[role='status']")).toBeTruthy();
    const failed = renderApp(<UsageView />, { usageError: "GET /api/usage?days=30 → 503" }, NOW);
    expect(failed.container.querySelector("[role='alert']")?.textContent).toContain("503");
  });

  it("shows an empty window when nothing ran", () => {
    renderApp(<UsageView />, usageState({ usage: { key: 7, report: EMPTY } }), NOW);
    expect(screen.getByText("No usage in this window")).toBeTruthy();
  });

  it("carries a table alternative for the chart", () => {
    const { container } = renderApp(<UsageView />, usageState(), NOW);
    const table = container.querySelector("table[data-usage-table]");
    expect(table?.querySelectorAll("tbody tr")).toHaveLength(7);
    expect(table?.textContent).toContain("10,000");
    // a report without window ends reads without a span in the panel's head
    const nameless = renderApp(
      <UsageView />,
      usageState({ usage: { key: 7, report: { ...REPORT, from: "", to: "" } } }),
      NOW,
    );
    expect(nameless.container.textContent).toContain("Daily usage");
  });
});
