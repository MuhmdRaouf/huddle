import { fireEvent, render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { describe, expect, it, vi } from "vite-plus/test";
import { Toolbar } from "../../src/kanban/components/Toolbar.tsx";
import {
  EMPTY_FILTERS,
  type Filters,
  filterCards,
  filtersFromSearch,
  searchFromFilters,
} from "../../src/kanban/store.ts";
import { snapshot } from "./helpers.ts";

const withPicks = (picks: Partial<Filters["picks"]>, over: Partial<Filters> = {}): Filters => ({
  ...EMPTY_FILTERS,
  ...over,
  picks: { ...EMPTY_FILTERS.picks, ...picks },
});

describe("filterCards", () => {
  it("matches the query against id, title, summary and tags, case-insensitively", () => {
    expect(filterCards(snapshot.cards, withPicks({}, { q: "KBN-UI" })).map((c) => c.id)).toEqual(["KBN-UI"]);
    expect(filterCards(snapshot.cards, withPicks({}, { q: "live updates" })).map((c) => c.id)).toEqual([
      "KBN-UI",
    ]);
    expect(filterCards(snapshot.cards, withPicks({}, { q: "export" })).map((c) => c.id)).toEqual([
      "KBN-EXPORT",
      "KBN-PLAN",
    ]);
    expect(filterCards(snapshot.cards, withPicks({}, { q: "nothing matches this" }))).toEqual([]);
  });

  it("shows a card matching any picked value within a kind", () => {
    const tags = filterCards(snapshot.cards, withPicks({ tag: ["go", "spec"] }));
    expect(tags.map((c) => c.id).sort()).toEqual(["KBN-API", "KBN-BOARD", "KBN-EMBED", "KBN-SPEC"]);
  });

  it("requires a match in every picked kind", () => {
    const both = filterCards(snapshot.cards, withPicks({ tag: ["go"], cluster: ["kanban server"] }));
    expect(both.map((c) => c.id)).toEqual(["KBN-API"]);
  });

  it("filters by holder, type and priority", () => {
    expect(filterCards(snapshot.cards, withPicks({ holder: ["glm-KBN-UI"] })).map((c) => c.id)).toEqual([
      "KBN-UI",
    ]);
    expect(
      filterCards(snapshot.cards, withPicks({ type: ["docs"], priority: ["P2"] })).map((c) => c.id),
    ).toEqual(["KBN-SKILL", "KBN-PLAN"]);
  });

  it("hides done only when asked", () => {
    const open = filterCards(snapshot.cards, withPicks({}, { hideDone: true }));
    expect(open.some((c) => c.column === "done")).toBe(false);
    expect(filterCards(snapshot.cards, EMPTY_FILTERS).length).toBe(snapshot.cards.length);
  });
});

describe("the URL query", () => {
  it("parses q, every chip kind and hide done", () => {
    expect(
      filtersFromSearch(
        "?q=core&cluster=kanban%20ui&tag=go,spec&holder=glm-KBN-UI&type=feature&priority=P1,P2&done=hide",
      ),
    ).toEqual(
      withPicks(
        {
          cluster: ["kanban ui"],
          tag: ["go", "spec"],
          holder: ["glm-KBN-UI"],
          type: ["feature"],
          priority: ["P1", "P2"],
        },
        { q: "core", hideDone: true },
      ),
    );
  });

  it("builds a query holding only what is set, and nothing for the empty filter", () => {
    expect(searchFromFilters(EMPTY_FILTERS)).toBe("");
    expect(searchFromFilters(withPicks({ tag: ["go"] }, { q: "x" }))).toBe("q=x&tag=go");
  });

  it("round trips: parsing what was built returns the same filters", () => {
    const filters = withPicks(
      { cluster: ["kanban ui"], tag: ["go", "spec"], holder: ["glm-KBN-UI"], priority: ["P0", "P3"] },
      { q: "board", hideDone: true },
    );
    expect(filtersFromSearch(`?${searchFromFilters(filters)}`)).toEqual(filters);
  });

  it("reads a bare search string with no leading question mark", () => {
    expect(filtersFromSearch("q=x")).toEqual(withPicks({}, { q: "x" }));
  });
});

describe("Toolbar", () => {
  it("focuses the search box when the reader presses / outside an input", () => {
    render(<Toolbar cards={snapshot.cards} filters={EMPTY_FILTERS} onChange={() => undefined} />);
    fireEvent.keyDown(document, { key: "/" });
    const box = screen.getByRole("searchbox", { name: "Search" });
    expect(document.activeElement).toBe(box);
    fireEvent.keyDown(box, { key: "/" });
    expect(document.activeElement).toBe(box);
  });

  it("toggles a chip by handing its parent the next filters", async () => {
    const onChange = vi.fn();
    render(<Toolbar cards={snapshot.cards} filters={EMPTY_FILTERS} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "go" }));
    expect(onChange).toHaveBeenCalledWith(withPicks({ tag: ["go"] }));
  });

  it("drops a picked chip when it is clicked again", async () => {
    const onChange = vi.fn();
    render(<Toolbar cards={snapshot.cards} filters={withPicks({ tag: ["go"] })} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "go" }));
    expect(onChange).toHaveBeenCalledWith(withPicks({}));
  });

  it("hands the hide-done toggle's new state to its parent", async () => {
    const onChange = vi.fn();
    render(<Toolbar cards={snapshot.cards} filters={EMPTY_FILTERS} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "Hide done" }));
    expect(onChange).toHaveBeenCalledWith(withPicks({}, { hideDone: true }));
  });
});
