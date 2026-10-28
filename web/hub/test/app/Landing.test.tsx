// Landing.test.tsx — the landing page's own paths: the states its sign-in card takes (no method,
// a passkey, an enrolment, both; the biometric button this browser's own passkey earns; the
// reason line only when the server sent one), the two ceremonies (a code posted, a passkey's
// begin-credential-finish with the reload), the inline errors in plain words, the command fields
// the first-time steps and the disclosure carry, the tour's lightbox, the scheme toggle, and the
// start-a-session guide one button away in its sheet.

import { act, cleanup, fireEvent, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import { Landing } from "../../src/app/Landing.tsx";
import { flush, legacyClasses, makeCtx, makeState, renderIn, stubMedia } from "../helpers.tsx";

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
  /** Flips the answer the code sign-in gives from now on. */
  setCodeAnswer(a: unknown): void;
};

/** Mounts the landing over a recording api and a stubbed navigator, fetch and reload. */
function mount(
  over: {
    available?: unknown;
    beginAnswer?: unknown;
    getAnswer?: unknown;
    finishAnswer?: unknown;
    codeAnswer?: unknown;
    health?: Record<string, unknown>;
    mediation?: boolean;
  } = {},
): Mount {
  cleanup();
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve(over.health ?? {}) })),
  );
  if (over.mediation === true) {
    vi.stubGlobal("PublicKeyCredential", {
      isConditionalMediationAvailable: () => Promise.resolve(true),
    });
  }
  const calls: { path: string; body: unknown }[] = [];
  const api: Api = {
    api: (path, o) => {
      calls.push({ path, body: o?.body });
      if (path === "/api/login/available") {
        const a = over.available;
        if (a instanceof Error) return Promise.reject(a);
        return Promise.resolve(a ?? { passkeys: false });
      }
      if (path === "/api/login/totp") {
        const a = code.answer;
        if (a instanceof Error) return Promise.reject(a);
        return Promise.resolve(a ?? { device: "Mac", expires_in: 604800 });
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
  const code = { answer: over.codeAnswer as unknown };
  const get = vi.fn(() =>
    holder.answer instanceof Error ? Promise.reject(holder.answer) : Promise.resolve(credential),
  );
  // the stub keeps the fields the landing reads: happy-dom's navigator holds its userAgent
  // behind a prototype getter a spread would drop, and a clipboard an earlier stub laid down
  vi.stubGlobal("navigator", {
    userAgent: navigator.userAgent,
    credentials: { get },
    clipboard: navigator.clipboard,
  });
  const reload = vi.fn();
  vi.stubGlobal("location", { ...location, reload });
  renderIn(<Landing />, makeCtx(makeState(), { api }));
  return {
    calls,
    get,
    reload,
    setGet: (a) => {
      holder.answer = a;
    },
    setCodeAnswer: (a) => {
      code.answer = a;
    },
  };
}

/** Drives the passkey button and lets the ceremony's promises land. */
const signIn = async (name = "Sign in with a passkey"): Promise<void> => {
  await act(async () => {
    screen.getByRole("button", { name }).click();
  });
  await flush();
};

beforeEach(() => {
  history.replaceState(null, "", "/");
  localStorage.clear();
  vi.stubGlobal(
    "matchMedia",
    () =>
      ({
        matches: false,
        addEventListener: () => {},
        removeEventListener: () => {},
      }) as unknown as MediaQueryList,
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

describe("the landing page's states", () => {
  it("shows the first-time card on a fresh hub, with no warning", async () => {
    mount({ available: { passkeys: false, totp: false }, health: { version: "1.2.3", machine: "box" } });
    await flush();
    expect(screen.getByText(/First time here\?/)).toBeDefined();
    expect(screen.getAllByText("In a Claude session").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Or in a terminal").length).toBeGreaterThan(0);
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.getByText("box")).toBeDefined();
    expect(screen.getByText("Huddle v1.2.3")).toBeDefined();
  });

  it("shows the reason line only when the server sent one", async () => {
    mount({ available: { passkeys: false, totp: false, reason: "This device was removed." } });
    await flush();
    expect(screen.getByText("This device was removed.")).toBeDefined();
  });

  it("shows the code field when the hub holds an enrolment, and the passkey button beside it", async () => {
    mount({ available: { passkeys: true, totp: true } });
    await flush();
    expect(screen.getByText("Enter the code from your authenticator app")).toBeDefined();
    expect(screen.getByRole("button", { name: "Sign in with a passkey" })).toBeDefined();
    expect(screen.getByText("Other ways to sign in")).toBeDefined();
    expect(screen.queryByText(/First time here\?/)).toBeNull();
    cleanup();

    mount({ available: { passkeys: true, totp: false } });
    await flush();
    expect(screen.queryByText("Enter the code from your authenticator app")).toBeNull();
    expect(screen.getByRole("button", { name: "Sign in with a passkey" })).toBeDefined();
    expect(screen.getByText("Other ways to sign in")).toBeDefined();
    cleanup();

    mount({ available: { passkeys: false, totp: true } });
    await flush();
    expect(screen.getByText("Enter the code from your authenticator app")).toBeDefined();
    expect(screen.queryByRole("button", { name: "Sign in with a passkey" })).toBeNull();
    expect(document.activeElement?.getAttribute("data-box")).toBe("0");
  });

  it("leads with the biometric button when this browser holds a passkey", async () => {
    localStorage.setItem("huddle:passkey-here", "1");
    mount({ available: { passkeys: true, totp: true } });
    await flush();
    expect(screen.getByRole("button", { name: "Sign in with this device's lock" })).toBeDefined();
    expect(screen.getByText("Or enter your authenticator code")).toBeDefined();
    expect(screen.queryByRole("button", { name: "Sign in with a passkey" })).toBeNull();
    localStorage.clear();

    // the code field stays the default without the mark
    mount({ available: { passkeys: true, totp: true } });
    await flush();
    expect(screen.getByText("Enter the code from your authenticator app")).toBeDefined();
  });

  it("parks the passkey offer in the code field's autofill once the field takes focus", async () => {
    localStorage.setItem("huddle:passkey-here", "1");
    const h = mount({ available: { passkeys: true, totp: true }, mediation: true });
    await flush();
    await flush(); // the autofill answer sits one promise past the available one
    // the field says its autofill carries a passkey, but nothing has begun yet
    expect((screen.getAllByRole("textbox")[0] as HTMLInputElement).getAttribute("autocomplete")).toBe(
      "one-time-code webauthn",
    );
    expect(h.calls.map((c) => c.path)).toEqual(["/api/login/available"]);
    expect(h.get).not.toHaveBeenCalled();
    // the field's first focus parks one begin as a conditional get, and its answer signs in
    fireEvent(screen.getAllByRole("textbox")[0] as HTMLInputElement, new Event("focusin", { bubbles: true }));
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual([
      "/api/login/available",
      "/api/login/begin",
      "/api/login/finish",
    ]);
    const asked = h.get.mock.calls[0]?.[0] as { mediation?: string; publicKey: { challenge: ArrayBuffer } };
    expect(asked.mediation).toBe("conditional");
    expect(localStorage.getItem("huddle:passkey-here")).toBe("1");
    expect(h.reload).toHaveBeenCalledTimes(1);
    // a later focus begins nothing new
    fireEvent(screen.getAllByRole("textbox")[0] as HTMLInputElement, new Event("focusin", { bubbles: true }));
    await flush();
    expect(h.calls.filter((c) => c.path === "/api/login/begin")).toHaveLength(1);
  });

  it("keeps the autofill offer quiet in a browser with no passkey saved here", async () => {
    const h = mount({ available: { passkeys: true, totp: true }, mediation: true });
    await flush();
    fireEvent(screen.getAllByRole("textbox")[0] as HTMLInputElement, new Event("focusin", { bubbles: true }));
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/login/available"]);
    expect(h.get).not.toHaveBeenCalled();
  });
});

describe("the landing page's frame", () => {
  it("paints its frame, header and version line from the tokens", async () => {
    mount({ available: { passkeys: false, totp: false }, health: { version: "1.2.3" } });
    await flush();
    const frame = document.querySelector("[data-landing]") as HTMLElement;
    expect(frame.className).toBe("ui-page");
    expect(legacyClasses(document.querySelector("[data-landing-head]") as HTMLElement)).toEqual([]);
    expect(screen.getByText("Huddle v1.2.3").className).toBe(
      "m-0 text-center text-xs text-[var(--ui-muted)]",
    );
  });
});

describe("the landing page's tokens", () => {
  /** The daisyUI classes legacyClasses leaves to each page: the form field, the key cap, the dot. */
  const DAISY = /^(input(-.+)?|kbd(-.+)?|status(-.+)?|bg-line)$/;
  const daisy = (root: ParentNode): string[] =>
    [...root.querySelectorAll("[class]")]
      .flatMap((el) => (el.getAttribute("class") ?? "").split(/\s+/))
      .filter((c) => DAISY.test(c));

  it("paints the sign-in card, its code field and its disclosure from the tokens", async () => {
    mount({ available: { passkeys: true, totp: true } });
    await flush();
    const card = document.querySelector("[data-landing-signin]") as HTMLElement;
    expect(legacyClasses(card)).toEqual([]);
    expect(daisy(card)).toEqual([]);
    for (const box of screen.getAllByRole("textbox")) expect(box.className).toContain("ui-input");
  });

  it("paints the tour's lightbox picture with the radius token", async () => {
    mount();
    await act(async () => {
      screen.getByRole("button", { name: "Kanban: open the tour step" }).click();
    });
    await flush();
    const box = document.querySelector("[data-tour-box]") as HTMLElement;
    expect(legacyClasses(box)).toEqual([]);
    expect(box.querySelector("img")?.className).toContain("rounded-[var(--ui-radius)]");
  });

  it("paints the start-a-session guide in its sheet from the tokens", async () => {
    mount();
    await act(async () => {
      screen.getByRole("button", { name: "Start a session" }).click();
    });
    const sheet = document.querySelector("dialog.ui-sheet") as HTMLElement;
    expect(legacyClasses(sheet)).toEqual([]);
    expect(daisy(sheet)).toEqual([]);
    expect(screen.getByRole("link", { name: /Full guide/ }).className).toBe("ui-btn ui-btn-ghost");
  });
});

describe("the landing page's sign-in", () => {
  it("posts a code and marks the shell for the passkey offer", async () => {
    const h = mount({ available: { totp: true } });
    await flush();
    fireEvent.input(screen.getAllByRole("textbox")[0] as HTMLInputElement, {
      target: { value: "123456" },
    });
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/login/available", "/api/login/totp"]);
    expect(h.calls[1]?.body).toEqual({ code: "123456" });
    expect(localStorage.getItem("huddle:totp-signed-in")).toBe("1");
    expect(h.reload).toHaveBeenCalledTimes(1);
    // a refused code says so inline and clears the field for the retyping
    h.setCodeAnswer(new Error("That code did not work. Check your app, or use a recovery code."));
    cleanup();
    const second = mount({ available: { totp: true } });
    second.setCodeAnswer(new Error("That code did not work. Check your app, or use a recovery code."));
    await flush();
    fireEvent.paste(screen.getAllByRole("textbox")[0] as HTMLInputElement, {
      clipboardData: { getData: () => "111111" },
    });
    await flush();
    expect(second.calls.map((c) => c.path)).toEqual(["/api/login/available", "/api/login/totp"]);
    expect(screen.getByText(/That code did not work/)).toBeDefined();
    expect((screen.getAllByRole("textbox")[0] as HTMLInputElement).value).toBe("");
  });

  it("runs the passkey ceremony, reloads and marks this browser", async () => {
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
    expect(localStorage.getItem("huddle:passkey-here")).toBe("1");
    expect(h.reload).toHaveBeenCalledTimes(1);
  });

  it("says a cancelled prompt inline and the button lives", async () => {
    const h = mount({
      available: { passkeys: true },
      getAnswer: new DOMException("the owner said no", "NotAllowedError"),
    });
    await flush();
    await signIn();
    expect(screen.getByText("Sign-in cancelled. Try again.")).toBeDefined();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/login/available", "/api/login/begin"]);
    expect(screen.getByRole("button", { name: "Sign in with a passkey" })).not.toHaveProperty(
      "disabled",
      true,
    );
    h.setGet(undefined);
    await signIn();
    expect(h.reload).toHaveBeenCalledTimes(1);
  });

  it("says the server's refusal inline", async () => {
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

describe("the landing page's copy fields", () => {
  it("offers /huddle:setup, the command that replaced /huddle:open", () => {
    mount();
    expect(screen.getByLabelText("Copy /huddle:setup")).toBeDefined();
    expect(screen.queryByLabelText("Copy /huddle:open")).toBeNull();
    expect(screen.getByLabelText("Copy huddle open")).toBeDefined();
  });

  it("copies /huddle:setup and flashes the button", async () => {
    vi.useFakeTimers();
    const { writes } = stubClipboardLanding();
    mount();
    const big = screen.getByText("Copy /huddle:setup").closest("button") as HTMLButtonElement;
    await act(async () => {
      big.click();
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(writes).toEqual(["/huddle:setup"]);
    expect(screen.getByText("Copied: paste it into a Claude session")).toBeDefined();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1700);
    });
    expect(screen.queryByText("Copied: paste it into a Claude session")).toBeNull();
    vi.useRealTimers();
  });

  it("stays quiet when the clipboard refuses", () => {
    vi.stubGlobal("navigator", {
      ...navigator,
      clipboard: { writeText: () => Promise.reject(new Error("no")) },
    });
    mount();
    void act(() => {
      screen.getByLabelText("Copy /huddle:setup").click();
    });
    expect(screen.queryByText(/Copied/)).toBeNull();
  });
});

describe("the landing page's tour and theme", () => {
  it("opens the lightbox and steps through the four tabs", async () => {
    mount();
    await act(async () => {
      screen.getByRole("button", { name: "Channels: open the tour step" }).click();
    });
    await flush();
    expect(screen.getByText("Channels (2 of 4)")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Next" }).click();
    });
    await flush();
    expect(screen.getByText("Radar (3 of 4)")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Previous" }).click();
    });
    await flush();
    expect(screen.getByText("Channels (2 of 4)")).toBeDefined();
  });

  it("flips the resolved scheme from the head's toggle and keeps it", async () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    delete document.documentElement.dataset.scheme;
    mount();
    expect(document.documentElement.dataset.scheme).toBeUndefined();
    await act(async () => {
      screen.getByRole("button", { name: "Switch to the dark theme" }).click();
    });
    expect(document.documentElement.dataset.scheme).toBe("dark");
    expect(localStorage.getItem("huddle:scheme")).toBe("dark");
    await act(async () => {
      screen.getByRole("button", { name: "Switch to the light theme" }).click();
    });
    expect(document.documentElement.dataset.scheme).toBe("light");
    expect(localStorage.getItem("huddle:scheme")).toBe("light");
  });
});

describe("the landing page's start-a-session sheet", () => {
  it("keeps the guide behind one button and opens it in a sheet", async () => {
    mount();
    expect(document.querySelector("[data-connect-card]")).toBeNull();
    await act(async () => {
      screen.getByRole("button", { name: "Start a session" }).click();
    });
    expect(document.querySelector("dialog.ui-sheet [data-connect-card]")).not.toBeNull();
    expect(screen.getByRole("tab", { name: "Other MCP" })).toBeDefined();
  });
});

/** A clipboard the test watches, stubbed the way the landing tests need (a fresh navigator each
 *  mount replaces it, so this wraps the same vi.fn). */
function stubClipboardLanding(): { writes: string[] } {
  const writes: string[] = [];
  vi.stubGlobal("navigator", {
    userAgent: navigator.userAgent,
    credentials: { get: () => Promise.resolve(credential) },
    clipboard: {
      writeText: (t: string) => {
        writes.push(t);
        return Promise.resolve();
      },
    },
  });
  return { writes };
}
