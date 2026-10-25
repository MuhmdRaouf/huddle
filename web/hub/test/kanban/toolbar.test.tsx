import { act, render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import { Toolbar } from "../../src/kanban/components/Toolbar.tsx";
import { EMPTY_FILTERS, type Filters } from "../../src/kanban/store.ts";
import type { Card } from "../../src/kanban/types.ts";
import { makeCard } from "./helpers.ts";

const withPicks = (picks: Partial<Filters["picks"]>, over: Partial<Filters> = {}): Filters => ({
  ...EMPTY_FILTERS,
  ...over,
  picks: { ...EMPTY_FILTERS.picks, ...picks },
});

/** The boards the picker lists, as /api/kanban/boards sends them. */
const BOARDS = [
  { key: "a1a1", root: "/repo/alpha", name: "alpha" },
  { key: "b2b2", root: "/repo/beta", name: "beta" },
];

/** The toolbar as the page owns it: the boards' picker, the filters in state. */
function Row({
  cards,
  initial,
  boardKey = "a1a1",
  onBoard = () => undefined,
  over = {},
}: {
  cards: Card[];
  initial: Filters;
  boardKey?: string | null;
  onBoard?: (key: string) => void;
  over?: Partial<Parameters<typeof Toolbar>[0]>;
}) {
  const [filters, setFilters] = useState(initial);
  return (
    <Toolbar
      cards={cards}
      filters={filters}
      onChange={setFilters}
      boards={BOARDS}
      boardKey={boardKey}
      onBoard={onBoard}
      epoch={3}
      connected
      live
      density="compact"
      onDensity={() => undefined}
      {...over}
    />
  );
}

/** Moves the fake clock on, the renders it causes flushed. */
async function pass(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

/** The row's kind buttons, in their standing order. */
const kindButtons = (container: Element): string[] =>
  [...container.querySelectorAll<HTMLButtonElement>(".toolbar button[data-kind]")].map(
    (b) => b.textContent ?? "",
  );

/** The kind's own button; tests name the kind they drive. */
function kindButton(kind: string, container: Element): HTMLButtonElement {
  const button = container.querySelector<HTMLButtonElement>(`button[data-kind="${kind}"]`);
  if (button === null) throw new Error(`no ${kind} button`);
  return button;
}

/** A menu's value entries: the checkboxes only, never the fold or the filter box. */
const optionsOf = (menu: HTMLElement): HTMLButtonElement[] => [
  ...menu.querySelectorAll<HTMLButtonElement>("button[role='menuitemcheckbox']"),
];

/** A board the tests name: four holders split across working, backlog and done columns. */
function board(): Card[] {
  let n = 0;
  const card = (over: Partial<Card>): Card =>
    makeCard({ id: `C-${(n += 1)}`, tags: [], holder: "", cluster: "alpha", ...over });
  return [
    card({ holder: "glm-A", column: "in-progress" }),
    card({ holder: "glm-A", column: "ready" }),
    card({ holder: "glm-B", column: "review", cluster: "beta" }),
    card({ holder: "glm-C", column: "blocked" }),
    card({ holder: "glm-D", column: "done" }),
    card({ column: "backlog", tags: ["kanban", "web"] }),
  ];
}

/** The toolbar as the page owns it: the filters live in state, so a menu click lands. */
function Harness({ cards, initial }: { cards: Card[]; initial: Filters }) {
  const [filters, setFilters] = useState(initial);
  return (
    <Toolbar
      cards={cards}
      filters={filters}
      onChange={setFilters}
      boards={BOARDS}
      boardKey="a1a1"
      onBoard={() => undefined}
      epoch={3}
      connected
      live
      density="compact"
      onDensity={() => undefined}
    />
  );
}

describe("the toolbar row", () => {
  it("is one compact row: the board picker, search, one menu button per kind, the hide-done toggle", () => {
    const { container } = render(<Row cards={board()} initial={EMPTY_FILTERS} />);
    expect(container.querySelectorAll(".chips, .chip")).toHaveLength(0);
    expect(kindButtons(container)).toEqual(["Domain", "Epic", "Holder", "Type", "Priority"]);
    expect(screen.getByRole("searchbox", { name: "Search" })).toBeTruthy();
    const picker = screen.getByRole("button", { name: "Board" });
    expect(picker.textContent).toContain("alpha");
  });

  it("moves the address when the picker picks another board", async () => {
    const onBoard = vi.fn();
    render(<Row cards={board()} initial={EMPTY_FILTERS} onBoard={onBoard} />);
    await userEvent.click(screen.getByRole("button", { name: "Board" }));
    await userEvent.click(screen.getByRole("option", { name: "beta repo/beta" }));
    expect(onBoard).toHaveBeenCalledWith("b2b2");
  });

  it("offers Domain only when some card carries tags", () => {
    const untagged = board().map((card) => ({ ...card, tags: [] }));
    const { container } = render(<Row cards={untagged} initial={EMPTY_FILTERS} />);
    expect(kindButtons(container)).toEqual(["Epic", "Holder", "Type", "Priority"]);
  });

  it("carries the hide-done toggle as a switch with its label beside it", async () => {
    const { container } = render(<Row cards={board()} initial={EMPTY_FILTERS} />);
    const wrap = container.querySelector(".toolbar .hide-done") as HTMLLabelElement;
    expect(wrap.textContent).toContain("Hide done");
    const toggle = wrap.querySelector("input") as HTMLInputElement;
    expect(toggle.type).toBe("checkbox");
    expect(toggle.checked).toBe(false);
    await userEvent.click(toggle);
    expect(toggle.checked).toBe(true);
  });

  it("names how many done cards the switch hides while it is on", async () => {
    const cards = [
      makeCard({ id: "OPEN-1", column: "backlog" }),
      ...Array.from({ length: 59 }, (_, i) => makeCard({ id: `DONE-${i}`, column: "done" })),
    ];
    render(<Row cards={cards} initial={{ ...EMPTY_FILTERS, hideDone: true }} />);
    const toggle = screen.getByRole("checkbox", { name: "Hide done (59)" });
    await userEvent.click(toggle);
    expect(screen.getByRole("checkbox", { name: "Hide done" })).toBeTruthy();
  });

  it("counts the picked values on its kind's button", () => {
    const { container } = render(<Row cards={board()} initial={withPicks({ holder: ["glm-A", "glm-B"] })} />);
    expect(kindButton("holder", container).textContent).toBe("Holder: 2");
    expect(kindButton("type", container).textContent).toBe("Type");
  });
});

describe("Feature: the header row", () => {
  it("Scenario: the live dot carries the epoch and the connection in its title", () => {
    // Given the toolbar of a live board that is connected
    const { container, unmount } = render(<Row cards={board()} initial={EMPTY_FILTERS} />);
    // Then the dot reads live, the epoch named beside it
    expect(container.querySelector(".live-dot")?.getAttribute("title")).toBe("epoch 3, live");
    unmount();
    // Given the same board while the stream is down
    const down = render(<Row cards={board()} initial={EMPTY_FILTERS} over={{ connected: false }} />);
    // Then the dot says it is reconnecting
    expect(down.container.querySelector(".live-dot")?.getAttribute("title")).toBe("epoch 3, reconnecting");
  });

  it("Scenario: the status line says how long ago the last frame came, and ticks", async () => {
    vi.useFakeTimers();
    try {
      // Given a live board whose last frame came just now
      const { container } = render(
        <Row cards={board()} initial={EMPTY_FILTERS} over={{ lastFrameAt: Date.now() }} />,
      );
      // When 12 s pass without another frame
      await pass(12_000);
      // Then the status line beside the dot reads the age
      const line = screen.getByRole("status");
      expect(line.textContent).toBe("Updated 12 s ago");
      expect(container.querySelector(".toolbar .stale-line")).toBe(line);
    } finally {
      vi.useRealTimers();
    }
  });

  it("Scenario: the status line reads Reconnecting once the stream has been down for 10 s", async () => {
    vi.useFakeTimers();
    try {
      // Given a connected live board with a recent frame
      const at = Date.now();
      const view = render(<Row cards={board()} initial={EMPTY_FILTERS} over={{ lastFrameAt: at }} />);
      // When the stream drops, a short blip stays quiet
      view.rerender(
        <Row cards={board()} initial={EMPTY_FILTERS} over={{ lastFrameAt: at, connected: false }} />,
      );
      await pass(9_000);
      expect(screen.getByRole("status").textContent).toBe("Updated 9 s ago");
      // Then 10 s after the drop it says it is reconnecting
      await pass(1_000);
      expect(screen.getByRole("status").textContent).toBe("Reconnecting");
      // And back up it reads the age again
      view.rerender(
        <Row cards={board()} initial={EMPTY_FILTERS} over={{ lastFrameAt: Date.now(), connected: true }} />,
      );
      await pass(1_000);
      expect(screen.getByRole("status").textContent).toBe("Updated 1 s ago");
    } finally {
      vi.useRealTimers();
    }
  });

  it("Scenario: no dot is shown when the board is not live", () => {
    // Given a static board's toolbar
    const { container } = render(<Row cards={board()} initial={EMPTY_FILTERS} over={{ live: false }} />);
    // Then there is no live dot
    expect(container.querySelector(".live-dot")).toBeNull();
  });

  it("Scenario: the density toggle flips and names the density it would show", async () => {
    // Given the toolbar at its default compact density
    const onDensity = vi.fn();
    const { container } = render(
      <Row cards={board()} initial={EMPTY_FILTERS} over={{ density: "compact", onDensity }} />,
    );
    const toggle = container.querySelector<HTMLButtonElement>(".density");
    expect(toggle?.textContent).toContain("Compact");
    expect(toggle?.getAttribute("aria-pressed")).toBe("false");
    // When the toggle is pressed
    await userEvent.click(toggle as HTMLButtonElement);
    // Then the page is asked for the comfortable density
    expect(onDensity).toHaveBeenCalledWith("comfortable");
  });

  it("Scenario: a tag every visible card carries is named once in the header row", () => {
    // Given a board whose every card carries the kanban tag
    const cards = board().map((card) => ({ ...card, tags: ["kanban"] }));
    // When the toolbar renders
    const { container } = render(
      <Row cards={cards} initial={EMPTY_FILTERS} over={{ sharedTags: ["kanban"] }} />,
    );
    // Then the header names the shared tag once
    expect(container.querySelector(".shared-note")?.textContent).toBe("All cards: kanban");
  });

  it("Scenario: a shared cluster is named the same way", () => {
    // Given a board whose every card sits in one cluster
    const { container } = render(
      <Row cards={board()} initial={EMPTY_FILTERS} over={{ sharedCluster: "hr" }} />,
    );
    // Then the header names it once
    expect(container.querySelector(".shared-note")?.textContent).toBe("All cards: hr");
  });

  it("Scenario: nothing is named when the cards share nothing", () => {
    // Given a board with no shared tag or cluster
    const { container } = render(<Row cards={board()} initial={EMPTY_FILTERS} />);
    // Then there is no shared note
    expect(container.querySelector(".shared-note")).toBeNull();
  });
});

describe("a kind's menu", () => {
  it("opens on click, lists the values with counts sorted by count then value, and reads as expanded", async () => {
    const { container } = render(
      <Toolbar
        cards={board()}
        filters={EMPTY_FILTERS}
        onChange={() => undefined}
        boards={BOARDS}
        boardKey="a1a1"
        onBoard={() => undefined}
        epoch={3}
        connected
        live
        density="compact"
        onDensity={() => undefined}
      />,
    );
    expect(kindButton("holder", container).getAttribute("aria-expanded")).toBe("false");
    await userEvent.click(kindButton("holder", container));
    const menu = screen.getByRole("menu", { name: "Holder" });
    expect(kindButton("holder", container).getAttribute("aria-expanded")).toBe("true");
    // glm-D sits in the folded Earlier group, so the open menu lists the working three only
    expect(optionsOf(menu).map((o) => o.textContent)).toEqual(["glm-A 2", "glm-B 1", "glm-C 1"]);
    await userEvent.click(kindButton("holder", container));
    expect(screen.queryByRole("menu", { name: "Holder" })).toBeNull();
    expect(kindButton("holder", container).getAttribute("aria-expanded")).toBe("false");
  });

  it("groups holder values into Working now and a collapsed Earlier, and unfolds on click", async () => {
    render(<Harness cards={board()} initial={EMPTY_FILTERS} />);
    await userEvent.click(screen.getByRole("button", { name: "Holder" }));
    expect(screen.getByText("Working now")).toBeTruthy();
    const earlier = screen.getByRole("button", { name: "Earlier 1" });
    expect(earlier.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("menuitemcheckbox", { name: "glm-D 1" })).toBeNull();
    await userEvent.click(earlier);
    expect(earlier.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("menuitemcheckbox", { name: "glm-D 1" })).toBeTruthy();
  });

  it("applies the other kinds' picks but not this kind's own", async () => {
    const { container } = render(
      <Toolbar
        cards={board()}
        filters={withPicks({ holder: ["glm-A"] })}
        onChange={() => undefined}
        boards={BOARDS}
        boardKey="a1a1"
        onBoard={() => undefined}
        epoch={3}
        connected
        live
        density="compact"
        onDensity={() => undefined}
      />,
    );
    await userEvent.click(kindButton("cluster", container));
    // only glm-A's cards count: alpha on C-1 and C-2, beta nowhere
    expect(optionsOf(screen.getByRole("menu", { name: "Epic" })).map((o) => o.textContent)).toEqual([
      "alpha 2",
    ]);
  });

  it("toggles a value and stays open for the next pick", async () => {
    const onChange = vi.fn();
    function SpyHarness({ cards, initial }: { cards: Card[]; initial: Filters }) {
      const [filters, setFilters] = useState(initial);
      return (
        <Toolbar
          cards={cards}
          filters={filters}
          onChange={(next) => {
            onChange(next);
            setFilters(next);
          }}
          boards={BOARDS}
          boardKey="a1a1"
          onBoard={() => undefined}
          epoch={3}
          connected
          live
          density="compact"
          onDensity={() => undefined}
        />
      );
    }
    render(<SpyHarness cards={board()} initial={EMPTY_FILTERS} />);
    await userEvent.click(screen.getByRole("button", { name: "Holder" }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "glm-A 2" }));
    expect(onChange).toHaveBeenCalledWith(withPicks({ holder: ["glm-A"] }));
    expect(screen.getByRole("menu", { name: "Holder" })).toBeTruthy();
    expect(screen.getByRole("menuitemcheckbox", { name: "glm-A 2" }).getAttribute("aria-checked")).toBe(
      "true",
    );
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "glm-A 2" }));
    expect(onChange).toHaveBeenCalledWith(withPicks({}));
  });

  it("closes on Escape and returns the focus to its button", async () => {
    const { container } = render(
      <Toolbar
        cards={board()}
        filters={EMPTY_FILTERS}
        onChange={() => undefined}
        boards={BOARDS}
        boardKey="a1a1"
        onBoard={() => undefined}
        epoch={3}
        connected
        live
        density="compact"
        onDensity={() => undefined}
      />,
    );
    const button = kindButton("holder", container);
    await userEvent.click(button);
    expect(screen.getByRole("menu", { name: "Holder" })).toBeTruthy();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("menu", { name: "Holder" })).toBeNull();
    expect(document.activeElement).toBe(button);
  });

  it("closes when a click lands outside the menu", async () => {
    const { container } = render(
      <Toolbar
        cards={board()}
        filters={EMPTY_FILTERS}
        onChange={() => undefined}
        boards={BOARDS}
        boardKey="a1a1"
        onBoard={() => undefined}
        epoch={3}
        connected
        live
        density="compact"
        onDensity={() => undefined}
      />,
    );
    await userEvent.click(kindButton("holder", container));
    expect(screen.getByRole("menu", { name: "Holder" })).toBeTruthy();
    await userEvent.click(document.body);
    expect(screen.queryByRole("menu", { name: "Holder" })).toBeNull();
  });

  it("opens from the keyboard, walks the entries with the arrows, and toggles with Space", async () => {
    const onChange = vi.fn();
    const { container } = render(
      <Toolbar
        cards={board()}
        filters={EMPTY_FILTERS}
        onChange={onChange}
        boards={BOARDS}
        boardKey="a1a1"
        onBoard={() => undefined}
        epoch={3}
        connected
        live
        density="compact"
        onDensity={() => undefined}
      />,
    );
    const button = kindButton("holder", container);
    button.focus();
    await userEvent.keyboard("{Enter}");
    const menu = screen.getByRole("menu", { name: "Holder" });
    const options = optionsOf(menu);
    expect(document.activeElement).toBe(options[0]);
    await userEvent.keyboard("{ArrowDown}{ArrowDown}");
    expect(document.activeElement).toBe(options[2]);
    await userEvent.keyboard("{End}");
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Earlier 1" }));
    await userEvent.keyboard("{Home}");
    expect(document.activeElement).toBe(options[0]);
    await userEvent.keyboard("{End}{ArrowUp}");
    expect(document.activeElement).toBe(options[2]);
    await userEvent.keyboard(" ");
    expect(onChange).toHaveBeenCalledWith(withPicks({ holder: ["glm-C"] }));
  });

  it("keeps / from stealing the focus while a menu is open", async () => {
    const { container } = render(
      <Toolbar
        cards={board()}
        filters={EMPTY_FILTERS}
        onChange={() => undefined}
        boards={BOARDS}
        boardKey="a1a1"
        onBoard={() => undefined}
        epoch={3}
        connected
        live
        density="compact"
        onDensity={() => undefined}
      />,
    );
    await userEvent.click(kindButton("holder", container));
    await userEvent.keyboard("/");
    expect(screen.getByRole("menu", { name: "Holder" })).toBeTruthy();
    expect(document.activeElement?.tagName).not.toBe("INPUT");
  });

  it("filters a long list from a box at its top", async () => {
    const cards = Array.from({ length: 11 }, (_, i) =>
      makeCard({ id: `L-${i}`, holder: `glm-${i}`, column: "ready", tags: [] }),
    );
    const { container } = render(
      <Toolbar
        cards={cards}
        filters={EMPTY_FILTERS}
        onChange={() => undefined}
        boards={BOARDS}
        boardKey="a1a1"
        onBoard={() => undefined}
        epoch={3}
        connected
        live
        density="compact"
        onDensity={() => undefined}
      />,
    );
    await userEvent.click(kindButton("holder", container));
    const box = screen.getByRole("searchbox", { name: "Filter Holder values" });
    await userEvent.type(box, "1");
    expect(optionsOf(screen.getByRole("menu", { name: "Holder" })).map((o) => o.textContent)).toEqual([
      "glm-1 1",
      "glm-10 1",
    ]);
  });

  it("says so when the facet has no values at all", async () => {
    const { container } = render(
      <Toolbar
        cards={board().map((c) => ({ ...c, holder: "" }))}
        filters={EMPTY_FILTERS}
        onChange={() => undefined}
        boards={BOARDS}
        boardKey="a1a1"
        onBoard={() => undefined}
        epoch={3}
        connected
        live
        density="compact"
        onDensity={() => undefined}
      />,
    );
    await userEvent.click(kindButton("holder", container));
    expect(screen.getByRole("menu", { name: "Holder" }).textContent).toContain("No values");
  });
});

describe("the pills", () => {
  it("lists every pick as a removable pill, with a Clear all beside them", async () => {
    const onChange = vi.fn();
    render(
      <Toolbar
        cards={board()}
        filters={withPicks({ holder: ["glm-A"], tag: ["kanban"] })}
        onChange={onChange}
        boards={BOARDS}
        boardKey="a1a1"
        onBoard={() => undefined}
        epoch={3}
        connected
        live
        density="compact"
        onDensity={() => undefined}
      />,
    );
    const pill = screen.getByRole("button", { name: "Remove Holder glm-A" });
    expect(pill.textContent).toContain("Holder: glm-A");
    await userEvent.click(pill);
    expect(onChange).toHaveBeenCalledWith(withPicks({ tag: ["kanban"] }));
    await userEvent.click(screen.getByRole("button", { name: "Clear all" }));
    expect(onChange).toHaveBeenCalledWith(withPicks({}));
  });

  it("appear when a menu pick lands and clear with one click", async () => {
    render(<Harness cards={board()} initial={EMPTY_FILTERS} />);
    await userEvent.click(screen.getByRole("button", { name: "Holder" }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "glm-A 2" }));
    expect(screen.getByRole("button", { name: "Remove Holder glm-A" })).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Clear all" }));
    expect(screen.queryByRole("button", { name: "Remove Holder glm-A" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Clear all" })).toBeNull();
  });
});
