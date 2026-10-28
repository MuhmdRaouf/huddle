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
import { api, openTab, signIn, theSetup } from "./helpers.ts";
import { repoRoot } from "./paths.ts";

/** The height the played keyboard covers: about the iPhone 15's own. */
const KEYBOARD = 300;

/** The parts axe leaves to their own specs: the shell's chrome and the channel rail beside the
 *  page on wide screens. */
const SHELL = ["[data-sidebar]", "[data-nav]", "#side"] as const;

/** The work page's colour-contrast debts that predate it, each named by its node: the work tab
 *  strip's quiet tabs (Work's own strip) and the status pill's tone ink, the same tinted-ink pair
 *  every status pill on the hub paints. Their cards retire these entries when they fix them. */
const WORK_KNOWN: readonly { rule: string; node: string }[] = [
  { rule: "color-contrast", node: "#wt-" },
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
