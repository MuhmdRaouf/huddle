// Channels.test.tsx — Settings › Linked channels (SPEC 8): every link grouped under its
// channel with the session's mark and title (the id when the registry does not know it), the
// level's own select that moves it, the digest age beside a session that has digested, the
// unlink that asks for the passkey when the server says step-up, the refusals said as the
// server spelled them, the empty word, and the link-another sheet that starts on the channel.

import { act, cleanup, screen, waitFor } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { FetchFn } from "../../src/api.ts";
import { Channels } from "../../src/app/Channels.tsx";
import type { RegistrySession, RegistryStore } from "../../src/registry.ts";
import { flush, makeRegistry, NOW, stubMedia } from "../helpers.tsx";
import { renderHuddle } from "../render.tsx";

/** One registry row, as the Now list's fixture spells them. */
const row = (over: Partial<RegistrySession> & { session_id: string }): RegistrySession => ({
  state: "working",
  last_event: "UserPromptSubmit",
  last_event_at: "2026-10-08T11:59:30Z",
  ...over,
});

/** One link as GET /api/links answers it. */
const link = (
  over: Partial<{ channel: string; session_id: string; level: string; digest_at: string }> = {},
) => ({
  channel: "kanban-release",
  session_id: "s-1",
  level: "digest",
  linked_at: "2026-10-08T10:00:00Z",
  ...over,
});

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
  calls: Array<{ url: string; init?: RequestInit }>;
  toast: ReturnType<typeof renderHuddle>["toast"];
  answerWith(status: number, body: unknown): void;
};

/** Renders the section over one fetch answer per call, with the registry knowing two of the
 *  linked sessions by title; the third link's session is one it has never seen. */
function mount(answers: Array<{ status: number; body: unknown }>, registry?: RegistryStore): Mount {
  stubMedia();
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const queue = [...answers];
  const fetchFn: FetchFn = (url, init) => {
    calls.push(init ? { url, init } : { url });
    const a = queue.shift() ?? {
      status: 200,
      body: {
        channels: [
          {
            channel: "kanban-release",
            links: [link({ session_id: "s-1" }), link({ session_id: "s-unknown", level: "overhear" })],
          },
          {
            channel: "billing",
            links: [link({ channel: "billing", session_id: "s-2", digest_at: "2026-10-08T11:59:30Z" })],
          },
        ],
      },
    };
    return Promise.resolve(
      new Response(JSON.stringify(a.body), {
        status: a.status,
        headers: { "content-type": "application/json" },
      }),
    );
  };
  const r = renderHuddle(<Channels />, {}, NOW, fetchFn, registry);
  return {
    calls,
    toast: r.toast,
    answerWith: (status, body) => {
      queue.push({ status, body });
    },
  };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** The registry the section reads the titles and states from: two of the three linked sessions
 *  are known by title, the third is one it has never seen. */
const seededRegistry = (): RegistryStore =>
  makeRegistry({
    sessions: [
      row({ session_id: "s-1", state: "working", title: "kanban-e2e", machine: "laptop" }),
      row({ session_id: "s-2", state: "idle", title: "hub-home", machine: "build-box" }),
    ],
  });

describe("the linked channels section", () => {
  it("TestTheListGroupsByChannelWithMarksTitlesAndDigestAges", async () => {
    mount([], seededRegistry());
    await waitFor(() => expect(screen.getByText("#kanban-release")).toBeDefined());
    const group = screen.getByText("#kanban-release").closest("[data-channel]") as HTMLElement;
    // the session the registry knows carries its mark and its title, the other falls back to its id
    const first = group.querySelector("[data-link='s-1']") as HTMLElement;
    expect(first.textContent).toContain("kanban-e2e");
    expect(first.querySelector("[role='img']")?.getAttribute("aria-label")).toBe("working");
    const second = group.querySelector("[data-link='s-unknown']") as HTMLElement;
    expect(second.textContent).toContain("s-unknown");
    expect(second.querySelector("[role='img']")?.getAttribute("aria-label")).toBe("idle");
    // a session that has digested says when, the other says nothing
    const billing = screen.getByText("#billing").closest("[data-channel]") as HTMLElement;
    expect(billing.textContent).toContain("digest 30 s");
    expect((billing.querySelector("select") as HTMLSelectElement).value).toBe("digest");
    expect(screen.getAllByRole("button", { name: "Unlink" }).length).toBe(3);
  });

  it("TestALevelMovePatchesAndUpdatesTheSelect", async () => {
    const h = mount([], seededRegistry());
    await waitFor(() => expect(screen.getByText("#kanban-release")).toBeDefined());
    h.answerWith(200, { channel: "kanban-release", session_id: "s-1", level: "overhear" });
    const select = screen.getByLabelText("Level for kanban-e2e") as HTMLSelectElement;
    await act(async () => {
      select.value = "overhear";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    expect(h.calls[1]?.url).toBe("/api/links/kanban-release/s-1");
    expect(h.calls[1]?.init?.method).toBe("PATCH");
    expect(JSON.parse(h.calls[1]?.init?.body as string)).toEqual({ level: "overhear" });
    expect((screen.getByLabelText("Level for kanban-e2e") as HTMLSelectElement).value).toBe("overhear");
  });

  it("TestAnUnlinkAsksThePasskeyWhenTheServerSaysSo", async () => {
    const h = mount([], seededRegistry());
    await waitFor(() => expect(screen.getByText("#kanban-release")).toBeDefined());
    h.answerWith(401, { error: "step-up required" });
    h.answerWith(200, { options: { publicKey: { challenge: "aGk" } } });
    h.answerWith(200, { valid_for: 300 });
    h.answerWith(200, { ok: true });
    stubGet();
    await act(async () => {
      screen.getAllByRole("button", { name: "Unlink" })[0]?.click();
    });
    await flush();
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    expect(screen.getByText("Unlink session")).toBeDefined();
    const what = document.querySelector("[data-stepup-what]") as HTMLElement;
    expect(what.textContent).toContain("kanban-e2e");
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    const deletes = h.calls.filter((c) => c.url === "/api/links/kanban-release/s-1");
    expect(deletes).toHaveLength(2);
    expect(deletes[0]?.init?.method).toBe("DELETE");
    await waitFor(() => expect(document.querySelector("[data-link='s-1']")).toBeNull());
  });

  it("TestAnUnlinkRefusalIsToasted", async () => {
    const h = mount([], seededRegistry());
    await waitFor(() => expect(screen.getByText("#kanban-release")).toBeDefined());
    h.answerWith(403, { error: "only the owner unlinks" });
    await act(async () => {
      screen.getAllByRole("button", { name: "Unlink" })[0]?.click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("only the owner unlinks", { bad: true });
    expect(document.querySelector("[data-link='s-1']")).not.toBeNull();
  });

  it("TestAFailedReadIsSaidAndTheEmptyRegistryHasItsWord", async () => {
    mount([{ status: 403, body: { error: "sign in first" } }], seededRegistry());
    await waitFor(() => expect(screen.getByText("sign in first")).toBeDefined());
    cleanup();
    mount([{ status: 200, body: { channels: [] } }]);
    await waitFor(() =>
      expect(
        screen.getByText("No linked sessions yet. Pick two or more on Now and choose Link."),
      ).toBeDefined(),
    );
  });

  it("TestTheLinkAnotherButtonOpensTheSheetOnTheChannel", async () => {
    const h = mount([], seededRegistry());
    await waitFor(() => expect(screen.getByText("#kanban-release")).toBeDefined());
    h.answerWith(200, { channel: "kanban-release" });
    const group = screen.getByText("#kanban-release").closest("[data-channel]") as HTMLElement;
    await act(async () => {
      group.querySelector<HTMLButtonElement>("[data-link-another]")?.click();
    });
    // the sheet starts with the channel's own sessions picked and the channel chosen
    expect(screen.getByRole("heading", { name: "Link 2 sessions" })).toBeDefined();
    expect((screen.getByRole("radio", { name: "Existing channel" }) as HTMLInputElement).checked).toBe(true);
    const select = screen.getByLabelText("Channel") as HTMLSelectElement;
    expect(select.value).toBe("kanban-release");
    await act(async () => {
      document.querySelector<HTMLButtonElement>("[data-link-submit]")?.click();
    });
    await flush();
    expect(JSON.parse(h.calls[1]?.init?.body as string)).toEqual({
      sessions: ["s-1", "s-unknown"],
      channel: "kanban-release",
      level: "digest",
    });
  });
});
