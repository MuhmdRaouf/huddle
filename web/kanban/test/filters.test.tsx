import { describe, expect, it } from "vite-plus/test";
import {
  DONE_HIDE_THRESHOLD,
  EMPTY_FILTERS,
  type Filters,
  facetValues,
  filterCards,
  filtersFromSearch,
  holderWorkingNow,
  searchFromFilters,
  togglePick,
} from "../src/store.ts";
import { makeCard, snapshot } from "./helpers.ts";

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

describe("facetValues", () => {
  it("counts one kind's values and sorts by count, then value", () => {
    expect(facetValues(snapshot.cards, EMPTY_FILTERS, "cluster")).toEqual([
      { value: "kanban docs", count: 3 },
      { value: "kanban board", count: 2 },
      { value: "kanban plugin", count: 2 },
      { value: "kanban ui", count: 2 },
      { value: "kanban server", count: 1 },
      { value: "kanban skill", count: 1 },
      { value: "kanban ticket", count: 1 },
    ]);
  });

  it("applies the search, hide done and the other kinds, but not this kind's own picks", () => {
    expect(facetValues(snapshot.cards, withPicks({}, { q: "KBN-UI" }), "tag")).toEqual([
      { value: "kanban", count: 1 },
      { value: "ui", count: 1 },
    ]);
    const open = facetValues(snapshot.cards, withPicks({}, { hideDone: true }), "tag");
    expect(open.map((f) => f.value)).not.toContain("spec");
    expect(open.find((f) => f.value === "kanban")?.count).toBe(9);
    expect(facetValues(snapshot.cards, withPicks({ tag: ["go"] }), "tag")[0]).toEqual({
      value: "kanban",
      count: 12,
    });
    expect(
      facetValues(snapshot.cards, withPicks({ cluster: ["kanban ui"] }), "tag")
        .map((f) => f.value)
        .sort(),
    ).toEqual(["export", "kanban", "ui"]);
  });

  it("drops a holder only found on done cards once hide done is on", () => {
    const doneHolder = makeCard({ id: "D-1", holder: "glm-DONE", column: "done" });
    const cards = [...snapshot.cards, doneHolder];
    expect(facetValues(cards, EMPTY_FILTERS, "holder").map((f) => f.value)).toContain("glm-DONE");
    expect(facetValues(cards, withPicks({}, { hideDone: true }), "holder").map((f) => f.value)).not.toContain(
      "glm-DONE",
    );
  });
});

describe("holderWorkingNow", () => {
  it("is true for the holders of cards a session is working, false for backlog and done", () => {
    const cards = [
      makeCard({ id: "W-1", holder: "glm-WORK", column: "in-progress" }),
      makeCard({ id: "R-1", holder: "glm-REVIEW", column: "review" }),
      makeCard({ id: "B-1", holder: "glm-BACKLOG", column: "backlog" }),
      makeCard({ id: "D-1", holder: "glm-DONE", column: "done" }),
    ];
    expect(holderWorkingNow(cards, EMPTY_FILTERS, "glm-WORK")).toBe(true);
    expect(holderWorkingNow(cards, EMPTY_FILTERS, "glm-REVIEW")).toBe(true);
    expect(holderWorkingNow(cards, EMPTY_FILTERS, "glm-BACKLOG")).toBe(false);
    expect(holderWorkingNow(cards, EMPTY_FILTERS, "glm-DONE")).toBe(false);
  });

  it("reads through the other filters, so a hidden card does not make a holder working", () => {
    const cards = [makeCard({ id: "W-1", holder: "glm-WORK", column: "in-progress", tags: ["go"] })];
    expect(holderWorkingNow(cards, withPicks({ tag: ["go"] }), "glm-WORK")).toBe(true);
    expect(holderWorkingNow(cards, withPicks({ tag: ["ui"] }), "glm-WORK")).toBe(false);
  });
});

describe("togglePick", () => {
  it("adds and drops one value of one kind, leaving the rest of the filters alone", () => {
    expect(togglePick(EMPTY_FILTERS, "tag", "go")).toEqual(withPicks({ tag: ["go"] }));
    expect(togglePick(withPicks({ tag: ["go"], holder: ["h"] }), "tag", "go")).toEqual(
      withPicks({ holder: ["h"] }),
    );
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

  it(`hides done by default only above ${DONE_HIDE_THRESHOLD} done cards, and done=show overrules`, () => {
    expect(filtersFromSearch("", 0).hideDone).toBe(false);
    expect(filtersFromSearch("", DONE_HIDE_THRESHOLD).hideDone).toBe(false);
    expect(filtersFromSearch("", DONE_HIDE_THRESHOLD + 1).hideDone).toBe(true);
    expect(filtersFromSearch("?done=show", 278).hideDone).toBe(false);
    expect(filtersFromSearch("?done=hide", 0).hideDone).toBe(true);
  });

  it("writes done=show on a done-heavy board once the reader shows done again", () => {
    expect(searchFromFilters(EMPTY_FILTERS, DONE_HIDE_THRESHOLD + 1)).toBe("done=show");
    expect(searchFromFilters(EMPTY_FILTERS, DONE_HIDE_THRESHOLD)).toBe("");
    expect(searchFromFilters(withPicks({}, { hideDone: true }), DONE_HIDE_THRESHOLD + 1)).toBe("done=hide");
  });

  it("round trips the done default both ways on a done-heavy board", () => {
    const shown = filtersFromSearch(`?${searchFromFilters(EMPTY_FILTERS, 278)}`, 278);
    expect(shown.hideDone).toBe(false);
    const hidden = filtersFromSearch(`?${searchFromFilters(EMPTY_FILTERS, 3)}`, 278);
    expect(hidden.hideDone).toBe(true);
  });
});
