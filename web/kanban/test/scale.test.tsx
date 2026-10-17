import { render } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { Board } from "../src/components/Board.tsx";
import { EMPTY_FILTERS } from "../src/store.ts";
import { makeCard, makeSnapshot } from "./helpers.ts";

describe("a board of 500 cards", () => {
  it("renders them all in one pass", () => {
    const cards = Array.from({ length: 500 }, (_, i) =>
      makeCard({
        id: `S-${i}`,
        column: "backlog",
        expedite: false,
        tags: ["scale"],
        badges: [{ kind: "priority", label: "P2" }],
        deps: [],
        holder: "",
      }),
    );
    const { container } = render(
      <Board
        snapshot={makeSnapshot(cards)}
        filters={EMPTY_FILTERS}
        changeSeq={0}
        changedIds={new Set()}
        onOpen={() => undefined}
      />,
    );
    expect(container.querySelectorAll("article.card")).toHaveLength(500);
    expect(container.querySelectorAll("ul.badges li")).toHaveLength(500);
    expect(container.querySelector('section[aria-label="Expedite"]')).toBeNull();
  });
});
