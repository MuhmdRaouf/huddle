// AccountMenu.test.tsx — the avatar that opens the account menu: the identity lines, the one
// Settings row, the scrim that closes it, and the health dot on the avatar.
import { act, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { AccountMenu } from "../../src/app/AccountMenu.tsx";
import { DESKTOP_QUERY, TABLET_QUERY } from "../../src/app/layout.ts";
import { flush, makeCtx, makeState, renderIn, stubMatchMedia, stubMedia } from "../helpers.tsx";

beforeEach(() => {
  stubMatchMedia(false);
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve({ version: "0.4.0", machine: "mbp" }) })),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Renders the avatar for a credential, with the hub's health as the shell reads it. */
function mount(who: { kind: string; name?: string }, healthy = true): void {
  renderIn(<AccountMenu healthy={healthy} />, makeCtx(makeState(), { who }));
}

/** Taps the avatar and lets the hub's /health answer land. */
async function openMenu(): Promise<void> {
  void act(() => {
    screen.getByRole("button", { name: "Account menu" }).click();
  });
  await flush();
}

describe("the account menu", () => {
  it("the avatar opens a menu with the identity and Settings; the scrim closes it", async () => {
    mount({ kind: "owner", name: "Raouf" });
    expect(screen.queryByRole("dialog")).toBeNull();
    await openMenu();
    const menu = screen.getByRole("dialog", { name: "Account menu" });
    expect(menu.textContent).toContain("Raouf · owner");
    expect(menu.textContent).toContain(`mbp · ${location.host} · 0.4.0`);
    const links = [...menu.querySelectorAll("a")] as HTMLAnchorElement[];
    expect(links.map((a) => [a.textContent, a.getAttribute("href")])).toEqual([["Settings", "#/settings"]]);
    void act(() => {
      (document.querySelector("[data-scrim]") as HTMLElement).click();
    });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("reads the credential alone when the hub names no one else", async () => {
    mount({ kind: "owner", name: "owner" });
    await openMenu();
    const menu = screen.getByRole("dialog", { name: "Account menu" });
    expect(menu.textContent).toContain("owner");
    expect(menu.textContent).not.toContain("owner · owner");
  });

  it("Escape and the Settings row close it", async () => {
    mount({ kind: "owner", name: "Raouf" });
    await openMenu();
    void act(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    await openMenu();
    void act(() => {
      screen.getByRole("link", { name: "Settings" }).click();
    });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("puts the health dot on the avatar while a part of the hub is failing", () => {
    mount({ kind: "owner" }, false);
    const avatar = screen.getByRole("button", { name: "Account menu" });
    expect(avatar.querySelector("[data-health-dot]")).not.toBeNull();
  });

  it("draws no dot while every part is well", () => {
    mount({ kind: "owner" });
    expect(document.querySelector("[data-health-dot]")).toBeNull();
  });

  it("hangs 8 px under the avatar on a phone and 14 px under it beside the top bar", async () => {
    const top = (): string => (screen.getByRole("dialog", { name: "Account menu" }) as HTMLElement).style.top;
    stubMedia({ [TABLET_QUERY]: false, [DESKTOP_QUERY]: false });
    const phone = renderIn(<AccountMenu healthy />, makeCtx(makeState(), { who: { kind: "owner" } }));
    await openMenu();
    expect(top()).toBe("8px");
    phone.unmount();
    stubMedia({ [TABLET_QUERY]: true, [DESKTOP_QUERY]: false });
    renderIn(<AccountMenu healthy />, makeCtx(makeState(), { who: { kind: "owner" } }));
    await openMenu();
    expect(top()).toBe("14px");
  });

  it("starts the divider at the text column, past the avatar", async () => {
    mount({ kind: "owner", name: "Raouf" });
    await openMenu();
    const rule = screen
      .getByRole("link", { name: "Settings" })
      .querySelector("span[aria-hidden]") as HTMLElement;
    expect(rule.className).toContain("left-12");
  });
});
