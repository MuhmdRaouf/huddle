import { describe, expect, it, vi } from "vite-plus/test";
import type { Ticket } from "../src/types.ts";
import { clone, snapshot, ticketApi } from "./helpers.ts";

/** The bodies an export embeds: one whole ticket per card id. */
function bodiesOf(cards: typeof snapshot.cards): Record<string, Ticket> {
  const bodies: Record<string, Ticket> = {};
  for (const card of cards) bodies[card.id] = { ...clone(ticketApi), card: clone(card) };
  return bodies;
}

describe("the exported page", () => {
  it("boots from window.__KANBAN__ and renders every card with no network", async () => {
    document.body.innerHTML = '<div id="root"><p class="boot">Loading the board…</p></div>';
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
    vi.resetModules();
    await import("../src/main.tsx");

    const cards = [...document.querySelectorAll<HTMLElement>("article.card")];
    expect(cards.map((card) => card.dataset.id ?? "").sort()).toEqual(
      snapshot.cards.map((card) => card.id).sort(),
    );
    expect(document.querySelector(".boot")).toBeNull();
    expect(document.querySelector('[role="status"]')).toBeNull();
  });
});
