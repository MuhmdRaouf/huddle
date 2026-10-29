// settings.spec.ts — the Settings tab on the phone and the desktop project: its six groups in
// order (Appearance, Notifications, Security, Spending, Tips, Hub), at 390 px a grouped list that
// leads with the hub card and shows each row's current value, at 1024 px a sidebar beside the
// page, the deep link to a group on both widths, each group page with its members, a member's own
// address opening the group that holds it, the appearance switch that overrides the OS scheme and
// keeps the choice across a reload, the push switch that says what the browser allows, the
// finished-turn push a paired device turns on and keeps across a reload, the hub's own facts, the
// hub's time zone (seeded by the first signed-in browser, picked again and kept across a reload),
// the Insights switches (tips for me on, tips to the sessions off, a flip kept across a reload),
// the hub's request-bodies switch (on by default, a tap read back off the hub, the Kept-now line
// under it), the phone guide in a sheet, the Spending page: its sections in the mock's order,
// a budget added through its sheet and removed from it, and the alerts switch flipped, and the
// Security page: its sections in the mock's order, a paired browser naming its own row This
// device, and the sign-out that lands on the sign-in page with the old cookie refused. Every
// group fits the viewport with no horizontal
// scroll, keeps
// its tap targets at least 44 px and passes axe.

import { expect, test, type Page } from "@playwright/test";
import { themeOf } from "../ui/src/themes.ts";
import { expectAccessible } from "./axe.ts";
import { n, pairThroughLink, signIn, theSetup, TOTALS, useVirtualAuthenticator } from "./helpers.ts";

/** A #rrggbb as computed style spells it. */
const rgb = (hex: string): string => {
  const nn = Number.parseInt(hex.slice(1), 16);
  return `rgb(${(nn >> 16) & 255}, ${(nn >> 8) & 255}, ${nn & 255})`;
};

/** The six groups, in order, each with its address, the members its page draws and the value its
 *  phone row shows once the reads land (the row's own text when there is no source yet). */
const GROUPS: readonly (readonly [name: string, hash: string, members: readonly string[], value: string | RegExp])[] = [
  ["Appearance", "#/settings/appearance", ["appearance"], /^Huddle · (System|Light|Dark)$/],
  ["Notifications", "#/settings/notifications", ["notifications"], ""],
  ["Security", "#/settings/security", ["devices", "phone", "signin", "signout"], /^\d+ devices?$/],
  ["Spending", "#/settings/spending", ["radar"], ""],
  ["Tips", "#/settings/tips", ["insights"], "On"],
  ["Hub", "#/settings/hub", ["hub", "zone", "capture", "selfcheck"], /\S/],
];

/** Opens one Settings address in a signed-in browser. The authenticator set-up the first sign-in
 *  offers is not this page's subject, so the browser starts with its "Later" already kept. */
async function openSettings(page: Page, hash = "#/settings"): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/${hash}`);
  await expect(page.locator("[data-settings]")).toBeVisible();
}

/** The sidebar's link by its name. */
const section = (page: Page, name: string) =>
  page.getByRole("navigation", { name: "Settings sections" }).getByRole("link", { name, exact: true });

/** The group page that is open: its link current in the sidebar on a wide screen, its name the
 *  header's title under the back link on the phone. */
async function expectGroupOpen(page: Page, name: string): Promise<void> {
  if ((page.viewportSize()?.width ?? 0) < 640) {
    await expect(page.getByRole("heading", { level: 1, name, exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "Back to Settings" })).toBeVisible();
  } else {
    await expect(section(page, name)).toHaveAttribute("aria-current", "page");
  }
}

/** Each element's left edge and top, to tell a stacked column from a row. */
const boxes = (page: Page, selector: string) =>
  page
    .locator(selector)
    .evaluateAll((els) => els.map((el) => el.getBoundingClientRect()).map((r) => ({ x: r.x, y: r.y, h: r.height })));

test.describe("at 390 px", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("the groups are rows of three cards, each naming its value, and no chip row", async ({ page }) => {
    await openSettings(page);
    const nav = page.getByRole("navigation", { name: "Settings sections" });
    const rows = nav.locator("li a[data-section]");
    await expect(rows).toHaveCount(GROUPS.length);
    expect(await rows.evaluateAll((els) => els.map((el) => el.getAttribute("href")))).toEqual(
      GROUPS.map(([, hash]) => hash),
    );
    for (const [i, [name, , , value]] of GROUPS.entries()) {
      await expect(rows.nth(i).locator("[data-label]")).toHaveText(name);
      await expect(rows.nth(i).locator("[data-value]")).toHaveText(value);
    }
    await expect(nav.locator("[data-card-heading]")).toHaveText([
      "Appearance and notifications",
      "Security and spending",
      "Tips and hub",
    ]);
    // a column of full-width rows, each a 44 px target: never a scrolling row of chips
    const at = await boxes(page, '[aria-label="Settings sections"] li a[data-section]');
    expect(new Set(at.map((b) => b.x)).size).toBe(1);
    expect(at.every((b, i) => i === 0 || b.y > (at[i - 1]?.y ?? 0))).toBe(true);
    expect(at.every((b) => b.h >= 44)).toBe(true);
    expect(await nav.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0);
    await expect(rows.and(page.locator("[aria-current]"))).toHaveCount(0);
    // the list is the page: no group's settings drawn under it, and no title of its own
    await expect(page.locator("[data-setting]")).toHaveCount(0);
    await expect(nav.locator("h1")).toHaveCount(0);
    await expectAccessible(page);
  });

  test("the list leads with the hub card, and the footnote says what stays where", async ({ page }) => {
    await openSettings(page);
    const card = page.locator("a[data-hub-card]");
    await expect(card).toBeVisible();
    await expect(card).toHaveAttribute("href", "#/settings/hub");
    await expect(card.locator("[data-hub-name]")).toHaveText(/\S/);
    // the owner's browser carries no device name, so the line is the version alone; this
    // unstamped build reports dev where a release names its semver
    await expect(card.locator("[data-hub-subtitle]")).toHaveText(/^huddle (dev|\d+\.\d+\.\d+)$/);
    await expect(card).toContainText("Healthy");
    await expect(page.locator("[data-footnote]")).toHaveText(
      "Theme and appearance are kept in this browser. Everything else is kept on the hub.",
    );
  });

  test("a row opens its group page and the back link returns to the list", async ({ page }) => {
    await openSettings(page);
    await page
      .getByRole("navigation", { name: "Settings sections" })
      .locator('a[data-section="security"]')
      .click();
    await expect(page).toHaveURL(/#\/settings\/security$/);
    await expectGroupOpen(page, "Security");
    await expect(page.locator("[data-setting]")).toHaveCount(4);
    await expect(page.getByRole("navigation", { name: "Settings sections" })).toHaveCount(0);
    await page.getByRole("link", { name: "Back to Settings" }).click();
    await expect(page).toHaveURL(/#\/settings$/);
    await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible();
    await expect(page.locator("[data-setting]")).toHaveCount(0);
  });

  test("the deep link to Notifications opens its page", async ({ page }) => {
    await openSettings(page, "#/settings/notifications");
    await expectGroupOpen(page, "Notifications");
    await expect(page.locator('[data-setting="notifications"]')).toBeVisible();
  });
});

test.describe("at 1024 px", () => {
  test.use({ viewport: { width: 1024, height: 768 } });

  test("the groups are a sidebar beside the chosen group's page", async ({ page }) => {
    await openSettings(page);
    const nav = page.getByRole("navigation", { name: "Settings sections" });
    await expect(section(page, "Appearance")).toHaveAttribute("aria-current", "page");
    await expect(page.locator('[data-setting="appearance"]')).toBeVisible();
    const at = await boxes(page, '[aria-label="Settings sections"] a[data-section]');
    expect(at).toHaveLength(GROUPS.length);
    expect(new Set(at.map((b) => b.x)).size).toBe(1);
    expect(at.every((b, i) => i === 0 || b.y > (at[i - 1]?.y ?? 0))).toBe(true);
    const side = await nav.boundingBox();
    const body = await page.locator("[data-settings]").boundingBox();
    expect(side && body && side.x + side.width <= body.x).toBe(true);
    await section(page, "Hub").click();
    await expect(page).toHaveURL(/#\/settings\/hub$/);
    await expect(section(page, "Hub")).toHaveAttribute("aria-current", "page");
    await expect(page.locator('[data-setting="hub"]')).toBeVisible();
    await expect(nav).toBeVisible();
    await expect(page.getByRole("link", { name: "Back to Settings" })).toHaveCount(0);
  });

  test("the deep link to Notifications opens its page beside the sidebar", async ({ page }) => {
    await openSettings(page, "#/settings/notifications");
    await expectGroupOpen(page, "Notifications");
    await expect(page.locator('[data-setting="notifications"]')).toBeVisible();
  });
});

test.describe("the Settings tab", () => {
  test("lists its groups in order, Appearance first", async ({ page }) => {
    await openSettings(page);
    const order = await page
      .getByRole("navigation", { name: "Settings sections" })
      .locator("a[data-section]")
      .evaluateAll((els) => els.map((el) => el.getAttribute("href")));
    expect(order).toEqual(GROUPS.map(([, hash]) => hash));
  });

  test("each group page draws its members, and a member's address opens its group", async ({ page }) => {
    await openSettings(page);
    for (const [name, hash, members] of GROUPS) {
      await page.goto(`${theSetup().rootUrl}/${hash}`);
      await expectGroupOpen(page, name);
      await expect(page.locator("[data-setting]")).toHaveCount(members.length);
      const drawn = await page
        .locator("[data-setting]")
        .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.setting));
      expect(drawn, name).toEqual([...members]);
    }
    for (const [member, name] of [
      ["appearance", "Appearance"],
      ["notifications", "Notifications"],
      ["devices", "Security"],
      ["phone", "Security"],
      ["signin", "Security"],
      ["signout", "Security"],
      ["radar", "Spending"],
      ["insights", "Tips"],
      ["hub", "Hub"],
      ["zone", "Hub"],
      ["capture", "Hub"],
      ["selfcheck", "Hub"],
    ] as const) {
      await page.goto(`${theSetup().rootUrl}/#/settings/${member}`);
      await expectGroupOpen(page, name);
      await expect(page.locator(`[data-setting="${member}"]`)).toBeVisible();
    }
  });

  test("the appearance control overrides the OS and keeps the choice across a reload", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "light" });
    await openSettings(page, "#/settings/appearance");
    const html = page.locator("html");
    const group = page.getByRole("radiogroup", { name: "Appearance" });
    await expect(html).toHaveAttribute("data-scheme", "light");
    await group.getByRole("radio", { name: "Dark", exact: true }).click();
    await expect(html).toHaveAttribute("data-scheme", "dark");
    await page.reload();
    await expect(group.getByRole("radio", { name: "Dark", exact: true })).toHaveAttribute("aria-checked", "true");
    await expect(html).toHaveAttribute("data-scheme", "dark");
    await group.getByRole("radio", { name: "System", exact: true }).click();
    await expect(html).toHaveAttribute("data-scheme", "light");
    await page.emulateMedia({ colorScheme: "dark" });
    await expect(html).toHaveAttribute("data-scheme", "dark");
  });

  test("picking a theme card paints it, and the pick survives a reload", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "dark" });
    await openSettings(page, "#/settings/appearance");
    const html = page.locator("html");
    const nord = themeOf("nord");
    await page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Nord" }).click();
    await expect(html).toHaveAttribute("data-theme", "nord");
    const bg = await page.evaluate(
      () => getComputedStyle(document.querySelector(".ui-shell") as Element).backgroundColor,
    );
    // a theme's bar colour is its own background, and the shell paints --ui-bg
    expect(bg).toBe(rgb(nord.bar.dark ?? ""));
    await page.reload();
    await expect(html).toHaveAttribute("data-theme", "nord");
    await expect(
      page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Nord" }),
    ).toHaveAttribute("aria-checked", "true");
  });

  test("each theme card paints its own theme in its preview", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "dark" });
    await openSettings(page, "#/settings/appearance");
    const bgs = await page.evaluate(() =>
      ["huddle", "catppuccin", "nord"].map((id) => {
        const el = document.querySelector(`[data-theme-value="${id}"] [data-theme]`);
        return getComputedStyle(el as Element).backgroundColor;
      }),
    );
    expect(new Set(bgs).size).toBe(bgs.length);
    // the dark page resolves every two-sided preview to its theme's dark side
    expect(bgs[2]).toBe(rgb(themeOf("nord").bar.dark ?? ""));
  });

  test("a preview follows the device's flip while the page stays open", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "light" });
    await openSettings(page, "#/settings/appearance");
    const nordBg = (): Promise<string> =>
      page.evaluate(
        () =>
          getComputedStyle(
            document.querySelector('[data-theme-value="nord"] [data-theme]') as Element,
          ).backgroundColor,
      );
    expect(await nordBg()).toBe(rgb(themeOf("nord").bar.light ?? ""));
    await page.emulateMedia({ colorScheme: "dark" });
    await expect.poll(nordBg).toBe(rgb(themeOf("nord").bar.dark ?? ""));
  });

  test("a dark-only theme blocks the Light choice", async ({ page }) => {
    await openSettings(page, "#/settings/appearance");
    const group = page.getByRole("radiogroup", { name: "Appearance" });
    const light = group.getByRole("radio", { name: "Light", exact: true });
    await expect(light).not.toHaveAttribute("aria-disabled", "true");
    await page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Monokai" }).click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "monokai");
    // Light sits blocked and unpicked: the browser refuses it, and Monokai keeps painting dark
    await expect(light).toHaveAttribute("aria-disabled", "true");
    await expect(light).toHaveAttribute("aria-checked", "false");
    await expect(page.locator("html")).toHaveAttribute("data-scheme", "dark");
  });

  test.describe("with the app's worker running", () => {
    // the push switch asks the worker's registration, so this case lets it run
    test.use({ serviceWorkers: "allow" });

    test("the notification switches say what this browser allows", async ({ page }) => {
      await openSettings(page, "#/settings/notifications");
      // the page reads as a Settings section: its heading, with the rows in one card under it
      await expect(page.locator('[data-setting="notifications"] > section > h2')).toHaveText(
        "Notifications",
      );
      await expect(page.locator("#set-notif")).toBeVisible();
      const push = page.locator("#set-push");
      await expect(push).toHaveAttribute("aria-checked", "false");
      const permission = await page.evaluate(() => Notification.permission);
      await expect(page.locator("#push-h")).toContainText(`Push permission: ${permission}.`);
      // iOS pushes only to the app added to the Home Screen, and this page runs in a tab
      await expect(page.locator("#push-h")).toContainText("add Huddle to the Home Screen");
      // the hub's own choice of what wakes a device stands beside the switches
      await expect(page.getByRole("radiogroup", { name: "Notifications" })).toBeVisible();
    });
  });

  test("a paired device's finished-turn push is off by default and its choice survives a reload", async ({
    page,
  }) => {
    // the choice is a paired device's own: this browser pairs under its own name first
    await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
    await useVirtualAuthenticator(page);
    await pairThroughLink(page, `E2E Turn ${test.info().project.name}`);
    await page.goto(`${theSetup().pairOrigin}/#/settings/notifications`);
    const turn = page.getByRole("switch", { name: "Push when a turn finishes" });
    await expect(turn).toBeEnabled();
    await expect(turn).toHaveAttribute("aria-checked", "false");
    await turn.click();
    await expect(turn).toHaveAttribute("aria-checked", "true");
    await page.reload();
    await expect(turn).toBeEnabled();
    await expect(turn).toHaveAttribute("aria-checked", "true");
    // the asks keep their own choice beside it
    await expect(page.getByRole("radio", { name: /Needs you only/ })).toBeChecked();
    await turn.click();
    await expect(turn).toHaveAttribute("aria-checked", "false");
    await page.reload();
    await expect(turn).toHaveAttribute("aria-checked", "false");
  });

  test("This hub names the hub, its address and its version", async ({ page }) => {
    await openSettings(page, "#/settings/hub");
    const facts = page.locator("[data-hub-facts]");
    await expect(facts).toContainText(theSetup().rootUrl.replace(/\/$/, ""));
    await expect(facts).toContainText(/\d+\.\d+\.\d+/);
  });

  test("the self-check says Radar matches the transcripts, both sides' numbers as text", async ({ page }) => {
    await openSettings(page, "#/settings/hub");
    // today's three seeded calls, as Radar's history holds them and as the transcript wrote them
    const check = page.locator('[data-setting="selfcheck"] [data-selfcheck]');
    await expect(check).toBeVisible();
    await expect(check).toHaveAttribute("data-tone", "ok");
    await expect(check).toContainText("Matches transcripts");
    const seeded = `3 requests, ${n(TOTALS.input)} in, ${n(TOTALS.output)} out, ${n(TOTALS.cacheRead)} cache read, ${n(TOTALS.cacheWrite)} cache write`;
    await expect(check.locator("p")).toHaveText(new RegExp(`Radar: ${seeded}\\.\\s+Transcripts: ${seeded}\\.`));
    await expect(check.locator("[title]")).toHaveCount(0);
  });

  test("the hub's time zone is seeded by a signed-in browser and a pick survives a reload", async ({ page }) => {
    await openSettings(page, "#/settings/hub");
    const heading = page.locator("#zone-h");
    const picker = page.getByRole("combobox", { name: "Time zone" });
    // this browser's first signed-in load sent its zone, so the hub holds one
    await expect(heading).toHaveText(/^Time zone: [A-Za-z_]+(\/[A-Za-z_+-]+)*( \(from your first sign-in\))?$/);
    await expect(picker).toBeEnabled();
    const next = (await picker.inputValue()) === "Asia/Tokyo" ? "Europe/Istanbul" : "Asia/Tokyo";
    await picker.selectOption(next);
    await expect(heading).toHaveText(`Time zone: ${next}`);
    await page.reload();
    await expect(heading).toHaveText(`Time zone: ${next}`);
    await expect(page.getByRole("combobox", { name: "Time zone" })).toHaveValue(next);
    await page.getByRole("combobox", { name: "Time zone" }).selectOption("UTC");
    await expect(heading).toHaveText("Time zone: UTC");
  });

  test("the Insights switches start on and off and keep a change across a reload", async ({ page }) => {
    await openSettings(page, "#/settings/insights");
    // the group's member is the Tips pane; its heading names the group, not the engine
    await expect(page.locator('[data-setting="insights"] > section > h2')).toHaveText("Tips");
    const tipsForMe = page.getByRole("switch", { name: "Tips for me", exact: true });
    const tipsToSessions = page.getByRole("switch", { name: "Tips to my sessions" });
    await expect(tipsForMe).toHaveAttribute("aria-checked", "true");
    await expect(tipsToSessions).toHaveAttribute("aria-checked", "false");
    // a flip answers the tap at once and PUTs its one field; the reload must not race the save
    const saved = (field: string) =>
      page.waitForResponse(
        (r) =>
          r.request().method() === "PUT" &&
          r.url().endsWith("/api/insights/settings") &&
          (r.request().postData() ?? "").includes(field),
      );
    await Promise.all([saved("tips_for_me"), tipsForMe.click()]);
    await Promise.all([saved("tips_to_sessions"), tipsToSessions.click()]);
    await expect(tipsForMe).toHaveAttribute("aria-checked", "false");
    await expect(tipsToSessions).toHaveAttribute("aria-checked", "true");
    await page.reload();
    await expect(tipsForMe).toHaveAttribute("aria-checked", "false");
    await expect(tipsToSessions).toHaveAttribute("aria-checked", "true");
    // the next read starts where the switches began
    await tipsForMe.click();
    await tipsToSessions.click();
    await expect(tipsForMe).toHaveAttribute("aria-checked", "true");
    await expect(tipsToSessions).toHaveAttribute("aria-checked", "false");
    await expectAccessible(page);
  });

  test("the request-bodies switch reads on, a tap turns it off and a second brings it back", async ({
    page,
  }) => {
    await openSettings(page, "#/settings/hub");
    const keep = page.getByRole("switch", { name: "Keep request bodies" });
    await expect(keep).toBeEnabled();
    await expect(keep).toHaveAttribute("aria-checked", "true");
    const flip = (on: boolean) =>
      page.waitForResponse(
        (r) =>
          r.request().method() === "POST" &&
          r.url().endsWith("/api/settings") &&
          (r.request().postDataJSON() as { capture?: boolean }).capture === on,
      );
    const setting = () =>
      page.evaluate(async () => {
        const r = await fetch("/api/settings");
        return (await r.json()) as { capture: boolean };
      });
    await Promise.all([flip(false), keep.click()]);
    await expect(keep).toHaveAttribute("aria-checked", "false");
    expect(await setting()).toMatchObject({ capture: false });
    await Promise.all([flip(true), keep.click()]);
    await expect(keep).toHaveAttribute("aria-checked", "true");
    expect(await setting()).toMatchObject({ capture: true });
    await expectAccessible(page);
  });

  test("the phone guide opens in a sheet", async ({ page }) => {
    await openSettings(page, "#/settings/phone");
    await page.getByRole("button", { name: "Add a phone" }).click();
    const sheet = page.getByRole("dialog", { name: "Add a phone" });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByRole("img", { name: "QR code: the phone's trust guide" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();
  });

  test.describe("the Spending page", () => {
    test("its sections stand in the mock's order, a budget is added through the sheet and removed from it, and the alerts switch flips", async ({
      page,
    }) => {
      await openSettings(page, "#/settings/spending");
      const view = page.locator('[data-setting="radar"]');
      await expect(view.getByRole("heading", { name: "Budgets" })).toBeVisible();
      // without a per-day budget on everything there is no Today section
      expect(await view.locator("section > h2").allTextContents()).toEqual([
        "Budgets",
        "Alerts",
        "History",
        "Data",
      ]);
      await view.getByRole("button", { name: "Add a budget" }).click();
      const sheet = page.getByRole("dialog", { name: "Add a budget" });
      await expect(sheet).toBeVisible();
      await sheet.getByRole("combobox", { name: "Scope" }).selectOption("total");
      await sheet.getByRole("combobox", { name: "Period" }).selectOption("day");
      await sheet.getByRole("textbox", { name: "Limit (USD)" }).fill("40");
      await Promise.all([
        page.waitForResponse(
          (r) => r.request().method() === "PUT" && r.url().endsWith("/api/radar/api/budgets"),
        ),
        sheet.getByRole("button", { name: "Add budget" }).click(),
      ]);
      await expect(sheet).toBeHidden();
      const row = view.getByRole("button", { name: /Per day · warn at 80% and 100%/ });
      await expect(row).toBeVisible();
      await expect(row).toContainText("Total");
      await expect(row).toContainText("$40.00");
      expect(await view.locator("section > h2").allTextContents()).toEqual([
        "Today",
        "Budgets",
        "Alerts",
        "History",
        "Data",
      ]);
      await expect(view.getByRole("progressbar", { name: "Today's spend" })).toBeVisible();
      // the row opens the edit sheet, and Remove budget takes the budget away
      await row.click();
      const edit = page.getByRole("dialog", { name: "Edit budget" });
      await expect(edit).toBeVisible();
      await expect(edit.getByRole("combobox", { name: "Period" })).toHaveValue("day");
      await edit.getByRole("button", { name: "Remove budget" }).click();
      await expect(row).toBeHidden();
      await expect(view.getByRole("progressbar", { name: "Today's spend" })).toHaveCount(0);
      // the alerts switch flips and its choice is read back off the hub; the server is shared
      // across the run's projects, so the switch may arrive in either state — flip it once and
      // put it back, asserting only the flip itself
      const alerts = view.getByRole("switch", { name: "Desktop notifications" });
      const start = await alerts.getAttribute("aria-checked");
      await Promise.all([
        page.waitForResponse(
          (r) => r.request().method() === "PUT" && r.url().endsWith("/api/radar/api/settings"),
        ),
        alerts.click(),
      ]);
      await expect(alerts).toHaveAttribute("aria-checked", start === "true" ? "false" : "true");
      await alerts.click();
      await expect(alerts).toHaveAttribute("aria-checked", start ?? "true");
      await expectAccessible(page);
    });
  });

  test.describe("the Security page", () => {
    test("its sections stand in the mock's order", async ({ page }) => {
      await openSettings(page, "#/settings/security");
      const heads = await page.locator("[data-settings] [data-setting] > section > h2").allTextContents();
      expect(heads[0] ?? "").toMatch(/^Devices( · \d+)?$/);
      expect(heads.slice(1)).toEqual(["Add a device", "Sign-in methods", "Sign out"]);
    });

    test("a paired browser names its own row This device, with no Remove on it", async ({ page }) => {
      // this browser pairs under its own name first, as a device of the run does
      await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
      await useVirtualAuthenticator(page);
      await pairThroughLink(page, `E2E Security ${test.info().project.name}`);
      await page.goto(`${theSetup().pairOrigin}/#/settings/security`);
      const devices = page.locator('[data-setting="devices"]');
      await expect(devices).toBeVisible();
      const mine = devices.locator("[data-this-device]");
      await expect(mine).toHaveText("This device");
      const row = devices.locator("li:has([data-this-device])");
      await expect(row).toContainText("This device · active now");
      await expect(row.getByRole("button", { name: "Remove" })).toHaveCount(0);
      await expect(page.getByRole("heading", { name: /^Devices · \d+$/ })).toBeVisible();
    });

    test("Sign out of this browser lands on the sign-in page and the old cookie is refused", async ({
      page,
    }) => {
      await openSettings(page, "#/settings/signout");
      // the cookie this browser holds stands for a live credential; replay it by hand so the
      // sign-out is judged on the hub, not on the cookie the server just cleared
      const session = (await page.context().cookies(theSetup().rootUrl)).find((c) =>
        c.name.startsWith("huddle_session_"),
      );
      if (!session) throw new Error("the browser holds no session cookie");
      const alive = () =>
        fetch(`${theSetup().rootUrl}/api/whoami`, {
          headers: { cookie: `${session.name}=${session.value}` },
        }).then((r) => r.status);
      await page.getByRole("button", { name: "Sign out of this browser" }).click();
      const confirm = page.getByRole("dialog", { name: "Sign out of this browser" });
      await expect(confirm).toBeVisible();
      // the first click only asked: the session still answers for this browser
      expect(await alive()).toBe(200);
      await confirm.getByRole("button", { name: "Sign out", exact: true }).click();
      await page.waitForURL((u) => u.pathname === "/");
      await expect(page.getByRole("navigation", { name: "Main" })).toHaveCount(0);
      await expect(page.locator("[data-landing]")).toBeVisible();
      // the credential the cookie stood for is dead on the hub
      expect(await alive()).toBe(401);
    });
  });

  for (const [name, hash] of GROUPS) {
    test(`${name}: no horizontal scroll, tap targets, axe clean`, async ({ page }) => {
      await openSettings(page, hash);
      await expectGroupOpen(page, name);
      await page
        .waitForFunction(() => !document.body.textContent?.includes("Loading…"), undefined, { timeout: 3000 })
        .catch(() => {});
      const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      expect(over).toBeLessThanOrEqual(0);
      // B's desk controls stand 40 px, so the 44 px floor is measured under a finger only
      if (test.info().project.use.hasTouch) {
        const small = await page.evaluate(() =>
          [
            ...document.querySelectorAll<HTMLElement>(
              '[data-settings] button, [data-settings] input, [data-settings] a, [data-settings] [role="radio"]',
            ),
          ]
            // a switch or a radio is tapped through the label around it
            .map((el) => ({ el, r: (el.closest("label") ?? el).getBoundingClientRect() }))
            .filter(({ r }) => r.width > 0 && (r.width < 44 || r.height < 44))
            .map(({ el, r }) => {
              const label = el.textContent?.trim() || el.getAttribute("aria-label") || el.id;
              return `${el.tagName} "${label}" ${r.width}x${r.height}`;
            }),
        );
        expect(small).toEqual([]);
      }
      await expectAccessible(page);
    });
  }
});
