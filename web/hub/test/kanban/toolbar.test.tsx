import { render, screen } from "@testing-library/preact";
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

/** The row's kind buttons, in their standing order. */
const kindButtons = (container: Element): string[] =>
  [...container.querySelectorAll<HTMLButtonElement>(".toolbar button")].map((b) => b.textContent ?? "");

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
  return <Toolbar cards={cards} filters={filters} onChange={setFilters} />;
}

describe("the toolbar row", () => {
  it("is one compact row: search, one menu button per kind, hide done — no chip wall", () => {
    const { container } = render(
      <Toolbar cards={board()} filters={EMPTY_FILTERS} onChange={() => undefined} />,
    );
    expect(container.querySelectorAll(".chips, .chip")).toHaveLength(0);
    expect(kindButtons(container)).toEqual(["Domain", "Epic", "Holder", "Type", "Priority", "Hide done"]);
    expect(screen.getByRole("searchbox", { name: "Search" })).toBeTruthy();
  });

  it("offers Domain only when some card carries tags", () => {
    const untagged = board().map((card) => ({ ...card, tags: [] }));
    const { container } = render(
      <Toolbar cards={untagged} filters={EMPTY_FILTERS} onChange={() => undefined} />,
    );
    expect(kindButtons(container)).toEqual(["Epic", "Holder", "Type", "Priority", "Hide done"]);
  });

  it("counts the picked values on its kind's button", () => {
    const { container } = render(
      <Toolbar
        cards={board()}
        filters={withPicks({ holder: ["glm-A", "glm-B"] })}
        onChange={() => undefined}
      />,
    );
    expect(kindButton("holder", container).textContent).toBe("Holder: 2");
    expect(kindButton("type", container).textContent).toBe("Type");
  });
});

describe("a kind's menu", () => {
  it("opens on click, lists the values with counts sorted by count then value, and reads as expanded", async () => {
    const { container } = render(
      <Toolbar cards={board()} filters={EMPTY_FILTERS} onChange={() => undefined} />,
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
      <Toolbar cards={board()} filters={withPicks({ holder: ["glm-A"] })} onChange={() => undefined} />,
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
      <Toolbar cards={board()} filters={EMPTY_FILTERS} onChange={() => undefined} />,
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
      <Toolbar cards={board()} filters={EMPTY_FILTERS} onChange={() => undefined} />,
    );
    await userEvent.click(kindButton("holder", container));
    expect(screen.getByRole("menu", { name: "Holder" })).toBeTruthy();
    await userEvent.click(document.body);
    expect(screen.queryByRole("menu", { name: "Holder" })).toBeNull();
  });

  it("opens from the keyboard, walks the entries with the arrows, and toggles with Space", async () => {
    const onChange = vi.fn();
    const { container } = render(<Toolbar cards={board()} filters={EMPTY_FILTERS} onChange={onChange} />);
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
      <Toolbar cards={board()} filters={EMPTY_FILTERS} onChange={() => undefined} />,
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
      <Toolbar cards={cards} filters={EMPTY_FILTERS} onChange={() => undefined} />,
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
