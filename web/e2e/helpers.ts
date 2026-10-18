// helpers.ts — what every spec shares: a root sign-in code minted over the API, the browser
// sign-in through that one-time link, the virtual authenticator a passkey ceremony runs on, and
// the headers a direct API call needs. The tests read the setup file the global setup wrote.

import { expect, type CDPSession, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { createHmac } from "node:crypto";
import { readSetup, repoRoot, runDir, type E2eSetup } from "./paths.ts";

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

/** The seeded Codex session: one turn twenty-five hours back (800 uncached in, 200 cached, 100
 *  out), outside the Day window and inside the usage ones. */
export const CODEX = {
  session: "9a1f0c3e-2b4d-4e5f-8a6b-7c8d9e0f1a2b",
  input: 800,
  output: 100,
  cacheRead: 200,
} as const;

/** The five seeded requests summed, the numbers the Usage view's tiles carry wherever it opens:
 *  three glm-5.3 today, one glm-5.3-flash and one codex turn twenty-five hours back. */
export const USAGE = {
  processed: 10_015 + CODEX.input + CODEX.output + CODEX.cacheRead,
  cached: 5_000 + CODEX.cacheRead,
  uncached: 2_410 + CODEX.input,
  output: 605 + CODEX.output,
  today: 10_000,
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
 *  shell paints. A hub with no authenticator enrolment offers its first-run set-up before the
 *  shell; these tests are not about that, so they take the skip. `base` names the origin to
 *  sign in at (the localhost one, when a passkey ceremony follows: the relying party is that
 *  name). */
export async function signIn(page: Page, base: string = theSetup().rootUrl): Promise<void> {
  const code = await loginCode();
  const settled = page.waitForURL((url) => !url.searchParams.has("code"));
  await page.goto(`${base}/?code=${code}`);
  await settled;
  const later = page.getByRole("button", { name: /Later/ });
  try {
    await later.waitFor({ state: "visible", timeout: 3_000 });
    await later.click();
  } catch {
    // the hub holds an enrolment (or this run already enrolled one): no gate stood there
  }
  // signed in is the shell itself: the five tabs stand in the top bar
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
}

/** One hub's temp dirs, the set `huddle pair` runs against so its link lands in that hub's
 *  own database. */
export type PairHome = {
  home: string;
  huddleHome: string;
  radarHome: string;
  claudeConfig: string;
  xdg: string;
};

/** The solo hub's home: the default every localhost spec mints its link in. */
export const soloPairHome: PairHome = {
  home: `${runDir}/home`,
  huddleHome: `${runDir}/huddle-home`,
  radarHome: `${runDir}/radar-home`,
  claudeConfig: `${runDir}/claude-config`,
  xdg: `${runDir}/xdg`,
};

/** The TLS hub's home: its link carries the https origin and the reserved name. */
export const tlsHubPairHome: PairHome = {
  home: `${runDir}/home-hub`,
  huddleHome: `${runDir}/huddle-home-hub`,
  radarHome: `${runDir}/radar-home-hub`,
  claudeConfig: `${runDir}/claude-config-hub`,
  xdg: `${runDir}/xdg-hub`,
};

/** The secret the set-up shows, as the app gave it: the groups of four joined back together. */
export async function totpSecret(page: Page): Promise<string> {
  const shown = await page.locator("[data-secret]").textContent();
  return (shown ?? "").replace(/\s+/g, "");
}

/** The RFC 6238 code for one secret at the current step, over node:crypto: HMAC-SHA1, dynamic
 *  truncation, six digits — the specification itself, not a library. The base32 decode is
 *  hand-rolled too: the test runner's Buffer knows no base32 spelling. */
export function totpCode(secret: string, offsetSteps = 0): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  const bits = [...secret.toUpperCase().replace(/=+$/, "")]
    .map((c) => alphabet.indexOf(c).toString(2).padStart(5, "0"))
    .join("");
  const bytes: number[] = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) bytes.push(Number.parseInt(bits.slice(i, i + 8), 2));
  const key = Buffer.from(bytes);
  const step = BigInt(Math.floor(Date.now() / 1000 / 30) + offsetSteps);
  const sum = createHmac("sha1", key)
    .update(Buffer.from(step.toString(16).padStart(16, "0"), "hex"))
    .digest();
  const at = sum[sum.length - 1]! & 0x0f;
  const code = ((sum[at]! & 0x7f) << 24) | (sum[at + 1]! << 16) | (sum[at + 2]! << 8) | sum[at + 3]!;
  return String(code % 1_000_000).padStart(6, "0");
}

/** Types one code into the six-box field, one digit per box; the sixth digit submits it. */
export async function typeCode(page: Page, code: string): Promise<void> {
  const boxes = page.locator("[data-code-field] input");
  for (let i = 0; i < code.length; i++) {
    await boxes.nth(i).fill(code[i] ?? "");
  }
}

/** The Mac desktop's user agent: the suite's Desktop Chrome descriptor carries a Windows one,
 *  and the platform's own wording (Touch ID) reads the agent, not the machine beneath it. */
export const MAC_UA =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36";

/** The run's environment: the temp homes the server runs on, so `huddle pair` mints its link
 *  into the same hub database the server spends it from. */
function pairEnv(target: PairHome): NodeJS.ProcessEnv {
  return {
    ...process.env,
    HOME: target.home,
    HUDDLE_HOME: target.huddleHome,
    RADAR_HOME: target.radarHome,
    CLAUDE_CONFIG_DIR: target.claudeConfig,
    XDG_CONFIG_HOME: `${target.xdg}-config`,
    XDG_CACHE_HOME: `${target.xdg}-cache`,
    XDG_DATA_HOME: `${target.xdg}-data`,
    XDG_STATE_HOME: `${target.xdg}-state`,
  };
}

/** Runs `huddle pair` and answers the link and the code it printed. */
export function mintPairLink(
  name: string,
  target: PairHome = soloPairHome,
): { link: string; code: string } {
  const out = execFileSync(`${runDir}/bin/huddle`, ["pair", "--name", name], {
    cwd: repoRoot,
    env: pairEnv(target),
    encoding: "utf8",
  });
  const link = /https?:\/\/[^\s]+\/pair#[A-Z2-7]{26}/.exec(out)?.[0];
  const code = /code ([A-Z2-7]{3}-[A-Z2-7]{3}) /.exec(out)?.[1];
  if (link === undefined || code === undefined) {
    throw new Error(`huddle pair printed no link or code:\n${out}`);
  }
  return { link, code };
}

/** Arms a virtual platform authenticator on the page, the way a phone's passkey works. */
export async function useVirtualAuthenticator(page: Page): Promise<CDPSession> {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2",
      transport: "internal",
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
    },
  });
  return cdp;
}

/** Drives the whole pairing a `huddle pair` link opens: the name stage, the code stage, the
 *  passkey the virtual authenticator makes, and the shell its cookie lands on. A caller that
 *  minted its own link (against another hub's home) passes it in. */
export async function pairThroughLink(
  page: Page,
  name: string,
  minted?: { link: string; code: string },
): Promise<void> {
  const { link, code } = minted ?? mintPairLink(name);
  await page.goto(link);
  const field = page.getByLabel("Device name");
  await expect(field).toBeVisible();
  await field.fill(name);
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByText("Pairing code")).toBeVisible();
  await expect(page.getByText(code)).toBeVisible();
  await page.getByRole("button", { name: "It matches" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
}

/** The pairing the shell's first-run checklist opens: the page mints its own link, then the
 *  same two stages run on the token the server answered with. The checklist rides the Now list,
 *  so the caller's tab moves there first. */
export async function pairThroughBanner(page: Page, name: string): Promise<void> {
  await openTab(page, "Now");
  await page.getByRole("button", { name: "Save passkey" }).click();
  const field = page.getByLabel("Device name");
  await expect(field).toBeVisible();
  await field.fill(name);
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByText("Pairing code")).toBeVisible();
  await page.getByRole("button", { name: "It matches" }).click();
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
