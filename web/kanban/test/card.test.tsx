import { act, fireEvent, render, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import { CardView } from "../src/components/Card.tsx";
import { cardOf, makeCard } from "./helpers.ts";
import { stubMatchMedia } from "./ui/match-media.ts";

const TITLE = "kanban: Preact board page, responsive and live, with its Vitest suite";

describe("CardView", () => {
  it("shows the cluster chip, the id, the title and the summary", () => {
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    const card = container.querySelector<HTMLElement>("article.card");
    expect(card?.dataset.id).toBe("KBN-UI");
    expect(card?.querySelector("header .chip")?.textContent).toBe("kanban ui");
    expect(card?.querySelector("header .cid")?.textContent).toBe("KBN-UI");
    expect(card?.querySelector("h3")?.textContent).toBe(TITLE);
    expect(card?.querySelector("p.summary")?.textContent).toContain("Preact board page");
  });

  it("renders the badges in the order the server derived them", () => {
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    const badges = [...(container.querySelectorAll("ul.badges li") ?? [])].map((li) => li.textContent);
    expect(badges).toEqual(["EXPEDITE", "P1", "feature", "L", "@glm-KBN-UI", "waits on KBN-API"]);
  });

  it("carries the badge's kind as its colour class", () => {
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    expect(container.querySelector("ul.badges li")?.classList.contains("b-expedite")).toBe(true);
  });

  it("lists the tags", () => {
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    expect([...container.querySelectorAll("ul.tags li")].map((li) => li.textContent)).toEqual([
      "kanban",
      "ui",
    ]);
  });

  it("shows the holder's initials with the full name on hover, nothing for an unheld card", () => {
    const held = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    const holder = held.container.querySelector(".holder");
    expect(holder?.textContent).toBe("GK");
    expect(holder?.getAttribute("title")).toBe("glm-KBN-UI");
    held.unmount();
    const free = render(<CardView card={cardOf("KBN-API")} flash={0} onOpen={() => undefined} />);
    expect(free.container.querySelector(".holder")).toBeNull();
  });

  it("opens the ticket from a button named '<id>: <title>'", () => {
    const onOpen = vi.fn();
    render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={onOpen} />);
    const button = screen.getByRole("button", { name: `KBN-UI: ${TITLE}` });
    fireEvent.click(button);
    expect(onOpen).toHaveBeenCalledTimes(1);
    const [id, opener] = onOpen.mock.calls[0] ?? [null, null];
    expect(id).toBe("KBN-UI");
    expect(opener).toBeInstanceOf(HTMLButtonElement);
  });

  it("flashes a card a delta changed for 1.5 s", async () => {
    vi.useFakeTimers();
    try {
      const { container } = render(<CardView card={cardOf("KBN-UI")} flash={3} onOpen={() => undefined} />);
      const card = container.querySelector<HTMLElement>("article.card");
      expect(card?.classList.contains("changed")).toBe(true);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1499);
      });
      expect(card?.classList.contains("changed")).toBe(true);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
      expect(card?.classList.contains("changed")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not flash a card no delta changed", () => {
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    expect(container.querySelector("article.card")?.classList.contains("changed")).toBe(false);
  });

  it("never flashes for a reader who prefers reduced motion", () => {
    stubMatchMedia((query) => query.includes("prefers-reduced-motion"));
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={3} onOpen={() => undefined} />);
    expect(container.querySelector("article.card")?.classList.contains("changed")).toBe(false);
  });

  it("leaves the summary element out when the card has none", () => {
    const { container } = render(
      <CardView card={makeCard({ summary: "" })} flash={0} onOpen={() => undefined} />,
    );
    expect(container.querySelector("p.summary")).toBeNull();
  });
});
