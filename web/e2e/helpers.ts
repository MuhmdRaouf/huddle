// helpers.ts — what every spec shares: a root sign-in code minted over the API, the browser
// sign-in through that one-time link, and the headers a direct API call needs. The tests read
// the setup file the global setup wrote.

import { expect, type Page } from "@playwright/test";
import { readSetup, type E2eSetup } from "./paths.ts";

/** The fixture's token numbers, as the transcript and the spool both carry them: three glm-5.3
 *  requests whose totals the dashboard must show exactly. A fourth call twenty-five hours back
 *  (a flash model, outside the Day window) is what lets the Day range answer from the requests
 *  in memory, so the overview carries the cache share. */
export const SEEDED = {
  model: "glm-5.3",
  session: "e2e-seed",
  requests: [
    { input: 1200, output: 300, cacheRead: 5000, cacheWrite: 0 },
    { input: 800, output: 200, cacheRead: 0, cacheWrite: 2000 },
    { input: 400, output: 100, cacheRead: 0, cacheWrite: 0 },
  ],
} as const;

/** The fixture's exact totals: the three requests summed. */
export const TOTALS = {
  input: 2400,
  output: 600,
  cacheRead: 5000,
  cacheWrite: 2000,
} as const;

/** 1,200 — the grouping the dashboard's number cells carry. */
export const n = (value: number): string => value.toLocaleString("en-US");

let setup: E2eSetup | null = null;

/** The run's setup, read once. */
export function theSetup(): E2eSetup {
  setup ??= readSetup();
  return setup;
}

/** The headers one direct API write needs: JSON and the action the server audits. */
function writeHeaders(path: string): Record<string, string> {
  return {
    "content-type": "application/json",
    "x-huddle-action": path.split("?", 1)[0] ?? path,
    "x-huddle-token": theSetup().token,
  };
}

/** One API call as the root: a GET when no body is given, a POST otherwise. */
export async function api<T>(path: string, body?: unknown): Promise<T> {
  const r = await fetch(`${theSetup().rootUrl}${path}`, {
    method: body === undefined ? "GET" : "POST",
    headers: writeHeaders(path),
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  if (!r.ok) throw new Error(`${path} answered ${r.status}: ${await r.text()}`);
  return (await r.json()) as T;
}

/** Mints one five-minute root sign-in code (one use). */
export async function loginCode(): Promise<string> {
  const r = await api<{ code: string }>("/api/login", {});
  return r.code;
}

/** Signs this browser in the way the owner does: the one-time link becomes a cookie, then the
 *  shell paints. */
export async function signIn(page: Page): Promise<void> {
  const code = await loginCode();
  const settled = page.waitForURL((url) => !url.searchParams.has("code"));
  await page.goto(`${theSetup().rootUrl}/?code=${code}`);
  await settled;
  // signed in is the shell itself: the five tabs stand in the top bar
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
}

/** Opens the tab the top bar names. */
export async function openTab(page: Page, name: string): Promise<void> {
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name }).click();
}

/** Collects the page's console errors and uncaught exceptions; `errors()` answers them. */
export class ConsoleErrors {
  private readonly seen: string[] = [];

  constructor(page: Page) {
    page.on("console", (msg) => {
      if (msg.type() === "error") this.seen.push(msg.text());
    });
    page.on("pageerror", (err) => {
      this.seen.push(String(err));
    });
  }

  /** Every console error and uncaught exception seen so far. */
  errors(): string[] {
    return [...this.seen];
  }
}
