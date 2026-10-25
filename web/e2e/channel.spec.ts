// channel.spec.ts — the page a Channels card opens, on the phone and at the desk. The chat lists
// the run's seeded ping and its pong newest last, each with its sender, its recipient, the body
// and the age. The composer posts a message into the chat, and on the phone it stays above the
// keyboard: the keyboard is played by a visual viewport that shrinks the way iOS's does, while
// the layout viewport keeps its height. The members sheet lists each session with the files it
// claimed and the owner's remove action. The two posting specs run on channels of their own, removed
// after each test, so the seeded channel keeps exactly its ping and pong and stays the only one.

import { expect, type Locator, type Page, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { api, openTab, signIn, theSetup } from "./helpers.ts";

/** The height the played keyboard covers: about the iPhone 15's own. */
const KEYBOARD = 300;

/** The parts axe leaves to their own specs: the shell's chrome and the channel rail beside the
 *  page on wide screens. */
const SHELL = ["[data-sidebar]", "[data-nav]", "#side"] as const;

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
async function freshChannel(project: string, sessions: readonly string[]): Promise<string> {
  const name = `chat-${project}-${Date.now().toString(36)}`;
  await api("/api/channels", { name });
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

/** Every box of the locators, checked to be at least one 44 px tap target on each side. */
async function expectTapTargets(targets: readonly Locator[]): Promise<void> {
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
