// screens.spec.ts — the screenshot suite: every hub page and drawer, on each project's width (the
// phone at 390, the tablet at 820, the desk at 1440), in the light scheme and then the dark one.
// Each shot lands under the run's output dir in screens/<project>/<page>-<scheme>.png, and each
// page must not scroll sideways and must pass axe; on a touch project every control is a target
// of at least 44 px. A part whose markup still paints daisyUI or legacy classes (a page, or the
// shell's chrome as "shell") sits in LEGACY_PAGES; every other part must paint none of them.
// What a page still fails today sits in KNOWN (the shell's share in SHELL_KNOWN), each entry
// named by the card that fixes it, so a new failure fails the run while the known ones wait for
// their card. The rows the pages show are seeded once per project, under ids that carry the
// project's name.

import { expect, test, type Page, type TestInfo } from "@playwright/test";
import path from "node:path";
import { axeViolations } from "./axe.ts";
import { api, expectTapTargets, n, SEEDED, signIn, theSetup } from "./helpers.ts";

/** Every page and drawer the suite shoots. */
type PageId =
  | "landing"
  | "now"
  | "session"
  | "needs"
  | "channels"
  | "channel"
  | "channel-inbox"
  | "channel-team"
  | "channel-work"
  | "session-drawer"
  | "task-drawer"
  | "settings-appearance"
  | "settings-notifications"
  | "settings-devices"
  | "settings-hub"
  | "radar-overview"
  | "radar-costs"
  | "radar-tools"
  | "radar-requests"
  | "radar-inspector"
  | "kanban"
  | "kanban-panel";

/** The parts whose markup still paints daisyUI or legacy classes: the shell's chrome, and the
 *  pages. Each HV-TOKENS card moves its part onto the tokens and removes the part's entry here,
 *  which turns the legacy check on for it. */
const LEGACY_PAGES: readonly (PageId | "shell")[] = [
  "shell",
  "landing",
  "now",
  "session",
  "needs",
  "channels",
  "channel",
  "channel-inbox",
  "channel-team",
  "channel-work",
  "session-drawer",
  "task-drawer",
  "settings-appearance",
  "settings-notifications",
  "settings-devices",
  "settings-hub",
  "radar-overview",
  "radar-costs",
  "radar-tools",
  "radar-requests",
  "radar-inspector",
  "kanban",
  "kanban-panel",
];

/** The page cards that move pages onto the tokens; each owns what its pages still fail. */
type TokensCard =
  | "HV-TOKENS-SHELL"
  | "HV-TOKENS-NOW"
  | "HV-TOKENS-NEEDS"
  | "HV-TOKENS-CHANNELS"
  | "HV-TOKENS-SETTINGS"
  | "HV-TOKENS-RADAR-OVERVIEW"
  | "HV-TOKENS-RADAR-TABLES"
  | "HV-TOKENS-INSPECTOR"
  | "HV-TOKENS-KANBAN";

/** The serious axe rules today's pages break. */
type AxeRule = "color-contrast" | "label-content-name-mismatch" | "scrollable-region-focusable";

/** What one page fails today: the axe rules that may fire on it, and whether its controls are
 *  still under 44 px on a touch width. The page's card fixes both and removes the entry. */
type Known = { card: TokensCard; axe?: readonly AxeRule[]; smallTargets?: true };

const KNOWN: Partial<Record<PageId, Known>> = {
  now: { card: "HV-TOKENS-NOW", axe: ["color-contrast", "scrollable-region-focusable"] },
  session: { card: "HV-TOKENS-NOW", axe: ["color-contrast", "scrollable-region-focusable"] },
  needs: { card: "HV-TOKENS-NEEDS", axe: ["color-contrast", "scrollable-region-focusable"] },
  channel: { card: "HV-TOKENS-CHANNELS", axe: ["label-content-name-mismatch"] },
  "channel-inbox": { card: "HV-TOKENS-CHANNELS", axe: ["label-content-name-mismatch"], smallTargets: true },
  "channel-team": {
    card: "HV-TOKENS-CHANNELS",
    axe: ["color-contrast", "label-content-name-mismatch"],
    smallTargets: true,
  },
  "channel-work": {
    card: "HV-TOKENS-CHANNELS",
    axe: ["color-contrast", "label-content-name-mismatch"],
    smallTargets: true,
  },
  "session-drawer": { card: "HV-TOKENS-CHANNELS", axe: ["color-contrast"], smallTargets: true },
  "task-drawer": { card: "HV-TOKENS-CHANNELS", smallTargets: true },
  "settings-notifications": { card: "HV-TOKENS-SETTINGS", smallTargets: true },
  "radar-overview": { card: "HV-TOKENS-RADAR-OVERVIEW", smallTargets: true },
  "radar-costs": { card: "HV-TOKENS-RADAR-TABLES", smallTargets: true },
  "radar-tools": { card: "HV-TOKENS-RADAR-TABLES", smallTargets: true },
  "radar-requests": { card: "HV-TOKENS-RADAR-TABLES", smallTargets: true },
  "radar-inspector": { card: "HV-TOKENS-INSPECTOR", axe: ["color-contrast"], smallTargets: true },
  kanban: { card: "HV-TOKENS-KANBAN", axe: ["label-content-name-mismatch"], smallTargets: true },
  "kanban-panel": { card: "HV-TOKENS-KANBAN", axe: ["label-content-name-mismatch"], smallTargets: true },
};

/** What the shell's own chrome fails on every page, by rule and the axe target its node starts
 *  with: the sidebar's channel switcher text in the dark scheme, and the tablet's Now tab whose
 *  name differs from its visible label. HV-TOKENS-SHELL fixes both and removes them. */
const SHELL_KNOWN: readonly { rule: AxeRule; node: string }[] = [
  { rule: "color-contrast", node: "#jump" },
  { rule: "label-content-name-mismatch", node: 'a[href$="#/now"]' },
];

/** The names a project's seeded rows go by: the project's name keeps the three projects' rows
 *  apart in one local run against one hub. */
type Seeded = { permit: string; task: string };

const seededFor = (project: string): Seeded => ({
  permit: `screens-permit-${project}`,
  task: `screens-task-${project}`,
});

/** One page the suite shoots: its id, and how a signed-in browser (a signed-out one for the
 *  landing) reaches it and knows it painted. */
type Screen = {
  id: PageId;
  signedIn: boolean;
  open: (page: Page, seeded: Seeded) => Promise<void>;
};

/** The hub's address for one hash route. */
const at = (hash: string): string => `${theSetup().rootUrl}/${hash}`;

/** A page the shell frames, reached by its own address. */
const route =
  (hash: (s: Seeded) => string) =>
  async (page: Page, seeded: Seeded): Promise<void> => {
    await page.goto(at(hash(seeded)));
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  };

/** A drawer over a page: the shared slide-over standing open. */
const drawer =
  (hash: (s: Seeded) => string) =>
  async (page: Page, seeded: Seeded): Promise<void> => {
    await route(hash)(page, seeded);
    await expect(page.locator("dialog[open] [data-slide-over]")).toBeVisible();
  };

/** A Settings group's page, open on its own address. */
const settings = (id: PageId, section: string): Screen => ({
  id,
  signedIn: true,
  open: async (page) => {
    await page.goto(at(`#/settings/${section}`));
    await expect(page.locator(`[data-settings][data-group="${section}"]`)).toBeVisible();
  },
});

/** A Radar view, its sub-tab the current one. */
const radar = (id: PageId, view: string, label: string): Screen => ({
  id,
  signedIn: true,
  open: async (page) => {
    await page.goto(at(`#/radar/${view}`));
    await expect(
      page.getByRole("navigation", { name: "Radar views" }).getByRole("link", { name: label, exact: true }),
    ).toHaveAttribute("aria-current", "page");
  },
});

/** Opens the requests table's row for the first seeded call. */
async function openFirstRequest(page: Page): Promise<void> {
  const first = SEEDED.requests[0];
  await page
    .getByRole("row")
    .filter({ hasText: n(first.input) })
    .filter({ hasText: n(first.output) })
    .click();
  await expect(page.locator("dialog[open] [data-slide-over]")).toBeVisible();
}

/** Opens the board's first fixture card; the phone's backlog keeps its P1 group closed, so the
 *  group opens first where the card is not in view. */
async function openFirstCard(page: Page): Promise<void> {
  const card = page.getByRole("button", { name: "E2E-1: First card" });
  await page.locator(".kanban-tab .card").first().waitFor();
  if (!(await card.isVisible())) {
    await page.locator('.lanes section[data-column="backlog"]').getByRole("button", { name: /^P1 / }).click();
  }
  await card.click();
  await expect(page.getByRole("dialog", { name: "E2E-1: First card" })).toBeVisible();
}

/** Every page, in the order a tour of the hub meets them. */
const SCREENS: readonly Screen[] = [
  {
    id: "landing",
    signedIn: false,
    open: async (page) => {
      await page.goto(at(""));
      await expect(page.locator("[data-landing]")).toBeVisible();
    },
  },
  { id: "now", signedIn: true, open: route(() => "#/now") },
  { id: "session", signedIn: true, open: route(() => `#/now/${SEEDED.session}`) },
  {
    id: "needs",
    signedIn: true,
    open: async (page, seeded) => {
      await route((s) => `#/now/${s.permit}`)(page, seeded);
      await expect(page.locator("[data-needs-sheet], [data-needs-bar]")).toBeVisible();
    },
  },
  { id: "channels", signedIn: true, open: route(() => "#/channels") },
  {
    id: "channel",
    signedIn: true,
    open: async (page, seeded) => {
      await route(() => "#/channels/e2e")(page, seeded);
      await expect(page.locator("[data-messages]")).toBeVisible();
    },
  },
  { id: "channel-inbox", signedIn: true, open: route(() => "#/channels/e2e/inbox") },
  { id: "channel-team", signedIn: true, open: route(() => "#/channels/e2e/team") },
  { id: "channel-work", signedIn: true, open: route(() => "#/channels/e2e/work") },
  { id: "session-drawer", signedIn: true, open: drawer(() => "#/channels/e2e/team?s=alpha") },
  { id: "task-drawer", signedIn: true, open: drawer((s) => `#/channels/e2e/work?t=${s.task}`) },
  settings("settings-appearance", "appearance"),
  settings("settings-notifications", "notifications"),
  settings("settings-devices", "devices"),
  settings("settings-hub", "hub"),
  radar("radar-overview", "overview", "Overview"),
  radar("radar-costs", "costs", "Costs"),
  radar("radar-tools", "tools", "Tools"),
  radar("radar-requests", "requests", "Requests"),
  {
    id: "radar-inspector",
    signedIn: true,
    open: async (page, seeded) => {
      await radar("radar-requests", "requests", "Requests").open(page, seeded);
      await openFirstRequest(page);
    },
  },
  {
    id: "kanban",
    signedIn: true,
    open: async (page, seeded) => {
      await route(() => "#/kanban")(page, seeded);
      await page.locator(".kanban-tab .card").first().waitFor();
    },
  },
  {
    id: "kanban-panel",
    signedIn: true,
    open: async (page, seeded) => {
      await route(() => "#/kanban")(page, seeded);
      await openFirstCard(page);
    },
  },
];

/** The two schemes each page is shot in, light first. */
const SCHEMES = ["light", "dark"] as const;

type Scheme = (typeof SCHEMES)[number];

/** The class names that mark daisyUI or legacy markup: the component classes and the theme's
 *  colour roles, after any variant prefix (`hover:`, `md:`) is stripped. */
const LEGACY_CLASS: readonly RegExp[] = [
  /^btn(-[\w-]+)?$/,
  /^tabs?(-[\w-]+)?$/,
  /^glass$/,
  /^badge-[\w-]+$/,
  /^(bg|text|border|ring|outline|fill|stroke|divide|from|via|to)-(base-(100|200|300|content)|(primary|secondary|accent|neutral|info|success|warning|error)(-content)?)(\/\d+)?$/,
];

/** What a page paints for itself: its main pane, the Now list beside it, and any dialog over it.
 *  Everything else is the shell's chrome. */
const CONTENT = "main, [data-now-pane], dialog, [role=dialog]";

/** The elements whose classes match LEGACY_CLASS, as `tag.class`, each pair once: in the page's
 *  own content, in the shell's chrome, or in both. */
function legacyClasses(page: Page, where: { content: boolean; shell: boolean }): Promise<string[]> {
  return page.evaluate(
    ({ sources, where, content }) => {
      const patterns = sources.map((s) => new RegExp(s));
      const found = new Set<string>();
      for (const el of document.querySelectorAll("[class]")) {
        if (!(el.closest(content) === null ? where.shell : where.content)) continue;
        for (const name of el.classList) {
          const bare = name.split(":").pop() ?? name;
          if (patterns.some((p) => p.test(bare))) found.add(`${el.tagName.toLowerCase()}.${name}`);
        }
      }
      return [...found].sort();
    },
    { sources: LEGACY_CLASS.map((p) => p.source), where, content: CONTENT },
  );
}

/** Waits until the panels stopped loading (their skeletons and "Loading…" lines are gone, or
 *  three seconds passed) and a slide-in is done, so the shot shows the page, not its arrival. */
async function settle(page: Page): Promise<void> {
  await page
    .waitForFunction(
      () =>
        document.querySelectorAll(".skeleton").length === 0 &&
        !document.body.textContent?.includes("Loading…"),
      undefined,
      { timeout: 3000 },
    )
    .catch(() => {});
  await page.waitForTimeout(400);
}

/** Shoots the page as it stands and runs every check on it. */
async function checkScreen(page: Page, screen: Screen, scheme: Scheme, info: TestInfo): Promise<void> {
  await expect(page.locator("html")).toHaveAttribute("data-scheme", scheme);
  await settle(page);
  await page.screenshot({
    path: path.join(info.project.outputDir, "screens", info.project.name, `${screen.id}-${scheme}.png`),
  });
  const over = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect.soft(over, `${screen.id} ${scheme} scrolls the page sideways by ${over}px`).toBeLessThanOrEqual(0);
  const where = { content: !LEGACY_PAGES.includes(screen.id), shell: !LEGACY_PAGES.includes("shell") };
  if (where.content || where.shell) {
    expect.soft(await legacyClasses(page, where), `${screen.id} ${scheme} paints legacy classes`).toEqual([]);
  }
  const known = KNOWN[screen.id];
  const violations = (await axeViolations(page))
    .map((v) => ({
      ...v,
      nodes: v.nodes.filter((node) => !SHELL_KNOWN.some((k) => k.rule === v.id && node.startsWith(k.node))),
    }))
    .filter((v) => v.nodes.length > 0 && !(known?.axe ?? []).some((rule) => rule === v.id));
  expect.soft(violations, `${screen.id} ${scheme}: axe violations`).toEqual([]);
  if (info.project.use.hasTouch === true && known?.smallTargets !== true) await expectTapTargets(page);
}

test.describe("every page on this project's width", () => {
  /** The need ids this project's seed raised, dismissed once the pages are shot. */
  const needs: number[] = [];

  test.beforeAll(async () => {
    const seeded = seededFor(test.info().project.name);
    // a session that stopped, then asked to run a command: the needs sheet's subject
    await api("/api/registry/report", {
      session_id: seeded.permit,
      machine: "e2e-machine",
      project: "e2e",
      title: "Asks to run the tests",
      model: "glm-5.3",
      last_event: "Stop",
      last_event_at: new Date().toISOString(),
    });
    const { id } = await api<{ id: number }>("/api/needs", {
      session_id: seeded.permit,
      tool_name: "Bash",
      tool_input: { command: "go run ./scripts/tmpguard -count=1 ./internal/huddle/... && bun run check" },
    });
    needs.push(id);
    // a task for the drawer; a worker that restarts after a failure finds it made already, and
    // the task drawer's own page is what proves it exists
    await api(`/api/c/e2e/op/task_create?as=alpha`, {
      id: seeded.task,
      title: "Shoot every page at three widths",
      owner: "beta",
    }).catch(() => {});
  });

  test.afterAll(async () => {
    // the other specs count the needs-you items, so this project's ask leaves with it
    for (const id of needs.splice(0)) await api(`/api/needs/${id}/dismiss`, {}).catch(() => {});
  });

  for (const screen of SCREENS) {
    test(`${screen.id}: no sideways scroll, axe clean, light and dark`, async ({ page }, info) => {
      const seeded = seededFor(info.project.name);
      await page.emulateMedia({ colorScheme: "light" });
      if (screen.signedIn) {
        await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
        await signIn(page);
      }
      await screen.open(page, seeded);
      for (const scheme of SCHEMES) {
        await test.step(scheme, async () => {
          await page.emulateMedia({ colorScheme: scheme });
          await checkScreen(page, screen, scheme, info);
        });
      }
    });
  }
});
