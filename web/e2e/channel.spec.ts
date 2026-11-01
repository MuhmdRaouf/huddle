// channel.spec.ts — the page a Channels card opens, on the phone and at the desk. The chat lists
// the run's seeded ping and its pong newest last, each with its sender, its recipient, the body
// and the age. The composer posts a message into the chat, and on the phone it stays above the
// keyboard: the keyboard is played by a visual viewport that shrinks the way iOS's does, while
// the layout viewport keeps its height. Picking one member, the composer says how the message
// travels: a session whose claude runs without the development-channels flag gets it on its next
// prompt, whatever its bridge acks. The To picker opens on the channel's main session, and
// Everyone stays its first option. The members sheet lists each session with the files it
// claimed and the owner's remove action. The channel's Settings view lists the sessions linked into
// it, each with its level and its unlink. The specs that post or link run on channels of their own,
// removed after each test, so the seeded channel keeps exactly its ping and pong and stays the only
// one.

import { expect, type Locator, type Page, test } from "@playwright/test";
import { axeViolations, expectAccessible } from "./axe.ts";
import { api, expectSheetGutter, openTab, signIn, theSetup } from "./helpers.ts";
import { repoRoot } from "./paths.ts";

/** The height the played keyboard covers: about the iPhone 15's own. */
const KEYBOARD = 300;

/** The parts axe leaves to their own specs: the shell's chrome and the channel rail beside the
 *  page on wide screens. */
const SHELL = ["[data-sidebar]", "[data-nav]", "#side"] as const;

/** The work page's colour-contrast debt that predates it, named by its node: the status pill's
 *  tone ink, the same tinted-ink pair every status pill on the hub paints. Its card retires the
 *  entry when it fixes it. */
const WORK_KNOWN: readonly { rule: string; node: string }[] = [
  { rule: "color-contrast", node: ".c-yellow" },
];

/** Axe over the work page, less the shell, `extra`'s parts and the known debts. */
async function expectWorkAccessible(page: Page, extra: readonly string[] = []): Promise<void> {
  const found = (await axeViolations(page, [...SHELL, ...extra]))
    .map((v) => ({
      ...v,
      nodes: v.nodes.filter(
        (node) => !WORK_KNOWN.some((k) => k.rule === v.id && node.startsWith(k.node)),
      ),
    }))
    .filter((v) => v.nodes.length > 0);
  expect(found).toEqual([]);
}

/** The team page's axe debts that predate it: the status pills' tone ink (kit's tinted-ink pair,
 *  the same one every hub pill paints) and the feed's tinted verb inks (app.css's hue carriers),
 *  each named by its node; and the roster row's accessible name, which carries the session's
 *  name and status word rather than the row's every visible word. HV-TOKENS-CHANNELS retires
 *  these with the page's own token pass. */
const TEAM_KNOWN: readonly { rule: string; node: string | RegExp }[] = [
  { rule: "color-contrast", node: ".badge-ghost" },
  { rule: "color-contrast", node: /\.c-(blue|lavender|idle)(\\\.|\b|$)/ },
  { rule: "label-content-name-mismatch", node: /^button\[aria-label="[^"]+, [^"]+"\]$/ },
];

/** Axe over the team page, less the shell, `extra`'s parts and the known debts. */
async function expectTeamAccessible(page: Page, extra: readonly string[] = []): Promise<void> {
  const found = (await axeViolations(page, [...SHELL, ...extra]))
    .map((v) => ({
      ...v,
      nodes: v.nodes.filter(
        (node) =>
          !TEAM_KNOWN.some(
            (k) =>
              k.rule === v.id &&
              (typeof k.node === "string" ? node.startsWith(k.node) : k.node.test(node)),
          ),
      ),
    }))
    .filter((v) => v.nodes.length > 0);
  expect(found).toEqual([]);
}

/** The channels this spec made, removed after each test so the specs after it see the run's one
 *  seeded channel alone. */
const made: { name: string; sessions: readonly string[] }[] = [];

test.afterEach(async () => {
  for (const { name, sessions } of made.splice(0)) {
    // a channel with a session online stays: each one leaves first (a removed one already left)
    for (const s of sessions) await api(`/api/c/${name}/op/leave?as=${s}`, {}).catch(() => {});
    const path = `/api/channels/${name}`;
    const r = await fetch(`${theSetup().rootUrl}${path}`, {
      method: "DELETE",
      headers: { "x-huddle-action": path, "x-huddle-token": theSetup().token },
    });
    if (!r.ok) throw new Error(`DELETE ${path} answered ${r.status}: ${await r.text()}`);
  }
});

/** A fresh channel with the named sessions joined, unique to this project and run. */
async function freshChannel(
  project: string,
  sessions: readonly string[],
  opts: { repo?: string } = {},
): Promise<string> {
  const name = `chat-${project}-${Date.now().toString(36)}`;
  await api("/api/channels", { name, repo: opts.repo });
  made.push({ name, sessions });
  for (const s of sessions) await api(`/api/c/${name}/op/join?as=${s}`, {});
  return name;
}

/** Plays the iOS keyboard: window.visualViewport becomes a stand-in whose height shrinks by
 *  `__kb(px)` and that fires resize, as Safari's does; the layout viewport stays as tall. */
async function playKeyboard(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const vv = new EventTarget();
    let kb = 0;
    Object.defineProperties(vv, {
      height: { get: () => window.innerHeight - kb },
      width: { get: () => window.innerWidth },
      offsetTop: { get: () => 0 },
      offsetLeft: { get: () => 0 },
      pageTop: { get: () => window.scrollY },
      pageLeft: { get: () => window.scrollX },
      scale: { get: () => 1 },
    });
    Object.defineProperty(window, "visualViewport", { configurable: true, get: () => vv });
    (window as unknown as { __kb: (px: number) => void }).__kb = (px: number) => {
      kb = px;
      vv.dispatchEvent(new Event("resize"));
    };
  });
}

/** Opens one channel in a signed-in browser. */
async function openChannel(page: Page, ch: string): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}`);
  await expect(page.locator("[data-chat]")).toBeVisible();
}

/** Every box of the locators, checked to be at least one 44 px tap target on each side. The
 *  thumb's 44 px floor is the phone's rule; under a pointer the design system's control is 40 px. */
async function expectTapTargets(targets: readonly Locator[]): Promise<void> {
  if (!test.info().project.use.hasTouch) return;
  for (const t of targets) {
    const box = await t.boundingBox();
    expect(box, `${t}`).not.toBeNull();
    expect(box?.height ?? 0, `${t} height`).toBeGreaterThanOrEqual(44);
    expect(box?.width ?? 0, `${t} width`).toBeGreaterThanOrEqual(44);
  }
}

/** The page scrolls nothing sideways. */
async function expectNoSideScroll(page: Page): Promise<void> {
  const over = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(over).toBeLessThanOrEqual(0);
}

/** Every element stands whole inside the viewport, however the page scrolls it into view. */
async function expectInsideViewport(page: Page, targets: readonly Locator[]): Promise<void> {
  const vp = page.viewportSize() ?? { width: 0, height: 0 };
  for (const t of targets) {
    await t.scrollIntoViewIfNeeded();
    const box = await t.boundingBox();
    expect(box, `${t}`).not.toBeNull();
    expect(box?.x ?? -1, `${t} left`).toBeGreaterThanOrEqual(0);
    expect(box?.y ?? -1, `${t} top`).toBeGreaterThanOrEqual(0);
    expect((box?.x ?? 0) + (box?.width ?? 0), `${t} right`).toBeLessThanOrEqual(vp.width + 1);
    expect((box?.y ?? 0) + (box?.height ?? 0), `${t} bottom`).toBeLessThanOrEqual(vp.height + 1);
  }
}

/** On the phone every element rests above the tab bar, never under it. */
async function expectClearOfTabBar(page: Page, targets: readonly Locator[]): Promise<void> {
  if (!test.info().project.use.hasTouch) return;
  const bar = await page.locator("[data-nav='bottom']").boundingBox();
  if (bar === null) return;
  for (const t of targets) {
    await t.scrollIntoViewIfNeeded();
    const box = await t.boundingBox();
    expect((box?.y ?? 0) + (box?.height ?? 0), `${t} clears the tab bar`).toBeLessThanOrEqual(bar.y + 1);
  }
}

/** A layer's controls at the pointer's full target: a radio or a switch is tapped through the
 *  label around it, so the label's box is the one the finger meets. */
async function expectLayerTargets(layer: Locator): Promise<void> {
  if (!test.info().project.use.hasTouch) return;
  const small = await layer.evaluate((root) =>
    [...root.querySelectorAll<HTMLElement>("button, input, select, textarea, a")]
      .filter((el) => el.getClientRects().length > 0)
      .map((el) => ({ el, r: (el.closest("label") ?? el).getBoundingClientRect() }))
      .filter(({ r }) => r.width > 0 && (r.width < 44 || r.height < 44))
      .map(
        ({ el, r }) =>
          `${el.tagName} "${el.textContent?.trim() || el.getAttribute("aria-label") || el.id}" ${r.width}x${r.height}`,
      ),
  );
  expect(small, "controls under the 44 px target").toEqual([]);
}

test("the channel page lists the seeded ping and pong with who told whom", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await openTab(page, "Channels");
  await page.locator('a[data-ch="e2e"]').click();

  const messages = page.locator("[data-messages]");
  await expect(messages.getByRole("heading", { name: "Messages" })).toBeVisible();
  const rows = messages.locator("[data-seq]");
  await expect(rows).toHaveCount(2);

  // newest last: the older ping opens the list, the pong that answers it sits under it
  const ping = rows.nth(0);
  const pong = rows.nth(1);
  await expect(ping).toContainText("alpha");
  await expect(ping).toContainText("beta");
  await expect(ping).toContainText("ping");
  await expect(pong).toContainText("beta");
  await expect(pong).toContainText("alpha");
  await expect(pong).toContainText("pong");
  // each row carries its age as a time element
  await expect(ping.locator("time")).toBeVisible();
  await expect(pong.locator("time")).toBeVisible();
});

test("the phone heads the channel page with #channel and a Channels back link", async ({ page }, info) => {
  test.skip(info.project.name !== "iphone", "the channel header is the phone's");
  await openChannel(page, "e2e");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("#e2e");
  // the tab row names the page: the intro paints no second title
  await expect(page.locator("[data-page-intro] h1")).toHaveCount(0);
  const back = page.getByRole("link", { name: "Back to Channels" });
  await expectTapTargets([back]);
  await back.click();
  await expect(page).toHaveURL(/#\/channels$/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Channels");
});

test("the plan progress breakdown stands in one column at 820 px, no row over another", async ({ page }) => {
  await page.setViewportSize({ width: 820, height: 1180 });
  await openChannel(page, "e2e");
  const rows = page.locator("[data-ovrows] > div");
  await expect(rows).toHaveCount(4);
  const boxes = await rows.evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect();
      return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
    }),
  );
  const overlaps = boxes.flatMap((a, i) =>
    boxes
      .slice(i + 1)
      .filter((b) => a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom)
      .map(() => i),
  );
  expect(overlaps, "rows that overlap another").toEqual([]);
  // one column: every row starts at the same left edge
  expect(new Set(boxes.map((b) => Math.round(b.left))).size).toBe(1);
});

test("the channel tabs fit one row on the phone", async ({ page }, info) => {
  test.skip(info.project.name !== "iphone", "the one scrolling row is the phone's");
  await openChannel(page, "e2e");
  const row = page.locator("[data-view-tabs]");
  const width = page.viewportSize()?.width ?? 0;
  for (const dest of ["overview", "today", "inbox", "team", "work", "knowledge", "settings"]) {
    await page.goto(`${theSetup().rootUrl}/#/channels/e2e/${dest}`);
    const active = row.locator(`[data-dest="${dest}"][aria-selected="true"]`);
    await expect(active).toBeVisible();

    // one line of 44 px tabs that scrolls sideways inside itself
    const box = await row.boundingBox();
    expect(box?.height ?? Number.POSITIVE_INFINITY, `${dest}: the row's height`).toBeLessThanOrEqual(60);
    const sizes = await row.evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth }));
    expect(sizes.scroll, `${dest}: the row scrolls sideways`).toBeGreaterThan(sizes.client);
    await expectNoSideScroll(page);

    // the open page's tab is scrolled into view
    await expect
      .poll(async () => {
        const b = await active.boundingBox();
        return b !== null && b.x >= 0 && b.x + b.width <= width + 1;
      }, { message: `${dest}: the active tab lies inside the viewport` })
      .toBe(true);
  }
});

test("the composer posts into the chat and stays above the keyboard", async ({ page }, info) => {
  const ch = await freshChannel(info.project.name, ["gamma", "delta"]);
  await playKeyboard(page);
  await openChannel(page, ch);

  const composer = page.locator("[data-chat-composer]");
  const box = composer.getByRole("textbox", { name: "Message" });
  const send = composer.getByRole("button", { name: "Send" });
  await expect(composer).toBeVisible();
  await expectTapTargets([box, send, composer.getByRole("combobox", { name: "To" })]);

  // the keyboard rises as the box takes focus
  await box.focus();
  await page.evaluate(
    (px) => (window as unknown as { __kb: (n: number) => void }).__kb(px),
    KEYBOARD,
  );
  const text = `hello from ${info.project.name}`;
  await box.fill(text);

  // the composer sits whole inside what the keyboard leaves visible
  const visible = await page.evaluate((px) => window.innerHeight - px, KEYBOARD);
  await expect.poll(async () => (await composer.boundingBox())?.y ?? -1).toBeGreaterThanOrEqual(0);
  await expect
    .poll(async () => {
      const b = await composer.boundingBox();
      return b ? b.y + b.height : Number.POSITIVE_INFINITY;
    })
    .toBeLessThanOrEqual(visible + 1);

  await send.click();
  const mine = page.locator("[data-messages] [data-seq]").filter({ hasText: text });
  await expect(mine).toHaveCount(1);
  await expect(mine).toContainText("You");
  await expect(mine.locator("time")).toBeVisible();
  await expect(box).toHaveValue("");
  // still above the keyboard once the message landed
  const after = await composer.boundingBox();
  expect((after?.y ?? 0) + (after?.height ?? 0)).toBeLessThanOrEqual(visible + 1);

  // the keyboard goes down: the composer rests above the tab bar again, nothing under it
  await page.evaluate(() => (window as unknown as { __kb: (n: number) => void }).__kb(0));
  const tabbar = page.locator("[data-nav='bottom']");
  if ((await tabbar.count()) > 0) {
    const bar = await tabbar.boundingBox();
    await expect
      .poll(async () => {
        const b = await composer.boundingBox();
        return b ? b.y + b.height : Number.POSITIVE_INFINITY;
      })
      .toBeLessThanOrEqual((bar?.y ?? 0) + 1);
  }
  await expectNoSideScroll(page);
  await expectAccessible(page, SHELL);
});

test("the composer says a message to a session without the channels flag waits for its next prompt", async ({
  page,
}, info) => {
  const ch = await freshChannel(info.project.name, ["eps"]);
  // the run seeds eps's session with no claude command line naming the flag, and its bridge
  // acks: the ack alone proves the bridge, not Claude Code's channel listener. Ended, so the
  // Now list's counts stay the run's own.
  const id = `waits-${info.project.name}`;
  await api("/api/registry/report", {
    session_id: id,
    machine: "e2e-machine",
    project: "e2e",
    channel: ch,
    member: "eps",
    last_event: "SessionEnd",
    last_event_at: new Date().toISOString(),
  });
  await api("/api/registry/push-ack", { channel: ch, as: "eps" });
  const row = await api<{ delivery_route: string }>(`/api/registry/sessions/${id}`);
  expect(row.delivery_route).toBe("next_prompt");

  await openChannel(page, ch);
  const composer = page.locator("[data-chat-composer]");
  await composer.getByRole("combobox", { name: "To" }).selectOption("eps");
  await expect(composer.locator("[data-route-line]")).toHaveText("Delivers on the next prompt");
  await expectNoSideScroll(page);
  await expectAccessible(page, SHELL);
});

test("the To picker opens on the seeded channel's main session and still offers Everyone", async ({
  page,
}) => {
  await openChannel(page, "e2e");
  const to = page.locator("[data-chat-composer]").getByRole("combobox", { name: "To" });
  // the seed joins alpha before beta: the roster's first top-level session is the main one
  await expect(to).toHaveValue("alpha");
  const first = to.locator("option").first();
  await expect(first).toHaveAttribute("value", "");
  await expect(first).toHaveText("Everyone");
});

test("the members sheet lists the sessions with their claims and removes one", async ({
  page,
}, info) => {
  const ch = await freshChannel(info.project.name, ["gamma", "delta"]);
  await api(`/api/c/${ch}/op/reserve?as=gamma`, {
    paths: ["/work/app/src/chat.ts"],
    note: "the chat",
  });
  await openChannel(page, ch);

  const open = page.getByRole("button", { name: /^Members/ });
  await expectTapTargets([open]);
  await open.click();
  const sheet = page.getByRole("dialog", { name: "Members" });
  await expect(sheet).toBeVisible();

  const gamma = sheet.locator('[data-member="gamma"]');
  const delta = sheet.locator('[data-member="delta"]');
  await expect(gamma).toBeVisible();
  await expect(delta).toBeVisible();
  // gamma's claim: the file it reserved, under its name
  await expect(gamma.locator("[data-claim]")).toContainText("chat.ts");
  await expect(delta.locator("[data-claim]")).toHaveCount(0);
  await expectAccessible(page, SHELL);

  const remove = gamma.getByRole("button", { name: "Remove gamma" });
  await expectTapTargets([remove]);
  await remove.click();
  const confirm = gamma.locator("[data-member-confirm]");
  await expect(confirm).toContainText("Remove gamma from this channel?");
  await expectTapTargets([
    confirm.getByRole("button", { name: "Remove" }),
    confirm.getByRole("button", { name: "Cancel" }),
  ]);
  await confirm.getByRole("button", { name: "Cancel" }).click();
  await expect(confirm).toHaveCount(0);

  await gamma.getByRole("button", { name: "Remove gamma" }).click();
  await gamma.locator("[data-member-confirm]").getByRole("button", { name: "Remove" }).click();
  await expect(sheet.locator('[data-member="gamma"]')).toHaveCount(0);
  await expect(delta).toBeVisible();
  await expectNoSideScroll(page);
});

test("the overview page keeps its layers and controls at the pointer's size", async ({ page }, info) => {
  const ch = await freshChannel(info.project.name, ["gamma", "delta"]);
  await api(`/api/c/${ch}/op/reserve?as=gamma`, {
    paths: ["/work/app/src/chat.ts"],
    note: "the chat",
  });
  await openChannel(page, ch);

  // the page's own controls take the pointer's full target: the activity window's chips carry
  // the 44 px hit area their css draws around the 28 px chip, and the Members button is one
  // full target itself
  const segment = page.getByRole("group", { name: "Activity window" });
  await expect(segment.locator("[data-win]")).toHaveCount(3);
  const members = page.getByRole("button", { name: /^Members/ });
  await expectTapTargets([members]);
  await expectInsideViewport(page, [...(await segment.locator("[data-win]").all()), members]);
  if (test.info().project.use.hasTouch) {
    const hits = await segment.evaluate((root) =>
      [...root.querySelectorAll<HTMLElement>("[data-win]")].map((el) => {
        const r = el.getBoundingClientRect();
        const before = getComputedStyle(el, "::before");
        const p = before.position === "absolute" ? Math.abs(Number.parseFloat(before.top)) : 0;
        return { l: r.left, r: r.right, t: r.top - p, b: r.bottom + p };
      }),
    );
    expect(hits.length > 0 && hits.every((h) => h.b - h.t >= 44 - 0.01)).toBe(true);
    expect(
      hits.every((a, i) =>
        hits.every(
          (b, j) =>
            i === j || a.r <= b.l || b.r <= a.l || a.b <= b.t || b.b <= a.t,
        ),
      ),
    ).toBe(true);
  }
  await expectNoSideScroll(page);

  // the members sheet: the page's one layer, whole inside the viewport, clear of the tab bar,
  // inside the phone's side gutter, its controls at the pointer's full target, and the page
  // still reads clean
  await page.getByRole("button", { name: /^Members/ }).click();
  const sheet = page.getByRole("dialog", { name: "Members" });
  await expect(sheet).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [sheet]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [sheet]);
  await expectSheetGutter(sheet.locator(".ui-sheet-body"));
  await expectLayerTargets(sheet);
  await expectAccessible(page, SHELL);

  // a confirmed remove raises the page's toast: alone, whole inside the viewport, above the
  // tab bar
  await sheet.locator('[data-member="gamma"]').getByRole("button", { name: "Remove gamma" }).click();
  await sheet
    .locator('[data-member="gamma"]')
    .locator("[data-member-confirm]")
    .getByRole("button", { name: "Remove" })
    .click();
  await expect(sheet.locator('[data-member="gamma"]')).toHaveCount(0);
  const toast = page.locator(".ui-toast");
  await expect(toast).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [toast]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [toast]);
  await expectNoSideScroll(page);
  await expectAccessible(page, SHELL);
});

test("the compose dialog, its picker menu and its toast keep one layer inside the viewport", async ({
  page,
}, info) => {
  const ch = await freshChannel(info.project.name, ["gamma"]);
  await openChannel(page, ch);

  // the overview's Send a message opens the compose dialog: the one layer on screen, whole
  // inside the viewport and clear of the tab bar, its composer controls at the pointer's
  // full target
  const open = page.getByRole("button", { name: "Send a message" });
  await expectTapTargets([open]);
  await open.click();
  const dialog = page.getByRole("dialog", { name: "Send a message" });
  await expect(dialog).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [dialog]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [dialog]);
  await expectTapTargets([
    dialog.getByRole("combobox", { name: "To" }),
    dialog.getByRole("button", { name: "Message" }),
    dialog.getByRole("button", { name: "Question" }),
    dialog.getByRole("button", { name: "Task" }),
    dialog.getByRole("button", { name: "Cancel" }),
    dialog.getByRole("button", { name: "Send to everyone" }),
  ]);
  await expectNoSideScroll(page);
  await expectAccessible(page, SHELL);

  // task mode retitles the dialog to "New task", adds the title field, and the waits-on
  // picker opens its menu as the same single layer
  await dialog.getByRole("button", { name: "Task" }).click();
  const task = page.getByRole("dialog", { name: "New task" });
  await expect(task).toBeVisible();
  const title = task.getByRole("textbox", { name: "Title" });
  await expect(title).toBeVisible();
  // the browser must not offer old entries over the title
  await expect(title).toHaveAttribute("autocomplete", "off");
  await expectTapTargets([title, task.getByRole("textbox", { name: "Details" })]);
  await task.getByRole("combobox", { name: "Tasks it waits on" }).focus();
  const menu = task.locator("#dlg-after-menu");
  await expect(menu).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [task]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [task]);
  // the picker's own empty menu paints text inside its role=listbox, the debt picker.tsx
  // carries; axe takes the rest of the layer
  await expectAccessible(page, [...SHELL, "#dlg-after-menu"]);
  await page.keyboard.press("Escape");

  // a send closes the dialog and raises the confirmation toast: alone, whole inside the
  // viewport, above the tab bar
  await task.getByRole("button", { name: "Message" }).click();
  const back = page.getByRole("dialog", { name: "Send a message" });
  await back
    .getByRole("textbox", { name: "Message" })
    .fill(`compose layers check from ${info.project.name}`);
  await back.getByRole("button", { name: /^Send to everyone/ }).click();
  const sent = page.locator(".ui-toast");
  await expect(sent).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [sent]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [sent]);
  await expectNoSideScroll(page);
  await expectAccessible(page, SHELL);
});

test("the channel's Settings view lists the sessions linked into it", async ({ page }, info) => {
  const ch = await freshChannel(info.project.name, []);
  // two ended sessions: linkable, and out of the Now list's Working and Needs you counts
  const sessions = ["a", "b"].map((x) => `link-${info.project.name}-${x}`);
  for (const id of sessions) {
    await api("/api/registry/report", {
      session_id: id,
      machine: "e2e-machine",
      project: "e2e",
      title: `Linked ${id}`,
      last_event: "SessionEnd",
      last_event_at: new Date().toISOString(),
    });
  }
  await api("/api/links", { sessions, channel: ch, level: "digest" });
  try {
    await openChannel(page, ch);
    await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/settings`);
    const linked = page.locator("[data-linked-sessions]");
    await expect(linked.getByRole("heading", { name: "Linked sessions" })).toBeVisible();
    for (const id of sessions) {
      const row = linked.locator(`[data-link="${id}"]`);
      await expect(row).toContainText(`Linked ${id}`);
      await expect(row.getByRole("combobox", { name: `Level for Linked ${id}` })).toHaveValue("digest");
      await expectTapTargets([row.getByRole("button", { name: "Unlink" })]);
    }
    await expect(linked.getByRole("button", { name: "Link another session" })).toBeVisible();
    await expectNoSideScroll(page);
  } finally {
    for (const id of sessions) {
      const path = `/api/links/${ch}/${id}`;
      await fetch(`${theSetup().rootUrl}${path}`, {
        method: "DELETE",
        headers: { "x-huddle-action": path, "x-huddle-token": theSetup().token },
      });
    }
  }
});

test("the channel's settings page holds the mock's sections and saves the name", async ({
  page,
}, info) => {
  const ch = await freshChannel(info.project.name, []);
  await openChannel(page, ch);
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/settings`);
  await expect(page.locator('[data-section="about"]')).toBeVisible();

  // the sections stand in the mock's order: linked sessions between Repository and This browser
  const order = await page.evaluate(() =>
    [...document.querySelectorAll("[data-section], [data-linked-sessions]")].map(
      (el) => el.getAttribute("data-section") ?? "linked",
    ),
  );
  expect(order).toEqual(["about", "members", "turn", "repo", "linked", "browser", "export"]);

  // a fresh channel's rows fall back to the mock's quiet words
  await expect(page.locator('[data-section="about"]')).toContainText(ch);
  await expect(page.locator('[data-section="about"]')).toContainText("None");
  await expect(page.locator('[data-section="members"]')).toContainText("Any local session");
  await expect(page.locator('[data-section="turn"]')).toContainText("Free (parallel)");
  await expect(page.locator('[data-section="repo"]')).toContainText("Not set");

  // About › Name opens the sheet; a save posts the title and the row says it
  const about = page.locator('[data-section="about"]');
  const nameRow = about.getByRole("button", { name: /^Name/ });
  await expectTapTargets([nameRow]);
  await nameRow.click();
  const sheet = page.getByRole("dialog", { name: "About" });
  await expect(sheet).toBeVisible();
  // the description stands three rows open, as its native textarea did
  const description = sheet.getByLabel("What the team is doing");
  await expect(description).toHaveAttribute("rows", "3");
  // and the box paints those rows: where the browser sizes the box to its content, the rows
  // attribute alone sets nothing
  const height = await description.evaluate((el) => {
    const s = getComputedStyle(el);
    const line = Number.parseFloat(s.lineHeight);
    if (Number.isNaN(line)) return null;
    const pad = Number.parseFloat(s.paddingTop) + Number.parseFloat(s.paddingBottom);
    const border = Number.parseFloat(s.borderTopWidth) + Number.parseFloat(s.borderBottomWidth);
    return { floor: 3 * line + pad + border, actual: el.getBoundingClientRect().height };
  });
  expect(height, "the line-height did not resolve to a length").not.toBeNull();
  expect(height?.actual ?? 0, `three lines need ${height?.floor}px`).toBeGreaterThanOrEqual(
    height?.floor ?? 0,
  );
  // the Field input, named through its label now that the page's ids stay unique
  await sheet.getByLabel("Title").fill(`Named ${ch}`);
  await sheet.getByRole("button", { name: "Save" }).click();
  await expect(sheet).toBeHidden();
  await expect(about).toContainText(`Named ${ch}`);
  await expectNoSideScroll(page);
  await expectAccessible(page, SHELL);
});

test("the channel settings page gives every element an id of its own", async ({ page }, info) => {
  const ch = await freshChannel(info.project.name, []);
  await openChannel(page, ch);
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/settings`);
  await expect(page.locator('[data-section="about"]')).toBeVisible();

  // About › Name opens the sheet that holds the field
  await page.locator('[data-section="about"]').getByRole("button", { name: /^Name/ }).click();
  const sheet = page.getByRole("dialog", { name: "About" });
  await expect(sheet).toBeVisible();

  // no two elements share an id: a repeat makes label[for] and aria-labelledby resolve to the
  // first match in the tree, leaving the later element with no accessible name
  const repeats = await page.evaluate(() => {
    const seen = new Set<string>();
    const dupes: string[] = [];
    for (const el of document.querySelectorAll("[id]")) {
      if (seen.has(el.id)) dupes.push(el.id);
      seen.add(el.id);
    }
    return dupes;
  });
  expect(repeats).toEqual([]);

  // the field's label reaches the input the way a reader and a tap both name it
  await sheet.getByLabel("Title").fill(`Named ${ch}`);
  await sheet.getByRole("button", { name: "Save" }).click();
  await expect(sheet).toBeHidden();
  await expect(page.locator('[data-section="about"]')).toContainText(`Named ${ch}`);
});

test("the settings page keeps its sheets, the link sheet and the toast inside the viewport", async ({
  page,
}, info) => {
  const ch = await freshChannel(info.project.name, []);
  // two ended sessions linked, so the page's link-another sheet joins its layers
  const sessions = ["a", "b"].map((x) => `layer-${info.project.name}-${x}`);
  for (const id of sessions) {
    await api("/api/registry/report", {
      session_id: id,
      machine: "e2e-machine",
      project: "e2e",
      last_event: "SessionEnd",
      last_event_at: new Date().toISOString(),
    });
  }
  await api("/api/links", { sessions, channel: ch, level: "digest" });
  try {
    await openChannel(page, ch);
    await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/settings`);
    await expect(page.locator('[data-section="about"]')).toBeVisible();

    // every sheet stands alone: one layer at a time, whole inside the viewport, its own
    // controls at the pointer's full target, and the page still reads clean
    for (const [section, row] of [
      ["about", /^Name/],
      ["members", /^api|^Any local session/],
      ["turn", /^Turn order/],
      ["repo", /^Path/],
    ] as const) {
      await page.locator(`[data-section="${section}"]`).getByRole("button", { name: row }).click();
      const layer = page.locator("dialog[open]");
      await expect(layer).toHaveCount(1);
      await expectInsideViewport(page, [layer.first()]);
      // the tab bar stands down while a layer is open: what remains above it is itself
      if ((await page.locator("[data-nav='bottom']").count()) > 0)
        await expectClearOfTabBar(page, [layer.first()]);
      await expectLayerTargets(layer.first());
      await expectAccessible(page, SHELL);
      await page.keyboard.press("Escape");
      await expect(layer).toHaveCount(0);
    }

    // a saved sheet raises the page's toast: alone, inside the viewport, above the tab bar
    await page.locator('[data-section="about"]').getByRole("button", { name: /^Name/ }).click();
    const about = page.getByRole("dialog", { name: "About" });
    await about.getByLabel("Title").fill(`Layered ${ch}`);
    await about.getByRole("button", { name: "Save" }).click();
    const toast = page.locator(".ui-toast");
    await expect(toast).toBeVisible();
    expect(await page.locator("dialog[open]").count()).toBeLessThanOrEqual(1);
    await expectInsideViewport(page, [toast]);
    if ((await page.locator("[data-nav='bottom']").count()) > 0)
      await expectClearOfTabBar(page, [toast]);
    await expectAccessible(page, SHELL);

    // the link sheet over the linked sessions: the page's one layer again, its picker, its
    // fields and its submit at the full target
    await page.locator("[data-link-another]").click();
    const link = page.locator("dialog[open]");
    await expect(link.locator("[data-link-sheet]")).toBeVisible();
    expect(await link.count()).toBeLessThanOrEqual(1);
    await expectInsideViewport(page, [link.first()]);
    if ((await page.locator("[data-nav='bottom']").count()) > 0)
      await expectClearOfTabBar(page, [link.first()]);
    await expectLayerTargets(link.first());
    await expectSheetGutter(link.locator("[data-link-sheet]"));
    // the sheet's one select stands inside the panel: a filled control that reaches the
    // panel's right edge sits under the panel's corner radius and reads as clipped
    if (test.info().project.use.hasTouch) {
      const edges = await link.first().evaluate((root) => {
        const panel = root.closest("dialog");
        const select = root.querySelector("select");
        if (!panel || !select) return null;
        const gutter = Number.parseFloat(getComputedStyle(panel).paddingRight) || 0;
        return {
          content: panel.getBoundingClientRect().right - gutter,
          select: select.getBoundingClientRect().right,
        };
      });
      expect(edges, "the sheet's select is on the page").not.toBeNull();
      expect(
        edges?.select ?? 0,
        "the select's right edge stays inside the sheet panel",
      ).toBeLessThan(edges?.content ?? 0);
    }
    await expectAccessible(page, SHELL);
    await page.keyboard.press("Escape");
    await expect(link).toHaveCount(0);
    await expectNoSideScroll(page);
  } finally {
    for (const id of sessions) {
      const path = `/api/links/${ch}/${id}`;
      await fetch(`${theSetup().rootUrl}${path}`, {
        method: "DELETE",
        headers: { "x-huddle-action": path, "x-huddle-token": theSetup().token },
      });
    }
  }
});

/** The work tab's views this spec drives, in the order the strip spells them. */
const WORK_VIEWS = ["list", "board", "graph"] as const;

test("the work list, board and graph keep their layers and controls at the pointer's size", async ({
  page,
}, info) => {
  const ch = await freshChannel(info.project.name, ["alpha"]);
  // one planned task, the one waiting on it, and one free task: three list rows, three board
  // cards, a graph with an edge to trace, and a dependency left for the picker to offer
  await api(`/api/c/${ch}/op/task_create?as=alpha`, { id: "wk-a", title: "First work task", phase: 1 });
  await api(`/api/c/${ch}/op/task_create?as=alpha`, {
    id: "wk-b",
    title: "Second work task",
    after: ["wk-a"],
    phase: 1,
  });
  await api(`/api/c/${ch}/op/task_create?as=alpha`, { id: "wk-c", title: "Third work task", phase: 1 });

  await openChannel(page, ch);

  // the view strip, the page's own buttons and the filter bar ride every view
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/work/list`);
  await expect(page.locator("[data-tid]")).toHaveCount(3);
  await expectNoSideScroll(page);
  const strip = page.getByRole("tablist", { name: "View" });
  const pageControls = [
    strip,
    page.getByRole("button", { name: "New task" }),
    page.getByText("Import a plan"),
    page.getByPlaceholder("Filter by id, title or owner"),
    page.locator("#wown"),
  ];
  await expectTapTargets(pageControls);
  await expectInsideViewport(page, pageControls);
  // the filter chips keep 28 px of chip under the shared 44 px hit area on a touch screen (the
  // same check the knowledge page's kind chips make)
  if (test.info().project.use.hasTouch) {
    const hits = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>("[data-f]")].every((el) => {
        const r = el.getBoundingClientRect();
        const before = getComputedStyle(el, "::before");
        const p = before.position === "absolute" ? Math.abs(Number.parseFloat(before.top)) : 0;
        return r.width > 0 && r.height + 2 * p >= 44 - 0.01;
      }),
    );
    expect(hits, "each filter chip's hit stands at least 44 px tall").toBe(true);
  }
  await expect(page.locator("dialog[open]")).toHaveCount(0);
  await expectWorkAccessible(page);

  // the drawer over the list: the page's one layer, its picker inside it
  await page.locator("[data-tid='wk-b']").click();
  const drawer = page.locator("dialog[open]");
  await expect(drawer.locator("[data-slide-over]")).toBeVisible();
  expect(await drawer.count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [drawer.first()]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [drawer.first()]);

  // the dependency picker's menu opens under its combobox, inside the drawer, and its rows and
  // its box stand at the pointer's target; the drawer's own markup stays the TaskDrawer card's,
  // so the axe read here covers the page and the picker alone
  const picker = drawer.getByRole("combobox", { name: "Add a task it waits on" });
  await expect(picker).toBeVisible();
  await picker.focus();
  // the drawer's own picker: the composer's closed dialog keeps one too, so the read scopes here
  const options = drawer.locator("[role='option']");
  await expect(options.filter({ hasText: "wk-c" })).toBeVisible();
  expect(await drawer.count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [options.first()]);
  if (test.info().project.use.hasTouch) {
    const small = await drawer.evaluate((root) =>
      [...root.querySelectorAll<HTMLElement>("[data-picker] button, [data-picker] input")]
        .filter((el) => el.getClientRects().length > 0)
        .filter((el) => {
          const r = el.getBoundingClientRect();
          return r.width > 0 && (r.width < 44 || r.height < 44);
        })
        .map((el) => {
          const r = el.getBoundingClientRect();
          const label = el.getAttribute("aria-label") ?? el.textContent?.trim() ?? el.id;
          return `${el.tagName} "${label}" ${r.width}x${r.height}`;
        }),
    );
    expect(small, "picker controls under the 44 px target").toEqual([]);
  }
  await expectAccessible(page, [
    ...SHELL,
    "dialog[open] [data-slide-over] *:not([data-picker]):not([data-picker] *)",
  ]);
  // the picker's menu takes the first Escape; the second falls through to the drawer
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  await expect(drawer).toHaveCount(0);

  for (const view of WORK_VIEWS) {
    await test.step(`the ${view} view`, async () => {
      await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/work/${view}`);
      if (view === "list") await expect(page.locator("[data-tid]")).toHaveCount(3);
      if (view === "board") await expect(page.locator("section[data-col='todo'] [data-id]")).toHaveCount(3);
      if (view === "graph") await expect(page.locator("svg.dgraph g[data-i]")).toHaveCount(3);
      await expectNoSideScroll(page);
      // the dependency graph is a horizontally scrollable drawing: on a narrow screen a box sits
      // across the panel's clip edge, and axe's 2.5.3 check reads only that box's visible painted
      // texts — a word sequence that cannot be contained by any one accessible name, since each
      // scroll position exposes a different subset with different word gluing. Every box carries
      // its full aria-label (asserted by unit tests), so the drawing itself stays out of this read.
      await expectWorkAccessible(page, ["svg.dgraph"]);

      // the board's move menu: the view's one popup layer, with the toast its pick raises
      if (view === "board") {
        const move = page.locator("button[data-mv='wk-a']");
        await expectTapTargets([move]);
        await move.click();
        const menu = page.getByRole("menu");
        await expect(menu).toBeVisible();
        expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
        await expectInsideViewport(page, [menu]);
        if ((await page.locator("[data-nav='bottom']").count()) > 0)
          await expectClearOfTabBar(page, [menu]);
        await expectWorkAccessible(page);
        await menu.getByRole("menuitem", { name: "Move to Done" }).click();
        const toast = page.locator(".ui-toast");
        await expect(toast).toBeVisible();
        expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
        await expectInsideViewport(page, [toast]);
        if ((await page.locator("[data-nav='bottom']").count()) > 0)
          await expectClearOfTabBar(page, [toast]);
        await expectNoSideScroll(page);
        await expectWorkAccessible(page);
      }

      // the graph's trace: a pick lights the river and offers the drawer at the pointer's size
      if (view === "graph") {
        await page.locator("svg.dgraph g[data-i='1']").click();
        const open = page.getByRole("link", { name: "Open wk-b" });
        await expect(open).toBeVisible();
        await expectTapTargets([open]);
        await expectInsideViewport(page, [open]);
        await expectWorkAccessible(page, ["svg.dgraph"]);
      }
    });
  }
});

test("the work board keeps its 250 px column scroll cap on the token scale", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the cap is a md+ width rule");
  const ch = await freshChannel(info.project.name, ["alpha"]);
  // enough cards that the To do column's list clamps at the cap
  for (let i = 0; i < 24; i++) {
    await api(`/api/c/${ch}/op/task_create?as=alpha`, { id: `cap-${i}`, title: `Cap task ${i}`, phase: 1 });
  }
  await openChannel(page, ch);
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/work/board`);
  const list = page.locator("section[data-col='todo'] ul");
  await expect(list).toBeVisible();
  const h = await list.evaluate((el) => ({
    rect: el.getBoundingClientRect().height,
    max: Number.parseFloat(getComputedStyle(el).maxHeight),
    dvh: window.innerHeight,
  }));
  // the cap is 100dvh minus the same 250 px the page always reserved: 250 px = 250/15 rem
  console.log(`CAPMEASURE max=${h.max} rect=${h.rect} dvh=${h.dvh}`);
  expect(Math.abs(h.max - (h.dvh - 250)), `computed cap ${h.max} at dvh ${h.dvh}`).toBeLessThanOrEqual(0.06);
  expect(h.rect, "the list is clamped at the cap").toBeGreaterThan(h.max - 2);
});

test("the work map and the repo views keep their layers inside the viewport", async ({ page }, info) => {
  // a channel with a repo: the repo's Code and Drift views join the page
  const ch = await freshChannel(info.project.name, ["alpha"], { repo: repoRoot });
  // two chained tasks: the map's critical path carries their drawer links
  await api(`/api/c/${ch}/op/task_create?as=alpha`, { id: "map-a", title: "First map task", phase: 1 });
  await api(`/api/c/${ch}/op/task_create?as=alpha`, {
    id: "map-b",
    title: "Second map task",
    after: ["map-a"],
    phase: 1,
  });

  await openChannel(page, ch);
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/work/map`);
  const map = page.locator("main");
  await expect(map.getByRole("heading", { name: "Critical path" })).toBeVisible();
  await expectNoSideScroll(page);
  const graph = map.getByRole("link", { name: "Show in Graph" });
  await expectTapTargets([graph]);
  await expectInsideViewport(page, [graph]);
  await expectClearOfTabBar(page, [graph]);
  await expectWorkAccessible(page);

  // the page's one layer: a chain task's drawer over the map. The drawer's own markup belongs to
  // the TaskDrawer card, so its open dialog stays out of the page's axe read.
  await map.locator("ol a").filter({ hasText: "Second map task" }).click();
  const layer = page.locator("dialog[open]");
  await expect(layer.locator("[data-slide-over]")).toBeVisible();
  expect(await layer.count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [layer.first()]);
  await expectAccessible(page, [...SHELL, "dialog[open]"]);
  await page.keyboard.press("Escape");
  await expect(layer).toHaveCount(0);

  // the repo's code view: the chips, the listing and the folder read on the tokens. The README's
  // markdown (web/ui's renderer over app.css's .code fences) stays out of the page's read; its
  // cards own it.
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/work/repo/code`);
  const files = page.getByRole("region", { name: "Files" });
  await expect(files).toBeVisible();
  await expectNoSideScroll(page);
  const chips = await page.getByRole("group", { name: "Repo view" }).locator("a").all();
  await expectTapTargets(chips);
  await expectInsideViewport(page, chips);
  await expectClearOfTabBar(page, chips);
  await expectWorkAccessible(page, [".prose-h"]);

  // the drift view: its table scrolls inside its own region
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/work/repo/drift`);
  await expect(page.getByRole("region", { name: "Drift rows" })).toBeVisible();
  await expectNoSideScroll(page);
  await expectWorkAccessible(page);
});

test("the task drawer holds its tabs, menu, excerpt dialog and toast at the pointer's size", async ({
  page,
}, info) => {
  // a channel with a repo: the file chips open excerpts, and every body section is seeded
  const ch = await freshChannel(info.project.name, ["alpha"], { repo: repoRoot });
  await api(`/api/c/${ch}/op/task_create?as=alpha`, {
    id: "drawer-1",
    title: "The drawer carries every section",
    owner: "alpha",
    what: "The task drawer paints with web/ui only.",
    how: ["Read the brief whole", "Swap section by section"],
    verify: [{ cmd: "bun run check", expect: "green" }],
  });
  await api(`/api/c/${ch}/op/task_update?as=alpha`, {
    id: "drawer-1",
    field: "value",
    value: "Done means the guard and the e2e run green.",
  });
  await api(`/api/c/${ch}/op/task_update?as=alpha`, {
    id: "drawer-1",
    field: "snippets",
    value: [{ title: "Tabs", lang: "tsx", path: "web/hub/src/work/TaskDrawer.tsx", code: "<Tabs />" }],
  });
  await api(`/api/c/${ch}/op/task_update?as=alpha`, {
    id: "drawer-1",
    field: "files",
    value: ["web/hub/src/work/TaskDrawer.tsx"],
  });
  await api(`/api/c/${ch}/op/task_update?as=alpha`, {
    id: "drawer-1",
    field: "refs",
    value: ["ADR-4"],
  });
  await api(`/api/c/${ch}/op/note?as=alpha`, {
    id: "drawer-1",
    kind: "direction",
    body: "The notes the sessions read.",
  });

  // the drawer over the work page: the one layer, whole inside the viewport, its controls at
  // the pointer's full target, the drawer's own markup reading clean
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/work?t=drawer-1`);
  const drawer = page.getByRole("dialog", { name: "Task drawer-1" });
  await expect(drawer.locator("[data-slide-over]")).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expect(drawer.locator("#td-title")).toContainText("drawer-1");
  await expect(drawer.locator('[data-sec="what"]')).toBeVisible();
  // the dependency Picker's h-7 input is picker.tsx's own debt (the Work card's file); on a
  // touch width every control of the drawer's own markup takes the pointer's full target
  if (test.info().project.use.hasTouch) {
    const small = await drawer.evaluate((root) =>
      [...root.querySelectorAll<HTMLElement>("button, input, select, textarea, a")]
        .filter((el) => el.getClientRects().length > 0)
        .filter((el) => el.getAttribute("placeholder") !== "Add a task it waits on")
        .map((el) => ({ el, r: (el.closest("label") ?? el).getBoundingClientRect() }))
        .filter(({ r }) => r.width > 0 && (r.width < 44 || r.height < 44))
        .map(
          ({ el, r }) =>
            `${el.tagName} "${el.textContent?.trim() || el.getAttribute("aria-label") || el.id}" ${r.width}x${r.height}`,
        ),
    );
    expect(small, "controls under the 44 px target").toEqual([]);
  }
  await expectInsideViewport(page, [drawer.locator("[data-slide-over]")]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [drawer.locator("[data-slide-over]")]);
  await expectAccessible(page, SHELL);

  // the Code tab: the snippet's tabs, the file chip and the refs, still one layer
  await drawer.locator('[data-tab="code"]').click();
  await expect(drawer.locator("#snip-body")).toBeVisible();
  await expect(drawer.getByText("Tabs", { exact: true })).toBeVisible();
  await expectLayerTargets(drawer);
  await expectAccessible(page, SHELL);

  // the file chip's excerpt dialog rides over the drawer: both stay inside the viewport, the
  // excerpt on top, and closing it hands the one layer back
  await drawer.locator('[data-sec="files"] [data-file]').click();
  const excerpt = page.locator("dialog[open]").last();
  await expect(excerpt.getByText("Lines 1 to")).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "drawer plus its excerpt").toBe(2);
  await expectInsideViewport(page, [excerpt]);
  await excerpt.getByRole("button", { name: "Close" }).click();
  await expect(page.locator("dialog[open]")).toHaveCount(1);

  // the Add section menu opens over the drawer as its own popup, and a pick lands the field's
  // editor straight away
  await drawer.locator('[data-tab="overview"]').click();
  await drawer.getByRole("button", { name: "Add section" }).click();
  const menu = drawer.locator(".menu.fixed");
  await expect(menu).toBeVisible();
  await expect(menu.getByRole("menuitem", { name: "How to use it" })).toBeVisible();
  await expectInsideViewport(page, [menu]);
  await expectAccessible(page, SHELL);
  await menu.getByRole("menuitem", { name: "How to use it" }).click();
  await expect(drawer.locator('[data-editor="use"] textarea')).toBeVisible();
  await expect(menu).toHaveCount(0);
  await drawer.getByRole("button", { name: "Cancel" }).click();
  await expect(drawer.locator('[data-editor="use"] textarea')).toHaveCount(0);

  // a status change with evidence lands the confirmation toast: alone, whole, above the tab bar
  await drawer.getByPlaceholder("Evidence or reason for the next change (optional)").fill("looks right");
  await drawer.locator('[data-st="doing"]').click();
  const toast = page.locator(".ui-toast");
  await expect(toast).toBeVisible();
  await expectInsideViewport(page, [toast]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [toast]);
  await expectNoSideScroll(page);
});

test("the knowledge page keeps its export menu and toast inside the viewport", async ({
  page,
}, info) => {
  const ch = await freshChannel(info.project.name, ["gamma"]);
  await api(`/api/c/${ch}/op/remember?as=gamma`, {
    kind: "fact",
    title: `Router port (${ch})`,
    body: "huddle-router listens on loopback only, and the hub reaches it there.",
    tags: ["ports", "router"],
  });
  await openChannel(page, ch);
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/knowledge`);
  const view = page.viewportSize();
  await expect(page.locator("#kblist")).toBeVisible();
  await expect(page.locator("#kblist")).toContainText(`Router port (${ch})`);

  // the page's own controls take the pointer's full target
  const exp = page.getByRole("button", { name: "Export" });
  const search = page.getByLabel("Search knowledge");
  const chips = page.locator("[data-k]");
  await expect(chips).toHaveCount(7);
  await expectTapTargets([exp, search, page.locator("#kblist a").first()]);
  // the kind chips take the finger's 44 px target through the hit area the Chip CSS draws
  // around their 28 px box: at a coarse pointer the ::before reaches --ui-tap out, and no
  // two hit areas overlap (the same check the tokens page makes for its chips)
  if (test.info().project.use.hasTouch) {
    const hits = await page.evaluate(() => {
      const chips = [...document.querySelectorAll<HTMLElement>("[data-k]")].map((el) => {
        const r = el.getBoundingClientRect();
        const before = getComputedStyle(el, "::before");
        const p = before.position === "absolute" ? Math.abs(Number.parseFloat(before.top)) : 0;
        return { r, hit: { l: r.left, r: r.right, t: r.top - p, b: r.bottom + p } };
      });
      return {
        tall: chips.length > 0 && chips.every((c) => c.hit.b - c.hit.t >= 44 - 0.01),
        apart: chips.every((a, i) =>
          chips.every(
            (b, j) =>
              i === j ||
              a.hit.r <= b.hit.l ||
              b.hit.r <= a.hit.l ||
              a.hit.b <= b.hit.t ||
              b.hit.b <= a.hit.t,
          ),
        ),
      };
    });
    expect(hits.tall, "each kind chip's hit stands at least 44 px tall").toBe(true);
    expect(hits.apart, "no two kind chips' hit areas overlap").toBe(true);
  }
  // the Kind chips wrap to at most two rows at the phone's width: 28 px chips on rows the
  // coarse pointer stands --ui-space-4 (16 px) apart — the wrapped rows' hit-area room —
  // so two rows stand 72 px
  const kind = page.getByRole("group", { name: "Kind" });
  const kindBox = await kind.boundingBox();
  expect(kindBox?.height ?? 0).toBeLessThanOrEqual(2 * 28 + 16 + 1);
  await expectAccessible(page, SHELL);

  // the export menu is the page's one popup layer: it stands alone, whole inside the viewport
  await exp.click();
  const menu = page.getByRole("menu");
  await expect(menu).toBeVisible();
  expect(await page.locator("dialog[open]").count()).toBe(0);
  expect(await page.getByRole("menu").all()).toHaveLength(1);
  const box = await menu.boundingBox();
  expect(box?.x ?? -1).toBeGreaterThanOrEqual(0);
  expect(box?.y ?? -1).toBeGreaterThanOrEqual(0);
  expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual((view?.width ?? 0) + 1);
  expect((box?.y ?? 0) + (box?.height ?? 0)).toBeLessThanOrEqual(view?.height ?? 0);
  await expectAccessible(page, SHELL);

  // Escape closes it the way the keyboard closes a menu, with the focus on an entry
  await menu.getByRole("menuitem").first().focus();
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  await expect(exp).toHaveAttribute("aria-expanded", "false");

  // the reader's actions stand open with the entry: Verify raises the page's toast, alone,
  // inside the viewport and above the tab bar
  await page.locator("#kblist a").first().click();
  const verify = page.getByRole("button", { name: "Verify" });
  await expect(verify).toBeVisible();
  await expectTapTargets([verify, page.getByRole("button", { name: "Share with every channel" })]);
  await verify.click();
  const toast = page.locator(".ui-toast");
  await expect(toast).toBeVisible();
  expect(await page.getByRole("menu").count()).toBe(0);
  const tb = await toast.boundingBox();
  expect(tb?.y ?? -1).toBeGreaterThanOrEqual(0);
  expect((tb?.x ?? 0) + (tb?.width ?? 0)).toBeLessThanOrEqual((view?.width ?? 0) + 1);
  const bar = page.locator("[data-nav='bottom']");
  if ((await bar.count()) > 0) {
    const bb = await bar.boundingBox();
    expect((tb?.y ?? 0) + (tb?.height ?? 0)).toBeLessThanOrEqual((bb?.y ?? 0) + 1);
  }
  await expectNoSideScroll(page);
  await expectAccessible(page, SHELL);
});

test("the Today digest keeps its controls and its toast inside the viewport", async ({ page }) => {
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/channels/e2e/today`);
  await expect(page.locator("#dgwrap")).toBeVisible();
  // the shell hosts the page, so its two controls paint in the intro's actions slot
  const copy = page.getByRole("button", { name: "Copy as text" });
  const since = page.locator("[data-dgwin]");
  await expect(copy).toBeVisible();
  await expect(since).toHaveCount(3);

  // the window chips take the finger's 44 px target through the hit area the shared chip draws
  // around its 28 px box (the same check the knowledge page's kind chips make)
  if (test.info().project.use.hasTouch) {
    const tall = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>("[data-dgwin]")].every((el) => {
        const r = el.getBoundingClientRect();
        const before = getComputedStyle(el, "::before");
        const p = before.position === "absolute" ? Math.abs(Number.parseFloat(before.top)) : 0;
        return r.width > 0 && r.height + 2 * p >= 44 - 0.01;
      }),
    );
    expect(tall, "each Since chip's hit stands at least 44 px tall").toBe(true);
  }
  const controls = [...(await since.all()), copy];
  await expectTapTargets([copy]);
  await expectInsideViewport(page, controls);
  await expectClearOfTabBar(page, controls);
  await expectNoSideScroll(page);
  // the digest's own surface stands alone: no dialog, menu or sheet under it
  expect(await page.locator("dialog[open]").count()).toBe(0);
  await expectAccessible(page, SHELL);

  // copying refetches the digest as text and raises the page's one toast: alone, whole inside
  // the viewport, above the tab bar
  await copy.click();
  const toast = page.locator(".ui-toast");
  await expect(toast).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [toast]);
  await expectClearOfTabBar(page, [toast]);
  await expectAccessible(page, SHELL);

  // the window chips still drive the digest: the pick refetches the digest op with the new
  // window. The header slot repaints on the shell's next render, so the wire carries the proof
  const refetch = page.waitForRequest(
    (r) => r.url().includes("/op/digest") && (r.postData() ?? "").includes('"since":"3d"'),
  );
  await since.filter({ hasText: "3 days" }).click();
  await refetch;
  await expectNoSideScroll(page);
});

test("the team page keeps its roster, its filters and its drawer inside the viewport", async ({
  page,
}, info) => {
  const ch = await freshChannel(info.project.name, ["alpha", "beta"]);
  // a feed of more than one kind: a message, a remembered note and a task that finished
  await api(`/api/c/${ch}/op/send?as=alpha`, { to: "beta", msg: "team layers ping" });
  await api(`/api/c/${ch}/op/remember?as=beta`, {
    kind: "fact",
    title: `Team page (${ch})`,
    body: "the roster and the feed keep their layers at every width.",
  });
  await api(`/api/c/${ch}/op/task_create?as=alpha`, { id: "team-1", title: "Layer check task" });
  await api(`/api/c/${ch}/op/task_status?as=alpha`, { id: "team-1", status: "done" });
  // one session that left: the roster's folded row of the sessions that are out
  await api(`/api/c/${ch}/op/join?as=delta`, {});
  await api(`/api/c/${ch}/op/leave?as=delta`, {});

  await openChannel(page, ch);
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/team`);
  await expect(page.getByRole("region", { name: "Sessions", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Activity", exact: true })).toBeVisible();

  // the roster names the sessions that are in, and folds the one that left away
  const roster = page.getByRole("region", { name: "Sessions", exact: true });
  await expect(roster.getByRole("button", { name: /^alpha,/ })).toBeVisible();
  await expect(roster.getByRole("button", { name: /^beta,/ })).toBeVisible();
  const left = roster.locator("details");
  await expect(left.locator("summary")).toContainText("1 left");
  const rows = [...(await roster.getByRole("button", { name: /^alpha,|^beta,/ }).all())];
  await expectTapTargets([...rows, left.locator("summary")]);
  await expectInsideViewport(page, [left.locator("summary"), ...rows]);
  await left.locator("summary").click();
  await expectInsideViewport(page, [left.getByRole("button", { name: /^delta,/ })]);
  await expectNoSideScroll(page);

  // the feed carries more than one kind: the two joins, the ping, the remembered note and the
  // task run (the created and status updates fold into one burst row)
  const feed = page.locator("#tl");
  await expect(feed.locator("[data-seq]")).toHaveCount(8);
  await expect(feed.locator("[data-seq]").filter({ hasText: "team layers ping" })).toHaveCount(1);
  await expect(feed.locator("details[data-bkey]")).toHaveCount(1);
  const chips = page.getByRole("group", { name: "Filter the feed" }).locator("[data-f]");
  await expect(chips).toHaveCount(6);
  await expectInsideViewport(page, [...(await chips.all())]);
  const speaker = page.locator("#tls");
  await expectTapTargets([speaker]);
  if (test.info().project.use.hasTouch) {
    const hits = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>("[data-f]")].map((el) => {
        const r = el.getBoundingClientRect();
        const before = getComputedStyle(el, "::before");
        const p = before.position === "absolute" ? Math.abs(Number.parseFloat(before.top)) : 0;
        return { l: r.left, r: r.right, t: r.top - p, b: r.bottom + p };
      }),
    );
    expect(hits.length > 0 && hits.every((h) => h.b - h.t >= 44 - 0.01)).toBe(true);
    expect(
      hits.every((a, i) =>
        hits.every(
          (b, j) => i === j || a.r <= b.l || b.r <= a.l || a.b <= b.t || b.b <= a.t,
        ),
      ),
    ).toBe(true);
  }
  await expectTeamAccessible(page, SHELL);

  // a filter pick keeps working on the tokens: the message stays, every other kind goes
  await chips.filter({ hasText: "Messages" }).click();
  await expect(chips.filter({ hasText: "Messages" })).toHaveAttribute("aria-pressed", "true");
  await expect(feed.locator("[data-seq]").filter({ hasText: "team layers ping" })).toHaveCount(1);
  await expect(feed.locator("[data-seq]")).toHaveCount(1);
  await chips.filter({ hasText: "All" }).click();
  await expect(feed.locator("[data-seq]")).toHaveCount(8);
  await expectNoSideScroll(page);

  // a roster row opens the session drawer: the page's one layer, whole inside the viewport and
  // clear of the tab bar (the drawer's markup is its own card's, so axe reads the page around it)
  await roster.getByRole("button", { name: /^alpha,/ }).click();
  const layer = page.locator("dialog[open]");
  await expect(layer).toHaveCount(1);
  await expectInsideViewport(page, [layer.first()]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [layer.first()]);
  await expectNoSideScroll(page);
  await expectTeamAccessible(page, [...SHELL, "dialog[open]"]);
  await page.keyboard.press("Escape");
  await expect(layer).toHaveCount(0);
  await expectTeamAccessible(page, SHELL);

  // the row is a real button, so Enter and Space reach it as clicks
  const alpha = roster.getByRole("button", { name: /^alpha,/ });
  for (const key of ["Enter", "Space"] as const) {
    await alpha.focus();
    await page.keyboard.press(key);
    await expect(layer).toHaveCount(1);
    await page.keyboard.press("Escape");
    await expect(layer).toHaveCount(0);
  }

  // the feed's cap keeps its rendered value: 20rem at the 15 px root is 300 px
  const cap = await page.evaluate(() => {
    const probe = document.createElement("div");
    probe.style.height = "calc(100dvh - 20rem)";
    document.body.appendChild(probe);
    const h = probe.getBoundingClientRect().height;
    probe.remove();
    return h;
  });
  expect(cap, "the feed cap's rendered value").toBe((page.viewportSize()?.height ?? 0) - 300);
  const log = await feed.evaluate((el) => el.getBoundingClientRect().height);
  expect(log).toBeGreaterThan(0);
  expect(log).toBeLessThanOrEqual(cap);
});

test("the inbox page keeps its cards, its note row and its toast inside the viewport", async ({
  page,
}, info) => {
  const ch = await freshChannel(info.project.name, ["greta"]);
  // one question and one gated task wait on the owner: the two need kinds the page answers
  await api(`/api/c/${ch}/op/send?as=greta`, {
    to: "owner",
    msg: "Which database should the service use?",
    ask: true,
  });
  await api(`/api/c/${ch}/op/import_plan?as=owner`, {
    plan: {
      phases: [
        { n: 0, title: "Plan", steps: [{ id: "gate-1", title: "Run the migration", gate: "ask-first" }] },
      ],
    },
  });
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await signIn(page);
  await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/inbox`);
  const asks = page.getByRole("region", { name: "Questions for you" });
  const gates = page.getByRole("region", { name: "Waiting for your approval" });
  await expect(asks).toBeVisible();
  await expect(gates).toBeVisible();

  // the page's own action controls take the pointer's full target and stand whole in the viewport
  const controls = [
    asks.getByRole("button", { name: "Yes, go ahead" }),
    asks.getByRole("button", { name: "Reply" }),
    asks.getByRole("textbox"),
    gates.getByRole("button", { name: "Approve", exact: true }),
    gates.getByRole("button", { name: "Approve with a note" }),
  ];
  await expectTapTargets(controls);
  await expectInsideViewport(page, controls);
  await expectNoSideScroll(page);

  // the empty reply box stands tall enough for its wrapped placeholder: no line clips
  const clip = await asks.getByRole("textbox").evaluate((el) => ({
    scroll: el.scrollHeight,
    client: el.clientHeight,
  }));
  expect(clip.scroll, "the empty reply box clips its placeholder").toBeLessThanOrEqual(clip.client);

  // the note row opens in place: its field, its Approve and its Cancel, whole in the viewport
  await gates.getByRole("button", { name: "Approve with a note" }).click();
  const noteRow = gates.locator("[data-note-for]");
  const note = noteRow.getByRole("textbox");
  await expect(note).toBeVisible();
  await expectInsideViewport(page, [noteRow]);
  await expectLayerTargets(noteRow);

  // the page opens no sheet, dialog or menu of its own: an approved gate leaves over one toast
  await note.fill("watch the index");
  await note.press("Enter");
  const toast = page.locator(".ui-toast");
  await expect(toast).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [toast]);
  if ((await page.locator("[data-nav='bottom']").count()) > 0)
    await expectClearOfTabBar(page, [toast]);

  // a quick reply raises the page's toast the same way, and the card leaves the list
  await asks.getByRole("button", { name: "Yes, go ahead" }).click();
  const sent = page.getByText("Reply sent");
  await expect(sent).toBeVisible();
  expect(await page.locator("dialog[open]").count(), "one layer at a time").toBeLessThanOrEqual(1);
  await expectInsideViewport(page, [sent]);
  await expectNoSideScroll(page);
  await expectAccessible(page, SHELL);
});

/** What a header's check finds after the page's scrollers move 400 px: the header's own
 *  background alpha, the text that shows inside its box, the points inside it that a scrolling
 *  pane answers, and the scrollers whose top edge lies above its bottom edge. */
type BleedReport = { alpha: number; texts: string[]; covered: number; overlapping: string[] };

/** Scrolls every scrolling pane that does not hold `header` by 400 px, then reads what shows
 *  through and behind the header. A text node bleeds when its box meets the header's and the
 *  point at its centre inside the header answers with the text's own element. */
async function bleedAfterScroll(page: Page, header: Locator): Promise<BleedReport> {
  return header.evaluate((head): BleedReport => {
    const scrollers = [...document.querySelectorAll<HTMLElement>("*")].filter(
      (el) =>
        el.scrollHeight > el.clientHeight + 4 &&
        ["auto", "scroll"].includes(getComputedStyle(el).overflowY) &&
        !el.contains(head),
    );
    for (const el of scrollers) el.scrollTop = 400;
    const box = head.getBoundingClientRect();
    const color = getComputedStyle(head).backgroundColor;
    const alpha = color.startsWith("rgba")
      ? Number.parseFloat(color.slice(color.lastIndexOf(",") + 1))
      : color === "transparent"
        ? 0
        : 1;

    const texts: string[] = [];
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const owner = node.parentElement;
      if (!owner || head.contains(owner) || !(node.textContent ?? "").trim()) continue;
      const range = document.createRange();
      range.selectNodeContents(node);
      for (const r of range.getClientRects()) {
        const left = Math.max(r.left, box.left);
        const right = Math.min(r.right, box.right);
        const top = Math.max(r.top, box.top);
        const bottom = Math.min(r.bottom, box.bottom);
        if (right - left < 1 || bottom - top < 1) continue;
        const hit = document.elementFromPoint((left + right) / 2, (top + bottom) / 2);
        if (hit && (hit === owner || owner.contains(hit))) texts.push((node.textContent ?? "").trim());
      }
    }

    let covered = 0;
    for (let x = box.left + 2; x < box.right; x += 40) {
      for (let y = box.top + 2; y < box.bottom; y += 8) {
        const hit = document.elementFromPoint(x, y);
        if (hit && scrollers.some((el) => el.contains(hit))) covered += 1;
      }
    }

    const overlapping = scrollers
      .filter((el) => el.getBoundingClientRect().top < box.bottom - 0.5)
      .map((el) => el.className.toString());
    return { alpha, texts, covered, overlapping };
  });
}

test("the channel tab strip stays opaque above scrolled content on every channel page", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the 2000 px desk is the desktop project's");
  const ch = await freshChannel(info.project.name, ["gamma", "delta"]);
  for (let i = 0; i < 40; i += 1)
    await api(`/api/c/${ch}/op/send?as=gamma`, { to: "delta", msg: `scrolled message ${i}` });
  for (let i = 0; i < 30; i += 1)
    await api(`/api/c/${ch}/op/task_create?as=gamma`, { id: `bleed-${i}`, title: `Bleed task ${i}`, phase: 1 });
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await page.setViewportSize({ width: 2000, height: 1200 });
  await signIn(page);

  for (const dest of ["overview", "today", "inbox", "team", "work", "knowledge", "settings"]) {
    await page.goto(`${theSetup().rootUrl}/#/channels/${ch}/${dest}`);
    const strip = page.locator("[data-view-tabs]");
    await expect(strip).toBeVisible();
    await expect(strip.locator(`[data-dest="${dest}"][aria-selected="true"]`)).toBeVisible();
    const report = await bleedAfterScroll(page, strip);
    expect(report.alpha, `${dest}: the strip's background alpha`).toBe(1);
    expect(report.texts, `${dest}: text showing inside the strip`).toEqual([]);
    expect(report.covered, `${dest}: strip points a scrolling pane answers`).toBe(0);
    expect(report.overlapping, `${dest}: panes reaching up under the strip`).toEqual([]);
  }
});

test("the Sessions and Kanban headers keep scrolled content out of their boxes at the desk", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "desktop", "the 2000 px desk is the desktop project's");
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await page.setViewportSize({ width: 2000, height: 1200 });
  await signIn(page);

  await page.goto(`${theSetup().rootUrl}/#/sessions`);
  const sessionsHead = page.locator("[data-sessions-pane] > header");
  await expect(sessionsHead).toBeVisible();
  const sessions = await bleedAfterScroll(page, sessionsHead);
  expect(sessions.texts, "Sessions: text showing inside the header").toEqual([]);
  expect(sessions.covered, "Sessions: header points a scrolling pane answers").toBe(0);
  expect(sessions.overlapping, "Sessions: panes reaching up under the header").toEqual([]);

  await page.goto(`${theSetup().rootUrl}/#/kanban`);
  const boardHead = page.locator(".kanban-head");
  await expect(boardHead).toBeVisible();
  const board = await bleedAfterScroll(page, boardHead);
  expect(board.texts, "Kanban: text showing inside the header").toEqual([]);
  expect(board.covered, "Kanban: header points a scrolling pane answers").toBe(0);
});
