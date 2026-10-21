// ChannelRemove.test.tsx — the Remove channel action on the channel cards: the creator's browser
// alone sees it, the confirmation names the channel and its kept history, a success drops the
// card, a refusal says the server's error in the confirmation.
import { act, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiOptions } from "../../src/api.ts";
import { ChannelCard, Home, type ChannelRow } from "../../src/pages/Home.tsx";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

beforeEach(() => {
  location.hash = "";
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const ROWS: ChannelRow[] = [
  { name: "old", title: "Old", needs: 0 },
  { name: "live", title: "Live", needs: 1 },
];

type Call = { path: string; method: string | undefined; body: unknown };

/** An api fake that records every call; deleteRoute answers DELETE /api/channels/<name>. */
function trackingApi(
  routes: Record<string, unknown>,
  deleteRoute?: (path: string) => unknown,
): Api & { calls: Call[] } {
  const calls: Call[] = [];
  const fake: Api = {
    api: (path: string, o?: ApiOptions) => {
      calls.push({ path, method: o?.method, body: o?.body });
      if (o?.method === "DELETE") {
        const r = deleteRoute?.(path) ?? {};
        return r instanceof Error ? Promise.reject(r) : Promise.resolve(r);
      }
      const r = routes[path];
      return r instanceof Error ? Promise.reject(r) : Promise.resolve(r);
    },
    op: () => Promise.resolve({}),
    channelPath: (ch: string, p: string) => `/api/c/${ch}${p}`,
    channelHref: (ch: string, p: string) => `#/channels/${ch}${p}`,
  };
  return Object.assign(fake, { calls });
}

/** Renders the page over an api, as the creator (kind owner) or a member. */
function openAs(api: Api, kind: string): ReturnType<typeof renderIn> {
  return renderIn(<Home />, makeCtx(makeState({ ch: null }), { api, who: { kind } }));
}

/** The Remove channel dialog, beside the New channel one the page also carries. */
function removeDialog(): HTMLDialogElement {
  const dlg = [...document.querySelectorAll("dialog")] as HTMLDialogElement[];
  const found = dlg.find((d) => d.querySelector("[data-confirm-remove]"));
  if (!found) throw new Error("no remove dialog");
  return found;
}

describe("the Remove channel action", () => {
  it("shows the action to the creator only", async () => {
    const api = trackingApi({ "/api/channels": ROWS });
    const view = openAs(api, "owner");
    await flush();
    expect(document.querySelectorAll("[data-remove]")).toHaveLength(2);
    view.unmount();
    const member = openAs(trackingApi({ "/api/channels": ROWS }), "member");
    await flush();
    expect(document.querySelectorAll("[data-remove]")).toHaveLength(0);
    member.unmount();
  });

  it("the confirmation names the channel and says the history is kept", async () => {
    const api = trackingApi({ "/api/channels": ROWS });
    openAs(api, "owner");
    await flush();
    void act(() => {
      (document.querySelector('[data-remove="old"]') as HTMLButtonElement).click();
    });
    await flush();
    const dlg = removeDialog();
    expect(dlg.open).toBe(true);
    expect(dlg.textContent).toContain("old");
    expect(dlg.textContent).toContain("history is kept");
    void act(() => {
      const cancel = [...dlg.querySelectorAll("button")].find((b) => b.textContent === "Cancel");
      cancel?.click();
    });
    await flush();
    expect(dlg.isConnected).toBe(false); // the confirmation is gone from the page
    expect(api.calls.filter((c) => c.method === "DELETE")).toHaveLength(0);
  });

  it("confirming calls DELETE /api/channels/<name> and the card leaves", async () => {
    let channels: unknown = ROWS;
    const api = trackingApi(
      {
        get "/api/channels"(): unknown {
          return channels;
        },
      },
      () => {
        channels = (channels as ChannelRow[]).filter((c) => c.name !== "old");
        return {};
      },
    );
    openAs(api, "owner");
    await flush();
    void act(() => {
      (document.querySelector('[data-remove="old"]') as HTMLButtonElement).click();
    });
    await flush();
    const dlg = removeDialog();
    void act(() => {
      (dlg.querySelector("[data-confirm-remove]") as HTMLButtonElement).click();
    });
    await flush();
    await flush(); // the DELETE lands, then the list reads again
    expect(api.calls.some((c) => c.method === "DELETE" && c.path === "/api/channels/old")).toBe(true);
    expect(dlg.isConnected).toBe(false); // the card and its confirmation are gone
    expect(document.querySelectorAll("[data-ch]")).toHaveLength(1);
    expect(document.querySelector('[data-ch="old"]')).toBeNull();
  });

  it("a 409 keeps the confirmation open with the server's error", async () => {
    const api = trackingApi({ "/api/channels": ROWS }, () =>
      Object.assign(new Error("old has 1 session(s) online"), { status: 409 }),
    );
    openAs(api, "owner");
    await flush();
    void act(() => {
      (document.querySelector('[data-remove="old"]') as HTMLButtonElement).click();
    });
    await flush();
    const dlg = removeDialog();
    void act(() => {
      (dlg.querySelector("[data-confirm-remove]") as HTMLButtonElement).click();
    });
    await flush();
    expect(dlg.open).toBe(true);
    expect(dlg.textContent).toContain("old has 1 session(s) online");
  });
});

describe("the card's remove button", () => {
  // Fresh rows: homeData sorts the shared ROWS array in place, so its indexes are not stable.
  const rmRow: ChannelRow = { name: "old", title: "Old", needs: 0 };
  const bareRow: ChannelRow = { name: "live", title: "Live", needs: 1 };

  it("is an icon-only button whose accessible name names the channel, with a tooltip", () => {
    renderIn(<ChannelCard c={rmRow} onRemove={() => {}} />);
    const btn = screen.getByRole("button", { name: "Remove channel old" });
    expect(btn.getAttribute("title")).toBe("Remove channel");
    expect(btn.textContent.trim()).toBe(""); // the trash icon only, no visible text to overlap a cell
  });

  it("gives the footer room for the button only when the card has one", () => {
    renderIn(
      <div>
        <ChannelCard c={rmRow} onRemove={() => {}} />
        <ChannelCard c={bareRow} />
      </div>,
    );
    const foot = (screen.getAllByTitle("Last event")[0] as HTMLElement).parentElement as HTMLElement;
    expect(foot.classList.contains("pr-10")).toBe(true);
    const bare = (screen.getAllByTitle("Last event")[1] as HTMLElement).parentElement as HTMLElement;
    expect(bare.classList.contains("pr-10")).toBe(false);
    expect(bare.querySelector("[data-remove]")).toBeNull();
  });

  it("fills its grid row so the absolute button sits inside the panel", () => {
    // The grid stretches the wrapper div; the panel must stretch with it, or the
    // bottom-anchored button on a shorter card lands below the panel's border.
    renderIn(<ChannelCard c={rmRow} onRemove={() => {}} />);
    const panel = document.querySelector('a[data-ch="old"]');
    expect(panel?.classList.contains("h-full")).toBe(true);
  });
});
