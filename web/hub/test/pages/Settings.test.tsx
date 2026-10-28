// Settings.test.tsx — the channel's settings page: the mock's sections and their rows, the four
// sheets and exactly what each posts, the refusals said inside the sheet, the members' live
// second lines, the filter reset, the exports and the Linked sessions card.
import { act, fireEvent, within } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiOptions } from "../../src/api.ts";
import { HuddleProvider, type HuddleContextValue } from "../../src/app/context.tsx";
import { Settings, startOptions } from "../../src/pages/Settings.tsx";
import type { RegistryState, RegistryStore } from "../../src/registry.ts";
import type { ChannelInfo, HuddleState, HuddleStore } from "../../src/store.ts";
import { flush, makeCtx, makeRegistry, makeState, renderIn, sess } from "../helpers.tsx";

beforeEach(() => {
  localStorage.clear();
  location.hash = "#/channels/ch/settings";
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** The channel row the settings read on open. */
const INFO = {
  name: "ch",
  views: ["code"],
  stats: { events: 11, tasks: 3, knowledge: 4 },
  config: {
    title: "Checkout",
    description: "the next version",
    members: ["api", "web"],
    created_at: "2026-10-01T09:00:00Z",
    handover: { api: { to: "web" } },
  },
};

/** The channel's live sessions on open. */
const ROSTER = { sessions: [sess({ name: "api" }), sess({ name: "sub.api", parent: "api" })] };

/** An api fake that remembers the op calls and serves the channel row. */
function apiFake(
  over: { info?: unknown; opError?: Error } = {},
): Api & { ops: Array<{ ch: string; name: string; args: unknown }> } {
  const ops: Array<{ ch: string; name: string; args: unknown }> = [];
  return {
    ops,
    api: vi.fn((path: string, _o?: ApiOptions) =>
      path.endsWith("/attention")
        ? Promise.resolve({ asks: [], gates: [], paused: [], blocked: [] })
        : Promise.resolve(over.info ?? INFO),
    ),
    op: (ch, name, args) => {
      if (over.opError) return Promise.reject(over.opError);
      ops.push({ ch, name, args });
      return Promise.resolve({});
    },
    channelPath: (ch, p) => `/api/c/${ch}${p}`,
    channelHref: (ch, p) => `#/channels/${ch}${p}`,
  };
}

/** A store fake whose channel-row read the test can watch. */
function storeFake(): { store: HuddleStore; loadInfo: ReturnType<typeof vi.fn> } {
  const loadInfo = vi.fn(async () => {});
  return { store: { loadInfo } as unknown as HuddleStore, loadInfo };
}

/** Renders the page over the state's channel row, an api fake and a store fake. */
function mount(
  over: {
    api?: ReturnType<typeof apiFake>;
    state?: HuddleState;
    registry?: RegistryStore;
    toast?: HuddleContextValue["toast"];
  } = {},
) {
  const api = over.api ?? apiFake();
  const { store, loadInfo } = storeFake();
  const state = over.state ?? makeState({ info: INFO as ChannelInfo, sessions: ROSTER });
  const view = renderIn(
    <Settings store={store} />,
    makeCtx(state, {
      api,
      ...(over.registry ? { registry: over.registry } : {}),
      ...(over.toast ? { toast: over.toast } : {}),
    }),
  );
  return { api, view, store, loadInfo };
}

/** The page's one section, found by its data-section name. */
const section = (name: string): HTMLElement => {
  const el = document.querySelector(`[data-section="${name}"]`);
  if (!el) throw new Error(`no ${name} section on the page`);
  return el as HTMLElement;
};

/** The one open sheet, or null when every sheet is closed. */
const openSheet = (): HTMLDialogElement | null =>
  [...document.querySelectorAll("dialog")].find((d) => d.open) ?? null;

/** The open sheet, failing the test when no sheet is open. */
const sheet = (): HTMLDialogElement => {
  const d = openSheet();
  if (!d) throw new Error("no sheet is open");
  return d;
};

/** Opens a section's sheet through its row, found by accessible name. */
const openRow = async (name: string | RegExp, inSection?: string): Promise<void> => {
  fireEvent.click(within(inSection ? section(inSection) : document.body).getByRole("button", { name }));
  await flush();
};

const sheetForm = (): HTMLFormElement => sheet().querySelector("form") as HTMLFormElement;

/** Submits a sheet's form and waits for the save to land. */
const submit = async (f: HTMLFormElement): Promise<void> => {
  await act(async () => {
    f.requestSubmit();
  });
  await flush();
};

const closeSheet = async (): Promise<void> => {
  fireEvent.click(within(sheet()).getByRole("button", { name: "Close" }));
  await flush();
};

describe("the page", () => {
  it("stands the sections in the mock's order, each row carrying its value from the channel row", async () => {
    mount();
    await flush();
    expect(
      [...document.querySelectorAll("[data-section]")].map((s) => s.getAttribute("data-section")),
    ).toEqual(["about", "members", "turn", "repo", "browser", "export"]);
    const about = within(section("about"));
    expect(about.getByRole("button", { name: "Name Checkout" })).toBeTruthy();
    expect(about.getByRole("button", { name: "Description the next version" })).toBeTruthy();
    const members = within(section("members"));
    expect(members.getByRole("button", { name: "api" })).toBeTruthy();
    expect(members.getByRole("button", { name: "web" })).toBeTruthy();
    const turn = within(section("turn"));
    expect(turn.getByRole("button", { name: "Turn order Free (parallel)" })).toBeTruthy();
    expect(turn.getByRole("button", { name: "Orchestrator None" })).toBeTruthy();
    const repo = within(section("repo"));
    expect(repo.getByRole("button", { name: "Path Not set" })).toBeTruthy();
    expect(repo.getByRole("button", { name: "Profile Not set" })).toBeTruthy();
    expect(within(section("browser")).getByRole("button", { name: "Reset filters" })).toBeTruthy();
    expect(section("browser").textContent).toContain(
      "Clears the filters and views this browser keeps for this channel.",
    );
  });

  it("gives a member's row the branch and machine of its live session", async () => {
    mount({
      registry: makeRegistry({
        sessions: [
          {
            session_id: "r-api",
            last_event: "UserPromptSubmit",
            last_event_at: "2026-10-08T11:59:30Z",
            member: "api",
            channel: "ch",
            branch: "main",
            machine: "mbp",
            state: "working",
          },
          {
            session_id: "r-web",
            last_event: "UserPromptSubmit",
            last_event_at: "2026-10-08T11:59:30Z",
            member: "web",
            channel: "ch",
            machine: "gtk",
            state: "idle",
          },
        ] as RegistryState["sessions"],
      }),
      state: makeState({
        info: INFO as ChannelInfo,
        sessions: { sessions: [sess({ name: "api" }), sess({ name: "web" })] },
      }),
    });
    await flush();
    const members = within(section("members"));
    expect(members.getByRole("button", { name: "api main · mbp" })).toBeTruthy();
    expect(members.getByRole("button", { name: "web gtk" })).toBeTruthy();
  });

  it("gives no second line to a member whose name is not a live session here", async () => {
    mount({
      registry: makeRegistry({
        sessions: [
          {
            session_id: "r-api",
            last_event: "UserPromptSubmit",
            last_event_at: "2026-10-08T11:59:30Z",
            member: "api",
            channel: "ch",
            branch: "main",
            machine: "mbp",
            state: "working",
          },
          {
            session_id: "r-web",
            last_event: "UserPromptSubmit",
            last_event_at: "2026-10-08T11:59:30Z",
            member: "web",
            channel: "ch",
            machine: "ghost",
            state: "working",
          },
        ] as RegistryState["sessions"],
      }),
    });
    await flush();
    const members = within(section("members"));
    expect(members.getByRole("button", { name: "api main · mbp" })).toBeTruthy();
    // web's row exists in the registry, but web is not a live session of this channel here
    expect(members.getByRole("button", { name: "web" })).toBeTruthy();
    expect(members.queryByRole("button", { name: "web ghost" })).toBeNull();
  });

  it("offers any local session when the config names no members", async () => {
    mount({ state: makeState({ info: { ...INFO, config: {} } as ChannelInfo, sessions: ROSTER }) });
    await flush();
    const members = within(section("members"));
    expect(members.getByRole("button", { name: "Any local session" })).toBeTruthy();
    expect(members.queryByRole("button", { name: "api" })).toBeNull();
  });

  it("names the channel row when the config carries no title", async () => {
    mount({ state: makeState({ info: { ...INFO, config: {} } as ChannelInfo, sessions: ROSTER }) });
    await flush();
    const about = within(section("about"));
    expect(about.getByRole("button", { name: "Name ch" })).toBeTruthy();
    expect(about.getByRole("button", { name: "Description None" })).toBeTruthy();
  });

  it("keeps reading the channel id when the store holds no channel row", async () => {
    mount({ state: makeState({ info: null }) });
    await flush();
    const about = within(section("about"));
    expect(about.getByRole("button", { name: "Name ch" })).toBeTruthy();
    expect(about.getByRole("button", { name: "Description None" })).toBeTruthy();
    expect(within(section("members")).getByRole("button", { name: "Any local session" })).toBeTruthy();
  });

  it("follows the live channel row: a store refresh shows in the rows", async () => {
    const { view } = mount();
    await flush();
    // another session saved a new title while this page was open: the store says so
    const live = makeState({
      info: { ...INFO, config: { ...INFO.config, title: "Renamed by another session" } } as ChannelInfo,
      sessions: ROSTER,
    });
    view.rerender(
      <HuddleProvider value={makeCtx(live)}>
        <Settings store={{ loadInfo: vi.fn(async () => {}) } as unknown as HuddleStore} />
      </HuddleProvider>,
    );
    expect(
      within(section("about")).getByRole("button", { name: "Name Renamed by another session" }),
    ).toBeTruthy();
  });

  it("holds the channel's Linked sessions card", async () => {
    mount();
    await flush();
    const card = document.querySelector("[data-linked-sessions]") as HTMLElement;
    expect(card.querySelector(".ui-card-title")?.textContent).toBe("Linked sessions");
  });

  it("renders nothing without an open channel", async () => {
    const { view } = mount({ state: makeState({ ch: null }) });
    await flush();
    expect(view.container.textContent).toBe("");
  });

  it("says the repo help's three states in the Repository sheet", async () => {
    mount();
    await flush();
    await openRow("Path Not set", "repo");
    expect(document.body.textContent).toContain("Available now: code.");
    await closeSheet();
    const v2 = renderIn(
      <Settings store={{ loadInfo: vi.fn(async () => {}) } as unknown as HuddleStore} />,
      makeCtx(
        makeState({
          info: { ...INFO, views: [], config: { ...INFO.config, repo: "/nowhere" } } as ChannelInfo,
        }),
      ),
    );
    await flush();
    fireEvent.click(within(v2.container as HTMLElement).getByRole("button", { name: "Path /nowhere" }));
    await flush();
    expect(v2.container.textContent).toContain("Huddle cannot read this path.");
    const v3 = renderIn(
      <Settings store={{ loadInfo: vi.fn(async () => {}) } as unknown as HuddleStore} />,
      makeCtx(makeState({ info: { ...INFO, views: [], config: { ...INFO.config } } as ChannelInfo })),
    );
    await flush();
    fireEvent.click(within(v3.container as HTMLElement).getByRole("button", { name: "Path Not set" }));
    await flush();
    expect(v3.container.textContent).not.toContain("Huddle cannot read this path.");
  });

  it("opens each sheet with today's fields prefilled from the config", async () => {
    mount();
    await flush();
    await openRow("Name Checkout", "about");
    let body = within(sheet());
    expect(body.getByRole("heading", { name: "About" })).toBeTruthy();
    expect((body.getByLabelText("Title") as HTMLInputElement).value).toBe("Checkout");
    expect((body.getByLabelText("What the team is doing") as HTMLTextAreaElement).value).toBe(
      "the next version",
    );
    await closeSheet();
    await openRow("api", "members");
    body = within(sheet());
    expect(body.getByRole("heading", { name: "Members" })).toBeTruthy();
    expect((body.getByLabelText("Allowed sessions") as HTMLInputElement).value).toBe("api, web");
    await closeSheet();
    await openRow("Turn order Free (parallel)", "turn");
    body = within(sheet());
    expect(body.getByRole("heading", { name: "Turn" })).toBeTruthy();
    expect((body.getByLabelText("Starts with the turn") as HTMLSelectElement).value).toBe("");
    expect((body.getByLabelText("Hand-over rules") as HTMLTextAreaElement).value).toBe(
      '{\n  "api": {\n    "to": "web"\n  }\n}',
    );
    await closeSheet();
    await openRow("Path Not set", "repo");
    body = within(sheet());
    expect(body.getByRole("heading", { name: "Repository" })).toBeTruthy();
    expect((body.getByLabelText("Repo path") as HTMLInputElement).value).toBe("");
    expect((body.getByLabelText("Profile") as HTMLInputElement).value).toBe("");
  });

  it("closes a sheet from its Close button", async () => {
    mount();
    await flush();
    await openRow("Name Checkout", "about");
    expect(sheet()).toBeTruthy();
    await closeSheet();
    expect(openSheet()).toBeNull();
  });

  it("shows the orchestrator field when the config carries the key", async () => {
    mount({
      state: makeState({
        info: { ...INFO, config: { ...INFO.config, orchestrator: "api", start: "api" } } as ChannelInfo,
        sessions: ROSTER,
      }),
    });
    await flush();
    const turn = within(section("turn"));
    expect(turn.getByRole("button", { name: "Turn order api" })).toBeTruthy();
    expect(turn.getByRole("button", { name: "Orchestrator api" })).toBeTruthy();
    await openRow("Turn order api", "turn");
    const body = within(sheet());
    const orch = body.getByLabelText("Orchestrator") as HTMLSelectElement;
    expect([...orch.options].map((o) => o.textContent)).toEqual(["None", "api", "web"]);
    expect(orch.selectedOptions[0]?.textContent).toBe("api");
    expect((body.getByLabelText("Starts with the turn") as HTMLSelectElement).value).toBe("api");
    expect(document.body.textContent).toContain("The orchestrator plans the work");
  });

  it("leaves the orchestrator field out when the config has no such key", async () => {
    mount();
    await flush();
    await openRow("Turn order Free (parallel)", "turn");
    expect(within(sheet()).queryByLabelText("Orchestrator")).toBeNull();
  });
});

describe("saving", () => {
  it("saves About with only the title and description, toasts, reads the row again, and closes", async () => {
    const toast = vi.fn();
    const { api, loadInfo } = mount({ toast });
    await flush();
    await openRow("Name Checkout", "about");
    fireEvent.input(within(sheet()).getByLabelText("Title"), { target: { value: "  Checkout 2  " } });
    fireEvent.input(within(sheet()).getByLabelText("What the team is doing"), {
      target: { value: "v2" },
    });
    const f = sheetForm();
    await submit(f);
    expect(api.ops).toEqual([
      { ch: "ch", name: "configure", args: { title: "Checkout 2", description: "v2" } },
    ]);
    // only a save reads the row again
    expect(loadInfo).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith("Saved");
    expect(openSheet()).toBeNull();
  });

  it("saves Members with only the member names", async () => {
    const { api } = mount();
    await flush();
    await openRow("api", "members");
    fireEvent.input(within(sheet()).getByLabelText("Allowed sessions"), {
      target: { value: "api, web,, docs" },
    });
    const f = sheetForm();
    await submit(f);
    expect(api.ops).toEqual([{ ch: "ch", name: "configure", args: { members: ["api", "web", "docs"] } }]);
    expect(openSheet()).toBeNull();
  });

  it("saves the turn with the start, the parsed hand-over rules and the orchestrator", async () => {
    const { api } = mount({
      state: makeState({
        info: { ...INFO, config: { ...INFO.config, orchestrator: "api" } } as ChannelInfo,
        sessions: ROSTER,
      }),
    });
    await flush();
    await openRow("Turn order Free (parallel)", "turn");
    (within(sheet()).getByLabelText("Starts with the turn") as HTMLSelectElement).value = "api";
    (within(sheet()).getByLabelText("Orchestrator") as HTMLSelectElement).value = "";
    const f = sheetForm();
    await submit(f);
    expect(api.ops).toEqual([
      {
        ch: "ch",
        name: "configure",
        args: { start: "api", handover: { api: { to: "web" } }, orchestrator: null },
      },
    ]);
  });

  it("refuses hand-over rules that are not JSON inside the sheet, without saving", async () => {
    const { api } = mount();
    await flush();
    await openRow("Turn order Free (parallel)", "turn");
    (within(sheet()).getByLabelText("Hand-over rules") as HTMLTextAreaElement).value = "{nope";
    const f = sheetForm();
    await submit(f);
    expect(within(sheet()).getByRole("alert").textContent).toContain(
      "The hand-over rules are not valid JSON:",
    );
    expect(api.ops).toEqual([]);
    expect(openSheet()).not.toBeNull();
  });

  it("saves the repo path and profile trimmed", async () => {
    const { api } = mount();
    await flush();
    await openRow("Path Not set", "repo");
    fireEvent.input(within(sheet()).getByLabelText("Repo path"), { target: { value: "  /srv/repo  " } });
    fireEvent.input(within(sheet()).getByLabelText("Profile"), { target: { value: "  web  " } });
    const f = sheetForm();
    await submit(f);
    expect(api.ops).toEqual([{ ch: "ch", name: "configure", args: { repo: "/srv/repo", profile: "web" } }]);
  });

  it("says the server's refusal in the sheet and keeps it open", async () => {
    const api = apiFake({ opError: new Error("read-only") });
    mount({ api });
    await flush();
    await openRow("Name Checkout", "about");
    const f = sheetForm();
    await submit(f);
    expect(within(sheet()).getByRole("alert").textContent).toBe("read-only");
    expect(openSheet()).not.toBeNull();
  });

  it("drops a refused save that lands after the page is gone", async () => {
    let rejectOp: (e: unknown) => void = () => {};
    const slow = {
      ...apiFake(),
      op: () =>
        new Promise((_resolve, reject) => {
          rejectOp = reject;
        }),
    } as unknown as Api;
    const { view } = mount({ api: slow as ReturnType<typeof apiFake> });
    await flush();
    await openRow("Name Checkout", "about");
    sheetForm().requestSubmit(); // a save is now in flight
    view.unmount(); // the sheet, its form and the error ref are gone
    await act(async () => {
      rejectOp(new Error("too late"));
      await flush();
    });
  });
});

describe("this browser's filters", () => {
  it("resets the per-viewer filters from the Reset filters row and toasts", async () => {
    localStorage.setItem("huddle:wfilter:ch", '"notes"');
    localStorage.setItem("huddle:wview:ch", '"board"');
    localStorage.setItem("huddle:scheme", "dark");
    localStorage.setItem("other:key", "1");
    const toast = vi.fn();
    renderIn(<Settings />, makeCtx(makeState(), { toast }));
    await flush();
    await act(async () => {
      fireEvent.click(within(section("browser")).getByRole("button", { name: "Reset filters" }));
    });
    await flush();
    expect(localStorage.getItem("huddle:wfilter:ch")).toBeNull();
    expect(localStorage.getItem("huddle:wview:ch")).toBeNull();
    expect(localStorage.getItem("huddle:scheme")).toBe("dark");
    expect(localStorage.getItem("other:key")).toBe("1");
    expect(toast).toHaveBeenCalledWith("Filters reset");
  });
});

describe("the exports", () => {
  it("links the three exports as rows over the channel path", async () => {
    mount();
    await flush();
    const rows = [...document.querySelectorAll('a[target="_blank"]')].filter((a) =>
      a.classList.contains("ui-list-row"),
    );
    expect(rows.map((a) => a.getAttribute("href"))).toEqual([
      "/api/c/ch/export.md",
      "/api/c/ch/plan.json",
      "/api/c/ch/timeline?limit=2000",
    ]);
    expect(rows.every((a) => a.getAttribute("rel") === "noopener")).toBe(true);
    expect(rows[0]?.textContent).toContain("Plan");
    expect(rows[0]?.textContent).toContain("Markdown");
    expect(rows[1]?.textContent).toContain("Tasks");
    expect(rows[1]?.textContent).toContain("JSON");
    expect(rows[2]?.textContent).toContain("Timeline");
    expect(rows[2]?.textContent).toContain("last 2,000 events");
  });
});

describe("branch corners", () => {
  it("saves an empty hand-over field as an empty object", async () => {
    const { api } = mount();
    await flush();
    await openRow("Turn order Free (parallel)", "turn");
    (within(sheet()).getByLabelText("Hand-over rules") as HTMLTextAreaElement).value = "";
    const f = sheetForm();
    await submit(f);
    expect(api.ops[0]?.args).toEqual({ start: "", handover: {} });
  });
});

describe("startOptions", () => {
  it("offers nobody first, filters dotted names, and keeps a vanished value", () => {
    expect(startOptions(["api", "a.sub", "web"], "")).toEqual([
      { value: "", label: "Nobody: work in parallel", selected: true },
      { value: "api", label: "api", selected: false },
      { value: "web", label: "web", selected: false },
    ]);
    const kept = startOptions(["api"], "gone");
    expect(kept.map((o) => o.value)).toEqual(["", "api", "gone"]);
    expect(kept.find((o) => o.value === "gone")?.selected).toBe(true);
    const picked = startOptions(["api", "web"], "web");
    expect(picked.find((o) => o.value === "web")?.selected).toBe(true);
    expect(picked.find((o) => o.value === "")?.selected).toBe(false);
  });
});
