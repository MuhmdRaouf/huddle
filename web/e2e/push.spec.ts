// push.spec.ts — Web Push end to end on a paired device: the Settings switch subscribes the
// browser with the hub's VAPID key, a needs-you item makes the hub send an encrypted push to the
// subscription's endpoint, and the app's service worker shows the notification that message
// carries, with its deep link. The push service is an HTTP server this test owns on a port the
// OS picks; the browser's PushManager hands out a subscription at that endpoint (headless
// Chromium reaches no real push service), with keys made in the page so the test can decrypt
// what the hub sent, and CDP delivers the decrypted message to the worker as the push service
// would.

import { expect, test, type Page } from "@playwright/test";
import {
  createDecipheriv,
  createPrivateKey,
  createPublicKey,
  diffieHellman,
  hkdfSync,
  type JsonWebKey,
} from "node:crypto";
import http from "node:http";
import { api, pairThroughLink, theSetup, useVirtualAuthenticator } from "./helpers.ts";

// the app-shell worker is the subject here, so this file lets it run; and the full Chromium in
// its new headless mode, because the headless shell answers every page "denied" for
// notifications whatever the context granted
test.use({ serviceWorkers: "allow", channel: "chromium" });

const DEVICE = `E2E Push Phone ${Date.now() % 100000}`;
const SESSION = "e2e-push-session";

/** The fake push service: its endpoint and every message the hub posted to it. */
type PushService = { url: string; messages: Buffer[]; close(): Promise<void> };

function startPushService(): Promise<PushService> {
  const messages: Buffer[] = [];
  const srv = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      messages.push(Buffer.concat(chunks));
      res.writeHead(201).end();
    });
  });
  return new Promise((resolve) => {
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr !== null ? addr.port : 0;
      resolve({
        url: `http://127.0.0.1:${port}/push/e2e`,
        messages,
        close: () => new Promise((done) => srv.close(() => done())),
      });
    });
  });
}

/** The page's PushManager, answering one subscription at the fake endpoint with keys made in
 *  the page; the private half and the auth secret wait on window for the test to read. */
function fakePushManager(endpoint: string): string {
  return `(() => {
    const b64 = (bytes) => btoa(String.fromCharCode(...new Uint8Array(bytes)))
      .replace(/\\+/g, "-").replace(/\\//g, "_").replace(/=+$/, "");
    let current = null;
    PushManager.prototype.getSubscription = async function () { return current; };
    PushManager.prototype.subscribe = async function () {
      const pair = await crypto.subtle.generateKey(
        { name: "ECDH", namedCurve: "P-256" }, true, ["deriveBits"]);
      const p256dh = b64(await crypto.subtle.exportKey("raw", pair.publicKey));
      const auth = b64(crypto.getRandomValues(new Uint8Array(16)));
      window.__pushKeys = { jwk: await crypto.subtle.exportKey("jwk", pair.privateKey), p256dh, auth };
      current = {
        endpoint: ${JSON.stringify(endpoint)},
        toJSON: () => ({ endpoint: ${JSON.stringify(endpoint)}, keys: { p256dh, auth } }),
        unsubscribe: async () => { current = null; return true; },
      };
      return current;
    };
  })();`;
}

/** The subscription's keys as the page made them. */
type PageKeys = { jwk: JsonWebKey; p256dh: string; auth: string };

/** Opens one aes128gcm Web Push message (RFC 8291) with the subscription's keys. */
function decrypt(message: Buffer, keys: PageKeys): string {
  const salt = message.subarray(0, 16);
  const idLen = message[20] ?? 0;
  const serverPub = message.subarray(21, 21 + idLen);
  const body = message.subarray(21 + idLen);
  const browserPub = Buffer.from(keys.p256dh, "base64url");
  const serverKey = createPublicKey({
    key: {
      kty: "EC",
      crv: "P-256",
      x: serverPub.subarray(1, 33).toString("base64url"),
      y: serverPub.subarray(33, 65).toString("base64url"),
    },
    format: "jwk",
  });
  const shared = diffieHellman({
    privateKey: createPrivateKey({ key: keys.jwk, format: "jwk" }),
    publicKey: serverKey,
  });
  const info = Buffer.concat([Buffer.from("WebPush: info\0"), browserPub, serverPub]);
  const ikm = Buffer.from(hkdfSync("sha256", shared, Buffer.from(keys.auth, "base64url"), info, 32));
  const cek = Buffer.from(hkdfSync("sha256", ikm, salt, Buffer.from("Content-Encoding: aes128gcm\0"), 16));
  const nonce = Buffer.from(hkdfSync("sha256", ikm, salt, Buffer.from("Content-Encoding: nonce\0"), 12));
  const decipher = createDecipheriv("aes-128-gcm", cek, nonce);
  decipher.setAuthTag(body.subarray(body.length - 16));
  const plain = Buffer.concat([decipher.update(body.subarray(0, body.length - 16)), decipher.final()]);
  const end = plain.lastIndexOf(2);
  return plain.subarray(0, end < 0 ? plain.length : end).toString("utf8");
}

/** The notifications the app's worker shows right now. */
async function shownNotifications(page: Page): Promise<Array<{ title: string; body: string; url: string }>> {
  return page.evaluate(async () => {
    const reg = await navigator.serviceWorker.ready;
    return (await reg.getNotifications()).map((n) => ({
      title: n.title,
      body: n.body,
      url: String((n.data as { url?: unknown } | null)?.url ?? ""),
    }));
  });
}

test("a needs-you item reaches the installed app as a push with its deep link", async ({ page, context }) => {
  const service = await startPushService();
  try {
    const origin = theSetup().pairOrigin;
    await context.grantPermissions(["notifications"]);
    await page.addInitScript({ content: fakePushManager(service.url) });
    await useVirtualAuthenticator(page);
    await pairThroughLink(page, DEVICE);

    // the worker is up before the switch asks for its registration
    await page.evaluate(() => navigator.serviceWorker.ready.then(() => true));
    await page.goto(`${origin}/#/channels/e2e/settings`);
    const sw = page.locator("#set-push");
    await expect(sw).toHaveAttribute("aria-checked", "false");
    await expect(page.getByText("Push permission: granted")).toBeVisible();
    await sw.click();
    await expect(sw).toHaveAttribute("aria-checked", "true");
    const keys = await page.evaluate(() => (window as unknown as { __pushKeys: PageKeys }).__pushKeys);

    // a session asks for a permission: the hub's sweep sends the item's push to the endpoint
    const { id } = await api<{ id: number }>("/api/needs", {
      session_id: SESSION,
      tool_name: "Bash",
      tool_input: { command: "npm test" },
    });
    let payload = "";
    await expect
      .poll(
        () => {
          // the hub may first carry items an earlier spec left with no device to hear them
          const opened = service.messages.map((m) => decrypt(m, keys));
          payload = opened.find((p) => p.includes(`"item":${id}`)) ?? "";
          return payload;
        },
        { timeout: 15_000 },
      )
      .not.toBe("");
    const sent = JSON.parse(payload) as { kind: string; tool: string; url: string };
    expect(sent).toMatchObject({ kind: "permission", tool: "Bash", url: `/#/now/${SESSION}?item=${id}` });
    expect(payload).not.toContain("npm test");

    // the push service delivers the message, again until it lands, the way a real one retries
    // a worker that is still waking: the worker shows the item's notification, once by its tag
    const cdp = await context.newCDPSession(page);
    const registration = new Promise<string>((resolve) => {
      cdp.on("ServiceWorker.workerRegistrationUpdated", ({ registrations }) => {
        const ours = registrations.find((r) => r.scopeURL.startsWith(origin) && !r.isDeleted);
        if (ours) resolve(ours.registrationId);
      });
    });
    await cdp.send("ServiceWorker.enable");
    const registrationId = await registration;
    await expect
      .poll(
        async () => {
          await cdp.send("ServiceWorker.deliverPushMessage", {
            origin: `${origin}/`,
            registrationId,
            data: payload,
          });
          await page.waitForTimeout(500);
          return shownNotifications(page);
        },
        { timeout: 15_000 },
      )
      .toEqual([{ title: "Bash", body: `${SESSION}`, url: `/#/now/${SESSION}?item=${id}` }]);

    // turning the switch off ends the hub's copy of the subscription
    await sw.click();
    await expect(sw).toHaveAttribute("aria-checked", "false");
  } finally {
    await service.close();
  }
});
