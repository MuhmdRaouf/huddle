// now.spec.ts — the owner's first screen, on the phone and the desktop: the sign-in link lands
// the browser on the hub, and the Now list shows the session the run seeded into the registry
// (on the desktop the tab lands on the row the list draws first, so its session screen stands in
// the main pane), on the phone a
// seeded ask's card sits above the fold under the set-up row that opens the checklist in its sheet,
// every session
// names its harness on its row and in the session it opens (the desk header's badge, the phone's facts
// line; the seeded Claude Code one and a Codex one), the phone's two-row header fits 390 px with its
// facts line above the tab bar, one whose
// last event asks for permission waits in Needs you with its state word,
// an agent-team teammate that went idle shows under its lead's row with the word idle, a seeded
// permission ask is allowed with one tap at its card's foot on the phone and at the session's
// needs bar on the desk. A session's
// stream on the phone opens at its latest event and folds repeated calls of one tool into one row
// a tap opens. The phone's page
// scrolls nothing sideways and every target on it is at least 44 px; axe finds nothing serious on
// either.

import { expect, test } from "@playwright/test";
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { expectAccessible } from "./axe.ts";
import {
  api,
  cutStreams,
  cuttableStreams,
  expectSheetGutter,
  flowingStreams,
  openTab,
  returnAfter,
  signIn,
  soloPairHome,
  theSetup,
} from "./helpers.ts";
import { ASK_COMMAND, ASK_EDIT } from "./seed.ts";

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
  await openTab(page, "Sessions");

  // the seeded session sits in the Working group, one row strong; the row link names it (on the
  // desk the title also stands in the open session's header and prompt bubble)
  await expect(page.getByRole("heading", { name: "Working · 1" })).toBeVisible();
  await expect(page.getByRole("link", { name: theSetup().sessionTitle })).toBeVisible();
  // sessions stand and none is picked: the desktop lands on the row the list draws first, so the
  // session screen stands in the main pane (the phone paints the list alone)
  if (!isMobile) await expect(page.locator("[data-session]")).toBeVisible();
});

test("on the phone a seeded ask's card sits above the fold, under the set-up row that opens the checklist", async ({
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
  await openTab(page, "Sessions");

  // the set-up row rides under the chips, above the fold; the checklist is no longer a card in
  // the list body — the row opens it in its sheet
  const setup = page.getByRole("button", { name: /^Set up · \d+ steps? left$/ });
  await expect(setup).toBeVisible();
  const setupBox = await setup.boundingBox();
  expect(setupBox?.y).toBeLessThan(844);
  const card = page.locator(`article[data-row='${session}']`);
  await expect(card.getByText("bun run lint")).toBeVisible();
  const cardBox = await card.boundingBox();
  expect(cardBox?.y).toBeLessThan(844);
  await expect(page.locator("[data-sessions-sections] [data-first-run]")).toHaveCount(0);
  await setup.tap();
  const sheet = page.locator("dialog[open].ui-sheet");
  await expect(sheet.locator("[data-first-run]")).toBeVisible();

  await api(`/api/needs/${id}/decide`, { decision: "deny" });
  await expect(card).toHaveCount(0);
});

test("every session names its harness: the seeded Claude Code one and a Codex one", async ({ page }, info) => {
  await signIn(page);
  await openTab(page, "Sessions");

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

  // the open session names its harness too: the desk keeps the badge in its status line, and
  // the phone carries the name its report feeds in the facts line at the pane's foot
  if (info.project.name === "desktop") {
    await page.locator("[data-row='e2e-seed']").click();
    const header = page.locator("[data-session] header [data-harness]");
    await expect(header).toHaveAttribute("data-harness", "claude");
    await expect(header).toContainText("Claude Code");
  } else {
    await page.locator(`a[href='#/sessions/${codex}']`).first().click();
    await expect(page.locator("[data-session-facts]")).toContainText("Codex");
  }
});

test("the phone's session header fits 390 px and the facts line rides above the tab bar", async ({
  page,
}, info) => {
  test.skip(info.project.name !== "iphone", "the phone header is the phone's");
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Sessions");
  await page.locator("a[href='#/sessions/e2e-seed']").first().click();
  await expect(page.locator("[data-session] header")).toBeVisible();

  // the title row and the where line fit: the page scrolls nothing sideways and no header
  // element ends past the 390 px edge
  const over = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(over).toBeLessThanOrEqual(0);
  const spill = await page.evaluate(
    () =>
      [...document.querySelectorAll("[data-session] header *")]
        .filter((el) => el.getClientRects().length > 0)
        .map((el) => el.getBoundingClientRect().right)
        .filter((right) => right > document.documentElement.clientWidth + 0.5).length,
  );
  expect(spill, "header elements past the viewport's edge").toBe(0);

  // the facts line sits at the pane's foot: inside the viewport, above the tab bar
  const facts = page.locator("[data-session-facts]");
  await expect(facts).toHaveText("glm-5.3");
  const box = await facts.boundingBox();
  const tab = await page.locator("[data-nav='bottom']").boundingBox();
  expect(box).not.toBeNull();
  expect(box && box.y + box.height).toBeLessThanOrEqual(tab?.y ?? 844);
});

test("a session whose last event asks for permission waits in Needs you first", async ({ page }, info) => {
  await signIn(page);
  await openTab(page, "Sessions");

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
  // the state rides as a word, not colour alone
  if (info.project.name === "desktop") {
    // the desk draws the ask as one plain row that opens the session
    const row = page.locator("a[data-row='e2e-permit']");
    await expect(row).toBeVisible();
    await expect(row).toHaveAttribute("href", "#/sessions/e2e-permit");
    await expect(row.locator("[data-state='needs-you']")).toHaveText("Needs you");
  } else {
    const card = page.locator("article[data-row='e2e-permit']");
    await expect(card).toBeVisible();
    await expect(card.getByRole("link", { name: /Waits on a permission/ })).toBeVisible();
    // the card's chip reads the word the mock writes, as the list's rows do
    await expect(card.locator("[data-state='needs-you']")).toHaveText("Needs you");
  }
});

test("a teammate that went idle shows under its lead's row", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Sessions");

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
    await openTab(page, "Sessions");
    const row = page.locator(`a[data-row='${session}']`);
    await expect(row.locator("[data-hooks-stale]")).toHaveText("hooks stale");
    // the transcript, not the twenty-minute-old report, settles the state: idle, never stuck
    await expect(row.locator("[data-state]")).toHaveText("Idle");
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
  await openTab(page, "Sessions");

  const decided = page.waitForResponse((r) => r.url().endsWith(`/api/needs/${id}/decide`));
  if (isMobile) {
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
    // the button is the card's last element: only the footer's 12 px and the card's 16 px padding
    // come between its bottom edge and the card's, so nothing of the card sits under it
    expect(cardBox && allowBox && cardBox.y + cardBox.height - (allowBox.y + allowBox.height)).toBeLessThanOrEqual(28);
    await allow.tap();
    const answer = await decided;
    expect(answer.status()).toBe(200);
    expect(((await answer.json()) as { decision?: string }).decision).toBe("allow");
    // the ask left the list: the card is gone and the session reads idle
    await expect(card).toHaveCount(0);
  } else {
    // the desk's row is one plain link: it opens the session, whose needs bar answers the ask
    await page.locator(`a[data-row='${session}']`).click();
    const bar = page.locator("[data-needs-bar]");
    await expect(bar).toBeVisible();
    await bar.getByRole("button", { name: /^Allow once/ }).click();
    const answer = await decided;
    expect(answer.status()).toBe(200);
    expect(((await answer.json()) as { decision?: string }).decision).toBe("allow");
    // back on the list the ask has left: the session reads idle
    await page.goBack();
    await expect(page.locator("[data-sessions-pane]")).toBeVisible();
  }
  await expect(page.locator(`[data-row='${session}'] [data-state='idle']`)).toHaveText("Idle");
});

test("the passkey sheet an allow opens keeps the 16 px side gutter", async ({ page }, info) => {
  test.skip(info.project.name !== "iphone", "the gutter is the phone's");
  // the ask stands on the TLS hub, whose door is where a signed-in browser owes the passkey
  // check: the machine's own loopback waives it, and a paired browser's fresh pairing is
  // itself an assertion. The ask leaves with the run: dismissed at the end, so no other case
  // counts it
  const { hubRootUrl, hubToken, hubUrl } = theSetup();
  const session = `e2e-stepup-${info.project.name}`;
  const post = async <T>(route: string, body: unknown): Promise<T> => {
    const r = await fetch(`${hubRootUrl}${route}`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-huddle-action": route.split("?", 1)[0] ?? route,
        "x-huddle-token": hubToken,
      },
      body: JSON.stringify(body),
    });
    if (!r.ok) throw new Error(`${route} answered ${r.status}: ${await r.text()}`);
    const text = await r.text();
    return (text === "" ? undefined : JSON.parse(text)) as T;
  };
  await post("/api/registry/report", {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    title: "Asks over the hub's door",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const { id } = await post<{ id: number }>("/api/needs", {
    session_id: session,
    tool_name: "Bash",
    tool_input: { command: "bun run test" },
  });

  // signed in by code on the listener itself, the way a phone earns its session there
  const login = await fetch(`${hubRootUrl}/api/login`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-huddle-action": "/api/login",
      "x-huddle-token": hubToken,
    },
    body: "{}",
  });
  if (!login.ok) throw new Error(`/api/login answered ${login.status}`);
  const { code } = (await login.json()) as { code: string };
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
  await page.goto(`${hubUrl}/?code=${code}`);
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  // a phone-shaped browser on a platform that holds a lock earns the biometrics offer over the
  // shell: close it when it stood, the ask's card is this test's subject
  const notNow = page.getByRole("button", { name: "Not now" });
  if (await notNow.isVisible().catch(() => false)) await notNow.click();
  await page.goto(`${hubUrl}/#/sessions`);

  const card = page.locator(`article[data-row='${session}']`);
  await expect(card.getByText("bun run test")).toBeVisible();
  await card.getByRole("button", { name: "Allow once" }).tap();
  // the allow through the door draws the 401: the passkey sheet stands over the card
  const sheet = page.locator("[data-stepup]");
  await expect(sheet).toBeVisible();
  await expectSheetGutter(sheet);

  await page.keyboard.press("Escape");
  await post(`/api/needs/${id}/dismiss`, {});
});

test("a long command's ask opens the phone sheet that wraps it and keeps Allow once in view", async (
  { page },
  info,
) => {
  test.skip(info.project.name !== "iphone", "the sheet is the phone's");
  // the session stopped, then its hook asked: the sheet holds the ask, and the denial at the end
  // leaves the other cases' counts as they were
  const session = "e2e-sheet-iphone";
  await api("/api/registry/report", {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    title: "Asks in a sheet",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const { id } = await api<{ id: number }>("/api/needs", {
    session_id: session,
    tool_name: "Bash",
    tool_input: { command: ASK_COMMAND },
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Sessions");
  await page.locator(`a[href='#/sessions/${session}']`).first().click();

  const sheet = page.locator("dialog[open].ui-sheet");
  await expect(sheet).toBeVisible();
  await expect(sheet.getByRole("heading", { name: /wants to run a command/ })).toBeVisible();
  // the command wraps inside its code box: nothing scrolls sideways
  const code = sheet.locator("code").filter({ hasText: ASK_COMMAND.slice(0, 24) });
  await expect(code).toBeVisible();
  const { scroll, client } = await code.evaluate((el) => ({
    scroll: el.scrollWidth,
    client: el.clientWidth,
  }));
  expect(scroll).toBeLessThanOrEqual(client);
  // the body scrolls; at its end the footer's allow is still inside the viewport
  await sheet.locator(".ui-sheet-body").evaluate((el) => (el.scrollTop = el.scrollHeight));
  const box = await sheet.getByRole("button", { name: "Allow once" }).boundingBox();
  expect(box && box.y + box.height).toBeLessThanOrEqual(844.5);
  // the tab bar stands down while the sheet holds the ask
  await expect(page.locator("[data-nav='bottom']")).toHaveCount(0);

  await api(`/api/needs/${id}/decide`, { decision: "deny" });
  await expect(sheet).toHaveCount(0);
});

test("an Edit ask opens the phone sheet with the change as a line diff", async ({ page }, info) => {
  test.skip(info.project.name !== "iphone", "the sheet is the phone's");
  const session = "e2e-edit-iphone";
  await api("/api/registry/report", {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    title: "Asks to edit a file",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const { id } = await api<{ id: number }>("/api/needs", {
    session_id: session,
    tool_name: "Edit",
    tool_input: ASK_EDIT,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Sessions");
  await page.locator(`a[href='#/sessions/${session}']`).first().click();

  const sheet = page.locator("dialog[open].ui-sheet");
  await expect(sheet).toBeVisible();
  await expect(sheet.getByText(ASK_EDIT.file_path)).toBeVisible();
  await expect(sheet.locator("[data-sign='-']").filter({ hasText: "const rate = 1;" })).toBeVisible();
  await expect(sheet.locator("[data-sign='+']").filter({ hasText: "return rate * 2;" })).toBeVisible();

  await api(`/api/needs/${id}/decide`, { decision: "deny" });
  await expect(sheet).toHaveCount(0);
});

test("the desktop's inline box shows the same previews", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the inline box is the wide layout's");
  // two stopped sessions, each holding one ask, answered at the end so the counts stay
  const seedAsk = async (session: string, title: string, tool_name: string, tool_input: unknown) => {
    await api("/api/registry/report", {
      session_id: session,
      machine: "e2e-machine",
      project: "e2e",
      title,
      last_event: "Stop",
      last_event_at: new Date().toISOString(),
    });
    const { id } = await api<{ id: number }>("/api/needs", { session_id: session, tool_name, tool_input });
    return id;
  };
  const bashId = await seedAsk("e2e-inline-bash", "Asks inline to run a command", "Bash", {
    command: ASK_COMMAND,
  });
  const editId = await seedAsk("e2e-inline-edit", "Asks inline to edit a file", "Edit", ASK_EDIT);
  await signIn(page);
  await openTab(page, "Sessions");

  await page.locator("a[href='#/sessions/e2e-inline-bash']").first().click();
  const bar = page.locator("[data-needs-bar]");
  await expect(bar).toBeVisible();
  const code = bar.locator("code").filter({ hasText: ASK_COMMAND.slice(0, 24) });
  await expect(code).toBeVisible();
  const { scroll, client } = await code.evaluate((el) => ({
    scroll: el.scrollWidth,
    client: el.clientWidth,
  }));
  expect(scroll).toBeLessThanOrEqual(client);

  await api(`/api/needs/${bashId}/decide`, { decision: "deny" });
  await page.locator("a[href='#/sessions/e2e-inline-edit']").first().click();
  await expect(bar.locator("[data-sign='+']").filter({ hasText: "return rate * 2;" })).toBeVisible();

  await api(`/api/needs/${editId}/decide`, { decision: "deny" });
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
  await openTab(page, "Sessions");
  // the finished turn holds the session in Needs you: its card's link opens it
  await page.locator(`a[href='#/sessions/${session}']`).first().click();
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
  // a prompt, then a Stop whose hook built a digest: the turn waits in Needs you. The desk opens
  // the list's first row by itself, and that read answers the turn — the sheet stays while the
  // session stands open — so no tab click may rewind the address off it; the dismissal at the end
  // keeps the other cases' counts as they were
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
  // signIn already lands on the sessions tab (the shell rewrites the empty home onto it), and the
  // desk has opened this very row: a tab click here would rewind the address and drop the sheet
  await page.locator(`a[href='#/sessions/${session}']`).first().click();

  const sheet = page.locator(isMobile ? "[data-needs-sheet]" : "[data-needs-bar]");
  await expect(sheet.getByRole("heading", { name: "Goal: fix the login bug" })).toBeVisible();
  await expect(sheet.getByText("Edited: internal/auth/login.go")).toHaveCount(0);
  await expect(sheet.getByRole("button", { name: "Answer" })).toHaveCount(0);
  const dismiss = sheet.getByRole("button", { name: "Dismiss" });
  const more = sheet.getByRole("button", { name: "More actions" });
  // the thumb's 44 px floor is the phone's rule; under a mouse the design system's control
  // height is 40 px, so the desktop bar's buttons keep their pointer sizes
  if (isMobile) {
    for (const button of [dismiss, more]) {
      const box = await button.boundingBox();
      expect(box?.height).toBeGreaterThanOrEqual(44);
      expect(box?.width).toBeGreaterThanOrEqual(44);
    }
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
  test.skip(
    info.project.name !== "iphone",
    "the phone keeps its list-first path; the desk opens the list's first row by itself",
  );
  // a prompt, then a Stop nobody opened: the turn item holds the session in Needs you, and
  // opening it answers the item, so the other cases' counts stay as they were
  const session = `e2e-open-${info.project.name}`;
  const base = { session_id: session, machine: "e2e-machine", project: "e2e", title: "Opens to clear" };
  await api("/api/registry/report", { ...base, last_event: "UserPromptSubmit", last_event_at: new Date().toISOString() });
  await api("/api/registry/report", { ...base, last_event: "Stop", last_event_at: new Date().toISOString() });
  const other = await context.newPage();
  for (const p of [page, other]) {
    await signIn(p);
    await openTab(p, "Sessions");
    await expect(p.locator(`a[href='#/sessions/${session}']`).first()).toBeVisible();
  }
  const heading = page.getByRole("heading", { name: /Needs you · \d+$/ });
  const before = Number(/· (\d+)/.exec((await heading.textContent()) ?? "")?.[1]);
  expect(before).toBeGreaterThanOrEqual(1);
  const count = (n: number): RegExp => (n > 0 ? new RegExp(`Needs you · ${n}$`) : /Needs you · /);

  // one browser opens the session in-app and comes back; neither page reloads
  const opened = page.waitForResponse((r) => r.url().endsWith(`/api/registry/sessions/${session}`));
  await page.locator(`a[href='#/sessions/${session}']`).first().click();
  expect((await opened).status()).toBe(200);
  await page.goBack();
  await expect(page.locator("[data-sessions-pane]")).toBeVisible();

  for (const p of [page, other]) {
    if (before > 1) await expect(p.getByRole("heading", { name: count(before - 1) })).toBeVisible();
    else await expect(p.getByRole("heading", { name: count(0) })).toHaveCount(0);
    await expect(p.locator(`[data-row='${session}'] [data-state='needs-you']`)).toHaveCount(0);
  }
  await other.close();
});

test("the desktop sets the list and the open pane side by side", async ({ page, isMobile }) => {
  test.skip(isMobile, "the phone paints the list alone");
  await signIn(page);
  await openTab(page, "Sessions");
  // no session was picked: the desk opened the row the list draws first, and the session's pane
  // stands beside the list
  await expect(page.locator("[data-session]")).toBeVisible();
  // the desk's auto-open re-renders the list; read both boxes once it settles
  await expect(async () => {
    const list = await page.locator("[data-sessions-pane]").boundingBox();
    const pane = await page.locator("[data-session]").boundingBox();
    expect(list && pane && list.x + list.width).toBeLessThanOrEqual(pane?.x ?? 0);
  }).toPass();
});

test("at tablet width the open session's header keeps its full name", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the tablet width is set on the desktop project");
  await page.setViewportSize({ width: 820, height: 1180 });
  await signIn(page);
  await openTab(page, "Sessions");
  await page.locator("[data-row='e2e-seed']").click();

  const name = page.locator("[data-session] header").getByText(theSetup().sessionTitle, { exact: true });
  await expect(name).toBeVisible();
  const { scroll, client } = await name.evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth }));
  expect(scroll).toBeLessThanOrEqual(client);
});

test("at desk width the title owns its row and the rail holds a Now section", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the desk width is the desktop project's");
  // one session whose title is long enough to fill the row it stands on; its turn has ended
  // (Stop), so the Working group keeps only the seed and the phone project's first test, which
  // pins that group to one, still finds it on this shared server
  const session = "e2e-desk-title";
  await api("/api/registry/report", {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    branch: "desk-title",
    title: "Kanban release: the export inlines every ticket and the board draws its flow bar",
    model: "claude-sonnet-4-5",
    context_percent: 41,
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  await signIn(page);
  await openTab(page, "Sessions");

  // the seeded session's short title is not cut while space is free
  await page.locator("[data-row='e2e-seed']").click();
  const name = page.locator("[data-session] header [data-session-title]");
  await expect(name).toHaveText(theSetup().sessionTitle);
  expect(await name.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);

  // the long title takes its row: at least 60% of the header's width
  await page.locator(`a[data-row='${session}']`).click();
  const long = page.locator("[data-session] header [data-session-title]");
  await expect(long).toHaveText(/Kanban release/);
  const box = await long.boundingBox();
  const headerBox = await page.locator("[data-session] header").boundingBox();
  expect(box && headerBox && box.width).toBeGreaterThanOrEqual((headerBox?.width ?? 0) * 0.6);

  // the rail opens with a Now section
  await expect(
    page.locator("[data-now-context]").getByRole("heading", { name: "Now", exact: true }),
  ).toBeVisible();
});

test("the phone's Sessions page scrolls nothing sideways and every target is at least 44 px", async ({
  page,
  isMobile,
}) => {
  test.skip(!isMobile, "the thumb's targets are the phone's");
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Sessions");
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(over).toBeLessThanOrEqual(0);
  const small = await page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>("[data-sessions-pane] :is(a, button, input, select, textarea)")]
      .filter((el) => el.getClientRects().length > 0)
      .map((el) => ({ el, r: el.getBoundingClientRect() }))
      .filter(({ r }) => r.width < 44 || r.height < 44)
      .map(({ el, r }) => `${el.tagName.toLowerCase()} "${(el.textContent ?? "").trim().slice(0, 30)}" ${Math.round(r.width)}x${Math.round(r.height)}`),
  );
  expect(small).toEqual([]);
});

test("the Sessions page passes axe", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Sessions");
  await expectAccessible(page);
});

/** Reports one idle session the Now list has not seen, so a row proves the list caught up. */
async function reportIdle(id: string, title: string): Promise<void> {
  await api("/api/registry/report", {
    session_id: id,
    machine: "e2e-machine",
    project: "e2e",
    title,
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
}

test("a registry stream gone silent is replaced within 40 s and the list catches up", async ({ page }, info) => {
  const id = `e2e-silent-${info.project.name}`;
  await test.step("Given the Now list on a stream that goes quiet with no error", async () => {
    await cuttableStreams(page);
    await page.clock.install();
    await signIn(page);
    await openTab(page, "Sessions");
    await expect(page.locator("[data-row='e2e-seed']")).toBeVisible();
    expect(await cutStreams(page, "/api/registry/live")).toBe(1);
  });

  await test.step("When a session reports while the stream is silent", async () => {
    await reportIdle(id, "Reported while the stream was silent");
    await page.waitForTimeout(1_000);
    await expect(page.locator(`[data-row='${id}']`)).toHaveCount(0);
  });

  await test.step("Then 40 s of silence replace the stream and the session shows", async () => {
    await page.clock.fastForward(40_000);
    await expect.poll(() => flowingStreams(page, "/api/registry/live"), { timeout: 5_000 }).toBe(1);
    await expect(page.locator(`[data-row='${id}']`)).toBeVisible({ timeout: 5_000 });
  });
});

test("a page back from the background re-subscribes the registry stream within 5 s", async ({ page }, info) => {
  const id = `e2e-return-${info.project.name}`;
  await test.step("Given the Now list on a stream that goes quiet with no error", async () => {
    await cuttableStreams(page);
    await signIn(page);
    await openTab(page, "Sessions");
    await expect(page.locator("[data-row='e2e-seed']")).toBeVisible();
    expect(await cutStreams(page, "/api/registry/live")).toBe(1);
    await reportIdle(id, "Reported while the page was away");
  });

  await test.step("When the page hides, 30 s pass, and it turns visible again", async () => {
    await page.waitForTimeout(500);
    await expect(page.locator(`[data-row='${id}']`)).toHaveCount(0);
    await returnAfter(page, 30_000);
  });

  await test.step("Then a fresh stream flows and the session shows within 5 s", async () => {
    await expect.poll(() => flowingStreams(page, "/api/registry/live"), { timeout: 5_000 }).toBe(1);
    await expect(page.locator(`[data-row='${id}']`)).toBeVisible({ timeout: 5_000 });
  });
});
