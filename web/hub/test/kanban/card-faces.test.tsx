// card-faces.test.tsx — the card's two faces: compact at about 72 px with the priority on the
// left edge, comfortable adding the summary and the tags, and what a shared board hides.
import { render } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { CardView } from "../../src/kanban/components/Card.tsx";
import { cardOf, makeCard } from "./helpers.ts";

const open = (children: Parameters<typeof CardView>[0]) => render(<CardView {...children} />);
const cardOfFixture = (id: string) => open({ card: cardOf(id), flash: 0, onOpen: () => undefined });

describe("Feature: the compact card", () => {
  it("Scenario: the priority is the left edge, never a chip", () => {
    // Given a P1 card
    const { container, unmount } = cardOfFixture("KBN-UI");
    // Then the card carries its priority for the edge and no priority chip is rendered
    expect(container.querySelector<HTMLElement>("article.card")?.dataset.priority).toBe("P1");
    expect(container.querySelector(".b-priority")).toBeNull();
    unmount();
    // Given a P3 card
    const p3 = open({ card: makeCard({ id: "P-3", priority: "P3" }), flash: 0, onOpen: () => undefined });
    // Then it carries its priority the same way
    expect(p3.container.querySelector<HTMLElement>("article.card")?.dataset.priority).toBe("P3");
  });

  it("Scenario: row 3 lists est, type and the state badges, and nothing else", () => {
    // Given the expedite fixture card with a waits-on badge
    const { container } = cardOfFixture("KBN-UI");
    // Then the meta row reads est, type and the waits-on chip
    const meta = container.querySelector("ul.meta");
    expect(meta?.textContent).toContain("L");
    expect(meta?.textContent).toContain("feature");
    expect(meta?.textContent).toContain("waits on KBN-API");
    // and the chips the edge and row 3 already show are not repeated
    expect(container.querySelectorAll(".b-priority, .b-type, .b-est, .b-holder")).toHaveLength(0);
  });

  it("Scenario: compact hides the summary and the tags", () => {
    // Given a card with a summary and tags
    const { container } = cardOfFixture("KBN-UI");
    // Then neither is on the card
    expect(container.querySelector("p.summary")).toBeNull();
    expect(container.querySelector("ul.tags")).toBeNull();
  });

  it("Scenario: row 1 carries the id with the holder's initials at the right", () => {
    // Given the held fixture card
    const { container } = cardOfFixture("KBN-UI");
    // Then the head names the id and the holder's initials
    expect(container.querySelector("article.card .cid")?.textContent).toBe("KBN-UI");
    expect(container.querySelector("article.card .holder")?.textContent).toBe("GK");
  });
});

describe("Feature: the comfortable card", () => {
  it("Scenario: comfortable adds the summary and the tags over the compact face", () => {
    // Given the fixture card at comfortable density
    const { container } = open({
      card: cardOf("KBN-UI"),
      flash: 0,
      onOpen: () => undefined,
      density: "comfortable",
    });
    // Then the summary and the tags are there beside the compact rows
    expect(container.querySelector("p.summary")?.textContent).toContain("Preact board page");
    expect(container.querySelector("ul.tags")?.textContent).toContain("kanban");
    expect(container.querySelector("ul.meta")?.textContent).toContain("feature");
  });
});

describe("Feature: what a shared board hides", () => {
  it("Scenario: a tag every card carries is hidden from the card", () => {
    // Given a card whose tags include the tag every visible card shares
    const { container } = open({
      card: makeCard({ tags: ["huddle", "router"] }),
      flash: 0,
      onOpen: () => undefined,
      density: "comfortable",
      sharedTags: ["huddle"],
    });
    // Then only the tag that is not shared shows
    expect([...container.querySelectorAll("ul.tags button")].map((b) => b.textContent)).toEqual(["router"]);
  });

  it("Scenario: a cluster every card shares is hidden from the card", () => {
    // Given a card in the cluster every visible card shares
    const { container, unmount } = open({
      card: makeCard({ cluster: "hr" }),
      flash: 0,
      onOpen: () => undefined,
      sharedCluster: "hr",
    });
    // Then the cluster prefix is not on the card
    expect(container.querySelector(".epic")).toBeNull();
    unmount();
    // Given the same card on a board whose clusters differ
    const mixed = open({ card: makeCard({ cluster: "hr" }), flash: 0, onOpen: () => undefined });
    // Then the cluster prefix shows
    expect(mixed.container.querySelector(".epic")?.textContent).toBe("hr");
  });
});
