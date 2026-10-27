// shell.spec.ts — the app shell on both projects. On the phone the five tabs sit in a bar pinned
// to the viewport's bottom, whatever scrolls, on the iPhone's own height and on a short one where
// every page runs longer than the screen; every page's content ends above the bar, nothing
// scrolls the page sideways, a code block scrolls inside its own box, and every control on every
// page is a target of at least 44 px. On the desktop the same five items stand in a left sidebar
// and no bottom bar is drawn. axe checks the shell's chrome on both (the pages' contents are their
// own specs'), and the page names a theme colour per scheme beside a manifest with a maskable icon
// and links a 180 px Apple Home Screen icon the hub serves as a PNG.
// A button styled by Tailwind utilities alone keeps its fill and its border over Radar's legacy
// button reset. A session's needs sheet shows a Bash ask's command, leaves "Runs in" out for a
// session with no branch, and fits at 390, 820 and 1440 px in both schemes with answers sized to
// the pointer (44 px under a finger, 40 px under one).
// An open app offers "Huddle updated" with one Reload once the hub's /health names another build.

import { expect, test, type Page } from "@playwright/test";
import { axeViolations, expectAccessible } from "./axe.ts";
import { api, expectTapTargets, SEEDED, signIn, theSetup } from "./helpers.ts";

/** The five tabs, in the order the shell draws them; Sessions may carry its needs-you count after it. */
const TABS = [/^Sessions\d*$/, /^Channels$/, /^Radar$/, /^Kanban$/, /^Settings$/];

/** Every page the shell frames: the five tabs and a channel's own page under Channels. */
const PAGES: readonly (readonly [name: string, hash: string])[] = [
  ["Sessions", "#/sessions"],
  ["Channels", "#/channels"],
  ["Channel overview", "#/c/e2e/overview"],
  ["Radar", "#/radar/overview"],
  ["Kanban", "#/kanban"],
  ["Settings", "#/settings"],
];

/** The pages the tap check walks: the five tabs, a channel's page and a session's own page under
 *  Sessions (its Back to Sessions sits there). */
const TAP_PAGES: readonly (readonly [name: string, hash: string])[] = [
  ["Sessions", "#/sessions"],
  ["A session", `#/sessions/${SEEDED.session}`],
  ["Channels", "#/channels"],
  ["Channel overview", "#/c/e2e/overview"],
  ["Radar", "#/radar/overview"],
  ["Kanban", "#/kanban"],
  ["Settings", "#/settings"],
];

/** The parts axe leaves to the pages' own specs: the main pane and the list beside it. */
const CONTENT = ["main", "[data-sessions-pane]"] as const;

/** The viewport heights the phone runs at: the iPhone 15's own, and a short screen (the keyboard
 *  up, a small phone) that makes every page longer than the view. */
const HEIGHTS = [659, 420] as const;

/** Opens one page in a signed-in browser. The authenticator set-up the first sign-in offers is
 *  not the shell's subject, so the browser starts with its "Later" already kept. */
async function open(page: Page, hash: string): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/${hash}`);
  // the shell is up when the nav shows; a page whose dialog opens with it (a session's pending
  // ask, a drawer's address) steps the phone's tab bar aside, so an open dialog waits instead
  await expect(
    page.getByRole("navigation", { name: "Main" }).or(page.locator("dialog[open]")).first(),
  ).toBeVisible();
  // the panels settle: their skeletons are gone, or three seconds passed
  await page
    .waitForFunction(() => document.querySelectorAll(".skeleton").length === 0, undefined, { timeout: 3000 })
    .catch(() => {});
}

/** Scrolls the window and every scroll box on the page to its end, the way a thumb reaches the
 *  bottom of a long page. */
async function scrollToEnd(page: Page): Promise<void> {
  await page.evaluate(() => {
    window.scrollTo(0, document.documentElement.scrollHeight);
    for (const el of document.querySelectorAll<HTMLElement>("*")) {
      if (el.scrollHeight > el.clientHeight) el.scrollTop = el.scrollHeight;
    }
  });
  await page.waitForTimeout(100);
}

/** The bar's box and the viewport's height. */
function barBox(page: Page): Promise<{ top: number; bottom: number; height: number; view: number }> {
  return page.evaluate(() => {
    const bar = document.querySelector("[data-nav='bottom']");
    const r = bar?.getBoundingClientRect();
    return { top: r?.top ?? -1, bottom: r?.bottom ?? -1, height: r?.height ?? 0, view: window.innerHeight };
  });
}

/** Every element of the main pane that still paints below the bar's top edge, once each box it
 *  sits in clips it: a painted part under the bar is a part the owner cannot read. */
function underBar(page: Page, barTop: number): Promise<string[]> {
  return page.evaluate((top) => {
    const clipped = (el: Element): { top: number; bottom: number } => {
      const r = el.getBoundingClientRect();
      let t = r.top;
      let b = r.bottom;
      for (let p = el.parentElement; p; p = p.parentElement) {
        const s = getComputedStyle(p);
        if (s.overflowY === "visible" && s.overflowX === "visible") continue;
        const pr = p.getBoundingClientRect();
        t = Math.max(t, pr.top);
        b = Math.min(b, pr.bottom);
      }
      return { top: t, bottom: b };
    };
    return [...document.querySelectorAll("main *")]
      .filter((el) => el.children.length === 0)
      .filter((el) => {
        const r = el.getBoundingClientRect();
        if (r.width < 1 || r.height < 1) return false;
        if (getComputedStyle(el).visibility === "hidden") return false;
        const c = clipped(el);
        return c.bottom - c.top >= 1 && c.bottom > top + 0.5;
      })
      .map((el) => `${el.tagName.toLowerCase()} "${(el.textContent ?? "").trim().slice(0, 40)}"`);
  }, barTop);
}

test.describe("the phone shell", () => {
  test.skip(({ isMobile }) => !isMobile, "the bottom bar is the phone's");

  for (const [name, hash] of PAGES) {
    for (const height of HEIGHTS) {
      test(`${name} at ${height} px: the bar stays pinned and nothing sits under it`, async ({ page }) => {
        await page.setViewportSize({ width: 393, height });
        await open(page, hash);
        const before = await barBox(page);
        expect(before.bottom, `${name}: the bar's bottom`).toBeCloseTo(before.view, 0);
        expect(before.height, `${name}: the bar is 50 px plus the inset`).toBeGreaterThanOrEqual(50);
        await scrollToEnd(page);
        const after = await barBox(page);
        expect(after.bottom, `${name}: the bar's bottom after scrolling`).toBeCloseTo(after.view, 0);
        expect(await underBar(page, after.top), `${name}: content under the bar`).toEqual([]);
        const over = await page.evaluate(
          () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
        );
        expect(over, `${name} scrolls the page sideways by ${over}px`).toBeLessThanOrEqual(0);
        const cut = await page.evaluate(() =>
          [...document.querySelectorAll<HTMLElement>("main pre")]
            .filter((pre) => pre.scrollWidth > pre.clientWidth)
            .filter((pre) => !["auto", "scroll"].includes(getComputedStyle(pre).overflowX))
            .map((pre) => (pre.textContent ?? "").slice(0, 40)),
        );
        expect(cut, `${name}: code blocks cut off instead of scrolling`).toEqual([]);
      });
    }
  }

  test("the installed app pins the bar to the screen's bottom edge by the large viewport", async ({ page }) => {
    await page.addInitScript(() =>
      Object.defineProperty(navigator, "standalone", { value: true, configurable: true }),
    );
    await open(page, "#/");
    expect(await page.evaluate(() => document.documentElement.dataset.standalone)).toBe("true");
    const before = await barBox(page);
    expect(before.bottom).toBeCloseTo(before.view, 0);
    await scrollToEnd(page);
    const after = await barBox(page);
    expect(after.bottom).toBeCloseTo(after.view, 0);
    // a fixed box's computed insets read back as used lengths, never "auto": the pin by its top
    // edge shows in the rule the page ships, and in where the bar lands
    const pin = await page.evaluate(() => {
      let rule: CSSStyleRule | undefined;
      for (const sheet of document.styleSheets) {
        for (const r of sheet.cssRules) {
          if (r instanceof CSSStyleRule && r.selectorText.includes("[data-standalone] .ui-tabbar")) rule = r;
        }
      }
      const bar = document.querySelector("[data-nav='bottom']");
      if (!(bar instanceof HTMLElement)) throw new Error("no bottom bar");
      return {
        height: bar.getBoundingClientRect().height,
        ruleBottom: rule?.style.bottom ?? null,
        ruleTop: rule?.style.top ?? null,
        top: getComputedStyle(bar).top,
        view: window.innerHeight,
      };
    });
    expect(pin.ruleBottom).toBe("auto");
    expect(pin.ruleTop).toBe("calc(100lvh - var(--ui-tabbar-h))");
    expect(Math.abs(Number.parseFloat(pin.top) - (pin.view - pin.height))).toBeLessThanOrEqual(1);
    const shell = await page.evaluate(() => {
      const el = document.querySelector(".ui-shell");
      if (!(el instanceof HTMLElement)) throw new Error("no .ui-shell");
      return { height: el.getBoundingClientRect().height, view: window.innerHeight };
    });
    expect(shell.height).toBeCloseTo(shell.view, 0);
    const scroll = await page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight);
    expect(scroll).toBeLessThanOrEqual(0);
  });

  test("a page in Safari keeps the bar pinned by bottom: 0", async ({ page }) => {
    await open(page, "#/");
    expect(await page.evaluate(() => document.documentElement.dataset.standalone)).toBeUndefined();
    const bottom = await page.evaluate(() => {
      const bar = document.querySelector("[data-nav='bottom']");
      if (!(bar instanceof HTMLElement)) throw new Error("no bottom bar");
      return getComputedStyle(bar).bottom;
    });
    expect(bottom).toBe("0px");
  });

  test("the five tabs are targets of at least 44 px", async ({ page }) => {
    await open(page, "#/sessions");
    const links = page.getByRole("navigation", { name: "Main" }).getByRole("link");
    await expect(links).toHaveText(TABS);
    for (const box of await links.evaluateAll((els) =>
      els.map((el) => {
        const r = el.getBoundingClientRect();
        return { w: r.width, h: r.height };
      }),
    )) {
      expect(box.w).toBeGreaterThanOrEqual(44);
      expect(box.h).toBeGreaterThanOrEqual(44);
    }
  });

  test("the old home and the old Now address land on the sessions tab", async ({ page }) => {
    for (const hash of ["#/", "#/now"]) {
      await open(page, hash);
      await expect(page, hash).toHaveURL(/#\/sessions$/);
      await expect(
        page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: /^Sessions/ }),
        hash,
      ).toHaveAttribute("aria-current", "page");
    }
  });

  for (const [name, hash] of TAP_PAGES) {
    test(`${name}: every control is a target of at least 44 px`, async ({ page }) => {
      await open(page, hash);
      await expectTapTargets(page);
    });
  }

  test("a tab opens its page and marks itself current", async ({ page }) => {
    await open(page, "#/sessions");
    const nav = page.getByRole("navigation", { name: "Main" });
    await nav.getByRole("link", { name: "Radar" }).click();
    await expect(page).toHaveURL(/#\/radar\//);
    await expect(nav.getByRole("link", { name: "Radar" })).toHaveAttribute("aria-current", "page");
  });

  test("passes axe", async ({ page }) => {
    await open(page, "#/sessions");
    await expectAccessible(page, CONTENT);
  });
});

test.describe("the desktop shell", () => {
  test.skip(({ isMobile }) => isMobile, "the sidebar is the desktop's");

  test("the five tabs stand in a left sidebar and no bottom bar is drawn", async ({ page }) => {
    await open(page, "#/sessions");
    const side = page.locator("[data-nav='side']");
    await expect(side).toBeVisible();
    await expect(side.getByRole("link")).toHaveText(TABS);
    await expect(page.locator("[data-nav='bottom']")).toHaveCount(0);
    const box = await page.locator("[data-sidebar]").boundingBox();
    const view = page.viewportSize();
    expect(box?.x).toBe(0);
    expect(box && view && box.height).toBeCloseTo(view?.height ?? 0, 0);
    await side.getByRole("link", { name: "Kanban" }).click();
    await expect(page).toHaveURL(/#\/kanban/);
    await expect(side.getByRole("link", { name: "Kanban" })).toHaveAttribute("aria-current", "page");
  });

  test("passes axe", async ({ page }) => {
    await open(page, "#/sessions");
    await expectAccessible(page, CONTENT);
  });
});

test.describe("the installed app", () => {
  test("names a theme colour per scheme and a manifest with a maskable icon", async ({ page }) => {
    await open(page, "#/sessions");
    const colours = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLMetaElement>("meta[name='theme-color']")].map((m) => m.media),
    );
    expect(colours).toEqual(["(prefers-color-scheme: dark)", "(prefers-color-scheme: light)"]);
    const manifest = (await (await page.request.get(`${theSetup().rootUrl}/manifest.webmanifest`)).json()) as {
      display: string;
      icons: { purpose?: string }[];
    };
    expect(manifest.display).toBe("standalone");
    expect(manifest.icons.some((i) => i.purpose === "maskable")).toBe(true);
  });

  test("links a 180 px Apple Home Screen icon that answers as a PNG", async ({ page }) => {
    await open(page, "#/sessions");
    const href = await page.locator("link[rel='apple-touch-icon']").getAttribute("href");
    expect(href).toBe("/icons/icon-apple-180.png");
    const answer = await page.request.get(`${theSetup().rootUrl}${href}`);
    expect(answer.status()).toBe(200);
    const png = await answer.body();
    // the PNG signature, then IHDR's width and height
    expect(png.subarray(1, 4).toString("ascii")).toBe("PNG");
    expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([180, 180]);
  });
});

test.describe("the update offer", () => {
  test("an open app offers the reload after the hub serves a new build", async ({ page }) => {
    // the hub binary cannot change mid-run: the page's first /health answers the build it loaded
    // with, and every later one names another launcher build of the same version
    let answered = 0;
    await page.route("**/health", async (route) => {
      const real = await route.fetch();
      const body = (await real.json()) as { executable: string };
      answered += 1;
      const executable = answered === 1 ? body.executable : `${body.executable}.next`;
      await route.fulfill({ response: real, json: { ...body, executable } });
    });
    await open(page, "#/sessions");
    await expect(page.getByRole("button", { name: "Reload" })).toHaveCount(0);
    // the owner comes back to the tab: the page asks the hub which build it serves
    await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
    const offer = page.getByRole("status").filter({ hasText: "Huddle updated" });
    await expect(offer).toBeVisible();
    const reload = offer.getByRole("button", { name: "Reload" });
    await expect(reload).toBeVisible();
    // the toast grows in from a smaller scale: its button reaches its full size once that ends
    // (the 44 px floor under a finger, --ui-control-h's 40 px under a pointer)
    const full = test.info().project.use.hasTouch ? 44 : 40;
    await expect.poll(async () => (await reload.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(full);
    await Promise.all([page.waitForEvent("load"), reload.click()]);
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  });

  test("the offer reads as one row above the tab bar, a 384 px card at the desktop's foot", async (
    { page },
    info,
  ) => {
    // the hub binary cannot change mid-run: the page's first /health answers the build it loaded
    // with, and every later one names another launcher build of the same version
    let answered = 0;
    await page.route("**/health", async (route) => {
      const real = await route.fetch();
      const body = (await real.json()) as { executable: string };
      answered += 1;
      const executable = answered === 1 ? body.executable : `${body.executable}.next`;
      await route.fulfill({ response: real, json: { ...body, executable } });
    });
    await open(page, "#/now");
    await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
    const offer = page.getByRole("status").filter({ hasText: "Huddle updated" });
    const reload = offer.getByRole("button", { name: "Reload" });
    const dismiss = offer.getByRole("button", { name: "Dismiss" });
    const text = offer.getByText("Huddle updated");
    await expect(offer).toBeVisible();
    await expect(reload).toBeVisible();
    await expect(dismiss).toBeVisible();
    await expect(text).toBeVisible();
    // the toast grows in from a smaller scale: measure once the grow-in has ended
    // (the 44 px floor under a finger, --ui-control-h's 40 px under a pointer)
    const full = test.info().project.use.hasTouch ? 44 : 40;
    await expect.poll(async () => (await reload.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(full);
    const m = await page.evaluate(() => {
      const row = [...document.querySelectorAll("[role='status']")].find((n) =>
        n.textContent?.includes("Huddle updated"),
      );
      if (!(row instanceof HTMLElement)) throw new Error("no update toast");
      const box = (el: Element) => {
        const r = el.getBoundingClientRect();
        return { left: r.left, right: r.right, bottom: r.bottom, width: r.width, cy: r.top + r.height / 2 };
      };
      const span = row.querySelector("span");
      const buttons = [...row.querySelectorAll("button")];
      const reload = buttons.find((b) => b.textContent?.includes("Reload"));
      const dismiss = buttons.find((b) => b.getAttribute("aria-label") === "Dismiss");
      if (!span || !reload || !dismiss) throw new Error("the toast is missing its text or a button");
      const bar = document.querySelector("[data-nav='bottom']")?.getBoundingClientRect();
      return {
        row: box(row),
        span: box(span),
        reload: box(reload),
        dismiss: box(dismiss),
        barTop: bar?.top ?? null,
        view: { w: window.innerWidth, h: window.innerHeight },
      };
    });
    // one row: the text and Reload share a centre line, and Dismiss stands to Reload's right
    expect(Math.abs(m.reload.cy - m.span.cy), "the text and Reload sit on one line").toBeLessThanOrEqual(4);
    expect(m.dismiss.left, "Dismiss sits right of Reload").toBeGreaterThan(m.reload.right);
    if (info.project.name === "desktop") {
      expect(Math.abs(m.row.width - 384), `the card is ${m.row.width} px wide`).toBeLessThanOrEqual(1);
      expect(Math.abs(m.view.w - m.row.right - 16), "16 px to the right edge").toBeLessThanOrEqual(1);
      expect(Math.abs(m.view.h - m.row.bottom - 16), "16 px to the bottom edge").toBeLessThanOrEqual(1);
    } else {
      expect(Math.abs(m.row.left - 12), `${m.row.left} px to the left edge`).toBeLessThanOrEqual(1);
      expect(Math.abs(m.view.w - m.row.right - 12), "12 px to the right edge").toBeLessThanOrEqual(1);
      if (m.barTop == null) throw new Error("no tab bar on the page");
      expect(m.barTop - m.row.bottom, "the row clears the tab bar by 8 px").toBeGreaterThanOrEqual(8);
    }
  });
});

test.describe("the utility layer", () => {
  test("a permission ask's Allow once keeps its fill and Deny its fail fill", async ({ page }, info) => {
    const session = `e2e-layer-${info.project.name}`;
    await api("/api/registry/report", {
      session_id: session,
      machine: "e2e-machine",
      project: "e2e",
      title: "Asks to build the bundle",
      last_event: "Stop",
      last_event_at: new Date().toISOString(),
    });
    const { id } = await api<{ id: number }>("/api/needs", {
      session_id: session,
      tool_name: "Bash",
      tool_input: { command: "bun run build" },
    });
    // the ask's own opening, not open(): the phone draws no tab bar while a dialog is up, and
    // this session's ask opens its sheet as soon as the page loads
    await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
    await signIn(page);
    await page.goto(`${theSetup().rootUrl}/#/sessions/${session}`);
    // the session screen's ask: the phone's bottom sheet, or the wide layouts' inline box
    const bar = page.locator("[data-needs-sheet], [data-needs-bar]");
    await expect(bar).toBeVisible();
    const allow = bar.locator("[data-act='allow']");
    const deny = bar.locator("[data-act='deny']");
    await expect(allow).toBeVisible();
    expect(await allow.evaluate((el) => getComputedStyle(el).backgroundColor)).not.toBe("rgba(0, 0, 0, 0)");
    // B's Deny carries no border; its fail-soft fill keeps it apart from Allow's accent
    const denyStyle = await deny.evaluate((el) => {
      const s = getComputedStyle(el);
      return { border: `${s.borderTopWidth} ${s.borderTopStyle}`, fill: s.backgroundColor };
    });
    expect(denyStyle.border).toBe("0px none");
    expect(denyStyle.fill).not.toBe("rgba(0, 0, 0, 0)");
    // the answer moves the session out of Needs you, so the Now spec's counts stay as seeded
    await api(`/api/needs/${id}/decide`, { decision: "deny" });
  });
});

test.describe("the needs sheet", () => {
  /** The widths the sheet is drawn at: a phone, a tablet and a desktop. */
  const WIDTHS = [390, 820, 1440] as const;

  test("shows the command, no Runs in without a branch, and fits every width in both schemes", async ({
    page,
  }, info) => {
    const session = `e2e-needs-${info.project.name}`;
    await api("/api/registry/report", {
      session_id: session,
      machine: "e2e-machine",
      project: "e2e",
      title: "Asks to build the bundle",
      last_event: "Stop",
      last_event_at: new Date().toISOString(),
    });
    const { id } = await api<{ id: number }>("/api/needs", {
      session_id: session,
      tool_name: "Bash",
      tool_input: { command: "bun run build", description: "Build the bundle" },
    });
    await open(page, `#/sessions/${session}`);
    for (const scheme of ["light", "dark"] as const) {
      for (const width of WIDTHS) {
        const at = `${width} px, ${scheme}`;
        await page.emulateMedia({ colorScheme: scheme });
        await page.setViewportSize({ width, height: 900 });
        await page.goto(`${theSetup().rootUrl}/#/sessions/${session}`);
        await expect(page.locator("html"), at).toHaveAttribute("data-scheme", scheme);
        const bar = page.locator("[data-needs-sheet], [data-needs-bar]");
        await expect(bar, at).toBeVisible();
        await expect(bar.locator("code").filter({ hasText: "bun run build" }), at).toHaveText("bun run build");
        await expect(bar, at).not.toContainText('"command"');
        await expect(bar, at).not.toContainText("Runs in");
        await expect(bar, at).toContainText("On e2e-machine. Waiting");
        for (const act of ["allow", "deny"]) {
          const button = bar.locator(`[data-act='${act}']`);
          expect(await button.evaluate((el) => el.tagName), `${at}: ${act}`).toBe("BUTTON");
          // a list re-read can re-draw the sheet between the resolve and the measure, so the
          // target sizes are polled, not read once; the 44 px floor holds under a finger only
          const floor = info.project.use.hasTouch ? 44 : 40;
          await expect
            .poll(async () => (await button.boundingBox())?.width ?? 0, `${at}: ${act} width`)
            .toBeGreaterThanOrEqual(floor);
          await expect
            .poll(async () => (await button.boundingBox())?.height ?? 0, `${at}: ${act} height`)
            .toBeGreaterThanOrEqual(floor);
        }
        const over = await page.evaluate(
          () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
        );
        expect(over, `${at}: the page scrolls sideways by ${over}px`).toBeLessThanOrEqual(0);
        const spill = await bar.evaluate((el) => {
          const r = el.getBoundingClientRect();
          return r.left < 0 || r.right > window.innerWidth + 0.5 || el.scrollWidth > el.clientWidth;
        });
        expect(spill, `${at}: the sheet spills past its box`).toBe(false);
        // axe runs over the whole page; the sheet answers only for the nodes inside it
        const found = await axeViolations(page);
        const inBar = await page.evaluate(
          (vs) =>
            vs.filter((v) =>
              v.nodes.some((sel) => {
                try {
                  return document.querySelector(sel)?.closest("[data-needs-sheet], [data-needs-bar]") != null;
                } catch {
                  return false;
                }
              }),
            ),
          found,
        );
        expect(inBar, `${at}: axe on the sheet`).toEqual([]);
      }
    }
    // the answer moves the session out of Needs you, so the Now spec's counts stay as seeded
    await api(`/api/needs/${id}/decide`, { decision: "deny" });
  });
});
