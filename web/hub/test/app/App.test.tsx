// App.test.tsx — the shell: the three layouts' chrome (the phone bottom nav, the tablet icon
// rail, the desktop top bar with the jump field and the machines line), the five tabs' placeholder
// panes, today's pages reachable under Channels, the routes with their redirects, the shell's
// keys, the no-channel page, and the first-run checklist the Now list carries.
import { act, cleanup, fireEvent, screen } from "@testing-library/preact";
import type { ComponentType } from "preact";
import { useEffect } from "preact/hooks";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { App, inboxOf, NoChannel } from "../../src/app/App.tsx";
import { HuddleProvider } from "../../src/app/context.tsx";
import { IntroActions } from "../../src/app/intro.tsx";
import { DESKTOP_QUERY, TABLET_QUERY } from "../../src/app/layout.ts";
import { parseHash } from "../../src/app/router.ts";
import type { ComposeOptions } from "../../src/compose/drafts.ts";
import { ensureRadar, stopRadar } from "../../src/radar/mount.tsx";
import type { RegistrySession } from "../../src/registry.ts";
import type { HuddleState } from "../../src/store.ts";
import { radarIo } from "../radar/ui/io.ts";
import {
  attention,
  flush,
  legacyClasses,
  makeCtx,
  makeRegistry,
  makeState,
  renderIn,
  sess,
  stubMedia,
} from "../helpers.tsx";

/** The three widths the layouts are cut at, as the two breakpoint queries answer them. */
const PHONE: Record<string, boolean> = { [TABLET_QUERY]: false, [DESKTOP_QUERY]: false };
const TABLET: Record<string, boolean> = { [TABLET_QUERY]: true, [DESKTOP_QUERY]: false };
const DESKTOP: Record<string, boolean> = { [TABLET_QUERY]: true, [DESKTOP_QUERY]: true };

beforeEach(() => {
  localStorage.clear();
  location.hash = "";
  stubMedia(PHONE);
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve({ version: "1.2.3" }) })),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  stopRadar(); // a test that booted the radar controller leaves no timers behind
});

/** Mounts the shell over a state. */
function mountApp(
  pages: Record<string, ComponentType> = {},
  state: HuddleState = makeState(),
  onCompose?: (prefill?: ComposeOptions) => void,
) {
  return renderIn(<App pages={pages} onCompose={onCompose} />, makeCtx(state));
}

/** Moves the address bar the way a click would. */
const nav = (hash: string): void => {
  void act(() => {
    location.hash = hash;
    window.dispatchEvent(new Event("hashchange"));
  });
};

/** One keystroke at the window, the way the key layer hears it. */
const press = (key: string, init: KeyboardEventInit = {}): void => {
  void act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key, ...init }));
  });
};

/** The legacy classes one element and everything under it paint with: the element's own, read
 *  from a childless copy, and its descendants'. Pane bodies another page owns are out of scope. */
const paints = (el: Element, skip?: string): string[] => {
  const one = document.createElement("div");
  one.append(el.cloneNode(false));
  return [...legacyClasses(one), ...legacyClasses(el, skip)];
};

/** The tab links of one chrome piece: the bottom bar, the icon rail or the sidebar. */
const linksOf = (nav: string): HTMLAnchorElement[] =>
  [...document.querySelectorAll(`[data-nav="${nav}"] a`)] as HTMLAnchorElement[];

describe("the three layouts", () => {
  it("under 640 px: a header with the tab's title, the pane, and the five tabs in a pinned bar", () => {
    stubMedia(PHONE);
    nav("#/now");
    mountApp();
    expect(document.querySelector("h1")?.textContent).toBe("Now");
    expect(screen.getByLabelText("Machines")).toBeDefined();
    expect(linksOf("bottom").map((a) => a.dataset.tab)).toEqual([
      "now",
      "channels",
      "radar",
      "kanban",
      "settings",
    ]);
    expect(linksOf("bottom").map((a) => a.textContent)).toEqual([
      "Now",
      "Channels",
      "Radar",
      "Kanban",
      "Settings",
    ]);
    expect(linksOf("bottom").map((a) => a.getAttribute("aria-current"))).toEqual([
      "page",
      null,
      null,
      null,
      null,
    ]);
    // 52 px targets in the bar pinned over the safe-area inset, the frame padded by its height
    for (const a of linksOf("bottom")) expect(a.className).toContain("min-h-[52px]");
    expect((document.querySelector("[data-nav='bottom']") as HTMLElement).className).toContain("ui-tabbar");
    expect(document.querySelector(".ui-shell.ui-shell-phone main.ui-shell-main")).not.toBeNull();
    expect(linksOf("rail")).toEqual([]);
    expect(linksOf("side")).toEqual([]);
  });

  it("from 640 px: an icon rail with the five tabs as labelled icons, the pane beside it", () => {
    stubMedia(TABLET);
    nav("#/radar/costs");
    mountApp();
    expect(linksOf("side")).toEqual([]);
    expect(linksOf("bottom")).toEqual([]);
    const rail = linksOf("rail");
    expect(rail.map((a) => a.getAttribute("aria-label"))).toEqual([
      "Now",
      "Channels",
      "Radar",
      "Kanban",
      "Settings",
    ]);
    expect(rail.map((a) => a.getAttribute("aria-current"))).toEqual([null, null, "page", null, null]);
    for (const a of rail) expect(a.className).toContain("size-[52px]");
  });

  it("from 1024 px: a sidebar with the five tabs, a top bar with the jump field and the machines line", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    mountApp();
    expect(linksOf("bottom")).toEqual([]);
    expect(linksOf("rail")).toEqual([]);
    const side = linksOf("side");
    expect(side.map((a) => a.dataset.tab)).toEqual(["now", "channels", "radar", "kanban", "settings"]);
    expect(side.map((a) => a.textContent)).toEqual(["Now", "Channels", "Radar", "Kanban", "Settings"]);
    expect(side.map((a) => a.getAttribute("aria-current"))).toEqual(["page", null, null, null, null]);
    expect(screen.getByRole("button", { name: "Jump to a session" })).toBeDefined();
    expect(document.querySelector("#jump kbd")).not.toBeNull();
    expect(screen.getByLabelText("Machines")).toBeDefined();
  });

  it("follows a viewport that crosses a breakpoint", () => {
    const media = stubMedia(PHONE);
    nav("#/now");
    mountApp();
    expect(linksOf("bottom")).toHaveLength(5);
    void act(() => {
      media.fire(TABLET_QUERY);
    });
    expect(linksOf("bottom")).toEqual([]);
    expect(linksOf("rail")).toHaveLength(5);
    void act(() => {
      media.fire(DESKTOP_QUERY);
    });
    expect(linksOf("rail")).toEqual([]);
    expect(linksOf("side")).toHaveLength(5);
  });

  it("carries the needs-you count on the Now tab in every layout", () => {
    const needsYou = makeState({
      attention: attention({
        asks: [
          { seq: 1, from: "api" },
          { seq: 2, from: "web" },
        ],
      }),
    });
    for (const [layout, name] of [
      [PHONE, "bottom"],
      [TABLET, "rail"],
      [DESKTOP, "side"],
    ] as const) {
      stubMedia(layout);
      nav("#/now");
      mountApp({}, needsYou);
      const now = document.querySelector(`[data-nav="${name}"] a[data-tab="now"]`) as HTMLElement;
      expect(now.querySelector(".ui-badge-needs-you")?.textContent, name).toBe("2");
    }
  });

  it("paints the Now count orange when every session waits on a finished turn", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    const row: RegistrySession = {
      session_id: "s-1",
      state: "needs-you",
      last_event: "Stop",
      last_event_at: "2026-10-08T11:00:00Z",
    };
    const turn = {
      id: 1,
      session: "s-1",
      kind: "turn",
      summary: "a finished turn",
      created_at: "2026-10-08T11:00:00Z",
      state: "open",
    };
    renderIn(
      <App pages={{}} />,
      makeCtx(makeState(), { registry: makeRegistry({ sessions: [row], needs: [turn] }) }),
    );
    const now = document.querySelector("[data-nav='side'] a[data-tab='now']") as HTMLElement;
    expect(now.querySelector(".ui-badge-turn-done")?.textContent).toBe("1");
    expect(now.querySelector(".ui-badge-needs-you")).toBeNull();
  });

  it("draws no needs-you badge when nothing needs the owner", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    mountApp();
    expect(document.querySelector("[data-nav='side'] .ui-badge")).toBeNull();
  });

  it("paints the chrome of every layout with no daisyUI or legacy class", () => {
    const chrome = "[data-nav], [data-sidebar], [data-desktop-bar], .ui-shell > header, [data-radar-views]";
    for (const [layout, hash] of [
      [PHONE, "#/now"],
      [PHONE, "#/radar/overview"],
      [TABLET, "#/radar/costs"],
      [DESKTOP, "#/radar/overview"],
      [DESKTOP, "#/now"],
    ] as const) {
      stubMedia(layout);
      nav(hash);
      const { unmount } = mountApp(
        {},
        makeState({ attention: attention({ asks: [{ seq: 1, from: "api" }] }) }),
      );
      expect(paints(document.querySelector(".ui-shell") as HTMLElement, ".ui-shell *"), hash).toEqual([]);
      expect(document.querySelectorAll(chrome).length, hash).toBeGreaterThan(1);
      for (const el of document.querySelectorAll(chrome)) expect(paints(el), hash).toEqual([]);
      unmount();
    }
  });

  it("paints the placeholder pane from the tokens", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    const row: RegistrySession = {
      session_id: "s-1",
      state: "working",
      last_event: "UserPromptSubmit",
      last_event_at: "2026-10-08T11:00:00Z",
    };
    renderIn(<App pages={{}} />, makeCtx(makeState(), { registry: makeRegistry({ sessions: [row] }) }));
    expect(paints(screen.getByText("No session open").parentElement as HTMLElement)).toEqual([]);
  });
});

describe("routes and panes", () => {
  it("renders Now's empty state, and the session screen when the address names one", () => {
    nav("#/now");
    mountApp();
    expect(screen.getByText("No sessions yet")).toBeDefined();
    nav("#/now/s-1");
    expect(screen.getByText("Reading the session…")).toBeDefined();
  });

  it("carries the empty registry's welcome in the main pane, once, and keeps the dashed box once sessions stand", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    // nothing registered: the welcome and its start-a-session card stand in the main pane, and
    // the list beside them drops its copy of the card
    mountApp();
    expect(screen.getByText("No sessions yet")).toBeDefined();
    expect(screen.getAllByRole("tab", { name: "Claude Code" })).toHaveLength(1);
    expect(document.querySelector("#main [data-no-sessions]")).not.toBeNull();
    expect(screen.queryByText("No session open")).toBeNull();
    // sessions stand and none is picked: the main pane keeps its pointer at the list
    cleanup();
    const row: RegistrySession = {
      session_id: "s-1",
      state: "working",
      last_event: "UserPromptSubmit",
      last_event_at: "2026-10-08T11:00:00Z",
    };
    renderIn(<App pages={{}} />, makeCtx(makeState(), { registry: makeRegistry({ sessions: [row] }) }));
    expect(screen.getByText("No session open")).toBeDefined();
    expect(screen.queryByText("No sessions yet")).toBeNull();
  });

  it("renders the channels page when no channel is named", () => {
    nav("#/channels");
    mountApp({ home: () => <div>channels page</div> }, makeState({ ch: null }));
    expect(screen.getByText("channels page")).toBeDefined();
    expect(document.querySelector("#side")).toBeNull();
    expect(document.querySelector("[data-view-tabs]")).toBeNull();
  });

  it("names the channels list once on the phone and keeps the intro's description", () => {
    nav("#/channels");
    mountApp({ home: () => <div>channels page</div> });
    expect(screen.getAllByRole("heading", { name: "Channels" })).toHaveLength(1);
    expect(
      screen.getByText("Each channel is one team of sessions. The ones that need you come first."),
    ).toBeDefined();
  });

  it("keeps the channels intro's own heading on desktop", () => {
    stubMedia(DESKTOP);
    nav("#/channels");
    mountApp({ home: () => <div>channels page</div> });
    expect(screen.getAllByRole("heading", { name: "Channels" })).toHaveLength(1);
    expect(document.querySelector("[data-page-intro] h1")?.textContent).toBe("Channels");
  });

  it("renders Radar's views as sub-tabs with the named one current", () => {
    nav("#/radar/costs");
    mountApp();
    const views = [...document.querySelectorAll('[aria-label="Radar views"] a')] as HTMLAnchorElement[];
    expect(views.map((a) => a.dataset.view)).toEqual([
      "overview",
      "costs",
      "models",
      "usage",
      "blocks",
      "requests",
      "tools",
      "agents",
      "routers",
      "alerts",
      "timeline",
    ]);
    expect(views.map((a) => a.getAttribute("aria-current"))).toEqual([
      null,
      "page",
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
      null,
    ]);
    expect(views[0]?.getAttribute("href")).toBe("#/radar/overview");
    // the phone carries the range in its header; the export button is the desktop's alone
    expect(document.querySelector('[data-radar-range] [data-range="7d"]')).not.toBeNull();
    expect(document.querySelector("[data-radar-export]")).toBeNull();
    // no controller runs in tests, so the pane itself stays empty until the entry starts one
    expect(document.querySelector("[data-radar-view]")).toBeNull();
    nav("#/radar/overview");
    expect(
      document
        .querySelector('[aria-label="Radar views"] a[data-view="overview"]')
        ?.getAttribute("aria-current"),
    ).toBe("page");
  });

  it("offers Hour, Day, Week and Month on the range, Hour pressed for the default range, then the one picked", () => {
    stubMedia(DESKTOP);
    const controller = ensureRadar(radarIo().io);
    nav("#/radar/overview");
    mountApp();
    const pressed = (): string[] =>
      ([...document.querySelectorAll("[data-radar-range] button")] as HTMLButtonElement[]).map(
        (button) => button.getAttribute("aria-pressed") ?? "",
      );
    const named = (): string[] =>
      ([...document.querySelectorAll("[data-radar-range] button")] as HTMLButtonElement[]).map(
        (button) => button.textContent ?? "",
      );
    expect(named()).toEqual(["Hour", "Day", "Week", "Month"]);
    // the default range is the last hour, and the control says so on first load
    expect(pressed()).toEqual(["true", "false", "false", "false"]);
    fireEvent.click(screen.getByRole("button", { name: "Day" }));
    expect(pressed()).toEqual(["false", "true", "false", "false"]);
    expect(controller.getState().range.preset).toBe("24h");
  });

  it("renders Settings' section row with Appearance current, an unknown address included", () => {
    nav("#/settings");
    mountApp();
    const sections = [
      ...document.querySelectorAll('[aria-label="Settings sections"] a'),
    ] as HTMLAnchorElement[];
    expect(sections.map((a) => a.dataset.section)).toEqual([
      "appearance",
      "notifications",
      "devices",
      "hub",
      "phone",
      "signin",
      "radar",
      "channels",
    ]);
    expect(sections[0]?.getAttribute("aria-current")).toBe("page");
    nav("#/settings/radar");
    mountApp();
    expect(
      document
        .querySelector('[aria-label="Settings sections"] a[data-section="radar"]')
        ?.getAttribute("aria-current"),
    ).toBe("page");
  });

  it("renders the notifications card under Settings › Notifications", () => {
    nav("#/settings/notifications");
    mountApp();
    expect(screen.getByRole("radiogroup", { name: "Notifications" })).toBeDefined();
  });

  it("sends an unknown address to Now with a rewrite", () => {
    nav("#/nope");
    mountApp();
    expect(location.hash).toBe("#/now");
    expect(screen.getByText("No sessions yet")).toBeDefined();
  });

  it("rewrites the old home into the channels list", () => {
    nav("#/");
    mountApp();
    expect(location.hash).toBe("#/channels");
  });

  it("leaves an address that moved on before the rewrite ran where it moved to", () => {
    // a page's own mount effect stands for a move that lands between the shell's render and its
    // rewrite (a click or a goto the instant the first-run gate closes): the rewrite must not
    // pull the address back to the canonical form of the route it replaced
    nav("#/");
    mountApp({
      home: () => {
        useEffect(() => {
          location.hash = "#/radar/requests";
        }, []);
        return <div>home page</div>;
      },
    });
    expect(location.hash).toBe("#/radar/requests");
  });

  it("rewrites an old channel address into its channels one and keeps the page working", () => {
    nav("#/c/ch/inbox");
    mountApp({ inbox: () => <div>inbox page</div> });
    expect(location.hash).toBe("#/channels/ch/inbox");
    expect(screen.getByText("inbox page")).toBeDefined();
  });

  it("keeps every page of today's channel reachable under Channels", () => {
    const pages: Record<string, ComponentType> = {
      overview: () => <div>overview page</div>,
      today: () => <div>today page</div>,
      inbox: () => <div>inbox page</div>,
      team: () => <div>team page</div>,
      work: () => <div>work page</div>,
      knowledge: () => <div>knowledge page</div>,
      settings: () => <div>the settings page</div>,
    };
    const labels: Record<string, string> = { settings: "the settings page" };
    for (const dest of Object.keys(pages)) {
      nav(`#/channels/ch/${dest}`);
      const mounted = mountApp(pages);
      expect(screen.getByText(labels[dest] ?? `${dest} page`), dest).toBeDefined();
      mounted.unmount(); // the next page mounts fresh, so no two shells hear one hashchange
    }
    // the sub paths the work views and knowledge use still parse
    expect(parseHash("#/channels/ch/work/repo/code", (_k, fb) => fb)).toMatchObject({
      dest: "work",
      sub: ["repo", "code"],
    });
    expect(parseHash("#/channels/ch/knowledge/12", (_k, fb) => fb)).toMatchObject({
      dest: "knowledge",
      sub: ["12"],
    });
  });

  it("falls back to Coming soon for a page nobody registered", () => {
    nav("#/channels/ch/overview");
    mountApp({ inbox: () => <div>inbox page</div> });
    expect(screen.getByText("Coming soon.")).toBeDefined();
    expect(document.querySelector("#main")?.textContent).toContain("Overview");
  });
});

describe("the shell's keys", () => {
  it("switches tabs with the g chords", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    mountApp();
    press("g");
    press("c");
    expect(location.hash).toBe("#/channels");
    press("g");
    press("r");
    expect(location.hash).toBe("#/radar/overview");
    press("g");
    press("b");
    expect(location.hash).toBe("#/kanban");
    press("g");
    press("s");
    expect(location.hash).toBe("#/settings");
    press("g");
    press("n");
    expect(location.hash).toBe("#/now");
  });

  it("leaves g k to the palette's Knowledge chord: the shell opens no Kanban with it", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    mountApp();
    press("g");
    press("k");
    // the shell takes no k chord; Knowledge's navigation is the palette layer's goChord,
    // proven with a channel open in Palette.test's goChord test
    expect(location.hash).not.toBe("#/kanban");
    expect(location.hash).toBe("#/now");
  });

  it("focuses the jump field with ⌘K, Ctrl-K and /", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    mountApp();
    press("k", { metaKey: true });
    expect(document.activeElement?.id).toBe("jump");
    press("k", { ctrlKey: true });
    expect(document.activeElement?.id).toBe("jump");
    press("/");
    expect(document.activeElement?.id).toBe("jump");
  });

  it("opens the palette from ⌘K where no jump field is drawn", () => {
    const onPalette = vi.fn();
    nav("#/now");
    renderIn(<App pages={{}} onPalette={onPalette} />, makeCtx(makeState()));
    press("k", { metaKey: true });
    expect(onPalette).toHaveBeenCalledTimes(1);
    press("/");
    expect(onPalette).toHaveBeenCalledTimes(2);
  });

  it("keeps the chords and / out of text fields and dialogs", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    const { container } = mountApp();
    const input = document.createElement("input");
    container.append(input);
    void act(() => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "g", bubbles: true }));
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "n", bubbles: true }));
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true }));
    });
    expect(location.hash).toBe("#/now");
    expect(document.activeElement?.id).not.toBe("jump");
    input.remove();
    const dlg = document.createElement("dialog");
    dlg.open = true;
    document.body.append(dlg);
    press("g");
    press("n");
    expect(location.hash).toBe("#/now");
    dlg.remove();
  });

  it("spends the chord on a key that is no tab and forgets it", () => {
    stubMedia(DESKTOP);
    nav("#/now");
    mountApp();
    press("g");
    press("x");
    press("n"); // free again: no chord pending, plain n types nothing and goes nowhere
    expect(location.hash).toBe("#/now");
  });
});

describe("under Channels", () => {
  it("lays the channel's pages out in three groups of destination tabs", () => {
    stubMedia(DESKTOP);
    nav("#/channels/ch/inbox");
    mountApp();
    const groups = [...document.querySelectorAll("[data-view-tabs] [role='tablist']")];
    expect(groups.map((g) => g.getAttribute("aria-label"))).toEqual(["Channel", "Work", "System"]);
    const tabs = [...document.querySelectorAll("[data-view-tabs] [role='tab']")];
    expect(tabs.map((t) => t.getAttribute("data-dest"))).toEqual([
      "overview",
      "today",
      "inbox",
      "team",
      "work",
      "knowledge",
      "settings",
    ]);
    expect(document.querySelector("[data-view-tabs] [aria-current='page']")?.getAttribute("data-dest")).toBe(
      "inbox",
    );
  });

  it("navigates the destination tabs by hash", () => {
    stubMedia(DESKTOP);
    nav("#/channels/ch/inbox");
    mountApp({ team: () => <div>team page</div> });
    void act(() => {
      (document.querySelector("[data-dest='team']") as HTMLButtonElement).click();
      window.dispatchEvent(new Event("hashchange"));
    });
    expect(location.hash).toBe("#/channels/ch/team");
    expect(screen.getByText("team page")).toBeDefined();
  });

  it("counts what follows each tab and names the channel in the title", () => {
    stubMedia(DESKTOP);
    nav("#/channels/ch/overview");
    mountApp(
      {},
      makeState({
        attention: attention({
          asks: [
            { seq: 1, from: "api" },
            { seq: 2, from: "web" },
          ],
        }),
        extras: { ch: "ch", approvals: [{ seq: 9, from: "api" }], obs: null },
        board: {
          steps: [
            { id: "a", status: "todo" },
            { id: "b", status: "doing" },
            { id: "c", status: "done" },
          ],
        },
        sessions: {
          sessions: [
            sess({ name: "api", state: "working" }),
            sess({ name: "web" }),
            sess({ name: "sub", parent: "api" }),
          ],
        },
      }),
    );
    expect(document.querySelector("[data-dest='inbox'] .ui-badge-needs-you")?.textContent).toBe("3");
    expect(document.querySelector("[data-dest='team'] .ui-badge-neutral")?.textContent).toBe("3");
    expect(document.querySelector("[data-dest='work'] .ui-badge-neutral")?.textContent).toBe("2");
    expect(document.title).toBe("(3) Checkout · Huddle");
  });

  it("keeps the inbox count out of another channel's extras", () => {
    stubMedia(DESKTOP);
    nav("#/channels/ch/inbox");
    mountApp(
      {},
      makeState({
        attention: attention({ asks: [{ seq: 1, from: "api" }] }),
        extras: { ch: "other", approvals: [{ seq: 1, from: "api" }], obs: null },
      }),
    );
    expect(document.title).toBe("(1) Checkout · Huddle");
  });

  it("names the channel in the title when the config has no title", () => {
    stubMedia(DESKTOP);
    nav("#/channels/ch/inbox");
    mountApp({}, makeState({ info: null }));
    expect(document.title).toBe("ch · Huddle");
  });

  it("sets the title without a channel part off the channel pages", () => {
    nav("#/now");
    mountApp({}, makeState({ attention: attention({ asks: [{ seq: 1, from: "api" }] }) }));
    expect(document.title).toBe("(1) Huddle");
  });

  it("counts the inbox over the attention snapshot alone", () => {
    const st = makeState({
      attention: attention({
        asks: [{ seq: 1, from: "api" }],
        gates: [{ id: "t9" }],
        paused: [{ name: "api" }],
        blocked: [],
      }),
    });
    expect(inboxOf(st)).toBe(3);
    expect(inboxOf(makeState({ attention: null, extras: null }))).toBe(0);
  });

  it("paints the page's intro and takes its page-wide controls", () => {
    stubMedia(DESKTOP);
    nav("#/channels/ch/work");
    mountApp({
      work: () => (
        <>
          <IntroActions>
            <button type="button" id="wnew">
              New task
            </button>
          </IntroActions>
          <div>work page</div>
        </>
      ),
    });
    const intro = document.querySelector("#main header[data-page-intro]") as HTMLElement;
    expect(intro.textContent).toContain("Work");
    expect(intro.querySelector("#wnew")).not.toBeNull();
    expect(screen.getByText("work page")).toBeDefined();
  });

  it("carries the session rail beside the page on tablet and desktop, not on the phone", () => {
    nav("#/channels/ch/team");
    for (const [layout, side] of [
      [TABLET, true],
      [DESKTOP, true],
      [PHONE, false],
    ] as const) {
      stubMedia(layout);
      const mounted = mountApp({ team: () => <div>team page</div> });
      expect(document.querySelector("#side") !== null, JSON.stringify(layout)).toBe(side);
      expect(screen.getByText("team page")).toBeDefined();
      mounted.unmount();
    }
  });

  it("offers the composer from the C key over the open channel", () => {
    const onCompose = vi.fn();
    nav("#/channels/ch/inbox");
    mountApp({}, makeState(), onCompose);
    press("c");
    expect(onCompose).toHaveBeenCalledTimes(1);
  });

  it("does not open the composer while the keystroke types into a field", () => {
    const onCompose = vi.fn();
    nav("#/channels/ch/inbox");
    const { container } = mountApp({}, makeState(), onCompose);
    const input = document.createElement("input");
    container.append(input);
    void act(() => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "c", bubbles: true }));
    });
    expect(onCompose).not.toHaveBeenCalled();
  });
});

describe("NoChannel", () => {
  it("says the channel is not there and offers the channels page", () => {
    renderIn(<NoChannel ch="ghost" />);
    expect(screen.getByText(/No channel “ghost”/)).toBeDefined();
    expect(screen.getByText("channels page").getAttribute("href")).toBe("#/channels");
    expect(legacyClasses()).toEqual([]);
    expect(screen.getByText("channels page").className).not.toMatch(/\blink\b/);
  });
});

describe("the first-run checklist's passkey row", () => {
  /** Mounts the shell as a browser holding one kind of credential, over a recording api. */
  function mountAs(kind: string, mintAnswer: unknown = { token: "ABCDEFGHijklmnopqrstuvwx" }) {
    const calls: { path: string; body: unknown }[] = [];
    const api = {
      api: (path: string, o?: { body?: unknown }) => {
        calls.push({ path, body: o?.body });
        if (path === "/api/pair/mint") {
          if (mintAnswer instanceof Error) return Promise.reject(mintAnswer);
          return Promise.resolve(mintAnswer);
        }
        return Promise.resolve({});
      },
      op: () => Promise.resolve({}),
      channelPath: (ch: string, p: string) => `/api/c/${ch}${p}`,
      channelHref: (ch: string, p: string) => `#/channels/${ch}${p}`,
    };
    renderIn(<App pages={{}} />, makeCtx(makeState(), { api, who: { kind } }));
    return { calls };
  }

  /** The checklist rides the Now list, so the shell mounts there. */
  const mountOnNow = (kind: string, mintAnswer?: unknown) => {
    location.hash = "#/now";
    return mountAs(kind, mintAnswer);
  };

  it("shows for an owner's link session, with its save and close buttons", () => {
    mountOnNow("owner");
    expect(screen.getByText("Save a passkey")).toBeDefined();
    expect(screen.getByText("Touch ID signs you in next time.")).toBeDefined();
    expect(screen.getByRole("button", { name: "Save passkey" })).toBeDefined();
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeDefined();
  });

  it("hides for a paired device's session and for a member's browser", () => {
    mountOnNow("device");
    expect(screen.queryByRole("button", { name: "Save passkey" })).toBeNull();
    cleanup();
    mountOnNow("member");
    expect(screen.queryByRole("button", { name: "Save passkey" })).toBeNull();
  });

  it("stays hidden after its dismissal, across remounts", () => {
    mountOnNow("owner");
    void act(() => {
      screen.getByRole("button", { name: "Dismiss" }).click();
    });
    expect(screen.queryByRole("button", { name: "Save passkey" })).toBeNull();
    cleanup();
    mountOnNow("owner");
    expect(screen.queryByRole("button", { name: "Save passkey" })).toBeNull();
  });

  it("mints a link and opens the pair screen with its token", async () => {
    const h = mountOnNow("owner");
    await act(async () => {
      screen.getByRole("button", { name: "Save passkey" }).click();
    });
    await flush();
    expect(h.calls).toContainEqual({ path: "/api/pair/mint", body: { action: "mint" } });
    // the pair screen owns the window: the name stage of the ceremony the token opened
    expect(screen.getByLabelText("Device name")).toBeDefined();
  });

  it("says the refusal and keeps the banner when the mint fails", async () => {
    mountOnNow("owner", Object.assign(new Error("only the owner may mint a pairing link"), { status: 403 }));
    await act(async () => {
      screen.getByRole("button", { name: "Save passkey" }).click();
    });
    await flush();
    expect(screen.getByText("only the owner may mint a pairing link")).toBeDefined();
    expect(screen.getByRole("button", { name: "Save passkey" })).toBeDefined();
    expect(screen.queryByLabelText("Device name")).toBeNull();
  });
});

describe("the channel list read", () => {
  // main.tsx hands the shell a fresh loadChannels arrow on every paint, and the list's answer
  // itself repaints: a read keyed on the function's identity fetched /api/channels in a loop
  // (about 500 a second) until the tab ran out of memory
  it("reads once per channel however often the shell repaints with a new loadChannels", () => {
    const load = vi.fn();
    const state = makeState();
    const ctx = makeCtx(state);
    const view = renderIn(<App pages={{}} loadChannels={() => load()} />, ctx);
    for (let i = 0; i < 5; i++) {
      view.rerender(
        <HuddleProvider value={ctx}>
          <App pages={{}} loadChannels={() => load()} />
        </HuddleProvider>,
      );
    }
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("reads again when the open channel changes", () => {
    const load = vi.fn();
    const view = renderIn(<App pages={{}} loadChannels={() => load()} />, makeCtx(makeState({ ch: "a" })));
    view.rerender(
      <HuddleProvider value={makeCtx(makeState({ ch: "b" }))}>
        <App pages={{}} loadChannels={() => load()} />
      </HuddleProvider>,
    );
    expect(load).toHaveBeenCalledTimes(2);
  });
});
