// standalone.test.tsx — the board page's own entry over an export payload: it boots from
// window.__KANBAN__ with the hub's board, renders every card with no network, and never
// shows the live dot or a reconnecting state.
import { screen, waitFor } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Ticket } from "../../src/kanban/types.ts";
import { clone, snapshot, ticketUi } from "./helpers.ts";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
});

/** The bodies an export embeds: one whole ticket per card id. */
function bodiesOf(cards: typeof snapshot.cards): Record<string, Ticket> {
  const bodies: Record<string, Ticket> = {};
  for (const card of cards) bodies[card.id] = { ...clone(ticketUi), card: clone(card) };
  return bodies;
}

describe("the standalone board page", () => {
  it("boots from window.__KANBAN__ and renders every card with no network", async () => {
    document.body.innerHTML = '<div id="root"></div>';
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new Error("the exported page must not fetch"))),
    );
    vi.stubGlobal(
      "EventSource",
      vi.fn(() => {
        throw new Error("the exported page must not open a stream");
      }),
    );
    vi.stubGlobal("__KANBAN__", { snapshot: clone(snapshot), bodies: bodiesOf(snapshot.cards) });
    await import("../../src/kanban/standalone.tsx");

    const open = snapshot.cards.filter((card) => card.column !== "done").length;
    await waitFor(() => {
      expect(document.querySelectorAll("article.card").length).toBe(open);
    });
    expect(document.querySelector(".kanban-tab")).not.toBeNull();
    // done stands as a rail carrying its count, exactly as the hub's tab draws this board
    const rail = document.querySelector<HTMLButtonElement>('section[data-column="done"] .rail-btn');
    expect(rail?.getAttribute("aria-label")).toBe(`Done, ${snapshot.cards.length - open} cards, show column`);
    // a static board has nothing to be live about: no dot, no reconnecting word anywhere
    expect(document.querySelector(".live-dot")).toBeNull();
    expect(document.body.textContent).not.toContain("reconnecting");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("renders the hub's board classes, so the tab's own styles dress the page", async () => {
    document.body.innerHTML = '<div id="root"></div>';
    vi.stubGlobal("__KANBAN__", { snapshot: clone(snapshot), bodies: bodiesOf(snapshot.cards) });
    await import("../../src/kanban/standalone.tsx");

    await screen.findAllByRole("button");
    for (const mark of [".kanban-tab", ".toolbar", ".columns", ".flow-bar"]) {
      expect(document.querySelector(mark)).not.toBeNull();
    }
  });
});
