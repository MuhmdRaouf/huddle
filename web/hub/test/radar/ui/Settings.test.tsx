import { screen, within } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vite-plus/test";
import { SettingsView } from "../../../src/radar/app/views/Settings.tsx";
import type { Budget, BudgetSpend } from "../../../src/radar/shared/api.ts";
import { draftOf, newDraft } from "../../../src/radar/state.ts";
import { uiFixtures } from "../helpers.ts";
import { renderApp } from "./render.tsx";
import { expectTokensOnly } from "./tokens.ts";

// every render in this file paints only token classes and labels its table cells for the phone
afterEach(() => expectTokensOnly());

const NOW = 1_790_000_000_000;
const fixtures = uiFixtures();

const TOTAL_DAY: Budget = { id: "t1", scope: "total", period: "day", limitUsd: 40, action: "warn" };
const ZAI_MONTH: Budget = { id: "b1", scope: "provider:zai", period: "month", limitUsd: 30, action: "stop" };

const spend = (overrides: Partial<BudgetSpend>): BudgetSpend => ({ ...fixtures.budgetSpend, ...overrides });

/** The sheet the draft opens, failing when none is. */
const openSheet = (): HTMLElement => {
  const sheet = document.querySelector("dialog[open].ui-sheet");
  expect(sheet).not.toBeNull();
  return sheet as HTMLElement;
};

/** The page's own section headings, in paint order. */
const sectionHeadings = (container: Element): string[] =>
  [...container.querySelectorAll("section > h2")].map((h) => h.textContent ?? "");

describe("Spending page sections", () => {
  it("orders the sections as the mock does, with Today only for a per-day total budget", () => {
    const withToday = renderApp(<SettingsView />, { budgets: [TOTAL_DAY, ZAI_MONTH] }, NOW);
    expect(sectionHeadings(withToday.container)).toEqual(["Today", "Budgets", "Alerts", "History", "Data"]);
    withToday.unmount();
    const without = renderApp(<SettingsView />, { budgets: [ZAI_MONTH] }, NOW);
    expect(sectionHeadings(without.container)).toEqual(["Budgets", "Alerts", "History", "Data"]);
  });
});

describe("Spending Today", () => {
  it("shows the day's spend, the bar and the reset note against a per-day total budget", () => {
    renderApp(
      <SettingsView />,
      {
        budgets: [TOTAL_DAY],
        budgetStatus: {
          version: 1,
          updatedAt: NOW,
          stopped: [],
          spend: [spend({ id: "t1", pct: 31, spentUsd: 12.4 })],
        },
        spendToday: 12.4,
      },
      NOW,
    );
    expect(screen.getByText("$12.40")).toBeTruthy();
    expect(screen.getByText("of $40.00")).toBeTruthy();
    const bar = screen.getByRole("progressbar", { name: "Today's spend" });
    expect(bar.getAttribute("aria-valuenow")).toBe("31");
    expect(bar.getAttribute("aria-valuemin")).toBe("0");
    expect(bar.getAttribute("aria-valuemax")).toBe("100");
    expect(screen.getByText("Resets at midnight.")).toBeTruthy();
  });

  it("stays away without a per-day total budget, even while budgets load", () => {
    const none = renderApp(<SettingsView />, { budgets: [ZAI_MONTH] }, NOW);
    expect(screen.queryByRole("progressbar", { name: "Today's spend" })).toBeNull();
    expect(none.container.textContent).not.toContain("Resets at midnight");
    none.unmount();
    const loading = renderApp(<SettingsView />, {}, NOW);
    expect(loading.container.textContent).not.toContain("Resets at midnight");
  });
});

describe("Spending budgets", () => {
  it("renders each saved budget as one row: plate, label, period line, Stop chip, limit, chevron", () => {
    renderApp(
      <SettingsView />,
      {
        budgets: [TOTAL_DAY, ZAI_MONTH],
        budgetStatus: {
          version: 1,
          updatedAt: NOW,
          stopped: [],
          spend: [spend({ id: "t1", pct: 31 })],
        },
      },
      NOW,
    );
    const total = screen.getByRole("button", { name: /Per day · warn at 80% and 100%/ });
    expect(within(total).getByText("Total")).toBeTruthy();
    expect(within(total).getByText("$40.00")).toBeTruthy();
    expect(total.querySelector('[data-icon="gauge"]')).not.toBeNull();
    expect(total.querySelector('[data-icon="chevronRight"]')).not.toBeNull();
    const zai = screen.getByRole("button", { name: /Per month · stop at 100%/ });
    expect(within(zai).getByText("Z.ai")).toBeTruthy();
    expect(within(zai).getByText("Stop")).toBeTruthy();
    expect(within(zai).getByText("$30.00")).toBeTruthy();
    expect(zai.querySelector('[data-icon="bot"]')).not.toBeNull();
  });

  it("reports edit-budget with the budget's id from a tapped row", async () => {
    const { act } = renderApp(<SettingsView />, { budgets: [ZAI_MONTH] }, NOW);
    await userEvent.click(screen.getByRole("button", { name: /Per month · stop at 100%/ }));
    expect(act).toHaveBeenCalledWith("edit-budget", "b1");
  });

  it("shows the edit sheet with the draft's choices and a Remove budget that reports the id", async () => {
    const { act } = renderApp(
      <SettingsView />,
      { budgets: [ZAI_MONTH], providers: ["zai"], draft: draftOf(ZAI_MONTH) },
      NOW,
    );
    const sheet = openSheet();
    expect(within(sheet).getByRole("heading", { name: "Edit budget" })).toBeTruthy();
    expect((within(sheet).getByRole("combobox", { name: "Scope" }) as HTMLSelectElement).value).toBe(
      "provider:zai",
    );
    expect((within(sheet).getByRole("combobox", { name: "Period" }) as HTMLSelectElement).value).toBe(
      "month",
    );
    expect((within(sheet).getByRole("combobox", { name: "At 100%" }) as HTMLSelectElement).value).toBe(
      "stop",
    );
    expect(
      (within(sheet).getByRole("textbox", { name: "Limit in US dollars" }) as HTMLInputElement).value,
    ).toBe("30");
    await userEvent.click(within(sheet).getByRole("button", { name: "Remove budget" }));
    expect(act).toHaveBeenCalledWith("remove-budget", "b1");
  });

  it("reports new-budget from the add row", async () => {
    const { act } = renderApp(<SettingsView />, { budgets: [TOTAL_DAY] }, NOW);
    await userEvent.click(screen.getByRole("button", { name: "Add a budget" }));
    expect(act).toHaveBeenCalledWith("new-budget");
  });

  it("shows the add sheet with an empty form and no Remove budget", () => {
    renderApp(<SettingsView />, { budgets: [TOTAL_DAY], providers: ["zai"], draft: newDraft(["zai"]) }, NOW);
    const sheet = openSheet();
    expect(within(sheet).getByRole("heading", { name: "Add a budget" })).toBeTruthy();
    expect((within(sheet).getByRole("combobox", { name: "Scope" }) as HTMLSelectElement).value).toBe(
      "provider:zai",
    );
    expect(
      (within(sheet).getByRole("textbox", { name: "Limit in US dollars" }) as HTMLInputElement).value,
    ).toBe("10");
    expect(within(sheet).queryByRole("button", { name: "Remove budget" })).toBeNull();
  });

  it("reports draft-field as field=value for every control, and saves on Enter", async () => {
    const { act } = renderApp(
      <SettingsView />,
      { budgets: [], providers: ["zai"], draft: { ...newDraft(["zai"]), scope: "provider:acme" } },
      NOW,
    );
    const sheet = openSheet();
    await userEvent.selectOptions(within(sheet).getByRole("combobox", { name: "Scope" }), "provider:zai");
    expect(act).toHaveBeenCalledWith("draft-field", "scope=provider:zai");
    await userEvent.selectOptions(within(sheet).getByRole("combobox", { name: "Period" }), "week");
    expect(act).toHaveBeenCalledWith("draft-field", "period=week");
    await userEvent.selectOptions(within(sheet).getByRole("combobox", { name: "At 100%" }), "stop");
    expect(act).toHaveBeenCalledWith("draft-field", "action=stop");
    const limit = within(sheet).getByRole("textbox", { name: "Limit in US dollars" }) as HTMLInputElement;
    await userEvent.clear(limit);
    await userEvent.type(limit, "7.5");
    expect(act).toHaveBeenLastCalledWith("draft-field", "limit=7.5");
    await userEvent.type(limit, "{Enter}");
    expect(act).toHaveBeenCalledWith("save-budget");
  });

  it("saves and cancels from the form's buttons, and closing the sheet cancels", async () => {
    const { act } = renderApp(
      <SettingsView />,
      { budgets: [], providers: ["zai"], draft: newDraft(["zai"]) },
      NOW,
    );
    const sheet = openSheet();
    await userEvent.click(within(sheet).getByRole("button", { name: "Add budget" }));
    expect(act).toHaveBeenCalledWith("save-budget");
    await userEvent.click(within(sheet).getByRole("button", { name: "Cancel" }));
    expect(act).toHaveBeenCalledWith("cancel-budget");
    await userEvent.click(within(sheet).getByRole("button", { name: "Close" }));
    expect(act).toHaveBeenCalledWith("cancel-budget");
    expect(document.querySelector("dialog[open].ui-sheet")).toBeNull();
  });

  it("keeps today's words for the empty state, the skeleton and the notes", () => {
    const loading = renderApp(<SettingsView />, {}, NOW);
    expect(loading.container.querySelector('[role="status"][aria-label="Loading budgets"]')).not.toBeNull();
    loading.unmount();
    const { container } = renderApp(<SettingsView />, { budgets: [] }, NOW);
    expect(container.textContent).toContain("No budgets yet");
    expect(container.textContent).toContain("Set a limit for everything or for one provider");
    expect(container.textContent).toContain(
      "A stop budget makes that provider's requests wait until the period ends. Claude requests are never stopped.",
    );
    expect(container.textContent).toContain("Estimates at API list price");
    expect(container.textContent).toContain("A Claude subscription may cover Claude usage");
  });

  it("shows a save problem or confirmation as a notice", () => {
    renderApp(<SettingsView />, { budgets: [], formMessage: { tone: "err", text: "Enter a limit" } }, NOW);
    expect(screen.getByRole("alert").textContent).toContain("Enter a limit");
    // a confirmation announces politely: the notice is a status, not an alert
    renderApp(<SettingsView />, { budgets: [], formMessage: { tone: "ok", text: "Budget saved" } }, NOW);
    expect(screen.getByText("Budget saved").closest('[role="status"]')).not.toBeNull();
  });
});

describe("Spending alerts", () => {
  it("starts on and runs the toggle action when clicked", async () => {
    const { act } = renderApp(<SettingsView />, { budgets: [] }, NOW);
    const switchEl = screen.getByRole("switch", { name: "Desktop notifications" });
    expect(switchEl.getAttribute("aria-checked")).toBe("true");
    await userEvent.click(switchEl);
    expect(act).toHaveBeenCalledWith("toggle-notifications");
  });

  it("shows off when notifications are disabled", () => {
    renderApp(
      <SettingsView />,
      { budgets: [], settings: { notifications: false, historyRetentionDays: 30 } },
      NOW,
    );
    expect(screen.getByRole("switch", { name: "Desktop notifications" }).getAttribute("aria-checked")).toBe(
      "false",
    );
  });
});

describe("Spending history", () => {
  const STATS = { bytes: 49_152, nodes: 3, requests: 421, roots: 2, retentionDays: 30 };

  it("shows a skeleton before the stats arrive, and why history is off when it is", () => {
    const loading = renderApp(<SettingsView />, { budgets: [] }, NOW);
    expect(
      loading.container.querySelector('[role="status"][aria-label="Reading the history store"]'),
    ).not.toBeNull();
    loading.unmount();
    const off = renderApp(<SettingsView />, { budgets: [], historyOff: true }, NOW);
    expect(off.container.textContent).toContain("History is off on this machine");
  });

  it("offers today's retention options as the radio group and runs the retention action", async () => {
    const { act } = renderApp(<SettingsView />, { budgets: [], historyStats: STATS }, NOW);
    const group = screen.getByRole("radiogroup", { name: "Keep history" });
    const radios = within(group).getAllByRole("radio") as HTMLInputElement[];
    expect(radios.map((r) => r.value)).toEqual(["7", "30", "90", "0"]);
    expect(radios.find((r) => r.checked)?.value).toBe("30");
    await userEvent.click(screen.getByLabelText("Forever"));
    expect(act).toHaveBeenCalledWith("retention", "0");
    await userEvent.click(screen.getByLabelText("7 days"));
    expect(act).toHaveBeenCalledWith("retention", "7");
  });

  it("notes where history lives", () => {
    const { container } = renderApp(<SettingsView />, { budgets: [], historyStats: STATS }, NOW);
    expect(container.textContent).toContain("Kept in ~/.agents/radar/history.db");
    expect(container.textContent).toContain("Older rows are pruned hourly; clearing cannot be undone.");
  });

  it("arms clear history before wiping, and both steps run through the one action", async () => {
    const { act } = renderApp(<SettingsView />, { budgets: [], historyStats: STATS }, NOW);
    await userEvent.click(screen.getByRole("button", { name: "Clear history" }));
    expect(act).toHaveBeenCalledWith("clear-history");
    const armed = renderApp(<SettingsView />, { budgets: [], historyStats: STATS, clearConfirm: true }, NOW);
    expect(armed.container.textContent).toContain("Delete every stored session, request and its content?");
    await userEvent.click(screen.getByRole("button", { name: "Clear now" }));
    expect(armed.act).toHaveBeenCalledWith("clear-history", "confirm");
    armed.unmount();
    const cancelling = renderApp(
      <SettingsView />,
      { budgets: [], historyStats: STATS, clearConfirm: true },
      NOW,
    );
    await userEvent.click(screen.getByRole("button", { name: "Keep it" }));
    expect(cancelling.act).toHaveBeenCalledWith("clear-history", "cancel");
    cancelling.unmount();
  });
});

describe("Spending data", () => {
  it("says where Radar reads and keeps what it shows", () => {
    const { container } = renderApp(<SettingsView />, { budgets: [] }, NOW);
    expect(container.textContent).toContain(
      "Claude Code's own session files under ~/.claude/projects, read as they are written.",
    );
    expect(container.textContent).toContain(
      "~/.agents/radar/history.db keeps past sessions once their live files close.",
    );
    expect(container.textContent).toContain("Budgets and notification settings live in ~/.agents/radar.");
  });
});
