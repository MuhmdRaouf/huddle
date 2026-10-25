// LinkedSessions.test.tsx — a channel's Settings › Linked sessions (SPEC 8): the channel's own
// links alone, each with the session's mark and title (the id when the registry does not know
// it), the level's own select that moves it, the digest age beside a session that has digested,
// the unlink that asks for the passkey when the server says step-up, the refusals said as the
// server spelled them, the empty word, and the link-another sheet that starts on the channel.

import { act, cleanup, screen, waitFor } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { FetchFn } from "../../src/api.ts";
import { LinkedSessions } from "../../src/pages/LinkedSessions.tsx";
import type { RegistrySession, RegistryStore } from "../../src/registry.ts";
import { flush, legacyClasses, makeRegistry, NOW, stubMedia } from "../helpers.tsx";
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

/** Renders kanban-release's card over one fetch answer per call; the links read also carries
 *  billing's, which the card leaves out. The registry knows two of the linked sessions by title;
 *  the third link's session is one it has never seen. */
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
            links: [
              link({ session_id: "s-1", digest_at: "2026-10-08T11:59:30Z" }),
              link({ session_id: "s-unknown", level: "overhear" }),
            ],
          },
          {
            channel: "billing",
            links: [link({ channel: "billing", session_id: "s-2" })],
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
  const r = renderHuddle(<LinkedSessions channel="kanban-release" />, {}, NOW, fetchFn, registry);
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

describe("a channel's linked sessions", () => {
  it("TestTheCardListsTheChannelsOwnLinksWithMarksTitlesAndDigestAges", async () => {
    mount([], seededRegistry());
    await waitFor(() => expect(document.querySelector("[data-link='s-1']")).not.toBeNull());
    expect(screen.getByRole("heading", { name: "Linked sessions" })).toBeDefined();
    // the session the registry knows carries its mark and its title, the other falls back to its id
    const first = document.querySelector("[data-link='s-1']") as HTMLElement;
    expect(first.textContent).toContain("kanban-e2e");
    expect(first.querySelector("[role='img']")?.getAttribute("aria-label")).toBe("working");
    const second = document.querySelector("[data-link='s-unknown']") as HTMLElement;
    expect(second.textContent).toContain("s-unknown");
    expect(second.querySelector("[role='img']")?.getAttribute("aria-label")).toBe("idle");
    // a session that has digested says when, the other says nothing
    expect(first.textContent).toContain("digest 30 s");
    expect(second.textContent).not.toContain("digest");
    expect((second.querySelector("select") as HTMLSelectElement).value).toBe("overhear");
    // another channel's links stay on that channel's own page
    expect(document.querySelector("[data-link='s-2']")).toBeNull();
    expect(screen.getAllByRole("button", { name: "Unlink" }).length).toBe(2);
  });

  it("TestTheSectionPaintsFromTheTokensAlone", async () => {
    mount([], seededRegistry());
    await waitFor(() => expect(document.querySelector("[data-link='s-1']")).not.toBeNull());
    for (const b of screen.getAllByRole("button", { name: "Unlink" }))
      expect(b.className).toContain("ui-btn-ghost");
    expect(screen.getAllByRole("button", { name: "Link another session" })[0]?.className).toContain(
      "ui-btn-secondary",
    );
    for (const s of screen.getAllByRole("combobox")) {
      expect(s.className).toContain("ui-input");
      expect(s.className.split(/\s+/)).not.toContain("select");
    }
    // the passkey and link-another dialogs are lent by their own pages
    expect(legacyClasses(document.body, "dialog")).toEqual([]);
  });

  it("TestALevelMovePatchesAndUpdatesTheSelect", async () => {
    const h = mount([], seededRegistry());
    await waitFor(() => expect(document.querySelector("[data-link='s-1']")).not.toBeNull());
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
    await waitFor(() => expect(document.querySelector("[data-link='s-1']")).not.toBeNull());
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
    await waitFor(() => expect(document.querySelector("[data-link='s-1']")).not.toBeNull());
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
    mount([
      { status: 200, body: { channels: [{ channel: "billing", links: [link({ channel: "billing" })] }] } },
    ]);
    await waitFor(() =>
      expect(
        screen.getByText("No sessions linked into this channel. Pick two or more on Now and choose Link."),
      ).toBeDefined(),
    );
    expect(screen.queryByRole("button", { name: "Link another session" })).toBeNull();
  });

  it("TestTheLinkAnotherButtonOpensTheSheetOnTheChannel", async () => {
    const h = mount([], seededRegistry());
    await waitFor(() => expect(document.querySelector("[data-link='s-1']")).not.toBeNull());
    h.answerWith(200, { channel: "kanban-release" });
    await act(async () => {
      document.querySelector<HTMLButtonElement>("[data-link-another]")?.click();
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
