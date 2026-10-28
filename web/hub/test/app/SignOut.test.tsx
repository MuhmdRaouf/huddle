// SignOut.test.tsx — the Sign-out section: this browser signs out after one confirm; everywhere
// asks for the passkey before it posts, and a refused or dismissed sign-out keeps you on the page.

import { act, screen, within } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { FetchFn } from "../../src/api.ts";
import { SignOut } from "../../src/app/SignOut.tsx";
import { flush, legacyClasses, NOW, stubMedia } from "../helpers.tsx";
import { renderHuddle } from "../render.tsx";

type Mount = {
  calls: Array<{ url: string; init?: RequestInit }>;
  toast: ReturnType<typeof renderHuddle>["toast"];
  assign: ReturnType<typeof vi.fn>;
};

/** Renders the Sign-out section over queued fetch answers; `location.assign` is stubbed. */
function mount(answers: Array<{ status: number; body: unknown }> = []): Mount {
  stubMedia();
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const queue = [...answers];
  const fetchFn: FetchFn = (url, init) => {
    calls.push(init ? { url, init } : { url });
    const a = queue.shift() ?? { status: 200, body: { ok: true } };
    return Promise.resolve(
      new Response(JSON.stringify(a.body), {
        status: a.status,
        headers: { "content-type": "application/json" },
      }),
    );
  };
  const assign = vi.fn();
  // the stub keeps the fields the section and the helpers read
  vi.stubGlobal("location", { ...location, assign });
  const r = renderHuddle(<SignOut />, {}, NOW, fetchFn);
  return { calls, toast: r.toast, assign };
}

/** Mocks navigator.credentials.get with one assertion; the test reads back what it was asked. */
function stubGet(): ReturnType<typeof vi.fn> {
  const get = vi.fn(() =>
    Promise.resolve({
      id: "cred-9",
      rawId: new Uint8Array([4, 5]).buffer,
      type: "public-key",
      response: {
        authenticatorData: new Uint8Array([8]).buffer,
        clientDataJSON: new Uint8Array([7]).buffer,
        signature: new Uint8Array([6, 6]).buffer,
        userHandle: null,
      },
    }),
  );
  vi.stubGlobal("navigator", { ...navigator, credentials: { get } });
  return get;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the sign-out section", () => {
  /** The section's own button: the confirm's button shares its name, so take the one outside
   * every dialog. */
  const outside = (name: string): HTMLElement =>
    screen.getAllByRole("button", { name }).find((b) => b.closest("dialog") === null) as HTMLElement;
  /** The one dialog standing. */
  const sheet = (): HTMLElement => document.querySelector("dialog[open]") as HTMLElement;

  it("TestThisBrowserSignsOutAfterOneConfirm", async () => {
    const h = mount();
    await act(async () => {
      outside("Sign out of this browser").click();
    });
    // nothing posts before the confirm
    expect(h.calls).toEqual([]);
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Sign out" }).click();
    });
    await flush();
    const post = h.calls.find((c) => c.url === "/api/signout");
    expect(post?.init?.method).toBe("POST");
    expect(h.assign).toHaveBeenCalledWith("/");
  });

  it("TestEverywhereAsksThePasskeyBeforeItPosts", async () => {
    const h = mount([
      { status: 200, body: { options: { publicKey: { challenge: "aGk" } } } },
      { status: 200, body: { ok: true } },
      { status: 200, body: { ok: true } },
    ]);
    stubGet();
    await act(async () => {
      outside("Sign out everywhere").click();
    });
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Sign out everywhere" }).click();
    });
    await flush();
    // the check names the action, and nothing has posted yet
    expect(document.querySelector("[data-stepup-what]")?.textContent).toContain("Sign out everywhere");
    expect(h.calls.some((c) => c.url === "/api/signout/all")).toBe(false);
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    const all = h.calls.find((c) => c.url === "/api/signout/all");
    expect(all?.init?.method).toBe("POST");
    expect(h.assign).toHaveBeenCalledWith("/");
  });

  it("TestADismissedPasskeyKeepsYouSignedIn", async () => {
    const h = mount([{ status: 200, body: { options: { publicKey: { challenge: "aGk" } } } }]);
    stubGet();
    await act(async () => {
      outside("Sign out everywhere").click();
    });
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Sign out everywhere" }).click();
    });
    await flush();
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Cancel" }).click();
    });
    await flush();
    expect(h.calls.some((c) => c.url === "/api/signout/all")).toBe(false);
    expect(h.assign).not.toHaveBeenCalled();
  });

  it("TestARefusedEverywhereIsToastedAndStays", async () => {
    const h = mount([
      { status: 200, body: { options: { publicKey: { challenge: "aGk" } } } },
      { status: 200, body: { ok: true } },
      { status: 500, body: { error: "not allowed" } },
    ]);
    stubGet();
    await act(async () => {
      outside("Sign out everywhere").click();
    });
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Sign out everywhere" }).click();
    });
    await flush();
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("not allowed", { bad: true });
    expect(h.assign).not.toHaveBeenCalled();
    expect(outside("Sign out everywhere")).toBeDefined();
  });

  it("TestARefusedSignOutIsToastedAndStays", async () => {
    const h = mount([{ status: 500, body: { error: "sign-out failed" } }]);
    await act(async () => {
      outside("Sign out of this browser").click();
    });
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Sign out" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("sign-out failed", { bad: true });
    expect(h.assign).not.toHaveBeenCalled();
    expect(outside("Sign out of this browser")).toBeDefined();
  });

  it("TestTheSectionIsOnTheTokens", () => {
    mount();
    expect(outside("Sign out of this browser").className).toContain("ui-btn-secondary");
    expect(outside("Sign out everywhere").className).toContain("ui-btn-danger");
    expect(
      screen.getByText(
        "Sign out everywhere ends every device's session, this browser too. It asks for your passkey first.",
      ),
    ).toBeDefined();
    expect(legacyClasses(document.body, "dialog")).toEqual([]);
  });
});
