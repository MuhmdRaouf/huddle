// phone.spec.ts — trusting the hub on a phone, the two ways the owner does it: the guide
// `huddle trust --phone` serves opens on an iPhone-shaped browser and shows the iOS steps and
// the laptop's fingerprint, and the dashboard's Settings → Devices offers "Add a phone" with the
// QR the hub serves. The guide's own home is a second temp home pinned to loopback, so nothing
// binds a real interface and no trust store is ever touched.

import { expect, test, type ChildProcess } from "@playwright/test";
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { openTab, signIn } from "./helpers.ts";
import { repoRoot, runDir } from "./paths.ts";

/** The iPhone's shape over the run's chromium: the Safari user agent is what the guide's
 *  iOS detection reads, and the mobile viewport keeps the page honest. */
const IPHONE = {
  userAgent:
    "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
  viewport: { width: 390, height: 844 },
  isMobile: true,
  hasTouch: true,
} as const;

/** The guide one `huddle trust --phone` run serves: where it answers and the fingerprint the
 *  laptop printed, plus the process to end when the test is done. */
type PhoneGuide = { url: string; fingerprint: string; child: ChildProcess };

/** Starts `huddle trust --phone` on its own temp home (a hub pinned to loopback, so the helper
 *  binds 127.0.0.1) and reads the link and fingerprint it prints. */
async function startPhoneGuide(): Promise<PhoneGuide> {
  const home = path.join(runDir, "phone-home");
  fs.rmSync(home, { recursive: true, force: true });
  fs.mkdirSync(home, { recursive: true });
  fs.writeFileSync(
    path.join(home, "config.toml"),
    'role = "hub"\n[hub]\nlisten = "127.0.0.1:0"\nhost = "hub.example.test"\n',
  );
  const child = spawn(path.join(runDir, "bin", "huddle"), ["trust", "--phone"], {
    cwd: repoRoot,
    stdio: ["ignore", "pipe", "pipe"],
    env: {
      ...process.env,
      HOME: path.join(runDir, "home"),
      HUDDLE_HOME: home,
      RADAR_HOME: path.join(runDir, "radar-home"),
      CLAUDE_CONFIG_DIR: path.join(runDir, "claude-config"),
      HUDDLE_TRUST_STORE_OFF: "1",
    },
  });
  let printed = "";
  child.stdout?.on("data", (chunk: Buffer) => {
    printed += chunk.toString();
  });
  const deadline = Date.now() + 20_000;
  for (;;) {
    const url = /http:\/\/127\.0\.0\.1:\d+\//.exec(printed)?.[0];
    const fingerprint = /root fingerprint ([0-9A-F:]{95})/.exec(printed)?.[1];
    if (url !== undefined && fingerprint !== undefined) {
      return { url, fingerprint, child };
    }
    if (child.exitCode !== null) {
      throw new Error(`huddle trust --phone exited ${child.exitCode}:\n${printed}`);
    }
    if (Date.now() > deadline) throw new Error(`huddle trust --phone never printed its link:\n${printed}`);
    await new Promise<void>((resolve) => setTimeout(resolve, 100));
  }
}

/** The guide each test starts, ended when the test ends. */
let guideChild: ChildProcess | null = null;

test.describe("the phone guide an iPhone opens", () => {
  test.use({ ...IPHONE });

  test.afterEach(() => {
    guideChild?.kill("SIGTERM");
    guideChild = null;
  });

  test("the guide shows the iOS steps, the fingerprint and the Android one hidden", async ({
    page,
  }) => {
    const guide = await startPhoneGuide();
    guideChild = guide.child;
    await page.goto(guide.url);

    await expect(page.getByRole("heading", { name: "Trust Huddle on this phone" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "iPhone or iPad" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Download the profile" })).toHaveAttribute(
      "href",
      "/ca.mobileconfig",
    );
    await expect(page.getByText("Certificate Trust Settings")).toBeVisible();
    await expect(page.getByText("Huddle Local Authority").first()).toBeVisible();
    // the fingerprint is the one the laptop printed beside its QR
    await expect(page.getByText(guide.fingerprint)).toBeVisible();
    // the Android steps stay out of the iPhone's way
    await expect(page.getByRole("heading", { name: "Android" })).toBeHidden();

    // the download serves the profile the page promises
    const profile = await page.request.get(`${guide.url}ca.mobileconfig`);
    expect(profile.status()).toBe(200);
    expect(await profile.text()).toContain("com.apple.security.root");
  });

  test("the guide's check names the hub it proves trust against", async ({ page }) => {
    const guide = await startPhoneGuide();
    guideChild = guide.child;
    await page.goto(guide.url);
    await expect(page.getByRole("button", { name: "Check" })).toBeVisible();
    await page.getByRole("button", { name: "Check" }).click();
    // hub.example.test resolves nowhere here, so the check answers the honest "not yet"
    await expect(page.getByText("Not trusted yet", { exact: false })).toBeVisible();
  });
});

test("Settings › Phone offers Add a phone with the QR the hub serves", async ({ page }) => {
  await signIn(page);
  await openTab(page, "Settings");
  await page
    .getByRole("navigation", { name: "Settings sections" })
    .getByRole("link", { name: "Phone" })
    .click();
  await expect(page.getByRole("heading", { name: "Phone" })).toBeVisible();

  await page.getByRole("button", { name: "Add a phone" }).click();
  const qr = page.getByRole("img", { name: "QR code: the phone's trust guide" });
  await expect(qr).toBeVisible();
  await expect(qr).toHaveAttribute("src", "/api/phone/qr.svg");
  // the fingerprint beside it is the paired-hex SHA-256 the terminal would print
  await expect(page.getByText(/^[0-9A-F]{2}(:[0-9A-F]{2}){31}$/)).toBeVisible();
});
