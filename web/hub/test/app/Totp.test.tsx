// Totp.test.tsx — the authenticator's own faces: the set-up's two stages (the QR and the secret
// the begin answers, the code that finishes, the recovery codes that show this one time), the
// first-run page's one skip, the Settings card's removal with its step-up retry, and the passkey
// offer's Turn on running the whole registration.

import { act, cleanup, fireEvent, screen } from "@testing-library/preact";
import type { JSX } from "preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiError } from "../../src/api.ts";
import { AuthenticatorCard, bioLabel, PasskeyOffer, TotpFirstRun, TotpSetup } from "../../src/app/Totp.tsx";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

/** The begin answer a hub sends: the URI, the secret, and a small QR (a finder pattern in one
 *  corner, dark modules spelled as 1). */
const BEGUN = {
  uri: "otpauth://totp/Huddle:book?secret=ABC234DEF&issuer=Huddle",
  secret: "ABC234DEF",
  qr: ["111000", "101000", "111000", "000000", "000011", "000011"],
};

/** A fake browser assertion the mocked navigator.credentials.get resolves with. */
const assertion = {
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

/** Types one code into the six-box field, one digit per box. */
const typeCode = (code: string): void => {
  const boxes = screen.getAllByRole("textbox").map((el) => el as HTMLInputElement);
  for (const [i, ch] of [...code].entries()) {
    const box = boxes[i];
    if (!box) throw new Error(`box ${i} of the code field is missing`);
    fireEvent.input(box, { target: { value: ch } });
  }
};

type Mount = {
  calls: { path: string; body: unknown }[];
  create: ReturnType<typeof vi.fn>;
  toast: ReturnType<typeof makeCtx>["toast"];
  copy: ReturnType<typeof makeCtx>["copy"];
  answer(path: string, a: unknown): void;
};

/** Mounts ui over one recording api whose answers the test flips per path. */
function mount(ui: (api: Api) => JSX.Element, answers: Record<string, unknown> = {}): Mount {
  cleanup();
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve({}) })),
  );
  const calls: { path: string; body: unknown }[] = [];
  const held: Record<string, unknown> = { ...answers };
  const api: Api = {
    api: (path, o) => {
      calls.push({ path, body: o?.body });
      const a = held[path];
      if (a instanceof Error) return Promise.reject(a);
      return Promise.resolve(a ?? {});
    },
    op: () => Promise.resolve({}),
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  };
  const create = vi.fn(() =>
    Promise.resolve({
      id: "cred-9",
      rawId: new Uint8Array([9]).buffer,
      type: "public-key",
      response: {
        attestationObject: new Uint8Array([5]).buffer,
        clientDataJSON: new Uint8Array([6]).buffer,
      },
    }),
  );
  vi.stubGlobal("navigator", {
    ...navigator,
    credentials: { create, get: vi.fn(() => Promise.resolve(assertion)) },
    userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
  });
  const ctx = makeCtx(makeState(), { api });
  renderIn(ui(api), ctx);
  return {
    calls,
    create,
    toast: ctx.toast,
    copy: ctx.copy,
    answer: (path, a) => {
      held[path] = a;
    },
  };
}

const httpError = (status: number, message: string): ApiError =>
  Object.assign(new Error(message), { status });

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

describe("the authenticator set-up", () => {
  it("TestShowsTheQrAndTheSecretThenFinishesOnTheCode", async () => {
    const onDone = vi.fn();
    const h = mount(() => <TotpSetup onDone={onDone} />, { "/api/totp/begin": BEGUN });
    await flush();
    expect(screen.getByRole("img", { name: "Authenticator QR code" })).toBeDefined();
    expect(screen.getByText("ABC2 34DE F")).toBeDefined();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/totp/begin"]);

    h.answer("/api/totp/finish", { recovery: ["abcd-efgh", "ijkl-mnop"] });
    typeCode("498123");
    await flush();
    expect(h.calls[1]?.path).toBe("/api/totp/finish");
    expect(h.calls[1]?.body).toEqual({ code: "498123" });
    expect(screen.getByText("Save your recovery codes")).toBeDefined();
    expect(screen.getByText("abcd-efgh")).toBeDefined();
    expect(screen.queryByRole("img", { name: "Authenticator QR code" })).toBeNull();
    await act(async () => {
      screen.getByRole("button", { name: "I saved them" }).click();
    });
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("TestSaysTheWrongCodeInlineAndKeepsTheSetUp", async () => {
    const h = mount(() => <TotpSetup onDone={() => {}} />, {
      "/api/totp/begin": BEGUN,
      "/api/totp/finish": httpError(400, "That code did not work. Check your app, or use a recovery code."),
    });
    await flush();
    typeCode("000000");
    await flush();
    expect(screen.getByText(/That code did not work/)).toBeDefined();
    expect(screen.getByRole("img", { name: "Authenticator QR code" })).toBeDefined();
    expect(h.calls.filter((c) => c.path === "/api/totp/finish")).toHaveLength(1);
  });
});

describe("the first-run page", () => {
  it("TestLaterSkipsTheSetUp", async () => {
    const onLater = vi.fn();
    mount(() => <TotpFirstRun onDone={() => {}} onLater={onLater} />, { "/api/totp/begin": BEGUN });
    await flush();
    expect(screen.getByText("Set up your authenticator app")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: /Later/ }).click();
    });
    expect(onLater).toHaveBeenCalledTimes(1);
  });
});

describe("the settings card", () => {
  it("TestRemovalRunsTheStepUpOnceAndRetries", async () => {
    const onChanged = vi.fn();
    const h = mount(() => <AuthenticatorCard enrolled onChanged={onChanged} />, {
      "/api/totp": httpError(401, "step-up required"),
      "/api/stepup/begin": { options: { publicKey: { challenge: "aGk", rpId: "localhost" } } },
      "/api/stepup/finish": {},
    });
    await act(async () => {
      screen.getByRole("button", { name: "Remove" }).click();
    });
    await flush();
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    h.answer("/api/totp", {});
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(h.calls.filter((c) => c.path === "/api/totp")).toHaveLength(2);
    expect(h.calls.map((c) => c.path)).toContain("/api/stepup/finish");
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("TestTheSetUpOpensInlineWhenNothingIsEnrolled", async () => {
    mount(() => <AuthenticatorCard enrolled={false} onChanged={() => {}} />, {
      "/api/totp/begin": BEGUN,
    });
    await flush();
    expect(screen.getByText(/Not set up/)).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Set up" }).click();
    });
    await flush();
    expect(screen.getByRole("img", { name: "Authenticator QR code" })).toBeDefined();
  });
});

describe("the passkey offer", () => {
  it("TestTurnOnRunsTheRegistration", async () => {
    const onDone = vi.fn();
    const h = mount(() => <PasskeyOffer open onDone={onDone} onClose={() => {}} />, {
      "/api/passkey/begin": { options: { publicKey: { challenge: "aGk", rp: { id: "localhost" } } } },
    });
    await flush();
    expect(screen.getByText("Use Touch ID next time?")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Turn on" }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/passkey/begin", "/api/passkey/finish"]);
    expect(h.create).toHaveBeenCalledTimes(1);
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("TestNotNowClosesWithoutRunningAnything", async () => {
    const onClose = vi.fn();
    const h = mount(() => <PasskeyOffer open onDone={() => {}} onClose={onClose} />, {});
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "Not now" }).click();
    });
    await flush();
    expect(h.calls).toEqual([]);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("the set-up's edges", () => {
  it("TestABeginWithNoQrOrSecretStillAsksForTheCode", async () => {
    mount(() => <TotpSetup onDone={() => {}} />, { "/api/totp/begin": { uri: "otpauth://totp/x" } });
    await flush();
    expect(screen.queryByRole("img", { name: "Authenticator QR code" })).toBeNull();
    expect(screen.queryByLabelText("Copy the secret")).toBeNull();
    expect(screen.getByText("Enter the 6-digit code the app shows")).toBeDefined();
  });

  it("TestTheRecoveryStageCopiesAndDownloads", async () => {
    const h = mount(() => <TotpSetup onDone={() => {}} />, {
      "/api/totp/begin": BEGUN,
      "/api/totp/finish": { recovery: ["aaaa-bbbb", "cccc-dddd"] },
    });
    await flush();
    typeCode("498123");
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "Copy" }).click();
    });
    expect(h.copy).toHaveBeenCalledWith("aaaa-bbbb\ncccc-dddd", "Recovery codes copied");
    await act(async () => {
      screen.getByRole("button", { name: "Download .txt" }).click();
    });
    expect(screen.getByText("Save your recovery codes")).toBeDefined();
  });

  it("TestARefusedStepUpLeavesTheSetUpUnbegun", async () => {
    const h = mount(() => <TotpSetup onDone={() => {}} />, {
      "/api/totp/begin": httpError(401, "step-up required"),
    });
    await flush();
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Cancel" }).click();
    });
    await flush();
    expect(h.calls.filter((c) => c.path === "/api/totp/begin")).toHaveLength(1);
    expect(screen.queryByRole("img", { name: "Authenticator QR code" })).toBeNull();
  });

  it("TestABeginThatFailsAfterTheStepUpIsSaidInline", async () => {
    const h = mount(() => <TotpSetup onDone={() => {}} />, {
      "/api/totp/begin": httpError(401, "step-up required"),
      "/api/stepup/begin": { options: { publicKey: { challenge: "aGk", rpId: "localhost" } } },
      "/api/stepup/finish": {},
    });
    await flush();
    h.answer("/api/totp/begin", httpError(500, "the hub refused"));
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(screen.getByText(/the hub refused/)).toBeDefined();
    expect(h.calls.filter((c) => c.path === "/api/totp/begin")).toHaveLength(2);
  });
});

describe("the settings card's refusals", () => {
  it("TestARemovalTheServerRefusedIsSaidAsAToast", async () => {
    const onChanged = vi.fn();
    const h = mount(() => <AuthenticatorCard enrolled onChanged={onChanged} />, {
      "/api/totp": httpError(500, "the hub refused"),
    });
    await act(async () => {
      screen.getByRole("button", { name: "Remove" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("the hub refused", { bad: true });
    expect(onChanged).not.toHaveBeenCalled();
  });

  it("TestA401ThatIsNotTheStepUpWordIsRefusedToo", async () => {
    const onChanged = vi.fn();
    const h = mount(() => <AuthenticatorCard enrolled onChanged={onChanged} />, {
      "/api/totp": httpError(401, "the session ended"),
    });
    await act(async () => {
      screen.getByRole("button", { name: "Remove" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("the session ended", { bad: true });
    expect(h.calls.filter((c) => c.path === "/api/stepup/begin")).toHaveLength(0);
  });
});

describe("the platform word", () => {
  it("TestBioLabelNamesEachPlatformsLock", () => {
    expect(bioLabel("Mozilla/5.0 (iPhone; CPU iPhone OS 17_4)").title).toBe("Use Face ID next time?");
    expect(bioLabel("Mozilla/5.0 (iPad; CPU OS 17_4)").title).toBe("Use Face ID next time?");
    expect(bioLabel("Mozilla/5.0 (Linux; Android 14)").title).toBe("Use your fingerprint next time?");
    expect(bioLabel("Mozilla/5.0 (X11; Linux x86_64)").title).toBe("Use this device's lock next time?");
  });
});

describe("the passkey offer's failures", () => {
  it("TestACreationTheBrowserRefusedIsSaidInline", async () => {
    const onDone = vi.fn();
    const h = mount(() => <PasskeyOffer open onDone={onDone} onClose={() => {}} />, {
      "/api/passkey/begin": { options: { publicKey: { challenge: "aGk", rp: { id: "localhost" } } } },
    });
    h.create.mockRejectedValueOnce(new Error("cancelled"));
    await act(async () => {
      screen.getByRole("button", { name: "Turn on" }).click();
    });
    await flush();
    expect(screen.getByText("The passkey was not created. Try again.")).toBeDefined();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/passkey/begin"]);
    expect(onDone).not.toHaveBeenCalled();
  });

  it("TestAFinishTheServerRefusedIsSaidInline", async () => {
    const onDone = vi.fn();
    mount(() => <PasskeyOffer open onDone={onDone} onClose={() => {}} />, {
      "/api/passkey/begin": { options: { publicKey: { challenge: "aGk", rp: { id: "localhost" } } } },
      "/api/passkey/finish": httpError(500, "the hub refused"),
    });
    await act(async () => {
      screen.getByRole("button", { name: "Turn on" }).click();
    });
    await flush();
    expect(screen.getByText(/the hub refused/)).toBeDefined();
    expect(onDone).not.toHaveBeenCalled();
  });
});
