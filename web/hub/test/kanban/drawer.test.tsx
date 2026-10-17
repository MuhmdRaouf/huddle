import { fireEvent, render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { describe, expect, it, vi } from "vite-plus/test";
import { Drawer, idFromHash, useDrawerSelection } from "../../src/kanban/components/Drawer.tsx";
import type { Card, Ticket } from "../../src/kanban/types.ts";
import { cardOf, clone, snapshot, ticketApi, ticketUi } from "./helpers.ts";

function renderDrawer(card: Card = cardOf("KBN-UI"), ticket: Ticket = ticketUi) {
  const onClose = vi.fn();
  const onOpenDep = vi.fn();
  const getTicket = vi.fn(async (id: string) =>
    clone(id === "KBN-UI" ? ticketUi : { ...clone(ticket), card: cardOf(id), body: `## ${id} body` }),
  );
  const view = render(<Drawer card={card} getTicket={getTicket} onClose={onClose} onOpenDep={onOpenDep} />);
  return { ...view, onClose, onOpenDep, getTicket };
}

describe("idFromHash", () => {
  it("reads the ticket id from a deep link hash", () => {
    expect(idFromHash("#/t/KBN-UI")).toBe("KBN-UI");
  });

  it("returns null for anything that is not a ticket hash", () => {
    expect(idFromHash("#/t/")).toBeNull();
    expect(idFromHash("")).toBeNull();
    expect(idFromHash("#/columns")).toBeNull();
    expect(idFromHash("#/t/KBN-UI/extra")).toBeNull();
  });
});

describe("Drawer", () => {
  it("opens as a dialog named '<id>: <title>'", () => {
    const { container } = renderDrawer();
    const dialog = container.querySelector("[data-drawer]");
    expect(dialog?.getAttribute("role")).toBe("dialog");
    expect(dialog?.getAttribute("aria-label")).toBe(
      "KBN-UI: kanban: Preact board page, responsive and live, with its Vitest suite",
    );
  });

  it("shows every front-matter field of the ticket", async () => {
    renderDrawer();
    await screen.findByText("claimed_by");
    for (const [key, value] of [
      ["claimed_by", "glm-KBN-UI"],
      ["branch", "port/KBN-UI"],
      ["type", "feature"],
      ["priority", "P1"],
      ["est", "L"],
      ["cluster", "kanban ui"],
    ] as const) {
      const field = screen.getByText(key).closest("div");
      expect(field?.textContent).toContain(value);
    }
  });

  it("shows the badges the server derived", async () => {
    const { container } = renderDrawer();
    await screen.findByText("claimed_by");
    const labels = [...container.querySelectorAll("ul.badges li")].map((li) => li.textContent);
    expect(labels).toContain("EXPEDITE");
    expect(labels).toContain("waits on KBN-API");
  });

  it("renders the body from markdown: headings, lists and safe links", async () => {
    renderDrawer();
    await screen.findByRole("heading", { level: 2, name: "Description" });
    const link = screen.getByRole("link", { name: "the radar UI" }) as HTMLAnchorElement;
    expect(link.getAttribute("href")).toBe("https://example.com/radar");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    const step = screen.getByText("Write the tests, red first.");
    expect(step.tagName).toBe("LI");
  });

  it("escapes raw HTML in the body: a script tag renders as text", async () => {
    const { container } = renderDrawer();
    await screen.findByRole("heading", { level: 2, name: "Description" });
    expect(container.querySelectorAll("script")).toHaveLength(0);
    expect(container.querySelectorAll("b")).toHaveLength(0);
    const body = container.querySelector(".body");
    expect(body?.textContent).toContain("<script>alert('boom')</script>");
    expect(body?.textContent).toContain("<b>bold of distrust</b>");
  });

  it("offers a dependency as a link when it names a card, as text when it does not", async () => {
    const { onOpenDep } = renderDrawer();
    await screen.findByText("claimed_by");
    await userEvent.click(screen.getByRole("button", { name: "KBN-API ready" }));
    expect(onOpenDep).toHaveBeenCalledWith("KBN-API");
    const plain = screen.getByText("see the wire shapes in the spec");
    expect(plain.closest("button")).toBeNull();
  });

  it("closes from Escape", async () => {
    const { onClose } = renderDrawer();
    await screen.findByText("claimed_by");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("moves focus into the drawer on open and keeps it there through Tab", async () => {
    const { container } = renderDrawer();
    const dialog = container.querySelector("[data-drawer]") as HTMLElement;
    await screen.findByText("claimed_by");
    expect(dialog.contains(document.activeElement)).toBe(true);
    for (let i = 0; i < 12; i++) {
      fireEvent.keyDown(dialog, { key: "Tab" });
      expect(dialog.contains(document.activeElement)).toBe(true);
    }
  });

  it("shows a loading state until the ticket arrives", () => {
    const { container } = renderDrawer(cardOf("KBN-UI"), ticketUi);
    expect(container.querySelector('[role="status"]')?.textContent).toContain("Loading");
  });
});

describe("useDrawerSelection", () => {
  function Harness() {
    const sel = useDrawerSelection();
    const card = sel.openId === null ? null : (snapshot.cards.find((c) => c.id === sel.openId) ?? null);
    return (
      <div>
        <button type="button" onClick={(e) => sel.open("KBN-UI", e.currentTarget as HTMLElement)}>
          open
        </button>
        {card !== null && (
          <Drawer
            card={card}
            getTicket={async (id) => clone(id === "KBN-UI" ? ticketUi : ticketApi)}
            onClose={sel.close}
            onOpenDep={sel.openDep}
          />
        )}
      </div>
    );
  }

  it("opens a ticket from its card, then closes on Esc and returns focus to the card", async () => {
    const { container } = render(<Harness />);
    const opener = screen.getByRole("button", { name: "open" });
    await userEvent.click(opener);
    expect(container.querySelector("[data-drawer]")).not.toBeNull();
    await screen.findByText("claimed_by");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(container.querySelector("[data-drawer]")).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it("swaps the drawer to the dependency without closing it", async () => {
    const { container } = render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "open" }));
    await screen.findByText("claimed_by");
    await userEvent.click(screen.getByRole("button", { name: "KBN-API ready" }));
    const dialog = container.querySelector("[data-drawer]");
    expect(dialog?.getAttribute("aria-label")).toBe("KBN-API: kanban: HTTP API, SSE stream and watcher");
    expect(container.querySelector("[data-drawer]")).not.toBeNull();
  });
});
