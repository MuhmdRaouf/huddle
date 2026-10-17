// LinkSheet.test.tsx — the sheet that ties the picked sessions into a channel (SPEC 8): the
// heading and the submit carry the count, digest is the level that starts checked with each
// level's own line under it, a name that breaks the rule blocks the submit, a new channel posts
// create and an existing one does not, the 401 runs the passkey sheet once and retries, a
// refusal is toasted as the server spelled it, and a link celebrates and closes.

import { act, cleanup, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { FetchFn } from "../../src/api.ts";
import { LinkSheet } from "../../src/app/LinkSheet.tsx";
import { flush, NOW } from "../helpers.tsx";
import { renderHuddle } from "../render.tsx";

/** The passkey check the step-up sheet runs, as the browser would answer it. */
function stubGet(): void {
  vi.stubGlobal("navigator", {
    ...navigator,
    credentials: {
      get: () =>
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
    },
  });
}

type Mount = {
  calls: Array<{ url: string; body: unknown }>;
  toast: ReturnType<typeof renderHuddle>["toast"];
  close: ReturnType<typeof vi.fn>;
  /** Re-answers every later call with this answer. */
  answerWith(status: number, body: unknown): void;
};

/** Renders the sheet over three picked sessions and the channel list the app already holds. */
function mount(
  answers: Array<{ status: number; body: unknown }> = [],
  sessions: readonly string[] = ["s-1", "s-2", "s-3"],
): Mount {
  const calls: Array<{ url: string; body: unknown }> = [];
  const queue = [...answers];
  const fetchFn: FetchFn = (url, init) => {
    calls.push({ url, body: init?.body ? JSON.parse(init.body as string) : undefined });
    const a = queue.shift() ?? { status: 200, body: {} };
    return Promise.resolve(
      new Response(JSON.stringify(a.body), {
        status: a.status,
        headers: { "content-type": "application/json" },
      }),
    );
  };
  const close = vi.fn();
  const r = renderHuddle(
    <LinkSheet sessions={sessions} onClose={close} />,
    { channels: [{ name: "old-one" }, { name: "old-two" }] },
    NOW,
    fetchFn,
  );
  return {
    calls,
    toast: r.toast,
    close,
    answerWith: (status, body) => {
      queue.push({ status, body });
    },
  };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the link sheet", () => {
  it("TestTheHeadingAndTheSubmitCarryTheCountAndDigestStartsChecked", () => {
    mount();
    expect(screen.getByRole("heading", { name: "Link 3 sessions" })).toBeDefined();
    // no name yet: the submit waits, without crying wolf about the empty field
    const submit = screen.getByRole("button", { name: "Link 3 sessions" });
    expect(submit.hasAttribute("disabled")).toBe(true);
    expect(screen.queryByText(/Lowercase letters, digits and dashes/)).toBeNull();
    // the radio's name is its label: the level's word and the line it says about itself
    expect((screen.getByRole("radio", { name: /^Digest / }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("radio", { name: /^Overhear / }) as HTMLInputElement).checked).toBe(false);
    expect((screen.getByRole("radio", { name: /^On request / }) as HTMLInputElement).checked).toBe(false);
  });

  it("TestEachLevelSaysWhatItWouldHear", () => {
    mount();
    expect(screen.getByText("Hears every message posted in the channel.")).toBeDefined();
    expect(screen.getByText("Gets each peer's newest digest once, on its next prompt.")).toBeDefined();
    expect(screen.getByText("Hears nothing until you brief it.")).toBeDefined();
  });

  it("TestANameThatBreaksTheRuleBlocksTheSubmit", async () => {
    mount();
    const name = screen.getByLabelText("Channel name") as HTMLInputElement;
    const submit = screen.getByRole("button", { name: "Link 3 sessions" }) as HTMLButtonElement;
    await act(async () => {
      name.value = "Bad Name";
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(screen.getByText(/Lowercase letters, digits and dashes/)).toBeDefined();
    expect(submit.disabled).toBe(true);
    await act(async () => {
      name.value = "kanban-release";
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(screen.queryByText(/Lowercase letters, digits and dashes/)).toBeNull();
    expect(submit.disabled).toBe(false);
  });

  it("TestANewChannelPostsCreateAndAnExistingOneDoesNot", async () => {
    const h = mount([{ status: 200, body: { channel: "fresh-link" } }]);
    const name = screen.getByLabelText("Channel name") as HTMLInputElement;
    await act(async () => {
      name.value = "fresh-link";
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      screen.getByRole("button", { name: "Link 3 sessions" }).click();
    });
    await flush();
    expect(h.calls[0]).toEqual({
      url: "/api/links",
      body: { sessions: ["s-1", "s-2", "s-3"], channel: "fresh-link", create: true, level: "digest" },
    });

    cleanup();
    const h2 = mount([{ status: 200, body: { channel: "old-two" } }]);
    await act(async () => {
      screen.getByRole("radio", { name: "Existing channel" }).click();
    });
    const select = screen.getByLabelText("Channel") as HTMLSelectElement;
    await act(async () => {
      select.value = "old-two";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      screen.getByRole("button", { name: "Link 3 sessions" }).click();
    });
    await flush();
    expect(h2.calls[0]).toEqual({
      url: "/api/links",
      body: { sessions: ["s-1", "s-2", "s-3"], channel: "old-two", level: "digest" },
    });
  });

  it("TestAStepUpRunsThePasskeyOnceAndTheRetryCelebrates", async () => {
    const h = mount([
      { status: 401, body: { error: "step-up required" } },
      { status: 200, body: { options: { publicKey: { challenge: "aGk" } } } },
      { status: 200, body: { valid_for: 300 } },
      { status: 200, body: { channel: "fresh-link" } },
    ]);
    stubGet();
    const name = screen.getByLabelText("Channel name") as HTMLInputElement;
    await act(async () => {
      name.value = "fresh-link";
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      screen.getByRole("button", { name: "Link 3 sessions" }).click();
    });
    await flush();
    // the sheet names the act and the sessions it ties before the passkey runs
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    expect(screen.getByText("Link sessions")).toBeDefined();
    expect(screen.getByText("s-1, s-2, s-3")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.url)).toEqual([
      "/api/links",
      "/api/stepup/begin",
      "/api/stepup/finish",
      "/api/links",
    ]);
    expect(h.toast).toHaveBeenCalledWith("Linked 3 sessions into #fresh-link");
    expect(h.close).toHaveBeenCalled();
  });

  it("TestARefusalIsToastedAsTheServerSpelledIt", async () => {
    const h = mount([{ status: 400, body: { error: "link takes 2 or more sessions" } }]);
    const name = screen.getByLabelText("Channel name") as HTMLInputElement;
    await act(async () => {
      name.value = "fresh-link";
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      screen.getByRole("button", { name: "Link 3 sessions" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("link takes 2 or more sessions", { bad: true });
    expect(h.close).not.toHaveBeenCalled();
  });
});
