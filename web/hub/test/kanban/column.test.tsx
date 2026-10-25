// column.test.tsx — a collapsed column is a rail: one button whose accessible name carries the
// title and the count, the count being the column's whole count when the shown cards are capped
// and the cards' own number otherwise. A grouped column (the phone's backlog) shows one header
// per group with its count, and only the open groups' cards.

import { fireEvent, render, screen } from "@testing-library/preact";
import { useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import { Column } from "../../src/kanban/components/Column.tsx";
import type { Grouping } from "../../src/kanban/store.ts";
import type { Card } from "../../src/kanban/types.ts";
import { makeCard } from "./helpers.ts";

function rail(count?: number) {
  const onExpand = vi.fn();
  render(
    <Column
      id="done"
      title="Done"
      cards={[makeCard({ id: "A-1" }), makeCard({ id: "A-2" })]}
      changeSeq={0}
      changedIds={new Set()}
      onOpen={() => {}}
      collapsed
      count={count}
      onExpand={onExpand}
    />,
  );
  return onExpand;
}

describe("a collapsed column", () => {
  it("is a rail named by its title and its whole count, and a click opens it", () => {
    const onExpand = rail(14);
    const button = screen.getByRole("button", { name: "Done, 14 cards, show column" });
    expect(button.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(button);
    expect(onExpand).toHaveBeenCalledTimes(1);
  });

  it("counts its own cards when no whole count is handed in", () => {
    rail();
    expect(screen.getByRole("button", { name: "Done, 2 cards, show column" })).toBeTruthy();
  });
});

/** 150 backlog cards: 10 P0, 40 P1, 50 P2 and 50 P3, spread over three clusters. */
function deepBacklog(): Card[] {
  const spread: [string, number][] = [
    ["P0", 10],
    ["P1", 40],
    ["P2", 50],
    ["P3", 50],
  ];
  const clusters = ["web", "kanban", "hub"];
  let n = 0;
  return spread.flatMap(([priority, count]) =>
    Array.from({ length: count }, () => {
      n += 1;
      return makeCard({ id: `G-${n}`, column: "backlog", priority, cluster: clusters[n % 3] ?? "web" });
    }),
  );
}

/** The backlog as the page owns it: the open groups live in state, so a header tap lands. */
function Grouped({ grouping, open }: { grouping: Grouping; open: string[] }) {
  const [opened, setOpened] = useState<ReadonlySet<string>>(new Set(open));
  return (
    <Column
      id="backlog"
      title="Backlog"
      cards={deepBacklog()}
      changeSeq={0}
      changedIds={new Set()}
      onOpen={() => {}}
      groups={{
        grouping,
        open: opened,
        onToggle: (key) =>
          setOpened((current) => {
            const next = new Set(current);
            if (!next.delete(key)) next.add(key);
            return next;
          }),
      }}
    />
  );
}

/** The column's group headers as name and expanded state, in their order. */
const headers = (container: Element): string[] =>
  [...container.querySelectorAll<HTMLButtonElement>("button.group-head")].map(
    (b) => `${b.textContent ?? ""} ${b.getAttribute("aria-expanded") ?? ""}`,
  );

describe("Feature: the phone's backlog groups its cards", () => {
  it("Scenario: by priority, four headers carry their counts and only P0 is open", () => {
    // Given 150 backlog cards across P0 to P3, grouped by priority with P0 open
    const { container } = render(<Grouped grouping="priority" open={["P0"]} />);
    // Then the four headers carry their counts, P0 alone expanded
    expect(headers(container)).toEqual(["P0 10 true", "P1 40 false", "P2 50 false", "P3 50 false"]);
    // And only the ten P0 cards are on the page
    expect(container.querySelectorAll("article.card")).toHaveLength(10);
  });

  it("Scenario: a tap on P1 opens its cards", () => {
    // Given the priority grouping with P0 open
    const { container } = render(<Grouped grouping="priority" open={["P0"]} />);
    // When P1's header is tapped
    fireEvent.click(screen.getByRole("button", { name: "P1 40" }));
    // Then P1 reads as expanded and its 40 cards join P0's 10
    expect(screen.getByRole("button", { name: "P1 40" }).getAttribute("aria-expanded")).toBe("true");
    expect(container.querySelectorAll("article.card")).toHaveLength(50);
  });

  it("Scenario: by cluster, the headers are the clusters and every one is closed", () => {
    // Given the same cards grouped by cluster with nothing open
    const { container } = render(<Grouped grouping="cluster" open={[]} />);
    // Then the headers name the clusters with their counts, all closed, and no card shows
    expect(headers(container)).toEqual(["hub 50 false", "kanban 50 false", "web 50 false"]);
    expect(container.querySelectorAll("article.card")).toHaveLength(0);
  });
});
