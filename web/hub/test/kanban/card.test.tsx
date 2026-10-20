import { act, fireEvent, render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { describe, expect, it, vi } from "vite-plus/test";
import { orderedTags, tagClass } from "../../src/kanban/components/Card.tsx";
import { CardView } from "../../src/kanban/components/Card.tsx";
import { cardOf, makeCard } from "./helpers.ts";
import { stubMatchMedia } from "./match-media.ts";

const TITLE = "kanban: Preact board page, responsive and live, with its Vitest suite";

/** The card as the board shows it at comfortable density, where the summary and tags live. */
function comfortable(children: Parameters<typeof CardView>[0]) {
  return render(<CardView {...children} density="comfortable" />);
}

describe("CardView", () => {
  it("shows the epic beside the id and the title, the summary at comfortable density", () => {
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    const card = container.querySelector<HTMLElement>("article.card");
    expect(card?.dataset.id).toBe("KBN-UI");
    expect(card?.querySelector("header .epic")?.textContent).toBe("kanban ui");
    expect(card?.querySelector("header .cid")?.textContent).toBe("KBN-UI");
    expect(card?.querySelector("h3")?.textContent).toBe(TITLE);
    // compact carries no summary
    expect(card?.querySelector("p.summary")).toBeNull();
    const wide = comfortable({ card: cardOf("KBN-UI"), flash: 0, onOpen: () => undefined });
    expect(wide.container.querySelector("p.summary")?.textContent).toContain("Preact board page");
  });

  it("renders the state badges after est and type in the meta row", () => {
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    const meta = container.querySelector("ul.meta");
    expect(meta?.querySelector(".est")?.textContent).toBe("L");
    expect(meta?.querySelector(".type")?.textContent).toBe("feature");
    const chips = [...(meta?.querySelectorAll("li.badge") ?? [])].map((li) => li.textContent);
    expect(chips).toEqual(["waits on KBN-API"]);
  });

  it("carries the badge's kind as its colour class", () => {
    const { container } = render(<CardView card={cardOf("KBN-UI")} flash={0} onOpen={() => undefined} />);
    expect(container.querySelector("ul.meta li.badge")?.classList.contains("b-waits")).toBe(true);
  });

  it("lists the tags as chips at comfortable density, the repo tags first", () => {
    const { container } = comfortable({
      card: makeCard({ tags: ["router", "cc-plugins", "huddle"] }),
      flash: 0,
      onOpen: () => undefined,
    });
    const chips = [...container.querySelectorAll("ul.tags button")];
    expect(chips.map((chip) => chip.textContent)).toEqual(["huddle", "cc-plugins", "router"]);
    expect(chips.map((chip) => chip.className)).toEqual(["repo-huddle", "repo-cc-plugins", ""]);
  });

  it("hands a tag up when its chip is clicked, without opening the ticket", async () => {
    const onTag = vi.fn();
    const onOpen = vi.fn();
    comfortable({ card: cardOf("KBN-UI"), flash: 0, onOpen, onTag });
    await userEvent.click(screen.getByRole("button", { name: "kanban" }));
    expect(onTag).toHaveBeenCalledWith("kanban");
    expect(onTag).not.toHaveBeenCalledWith("ui");
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("orders the tags with the repo tags leading", () => {
    expect(orderedTags(["web", "huddle", "router", "cc-plugins"])).toEqual([
      "huddle",
      "cc-plugins",
      "web",
      "router",
    ]);
    expect(orderedTags(["web", "router"])).toEqual(["web", "router"]);
    expect(orderedTags([])).toEqual([]);
  });

  it("marks only the repo tags with an accent class", () => {
    expect(tagClass("huddle")).toBe("repo-huddle");
    expect(tagClass("cc-plugins")).toBe("repo-cc-plugins");
    expect(tagClass("router")).toBe("");
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
    const { container } = comfortable({ card: makeCard({ summary: "" }), flash: 0, onOpen: () => undefined });
    expect(container.querySelector("p.summary")).toBeNull();
  });
});
