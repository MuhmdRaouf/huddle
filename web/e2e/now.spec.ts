// now.spec.ts — the owner's first screen, on the phone and the desktop: the sign-in link lands
// the browser on the hub, and the Now list shows the session the run seeded into the registry
// (with the desktop's main pane keeping its pointer beside it while none is picked), on the phone a
// seeded ask's card sits above the fold with the first-run checklist after Needs you, every session
// names its harness on its row and in its header (the seeded Claude Code one and a Codex one), one whose
// last event asks for permission waits in Needs you with its state word, behind the filter too,
// an agent-team teammate that went idle shows under its lead's row with the word idle, a seeded
// permission ask is allowed with one tap on the 48 px button at its card's foot, and the Radar
// card carries today's seeded cost and requests. A session's stream on the phone opens at its
// latest event and folds repeated calls of one tool into one row a tap opens. The phone's page
// scrolls nothing sideways and every target on it is at least 44 px; axe finds nothing serious on
// either.

import { expect, test } from "@playwright/test";
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { expectAccessible } from "./axe.ts";
import { api, openTab, SEEDED, signIn, soloPairHome, theSetup, TOTALS } from "./helpers.ts";

/** Today's three seeded glm-5.3 calls at the price table's 1.40 / 4.40 / 0.26 USD per million. */
const TODAY_USD = (TOTALS.input * 1.4 + TOTALS.output * 4.4 + TOTALS.cacheRead * 0.26) / 1e6;

// the authenticator set-up the first sign-in offers is not this page's subject: the browser
// starts with its "Later" already kept
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
});

test("the sign-in link opens the hub and the Now list shows the seeded session", async ({
  page,
  isMobile,
}) => {
  await signIn(page);
  await openTab(page, "Now");

  // the seeded session sits in the Working group, one row strong
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeVisible();
  await expect(page.getByText(theSetup().sessionTitle)).toBeVisible();
  // sessions stand and none is picked: the desktop's main pane keeps its pointer at the list
  // (the phone paints the list alone)
  if (!isMobile) await expect(page.getByText("No session open")).toBeVisible();
});

test("on the phone a seeded ask's card sits above the fold, before the first-run checklist", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "iphone", "the fold is the phone's");
  // the session stopped, then its hook asked: the open item holds it in Needs you, and the
  // denial at the end moves it to Idle so the other cases' counts stay as they were
  const session = `e2e-fold-${info.project.name}`;
  await api("/api/registry/report", {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    title: "Asks above the fold",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const { id } = await api<{ id: number }>("/api/needs", {
    session_id: session,
    tool_name: "Bash",
    tool_input: { command: "bun run lint" },
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Now");

  const checklist = page.locator("[data-first-run]");
  await expect(checklist).toBeVisible();
  const card = page.locator(`article[data-row='${session}']`);
  await expect(card.getByText("bun run lint")).toBeVisible();
  const cardBox = await card.boundingBox();
  expect(cardBox?.y).toBeLessThan(844);
  const needsBox = await page.locator("section[aria-labelledby='now-needsYou']").boundingBox();
  const checklistBox = await checklist.boundingBox();
  expect(needsBox && checklistBox && checklistBox.y).toBeGreaterThanOrEqual((needsBox?.y ?? 0) + (needsBox?.height ?? 0));

  await api(`/api/needs/${id}/decide`, { decision: "deny" });
  await expect(card).toHaveCount(0);
});

test("every session names its harness: the seeded Claude Code one and a Codex one", async ({ page }, info) => {
  await signIn(page);
  await openTab(page, "Now");

  // a Codex session reports its turn done, so it sits with the idle ones and the Working count
  // the other cases read stays as it was
  const codex = `e2e-codex-${info.project.name}`;
  await api("/api/registry/report", {
    session_id: codex,
    machine: "e2e-machine",
    project: "e2e",
    title: "Runs in Codex",
    harness: "codex",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const seeded = page.locator("[data-row='e2e-seed'] [data-harness]");
  await expect(seeded).toHaveAttribute("data-harness", "claude");
  await expect(seeded).toContainText("Claude Code");
  const other = page.locator(`[data-row='${codex}'] [data-harness]`);
  await expect(other).toHaveAttribute("data-harness", "codex");
  await expect(other).toContainText("Codex");

  // the open session's header names it too
  await page.locator("[data-row='e2e-seed']").click();
  const header = page.locator("[data-session] header [data-harness]");
  await expect(header).toHaveAttribute("data-harness", "claude");
  await expect(header).toContainText("Claude Code");
});

test("a session whose last event asks for permission waits in Needs you first", async ({
  page,
}) => {
  await signIn(page);
  await openTab(page, "Now");

  // a session reports a permission request and nothing after: the registry reads needs-you
  await api("/api/registry/report", {
    session_id: "e2e-permit",
    machine: "e2e-machine",
    project: "e2e",
    title: "Waits on a permission",
    last_event: "PermissionRequest",
    last_event_at: new Date().toISOString(),
  });
  await expect(page.getByRole("heading", { name: "Needs you · 1" })).toBeVisible();
  const card = page.locator("article[data-row='e2e-permit']");
  await expect(card).toBeVisible();
  await expect(card.getByRole("link", { name: /Waits on a permission/ })).toBeVisible();
  // the state rides as a word, not colour alone
  await expect(card.locator("[data-state='needs-you']")).toHaveText("needs you");

  // the filter holds the needs-you list alone, then gives the whole list back
  await page.getByRole("button", { name: "Needs you" }).click();
  await expect(page.getByRole("heading", { name: "Needs you · 1" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeHidden();
  await page.getByRole("button", { name: "Needs you" }).click();
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeVisible();
});

test("a teammate that went idle shows under its lead's row", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Now");

  // the lead's TeammateIdle hook reports the teammate under the lead's session, then the lead's
  // own turn ends: the teammate stays listed under the idle lead (and the Working count the
  // other cases read stays as it was)
  const lead = { session_id: "e2e-lead", machine: "e2e-machine", project: "e2e", title: "Leads a team" };
  await api("/api/registry/report", {
    ...lead,
    teammate: { name: "researcher", state: "idle" },
    last_event: "TeammateIdle",
    last_event_at: new Date().toISOString(),
  });
  await api("/api/registry/report", { ...lead, last_event: "Stop", last_event_at: new Date().toISOString() });
  const row = page.locator("[data-select-row='e2e-lead'], [data-hold-row='e2e-lead']");
  await expect(row.getByText("Leads a team")).toBeVisible();
  const mate = row.locator("[data-teammate='researcher']");
  await expect(mate).toBeVisible();
  await expect(mate).toContainText("researcher");
  // the teammate's state rides as a word
  await expect(mate.locator("[data-state='idle']")).toHaveText("idle");
});

test("a session with stale hooks shows its chip and the resume line, never stuck", async ({ page }, info) => {
  // the hub reads hooks stale when a session's transcript runs ahead of its last report while
  // its pid lives: the session reports once, twenty minutes back (stuck, on its reports alone),
  // with the pid of a stand-in process this test starts and stops (its command names claude, so
  // the hub's pid sweep keeps it), and its transcript under the run's Claude config moved twelve
  // minutes back, eight minutes after that report and quiet past the ten-minute limit since
  const session = `e2e-stale-${info.project.name}`;
  const standIn = spawn(process.execPath, ["-e", "setInterval(() => {}, 1 << 30)", "claude"], {
    stdio: "ignore",
  });
  try {
    const minutesAgo = (m: number): Date => new Date(Date.now() - m * 60_000);
    await api("/api/registry/report", {
      session_id: session,
      machine: "e2e-machine",
      project: "e2e",
      title: "Hooks went quiet",
      pid: standIn.pid,
      last_event: "UserPromptSubmit",
      last_event_at: minutesAgo(20).toISOString(),
    });
    const transcripts = path.join(soloPairHome.claudeConfig, "projects", "e2e-stale");
    fs.mkdirSync(transcripts, { recursive: true });
    const transcript = path.join(transcripts, `${session}.jsonl`);
    fs.writeFileSync(transcript, "");
    fs.utimesSync(transcript, minutesAgo(12), minutesAgo(12));

    await signIn(page);
    await openTab(page, "Now");
    const row = page.locator(`a[data-row='${session}']`);
    await expect(row.locator("[data-hooks-stale]")).toHaveText("hooks stale");
    // the transcript, not the twenty-minute-old report, settles the state: idle, never stuck
    await expect(row.locator("[data-state]")).toHaveText("idle");
    await expect(page.locator(`[data-row='${session}'] [data-state='stuck']`)).toHaveCount(0);
    await expectAccessible(page);

    await row.click();
    const view = page.locator("[data-session]");
    await expect(view.locator("header [data-hooks-stale]")).toHaveText("hooks stale");
    await expect(view.getByText(/^Hooks stopped reporting: restart this session with/)).toBeVisible();
    await expect(view.locator("[data-resume] code")).toHaveText(`claude --resume ${session}`);
    await expect(view.locator("[data-state='stuck']")).toHaveCount(0);
    // the chip and the line are report only: they offer nothing to press
    await expect(view.locator("[data-resume] :is(a, button)")).toHaveCount(0);
    await expectAccessible(page);
  } finally {
    standIn.kill();
  }
});

test("a seeded permission ask is allowed with one tap at its card's foot", async ({ page, isMobile }, info) => {
  // the session stopped, then its hook asked: the open item alone holds it in Needs you, so the
  // decision moves it to Idle and the other cases' counts stay as they were
  const session = `e2e-allow-${info.project.name}`;
  await api("/api/registry/report", {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    title: "Asks to run the tests",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const { id } = await api<{ id: number }>("/api/needs", {
    session_id: session,
    tool_name: "Bash",
    tool_input: { command: "bun run test" },
  });
  await signIn(page);
  await openTab(page, "Now");

  const card = page.locator(`article[data-row='${session}']`);
  await expect(card.getByText("bun run test")).toBeVisible();
  const allow = card.getByRole("button", { name: "Allow once" });
  const deny = card.getByRole("button", { name: "Deny" });
  // both answers sit side by side in the card's footer, 48 px tall: one thumb reaches either
  for (const button of [allow, deny]) {
    const box = await button.boundingBox();
    expect(box?.height).toBeGreaterThanOrEqual(48);
  }
  const cardBox = await card.boundingBox();
  const allowBox = await allow.boundingBox();
  expect(cardBox && allowBox && cardBox.y + cardBox.height - (allowBox.y + allowBox.height)).toBeLessThanOrEqual(16);

  const decided = page.waitForResponse((r) => r.url().endsWith(`/api/needs/${id}/decide`));
  if (isMobile) await allow.tap();
  else await allow.click();
  const answer = await decided;
  expect(answer.status()).toBe(200);
  expect(((await answer.json()) as { decision?: string }).decision).toBe("allow");
  // the ask left the list: the card is gone and the session reads idle
  await expect(card).toHaveCount(0);
  await expect(page.locator(`[data-row='${session}'] [data-state='idle']`)).toHaveText("idle");
});

test("the phone's stream folds repeated calls into one row a tap opens, and opens at the end", async ({
  page,
  isMobile,
}, info) => {
  test.skip(!isMobile, "the thumb's targets are the phone's");
  // three Read calls in a row, then a Bash call whose PostToolUse never lands before the turn
  // stops: the Stop keeps the session out of Working (the count the other cases read stays) and
  // closes the lone call as ended
  const session = `e2e-stream-${info.project.name}`;
  const base = {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    title: "Reads three files",
    cwd: "/w",
  };
  const report = (over: Record<string, string>) =>
    api("/api/registry/report", { ...base, last_event_at: new Date().toISOString(), ...over });
  await report({ last_event: "UserPromptSubmit", prompt_line: "Read the three files" });
  await report({ last_event: "PreToolUse", tool: "Read", tool_target: "/w/a.ts" });
  await report({ last_event: "PostToolUse", tool: "Read", tool_target: "/w/a.ts" });
  await report({ last_event: "PostToolUse", tool: "Read", tool_target: "/w/b.ts" });
  await report({ last_event: "PostToolUse", tool: "Read", tool_target: "/w/c.ts" });
  await report({ last_event: "PreToolUse", tool: "Bash", tool_target: "git status" });
  await report({ last_event: "Stop" });

  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Now");
  // the finished turn holds the session in Needs you: its card's link opens it
  await page.locator(`a[href='#/now/${session}']`).first().click();
  const stream = page.locator("[data-stream]");
  const group = stream.getByRole("button", { name: /Read.*3 calls/ });
  await expect(group).toContainText("×3");
  await expect(stream.locator("[data-tool]")).toHaveCount(2);
  await expect(stream.locator("[data-tool][data-ended]")).toContainText("git status");
  // the stream opens at its latest event
  expect(await stream.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight)).toBeLessThanOrEqual(8);
  const box = await group.boundingBox();
  expect(box?.height).toBeGreaterThanOrEqual(44);

  await group.tap();
  await expect(group).toHaveAttribute("aria-expanded", "true");
  await expect(stream.locator("[data-tool-calls] code")).toHaveText(["a.ts", "b.ts", "c.ts"]);
  await expectAccessible(page);
});

test("a finished turn's sheet shows its digest line with Snooze and Dismiss, and no Answer", async ({
  page,
  isMobile,
}, info) => {
  // a prompt, then a Stop whose hook built a digest: nobody opened the session, so the turn waits
  // in Needs you; the dismissal at the end keeps the other cases' counts as they were
  const session = `e2e-digest-${info.project.name}`;
  const base = { session_id: session, machine: "e2e-machine", project: "e2e", title: "Fixes the login" };
  await api("/api/registry/report", { ...base, last_event: "UserPromptSubmit", last_event_at: new Date().toISOString() });
  await api("/api/registry/report", {
    ...base,
    digest: "Goal: fix the login bug\nEdited: internal/auth/login.go",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  await signIn(page);
  await openTab(page, "Now");
  await page.locator(`a[href='#/now/${session}']`).first().click();

  const sheet = page.locator(isMobile ? "[data-needs-sheet]" : "[data-needs-bar]");
  await expect(sheet.getByRole("heading", { name: "Goal: fix the login bug" })).toBeVisible();
  await expect(sheet.getByText("Edited: internal/auth/login.go")).toHaveCount(0);
  await expect(sheet.getByRole("button", { name: "Answer" })).toHaveCount(0);
  const dismiss = sheet.getByRole("button", { name: "Dismiss" });
  const more = sheet.getByRole("button", { name: "More actions" });
  for (const button of [dismiss, more]) {
    const box = await button.boundingBox();
    expect(box?.height).toBeGreaterThanOrEqual(44);
    expect(box?.width).toBeGreaterThanOrEqual(44);
  }
  await more.click();
  await expect(sheet.getByRole("menuitem", { name: "Snooze 1 hour" })).toBeVisible();
  await more.click();
  await expectAccessible(page);

  if (isMobile) await dismiss.tap();
  else await dismiss.click();
  await expect(sheet).toHaveCount(0);
});

test("opening a session clears its finished turn from Needs you in every open browser", async ({
  page,
  context,
}, info) => {
  // a prompt, then a Stop nobody opened: the turn item holds the session in Needs you, and
  // opening it answers the item, so the other cases' counts stay as they were
  const session = `e2e-open-${info.project.name}`;
  const base = { session_id: session, machine: "e2e-machine", project: "e2e", title: "Opens to clear" };
  await api("/api/registry/report", { ...base, last_event: "UserPromptSubmit", last_event_at: new Date().toISOString() });
  await api("/api/registry/report", { ...base, last_event: "Stop", last_event_at: new Date().toISOString() });
  const other = await context.newPage();
  for (const p of [page, other]) {
    await signIn(p);
    await openTab(p, "Now");
    await expect(p.locator(`a[href='#/now/${session}']`).first()).toBeVisible();
  }
  const heading = page.getByRole("heading", { name: /Needs you · \d+$/ });
  const before = Number(/· (\d+)/.exec((await heading.textContent()) ?? "")?.[1]);
  expect(before).toBeGreaterThanOrEqual(1);
  const count = (n: number): RegExp => (n > 0 ? new RegExp(`Needs you · ${n}$`) : /Needs you · /);

  // one browser opens the session in-app and comes back; neither page reloads
  const opened = page.waitForResponse((r) => r.url().endsWith(`/api/registry/sessions/${session}`));
  await page.locator(`a[href='#/now/${session}']`).first().click();
  expect((await opened).status()).toBe(200);
  await page.goBack();
  await expect(page.locator("[data-now-pane]")).toBeVisible();

  for (const p of [page, other]) {
    if (before > 1) await expect(p.getByRole("heading", { name: count(before - 1) })).toBeVisible();
    else await expect(p.getByRole("heading", { name: count(0) })).toHaveCount(0);
    await expect(p.locator(`[data-row='${session}'] [data-state='needs-you']`)).toHaveCount(0);
  }
  await other.close();
});

test("the Radar card shows today's seeded cost and requests", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Now");
  const radar = page.getByRole("region", { name: "Today in Radar" });
  await expect(radar).toBeVisible();
  const cost = radar.locator("[data-radar-cost]");
  await expect(cost).toHaveText("$0.0073");
  expect(Number(await cost.getAttribute("data-radar-cost"))).toBeCloseTo(TODAY_USD, 9);
  await expect(radar.locator("[data-radar-requests]")).toHaveText(String(SEEDED.requests.length));
});

test("the desktop sets the list and the open pane side by side", async ({ page, isMobile }) => {
  test.skip(isMobile, "the phone paints the list alone");
  await signIn(page);
  await openTab(page, "Now");
  const list = await page.locator("[data-now-pane]").boundingBox();
  const pane = await page.getByText("No session open").boundingBox();
  expect(list && pane && list.x + list.width).toBeLessThanOrEqual(pane?.x ?? 0);
  await expect(page.getByRole("region", { name: "Today in Radar" })).toBeVisible();
});

test("the phone's Now scrolls nothing sideways and every target is at least 44 px", async ({
  page,
  isMobile,
}) => {
  test.skip(!isMobile, "the thumb's targets are the phone's");
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Now");
  await expect(page.getByRole("region", { name: "Today in Radar" })).toBeVisible();
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(over).toBeLessThanOrEqual(0);
  const small = await page.evaluate(() =>
    // the first-run checklist rides above the list on its own component's terms
    [...document.querySelectorAll<HTMLElement>("[data-now-pane] :is(a, button, input, select, textarea)")]
      .filter((el) => !el.closest("[data-first-run]"))
      .filter((el) => el.getClientRects().length > 0)
      .map((el) => ({ el, r: el.getBoundingClientRect() }))
      .filter(({ r }) => r.width < 44 || r.height < 44)
      .map(({ el, r }) => `${el.tagName.toLowerCase()} "${(el.textContent ?? "").trim().slice(0, 30)}" ${Math.round(r.width)}x${Math.round(r.height)}`),
  );
  expect(small).toEqual([]);
});

test("the Now page passes axe", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Now");
  await expect(page.getByRole("region", { name: "Today in Radar" })).toBeVisible();
  await expectAccessible(page);
});
