// Pair.test.tsx — the pairing screen a `huddle pair` link opens at /pair: the token read off the
// fragment, the device name a user agent offers, the code both sides compare, the passkey the
// browser creates on "It matches", the finish that lands the app on Now, the mismatch that sends
// nothing, the NotAllowedError that offers a Retry, the server's refusals said as they came, and
// the incomplete link that asks for a new one.

import { act, cleanup, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import { App } from "../../src/app/App.tsx";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

/** The link token the tests pair with. */
const TOKEN = "tok-123";

/** The creation options the server's begin answers, buffers already base64url. */
const OPTIONS = {
  publicKey: {
    rp: { name: "Huddle", id: "localhost" },
    user: { id: "dXNlcg", name: "owner", displayName: "Owner" },
    challenge: "Y2hhbGxlbmdl",
    pubKeyCredParams: [{ type: "public-key", alg: -7 }],
  },
};

/** A fake browser credential the mocked navigator.credentials.create resolves with. */
const credential = {
  id: "cred-1",
  rawId: new Uint8Array([1, 2, 3]).buffer,
  type: "public-key",
  response: {
    attestationObject: new Uint8Array([9, 9]).buffer,
    clientDataJSON: new Uint8Array([7]).buffer,
  },
};

type Mount = {
  calls: { path: string; body: unknown }[];
  create: ReturnType<typeof vi.fn>;
  /** Flips the answer the create mock gives from now on (the Retry test's first rejection). */
  setCreate(a: unknown): void;
};

/** Mounts the shell at /pair#<token> over a recording api and a stubbed navigator. The create
 *  mock answers `createAnswer` (an Error rejects); the begin answers `beginAnswer`. */
function mount(
  over: {
    token?: string | undefined;
    userAgent?: string | undefined;
    beginAnswer?: unknown;
    createAnswer?: unknown;
  } = {},
): Mount {
  cleanup();
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
  const calls: { path: string; body: unknown }[] = [];
  const api: Api = {
    api: (path, o) => {
      calls.push({ path, body: o?.body });
      if (path === "/api/pair/begin") {
        const a = over.beginAnswer;
        if (a instanceof Error) return Promise.reject(a);
        return Promise.resolve(a ?? { options: OPTIONS, code: "K7Q42M", expires_in: 252 });
      }
      return Promise.resolve({ device: "iPhone", expires_in: 43200 });
    },
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  } as Api;
  const holder = { answer: over.createAnswer as unknown };
  const create = vi.fn(() =>
    holder.answer instanceof Error ? Promise.reject(holder.answer) : Promise.resolve(credential),
  );
  vi.stubGlobal("navigator", {
    ...navigator,
    userAgent:
      over.userAgent ?? "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 like Gecko",
    credentials: { create },
  });
  history.replaceState(null, "", `/pair${over.token === undefined ? `#${TOKEN}` : over.token}`);
  renderIn(<App pages={{}} />, makeCtx(makeState(), { api }));
  return { calls, create, setCreate: (a: unknown) => (holder.answer = a) };
}

/** Repaints the shell the way the browser's own hashchange event would. */
const hashchange = async (): Promise<void> => {
  await act(async () => {
    window.dispatchEvent(new Event("hashchange"));
  });
  await flush();
};

/** Drives the first stage: names the device and posts the begin. */
const begin = async (): Promise<void> => {
  await act(async () => {
    screen.getByRole("button", { name: "Continue" }).click();
  });
  await flush();
};

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
  history.replaceState(null, "", "/");
  location.hash = "";
});

describe("the pair screen", () => {
  it("TestThePairingTokenReachesTheBegin", async () => {
    mount({ userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X)" });
    expect(screen.getByLabelText("Device name")).toHaveProperty("value", "iPhone");
    await begin();
    expect(screen.getByText("K7Q42M")).toBeDefined();
    expect(screen.getByText("Check this matches the code in your terminal")).toBeDefined();
  });

  it("TestTheNameFieldOffersTheUserAgent'sDevice", () => {
    mount({ userAgent: "Mozilla/5.0 (Linux; Android 14; Pixel 8)" });
    expect(screen.getByLabelText("Device name")).toHaveProperty("value", "Android");
    expect(screen.getByText(/Pair this device as “Android”\?/)).toBeDefined();
  });

  it("TestTheSuccessPathLandsOnNow", async () => {
    mount({ userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X)" });
    await begin();
    await act(async () => {
      screen.getByRole("button", { name: "It matches" }).click();
    });
    await flush();
    expect(location.pathname).toBe("/");
    expect(location.hash).toBe("#/now");
    // the address change repaints the shell over the Now tab
    await hashchange();
    expect(screen.getByRole("heading", { level: 1 })?.textContent).toBe("Now");
  });

  it("TestTheFinishCarriesTheTokenAndTheEncodedCredential", async () => {
    const h = mount();
    await begin();
    await act(async () => {
      screen.getByRole("button", { name: "It matches" }).click();
    });
    await flush();
    expect(h.create).toHaveBeenCalledTimes(1);
    const asked = h.create.mock.calls[0]?.[0] as { publicKey: { challenge: ArrayBuffer } };
    expect(new Uint8Array(asked.publicKey.challenge)).toEqual(new TextEncoder().encode("challenge"));
    const finish = h.calls.find((c) => c.path === "/api/pair/finish");
    expect(finish?.body).toEqual({
      token: TOKEN,
      response: {
        id: "cred-1",
        rawId: "AQID",
        type: "public-key",
        response: { attestationObject: "CQk", clientDataJSON: "Bw" },
      },
    });
  });

  it("TestAMismatchSendsNothingMore", async () => {
    const h = mount();
    await begin();
    await act(async () => {
      screen.getByRole("button", { name: "The code does not match" }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/pair/begin"]);
    expect(h.create).not.toHaveBeenCalled();
    expect(screen.getByText("Pairing cancelled. Run `huddle pair` again on your computer.")).toBeDefined();
  });

  it("TestANotAllowedErrorOffersARetry", async () => {
    const h = mount({
      createAnswer: new DOMException("the owner said no", "NotAllowedError"),
    });
    await begin();
    await act(async () => {
      screen.getByRole("button", { name: "It matches" }).click();
    });
    await flush();
    expect(screen.getByText("The passkey was not created. Try again.")).toBeDefined();
    expect(screen.getByRole("button", { name: "Retry" })).toBeDefined();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/pair/begin"]);
    // the retry runs the same ceremony again, and this time the passkey lands
    h.setCreate(undefined);
    await act(async () => {
      screen.getByRole("button", { name: "Retry" }).click();
    });
    await flush();
    expect(h.create).toHaveBeenCalledTimes(2);
    expect(h.calls.map((c) => c.path)).toEqual(["/api/pair/begin", "/api/pair/finish"]);
    expect(location.pathname).toBe("/");
  });

  it("TestARefusedBeginShowsTheServer'sText", async () => {
    mount({
      beginAnswer: Object.assign(new Error("the pairing link expired: run huddle pair again"), {
        status: 410,
      }),
    });
    await begin();
    expect(screen.getByText("the pairing link expired: run huddle pair again")).toBeDefined();
    cleanup();
    mount({
      beginAnswer: Object.assign(new Error("device name: a letter or digit first, up to 32 of them"), {
        status: 400,
      }),
    });
    await begin();
    expect(screen.getByText("device name: a letter or digit first, up to 32 of them")).toBeDefined();
  });

  it("TestAMissingTokenAsksForANewLink", () => {
    mount({ token: "" });
    expect(screen.getByText("This link is incomplete. Run `huddle pair` again.")).toBeDefined();
    expect(screen.queryByRole("button", { name: "Continue" })).toBeNull();
  });
});
