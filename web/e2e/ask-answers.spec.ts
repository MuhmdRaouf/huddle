// ask-answers.spec.ts — Claude's questions open as an answer sheet: one card per question, the
// caption counts the answered ones, Send stays out of reach until every question has an answer,
// and sending allows the ask with the answers keyed by each question's text — the session's wait
// carries them back to the terminal. Nothing scrolls sideways at the phone's width and axe finds
// nothing serious on the open sheet, on the phone and on the desktop.

import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe.ts";
import { api, openTab, signIn } from "./helpers.ts";
import { ASK_QUESTIONS } from "./seed.ts";

/** The answers the run sends: Postgres on the single choice, ESLint on the multi-select. */
const ANSWERS = {
  "Which database should this project use?": "Postgres",
  "Which linters should run?": "ESLint",
};

// the authenticator set-up the first sign-in offers is not this page's subject: the browser
// starts with its "Later" already kept
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("huddle:totp-later", "true"));
});

test("a two-question ask opens the answer sheet and the wait carries the owner's answers", async (
  { page, isMobile },
  info,
) => {
  // the session stopped, then its hook asked: sending the answers allows the ask, so the item
  // leaves the list and the session reads idle
  const session = `e2e-ask-answers-${info.project.name}`;
  await api("/api/registry/report", {
    session_id: session,
    machine: "e2e-machine",
    project: "e2e",
    title: "Asks two questions",
    last_event: "Stop",
    last_event_at: new Date().toISOString(),
  });
  const { id } = await api<{ id: number }>("/api/needs", {
    session_id: session,
    tool_name: "AskUserQuestion",
    tool_input: ASK_QUESTIONS,
  });
  if (isMobile) await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await openTab(page, "Sessions");
  await page.locator(`a[href='#/sessions/${session}']`).first().click();

  // the phone's sheet title lives in the dialog's head, above section[data-needs-sheet]
  const ask = page.locator(isMobile ? "dialog[open].ui-sheet" : "[data-needs-bar]");
  await expect(ask.getByRole("heading", { name: /asks 2 questions/ })).toBeVisible();
  await expect(ask.locator("[data-ask-card]")).toHaveCount(2);
  await expect(ask.getByText("0 of 2 answered")).toBeVisible();
  if (isMobile) {
    // the page and the sheet body scroll nothing sideways at the phone's width
    const doc = await page.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      client: document.documentElement.clientWidth,
    }));
    expect(doc.scroll).toBeLessThanOrEqual(doc.client);
    const body = await page.locator("dialog[open].ui-sheet .ui-sheet-body").evaluate((el) => ({
      scroll: el.scrollWidth,
      client: el.clientWidth,
    }));
    expect(body.scroll).toBeLessThanOrEqual(body.client);
  }
  // the Now page's own parts stay out of this sheet's audit: the transcript pane and the session
  // list's raw-input preview overflow without keyboard access on a long ask, which is Now's to fix
  await expectAccessible(page, ["[data-stream]", "[data-ask]"]);

  const send = ask.getByRole("button", { name: "Send answers" });
  await expect(send).toBeDisabled();
  if (isMobile) await ask.getByRole("radio", { name: /Postgres/ }).tap();
  else await ask.getByRole("radio", { name: /Postgres/ }).click();
  if (isMobile) await ask.getByRole("checkbox", { name: /ESLint/ }).tap();
  else await ask.getByRole("checkbox", { name: /ESLint/ }).click();
  await expect(ask.getByText("2 of 2 answered")).toBeVisible();
  await expect(send).toBeEnabled();

  // the terminal's wait, on the page's own session, holds until the answers land
  const waiting = page.evaluate(
    async (needId) =>
      (await (await fetch(`/api/needs/${needId}/wait?timeout=20`)).json()) as {
        decision?: string;
        answers?: Record<string, string>;
      },
    id,
  );
  const decided = page.waitForResponse((r) => r.url().endsWith(`/api/needs/${id}/decide`));
  if (isMobile) await send.tap();
  else await send.click();
  const answer = await decided;
  expect(answer.status()).toBe(200);
  expect(((await answer.json()) as { decision?: string }).decision).toBe("allow");
  // the wait answers with the need the decision landed on; the answers carry the ask
  const waited = (await waiting) as { decision?: string; answers?: Record<string, string> };
  expect(waited.decision).toBe("allow");
  expect(waited.answers).toEqual(ANSWERS);
  // the ask left the list
  await expect(ask).toHaveCount(0);
});
