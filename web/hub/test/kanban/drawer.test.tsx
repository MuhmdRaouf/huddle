// drawer.test.tsx — the card detail panel: the placements (docked, overlay, full screen), the
// pure helpers behind it (relative dates, the log split, the field rows, the unblocks set), the
// copy button, the resize handle and the failure path.
import { fireEvent, render, screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import {
  Drawer,
  PANEL_DEFAULT_WIDTH,
  PANEL_MAX_WIDTH,
  PANEL_MIN_WIDTH,
  fieldRows,
  idFromHash,
  readPanelWidth,
  relativeTime,
  splitLog,
  unblocks,
  useDrawerSelection,
  writePanelWidth,
} from "../../src/kanban/components/Drawer.tsx";
import type { Card, Ticket } from "../../src/kanban/types.ts";
import { cardOf, clone, snapshot, ticketApi, ticketUi } from "./helpers.ts";
import { stubMatchMedia } from "./match-media.ts";

/** A Drawer on the given cards, the ticket fetch faked per id. */
function renderDrawer(card: Card = cardOf("KBN-UI"), cards: Card[] = snapshot.cards) {
  const onClose = vi.fn();
  const onOpenDep = vi.fn();
  const getTicket = vi.fn(async (id: string) =>
    clone(id === "KBN-UI" ? ticketUi : { ...clone(ticketApi), card: cardOf(id), body: `## ${id} body` }),
  );
  const view = render(
    <Drawer card={card} cards={cards} getTicket={getTicket} onClose={onClose} onOpenDep={onOpenDep} />,
  );
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

describe("Feature: dates read as the reader does", () => {
  // a fixed now, offset-free stamps: the answer must not depend on the host's zone
  const now = new Date(2026, 9, 20, 12, 0, 0);

  it("Scenario: a stamp from today reads with its time", () => {
    // Given a timestamp from this morning
    // When it is rendered relative to noon today
    // Then it reads "today" and the clock
    expect(relativeTime("2026-10-20T09:02:00", now)).toBe("today 09:02");
  });

  it("Scenario: yesterday's stamp says yesterday", () => {
    expect(relativeTime("2026-10-19T23:40:00", now)).toBe("yesterday 23:40");
  });

  it("Scenario: a stamp of a few days back counts the days", () => {
    expect(relativeTime("2026-10-17T08:15:00", now)).toBe("3 days ago");
  });

  it("Scenario: an older stamp falls back to the date", () => {
    expect(relativeTime("2025-06-01T10:00:00", now)).toBe("1 Jun 2025");
  });

  it("Scenario: a stamp that is not a date reads as written", () => {
    expect(relativeTime("not a date", now)).toBe("not a date");
  });
});

describe("Feature: the log travels beside the body", () => {
  const withLog = "## Description\n\nWork.\n\n## Log\n\n- one\n- two\n\n## After\n\nmore\n";

  it("Scenario: the Log section splits out of the body and counts its entries", () => {
    // Given a body whose Log sits between Description and After
    // When it is split
    // Then the body keeps both other sections and the log carries its two items
    const split = splitLog(withLog);
    expect(split.body).toBe("## Description\n\nWork.\n\n## After\n\nmore");
    expect(split.log).toBe("- one\n- two");
    expect(split.entries).toBe(2);
  });

  it("Scenario: a body without a Log stays whole", () => {
    const split = splitLog("## Description\n\nWork.\n");
    expect(split.body).toBe("## Description\n\nWork.");
    expect(split.log).toBe("");
    expect(split.entries).toBe(0);
  });
});

describe("Feature: the fields read as a spec", () => {
  it("Scenario: the header's fields are not repeated and the rest is listed once", () => {
    // Given the KBN-UI ticket, whose header already carries id, title, summary, type, priority,
    // est, holder and whose deps read as Waits on
    // When the rows are built
    // Then the shown rows are the known ones and every other field lands under More fields
    const { shown, more } = fieldRows(ticketUi);
    expect(shown.map((row) => row.key)).toEqual([
      "cluster",
      "repos",
      "tags",
      "created",
      "updated",
      "branch",
      "worktree",
    ]);
    expect(more.map((row) => row.key)).toEqual(["expedite", "claimed_by", "claimed_at"]);
  });

  it("Scenario: created merges the stamp and the author, updated reads the card", () => {
    const { shown } = fieldRows(ticketUi);
    const created = shown.find((row) => row.key === "created");
    expect(created?.value).toBe("2026-10-10T11:00:00+03:00");
    expect(created?.suffix).toBe("by raouf");
    const updated = shown.find((row) => row.key === "updated");
    expect(updated?.value).toBe(cardOf("KBN-UI").updatedAt);
  });

  it("Scenario: a date renders as a time with the full ISO in its title", async () => {
    // Given the panel on KBN-UI
    // When the Updated row paints
    // Then the value is a <time> whose datetime and title carry the ISO
    renderDrawer();
    const time = (await screen
      .findByText("Updated")
      .then((label) => label.closest(".row")?.querySelector("time"))) as HTMLTimeElement | null;
    expect(time?.getAttribute("datetime")).toBe("2026-10-15T20:26:00+03:00");
    expect(time?.getAttribute("title")).toBe("2026-10-15T20:26:00+03:00");
  });

  it("Scenario: an array field renders as chips", async () => {
    renderDrawer();
    const tags = await screen.findByText("Tags").then((label) => label.closest(".row"));
    expect(tags?.querySelector(".chipset")?.textContent).toBe("kanbanui");
  });

  it("Scenario: paths and URLs may wrap, nothing else does", () => {
    const { shown } = fieldRows(ticketUi);
    // Given the worktree path and the tags
    // When the rows are built
    // Then the wrapping hint travels on the value, not on the whole grid
    expect(shown.find((row) => row.key === "worktree")?.value).toBe("/tmp/wt/KBN-UI");
    expect(shown.find((row) => row.key === "tags")?.kind).toBe("chips");
  });

  it("Scenario: the ticket's unknown fields sit under a closed More fields", async () => {
    renderDrawer();
    // Given the KBN-UI ticket with claimed_by, claimed_at and expedite beyond the known ones
    // When the panel paints
    // Then More fields says 3 and opens onto them
    const summary = await screen.findByText(/^More fields \(3\)$/);
    const details = summary.closest("details") as HTMLDetailsElement;
    expect(details.open).toBe(false);
    fireEvent.click(summary);
    expect(details.open).toBe(true);
    expect(screen.getByText("claimed_by")).toBeDefined();
    expect(screen.getByText("glm-KBN-UI")).toBeDefined();
  });
});

describe("Feature: the panel's dependencies navigate", () => {
  it("Scenario: Waits on lists each dependency as a row with its column", async () => {
    // Given KBN-UI waiting on KBN-API and one plain-text note
    // When the panel paints
    // Then the card dep is a button beside its title and column, the text dep stays text
    const { onOpenDep } = renderDrawer();
    await screen.findByText("Waits on");
    await userEvent.click(screen.getByRole("button", { name: "KBN-API" }));
    expect(onOpenDep).toHaveBeenCalledWith("KBN-API");
    const row = screen.getByRole("button", { name: "KBN-API" }).closest(".dep-row");
    expect(row?.textContent).toContain("kanban: HTTP API, SSE stream and watcher");
    expect(row?.textContent).toContain("Ready");
    const plain = screen.getByText("see the wire shapes in the spec");
    expect(plain.closest("button")).toBeNull();
  });

  it("Scenario: Unblocks lists the cards that depend on this one, read off the snapshot", () => {
    // Given KBN-EMBED depending on KBN-UI in the snapshot
    // When the panel opens on KBN-UI
    // Then the Unblocks row names KBN-EMBED with its own column
    renderDrawer();
    const row = screen.getByText("Unblocks").closest(".dep-row");
    expect(row?.textContent).toContain("KBN-EMBED");
    expect(row?.textContent).toContain("Backlog");
  });

  it("Scenario: a card with no dependencies omits both sections", () => {
    // Given KBN-EXPORT, which nothing waits on and nothing depends on
    // When the panel opens on it
    // Then neither Waits on nor Unblocks is rendered
    renderDrawer(cardOf("KBN-EXPORT"));
    expect(screen.queryByText("Waits on")).toBeNull();
    expect(screen.queryByText("Unblocks")).toBeNull();
  });

  it("Scenario: a click on a Waits on row swaps the panel to that card", async () => {
    // Given the panel on KBN-UI
    // When the reader clicks the KBN-API row
    // Then the dialog is KBN-API's, without closing and without losing the opener
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
              cards={snapshot.cards}
              getTicket={async (id) => clone(id === "KBN-UI" ? ticketUi : ticketApi)}
              onClose={sel.close}
              onOpenDep={sel.openDep}
            />
          )}
        </div>
      );
    }
    const { container } = render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "open" }));
    await screen.findByText("Waits on");
    await userEvent.click(screen.getByRole("button", { name: "KBN-API" }));
    const dialog = container.querySelector("[data-drawer]");
    expect(dialog?.getAttribute("aria-label")).toBe("KBN-API: kanban: HTTP API, SSE stream and watcher");
    expect(container.querySelector("[data-drawer]")).not.toBeNull();
  });
});

describe("Feature: the copy button hands the id over", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("Scenario: clicking the copy button writes the id and says Copied", async () => {
    // Given a clipboard the browser provides
    // When the reader clicks the panel's copy button
    // Then the id lands on the clipboard and the button says Copied for a while
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    renderDrawer();
    const button = await screen.findByRole("button", { name: "Copy KBN-UI" });
    await userEvent.click(button);
    expect(writeText).toHaveBeenCalledWith("KBN-UI");
    expect(screen.getByText("Copied")).toBeDefined();
  });
});

describe("Feature: the docked panel resizes and remembers", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("Scenario: the arrow keys resize within 360 and 720 and the width is remembered", async () => {
    // Given the panel docked at its default 440
    // When the reader presses ArrowLeft twice and ArrowRight once on the handle
    // Then the width moves 24 px a step, clamps at 720, and lands in localStorage
    stubMatchMedia((query) => query.includes("min-width: 1200"));
    const { container } = renderDrawer();
    const separator = (await screen.findByRole("separator")) as HTMLElement;
    expect(separator.getAttribute("aria-valuemin")).toBe(String(PANEL_MIN_WIDTH));
    expect(separator.getAttribute("aria-valuemax")).toBe(String(PANEL_MAX_WIDTH));
    expect(separator.getAttribute("aria-valuenow")).toBe(String(PANEL_DEFAULT_WIDTH));
    const panel = container.querySelector("[data-drawer]") as HTMLElement;
    fireEvent.keyDown(separator, { key: "ArrowLeft" });
    fireEvent.keyDown(separator, { key: "ArrowLeft" });
    expect(panel.style.width).toBe("488px");
    expect(readPanelWidth()).toBe(488);
    fireEvent.keyDown(separator, { key: "ArrowRight" });
    expect(panel.style.width).toBe("464px");
    expect(separator.getAttribute("aria-valuenow")).toBe("464");
  });

  it("Scenario: the clamp holds at both ends", () => {
    // Given a stored width beyond the ceiling
    // When the reader keeps pressing ArrowLeft
    // Then the width stops at 720 and never leaves the range
    writePanelWidth(719);
    stubMatchMedia((query) => query.includes("min-width: 1200"));
    const { container } = renderDrawer();
    const separator = screen.getByRole("separator") as HTMLElement;
    const panel = container.querySelector("[data-drawer]") as HTMLElement;
    for (let i = 0; i < 5; i++) fireEvent.keyDown(separator, { key: "ArrowLeft" });
    expect(panel.style.width).toBe("720px");
    for (let i = 0; i < 40; i++) fireEvent.keyDown(separator, { key: "ArrowRight" });
    expect(panel.style.width).toBe("360px");
  });

  it("Scenario: dragging the handle resizes the panel the way the pointer moves", () => {
    // Given the docked panel
    // When the pointer drags the handle 160 px to the left
    // Then the width grows by 160 and is remembered; after the drag ends a move does nothing
    stubMatchMedia((query) => query.includes("min-width: 1200"));
    const { container } = renderDrawer();
    const separator = screen.getByRole("separator") as HTMLElement;
    const panel = container.querySelector("[data-drawer]") as HTMLElement;
    fireEvent.pointerDown(separator, { pointerId: 1, clientX: 500 });
    fireEvent.pointerMove(separator, { pointerId: 1, clientX: 340 });
    expect(panel.style.width).toBe("600px");
    expect(readPanelWidth()).toBe(600);
    fireEvent.pointerUp(separator, { pointerId: 1 });
    fireEvent.pointerMove(separator, { pointerId: 1, clientX: 200 });
    expect(panel.style.width).toBe("600px");
  });

  it("Scenario: a localStorage that throws falls back to 440", () => {
    // Given a browser that refuses storage (private mode)
    // When the width is read
    // Then the default stands and nothing crashes
    const throwing = {
      getItem: () => {
        throw new Error("denied");
      },
      setItem: () => {
        throw new Error("denied");
      },
    };
    expect(readPanelWidth(throwing)).toBe(440);
    expect(() => writePanelWidth(500, throwing)).not.toThrow();
  });

  it("Scenario: a stored width outside the range clamps back in", () => {
    // Given a stored 2000 from an older build
    // When the width is read
    // Then it reads as the ceiling
    writePanelWidth(2000);
    expect(readPanelWidth()).toBe(PANEL_MAX_WIDTH);
    writePanelWidth(-5);
    expect(readPanelWidth()).toBe(PANEL_MIN_WIDTH);
    localStorage.removeItem("kanban.panelWidth");
    expect(readPanelWidth()).toBe(PANEL_DEFAULT_WIDTH);
  });
});

describe("Feature: the panel opens, fails and recovers", () => {
  it("Scenario: a skeleton stands in while the ticket loads", () => {
    // Given a fetch that never answers
    // When the panel opens
    // Then the header stands from the card and four skeleton rows wait for the ticket's fields
    const getTicket = vi.fn(async () => new Promise<Ticket>(() => undefined));
    render(
      <Drawer
        card={cardOf("KBN-UI")}
        cards={snapshot.cards}
        getTicket={getTicket}
        onClose={() => {}}
        onOpenDep={() => {}}
      />,
    );
    expect(screen.getByRole("status")).toBeDefined();
    expect(
      screen.getByRole("heading", {
        name: "kanban: Preact board page, responsive and live, with its Vitest suite",
      }),
    ).toBeDefined();
    expect(document.querySelectorAll("[data-drawer] .skeleton")).toHaveLength(4);
    expect(screen.queryByText("Tags")).toBeNull();
  });

  it("Scenario: a failed ticket says so and Try again refetches", async () => {
    // Given a fetch that rejects once and answers after
    // When the reader follows Try again
    // Then the ticket arrives and the failure line goes
    const getTicket = vi.fn<(id: string) => Promise<Ticket>>();
    getTicket.mockRejectedValueOnce(new Error("down"));
    getTicket.mockResolvedValue(clone(ticketUi));
    render(
      <Drawer
        card={cardOf("KBN-UI")}
        cards={snapshot.cards}
        getTicket={getTicket}
        onClose={() => {}}
        onOpenDep={() => {}}
      />,
    );
    await screen.findByRole("alert");
    expect(screen.getByText("This card could not be loaded.")).toBeDefined();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByText("Tags");
    expect(screen.queryByRole("alert")).toBeNull();
    expect(getTicket).toHaveBeenCalledTimes(2);
  });

  it("Scenario: focus moves to the heading on open and back to the opener on close", async () => {
    // Given the panel open on a card clicked from the board
    // When it opens and closes again
    // Then the heading holds focus inside, and the opener gets it back
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
              cards={snapshot.cards}
              getTicket={async (id) => clone(id === "KBN-UI" ? ticketUi : ticketApi)}
              onClose={sel.close}
              onOpenDep={sel.openDep}
            />
          )}
        </div>
      );
    }
    const { container } = render(<Harness />);
    const opener = screen.getByRole("button", { name: "open" });
    await userEvent.click(opener);
    const dialog = container.querySelector("[data-drawer]") as HTMLElement;
    const heading = dialog.querySelector("h2") as HTMLElement;
    await screen.findByText("Tags");
    expect(document.activeElement).toBe(heading);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(container.querySelector("[data-drawer]")).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it("Scenario: Escape closes and Tab keeps focus inside", async () => {
    // Given the open panel
    // When the reader presses Escape and tabs around
    // Then the panel closes on Escape, and Tab wraps inside without leaving
    const { container, onClose } = renderDrawer();
    const dialog = container.querySelector("[data-drawer]") as HTMLElement;
    await screen.findByText("Tags");
    for (let i = 0; i < 12; i++) {
      fireEvent.keyDown(dialog, { key: "Tab" });
      expect(dialog.contains(document.activeElement)).toBe(true);
    }
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("Feature: the body reads as a spec", () => {
  it("Scenario: the body renders markdown with safe links, headings and lists", async () => {
    // Given the KBN-UI body with a link, headings and a plan
    // When the panel paints
    // Then the link opens in its own tab and the plan reads as a list
    renderDrawer();
    await screen.findByRole("heading", { level: 2, name: "Description" });
    const link = screen.getByRole("link", { name: "the radar UI" }) as HTMLAnchorElement;
    expect(link.getAttribute("href")).toBe("https://example.com/radar");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    const step = screen.getByText("Write the tests, red first.");
    expect(step.tagName).toBe("LI");
  });

  it("Scenario: raw HTML in the body stays escaped text", async () => {
    // Given a body carrying a script tag (here after the Log, so it renders in the log's body)
    // When it renders
    // Then nothing executes and the tags read as text
    const { container } = renderDrawer();
    await screen.findByRole("heading", { level: 2, name: "Description" });
    expect(container.querySelectorAll("script")).toHaveLength(0);
    expect(container.querySelectorAll("b")).toHaveLength(0);
    expect(container.querySelector("[data-drawer]")?.textContent).toContain("<script>alert('boom')</script>");
    expect(container.querySelector("[data-drawer]")?.textContent).toContain("<b>bold of distrust</b>");
  });

  it("Scenario: the log is collapsed and counts its entries", async () => {
    // Given a body whose Log holds one entry
    // When the panel paints
    // Then the log lives in a closed details named Log (1 entry) and opens onto it
    renderDrawer();
    const summary = await screen.findByText("Log (1 entry)");
    const details = summary.closest("details") as HTMLDetailsElement;
    expect(details.open).toBe(false);
    fireEvent.click(summary);
    expect(details.open).toBe(true);
    expect(details.textContent).toContain("claimed");
  });

  it("Scenario: task-list checkboxes render disabled", async () => {
    // Given a body with a task list
    // When it renders
    // Then each item starts with a checkbox no one can tick
    const ticket = clone(ticketUi);
    ticket.body = "## Plan\n\n- [ ] open step\n- [x] done step\n";
    render(
      <Drawer
        card={cardOf("KBN-UI")}
        cards={snapshot.cards}
        getTicket={async () => ticket}
        onClose={() => {}}
        onOpenDep={() => {}}
      />,
    );
    const boxes = (await screen
      .findByText("Plan")
      .then(() => document.querySelectorAll("input[type=checkbox]"))) as NodeListOf<HTMLInputElement>;
    expect(boxes).toHaveLength(2);
    for (const box of boxes) expect(box.disabled).toBe(true);
    expect(boxes[1]?.checked).toBe(true);
  });

  it("Scenario: the body width stays readable", async () => {
    // Given the panel on KBN-UI
    // When the body paints
    // Then it carries the panel-body class the stylesheet caps at 72ch
    const { container } = renderDrawer();
    await screen.findByRole("heading", { level: 2, name: "Description" });
    expect(container.querySelector(".panel-body")).not.toBeNull();
  });
});

describe("Feature: the placements follow the window", () => {
  afterEach(() => {
    localStorage.clear();
  });

  it("Scenario: at desktop width the panel docks beside the board", () => {
    // Given a window of 1200 px and more
    // When the panel opens
    // Then it is docked: a separator handle, no scrim, its width inline
    stubMatchMedia((query) => query.includes("min-width: 1200"));
    const { container } = renderDrawer();
    expect(container.querySelector("[data-drawer]")?.classList.contains("docked")).toBe(true);
    expect(container.querySelector(".panel-scrim")).toBeNull();
    expect(container.querySelector(".back")).toBeNull();
  });

  it("Scenario: at tablet width the panel is a sheet over a scrim", async () => {
    // Given a window between 640 and 1199
    // When the panel opens
    // Then a scrim stands behind it and a click on it closes
    stubMatchMedia((query) => query.includes("max-width: 1199"));
    const { container, onClose } = renderDrawer();
    const scrim = container.querySelector(".panel-scrim") as HTMLElement;
    expect(scrim).not.toBeNull();
    expect(container.querySelector("[data-drawer]")?.classList.contains("sheet")).toBe(true);
    await userEvent.click(scrim);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Scenario: on a phone the panel is full screen with a Back button", async () => {
    // Given a PWA window under 640
    // When the panel opens
    // Then the header holds Back and it closes back to the board
    stubMatchMedia((query) => query.includes("max-width: 639"));
    const { container, onClose } = renderDrawer();
    const back = container.querySelector(".back") as HTMLElement;
    expect(back).not.toBeNull();
    expect(container.querySelector("[data-drawer]")?.classList.contains("full")).toBe(true);
    await userEvent.click(back);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Scenario: the header carries the id, the title and the chips", async () => {
    // Given the panel on KBN-UI
    // When it paints
    // Then the id reads in the mono stack with its copy button, the title is the h2, and the
    // column, priority, type, est and holder sit as chips
    stubMatchMedia(() => false);
    const { container } = renderDrawer();
    await screen.findByText("KBN-UI");
    const dialog = container.querySelector("[data-drawer]") as HTMLElement;
    expect(dialog.getAttribute("aria-label")).toBe(
      "KBN-UI: kanban: Preact board page, responsive and live, with its Vitest suite",
    );
    expect(dialog.querySelector(".cid")?.textContent).toBe("KBN-UI");
    expect(dialog.querySelector("h2")?.textContent).toBe(
      "kanban: Preact board page, responsive and live, with its Vitest suite",
    );
    const chips = [...dialog.querySelectorAll(".chips .chip")].map((chip) => chip.textContent);
    expect(chips).toContain("In progress");
    expect(chips).toContain("P1");
    expect(chips).toContain("feature");
    expect(chips).toContain("L");
    expect(chips).toContain("@glm-KBN-UI");
    const columnChip = dialog.querySelector('.chip[data-column="in-progress"]');
    expect(columnChip).not.toBeNull();
    expect(screen.queryByRole("list", { name: /badges/i })).toBeNull();
  });

  it("Scenario: unblocks computes from the cards the board holds", () => {
    // Given the snapshot
    // When the unblocks of KBN-TICKET are read
    // Then both dependent cards come back in board order
    expect(unblocks("KBN-TICKET", snapshot.cards).map((card) => card.id)).toEqual([
      "KBN-BOARD",
      "KBN-PARITY",
    ]);
    expect(unblocks("KBN-UI", snapshot.cards).map((card) => card.id)).toEqual(["KBN-EMBED"]);
    expect(unblocks("KBN-EXPORT", snapshot.cards)).toEqual([]);
  });
});
