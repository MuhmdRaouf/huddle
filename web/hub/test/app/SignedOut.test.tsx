// SignedOut.test.tsx — the signed-out page's own paths: the passkey button only when the home
// holds a passkey, the ceremony the button runs (begin, the browser's credential, finish, the
// reload), the inline errors in plain words (a cancelled prompt, the server's refusal), and the
// command fields that stay under their smaller heading either way.

import { act, cleanup, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import { SignedOut } from "../../src/app/App.tsx";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

/** The assertion options a login begin answers, buffers already base64url. */
const OPTIONS = {
  publicKey: { rpId: "localhost", challenge: "Y2hhbGxlbmdl", userVerification: "required" },
};

/** A fake browser assertion the mocked navigator.credentials.get resolves with. */
const credential = {
  id: "cred-9",
  rawId: new Uint8Array([9, 8, 7]).buffer,
  type: "public-key",
  response: {
    authenticatorData: new Uint8Array([5]).buffer,
    clientDataJSON: new Uint8Array([6]).buffer,
    signature: new Uint8Array([7]).buffer,
    userHandle: new Uint8Array([1, 2]).buffer,
  },
};

type Mount = {
  calls: { path: string; body: unknown }[];
  get: ReturnType<typeof vi.fn>;
  reload: ReturnType<typeof vi.fn>;
  /** Flips the answer the get mock gives from now on. */
  setGet(a: unknown): void;
};

/** Mounts the signed-out page over a recording api and a stubbed navigator and reload. */
function mount(
  over: {
    available?: unknown;
    beginAnswer?: unknown;
    getAnswer?: unknown;
    finishAnswer?: unknown;
  } = {},
): Mount {
  cleanup();
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve({}) })),
  );
  const calls: { path: string; body: unknown }[] = [];
  const api: Api = {
    api: (path, o) => {
      calls.push({ path, body: o?.body });
      if (path === "/api/login/available") {
        const a = over.available;
        if (a instanceof Error) return Promise.reject(a);
        return Promise.resolve(a ?? { passkeys: false });
      }
      if (path === "/api/login/begin") {
        const a = over.beginAnswer;
        if (a instanceof Error) return Promise.reject(a);
        return Promise.resolve(a ?? { options: OPTIONS });
      }
      const a = over.finishAnswer;
      if (a instanceof Error) return Promise.reject(a);
      return Promise.resolve(a ?? { device: "iPhone", expires_in: 604800 });
    },
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  } as Api;
  const holder = { answer: over.getAnswer as unknown };
  const get = vi.fn(() =>
    holder.answer instanceof Error ? Promise.reject(holder.answer) : Promise.resolve(credential),
  );
  vi.stubGlobal("navigator", { ...navigator, credentials: { get } });
  const reload = vi.fn();
  vi.stubGlobal("location", { ...location, reload });
  renderIn(<SignedOut />, makeCtx(makeState(), { api }));
  return { calls, get, reload, setGet: (a) => (holder.answer = a) };
}

/** Drives the sign-in button and lets the ceremony's promises land. */
const signIn = async (): Promise<void> => {
  await act(async () => {
    screen.getByRole("button", { name: "Sign in with a passkey" }).click();
  });
  await flush();
};

beforeEach(() => {
  history.replaceState(null, "", "/");
});

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

describe("the signed-out page", () => {
  it("TestShowsThePasskeyButtonOnlyWhenTheHomeHoldsOne", async () => {
    mount({ available: { passkeys: true } });
    await flush();
    expect(screen.getByRole("button", { name: "Sign in with a passkey" })).toBeDefined();
    expect(screen.getByText("No passkey on this device?")).toBeDefined();
    expect(screen.getByText("In a Claude session")).toBeDefined();
    cleanup();

    mount({ available: { passkeys: false } });
    await flush();
    expect(screen.queryByRole("button", { name: "Sign in with a passkey" })).toBeNull();
    expect(screen.getByText(/once, then save a passkey: next time Touch ID signs you in/)).toBeDefined();
    expect(screen.getByText("No passkey on this device?")).toBeDefined();
    expect(screen.getByText("In a Claude session")).toBeDefined();
    expect(screen.getByText("Or in a terminal")).toBeDefined();
  });

  it("TestTheButtonRunsTheCeremonyAndReloads", async () => {
    const h = mount({ available: { passkeys: true } });
    await flush();
    await signIn();
    expect(h.calls.map((c) => c.path)).toEqual([
      "/api/login/available",
      "/api/login/begin",
      "/api/login/finish",
    ]);
    expect(h.get).toHaveBeenCalledTimes(1);
    const asked = h.get.mock.calls[0]?.[0] as { publicKey: { challenge: ArrayBuffer } };
    expect(new Uint8Array(asked.publicKey.challenge)).toEqual(new TextEncoder().encode("challenge"));
    expect(h.calls[2]?.body).toEqual({
      response: {
        id: "cred-9",
        rawId: "CQgH",
        type: "public-key",
        response: {
          authenticatorData: "BQ",
          clientDataJSON: "Bg",
          signature: "Bw",
          userHandle: "AQI",
        },
      },
    });
    expect(h.reload).toHaveBeenCalledTimes(1);
  });

  it("TestACancelledPromptSaysSoInlineAndTheButtonLives", async () => {
    const h = mount({
      available: { passkeys: true },
      getAnswer: new DOMException("the owner said no", "NotAllowedError"),
    });
    await flush();
    await signIn();
    expect(screen.getByText("Sign-in cancelled. Try again.")).toBeDefined();
    // nothing reached the finish, and the button never sits disabled with no reason
    expect(h.calls.map((c) => c.path)).toEqual(["/api/login/available", "/api/login/begin"]);
    expect(screen.getByRole("button", { name: "Sign in with a passkey" })).not.toHaveProperty(
      "disabled",
      true,
    );
    // a second try runs the whole ceremony again
    h.setGet(undefined);
    await signIn();
    expect(h.reload).toHaveBeenCalledTimes(1);
  });

  it("TestTheServersRefusalShowsInline", async () => {
    mount({
      available: { passkeys: true },
      finishAnswer: Object.assign(new Error("this sign-in's challenge is spent or stale: begin again"), {
        status: 400,
      }),
    });
    await flush();
    await signIn();
    expect(screen.getByText("this sign-in's challenge is spent or stale: begin again")).toBeDefined();
    expect(screen.queryByText("Sign-in cancelled. Try again.")).toBeNull();
  });
});
