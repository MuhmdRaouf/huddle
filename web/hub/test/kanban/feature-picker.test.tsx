// feature-picker.test.tsx — the feature picker: a button naming the picked feature or All features,
// a list of the board's features with their counts and the closed ones read-only under them,
// driven from the keyboard, a sheet on a phone. Beside it, the pane the picker lives in: who may
// close, and the note a picked word leaves under the head.
import { render, screen, within } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { HuddleContext } from "../../src/app/context.tsx";
import { FeaturePicker } from "../../src/kanban/components/FeaturePicker.tsx";
import { type Io } from "../../src/kanban/store.ts";
import { BoardPane } from "../../src/kanban/KanbanTab.tsx";
import type { Snapshot } from "../../src/kanban/types.ts";
import { makeCtx, makeState } from "../helpers.tsx";
import { clone, makeCard, makeSnapshot, ticketUi } from "./helpers.ts";
import { stubMatchMedia } from "./match-media.ts";

const AGO_2D = new Date(Date.now() - 48 * 3600 * 1000).toISOString();

const CARDS = [
  makeCard({ id: "F-A1", cluster: "alpha" }),
  makeCard({ id: "F-A2", cluster: "alpha" }),
  makeCard({ id: "F-B1", cluster: "beta" }),
  makeCard({ id: "F-N", cluster: "" }),
];

const CLOSED = [
  { name: "gamma", closedAt: AGO_2D, cards: 7 },
  { name: "delta", closedAt: "", cards: 3 },
];

afterEach(() => {
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

describe("Feature: the feature picker", () => {
  it("Scenario: the button names the picked feature, or All features", () => {
    // Given no feature picked, the button says All features
    const { unmount } = render(
      <FeaturePicker cards={CARDS} closed={CLOSED} feature={null} onFeature={() => undefined} />,
    );
    expect(screen.getByRole("button", { name: "Feature" }).textContent).toContain("All features");
    unmount();
    // Given alpha picked, the button names it
    render(<FeaturePicker cards={CARDS} closed={CLOSED} feature="alpha" onFeature={() => undefined} />);
    expect(screen.getByRole("button", { name: "Feature" }).textContent).toContain("alpha");
  });

  it("Scenario: the list holds All features, every feature by name with its count, and No feature", async () => {
    // Given a board with two features and one card with none, alpha picked
    const onFeature = vi.fn();
    render(<FeaturePicker cards={CARDS} closed={CLOSED} feature="alpha" onFeature={onFeature} />);
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // When the list is read
    const list = screen.getByRole("listbox", { name: "Features" });
    expect(list).toBeTruthy();
    // Then All features counts every card, the features follow sorted by name, No feature is last
    const options = screen.getAllByRole("option");
    expect(options.map((o) => o.getAttribute("aria-label"))).toEqual([
      "All features 4",
      "alpha 2",
      "beta 1",
      "No feature 1",
    ]);
    // And the picked one reads as selected
    expect(screen.getByRole("option", { name: "alpha 2" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("option", { name: "beta 1" }).getAttribute("aria-selected")).toBe("false");
  });

  it("Scenario: picking hands the feature word up — a name, No feature as -, All features as null", async () => {
    // Given the open picker
    const onFeature = vi.fn();
    render(<FeaturePicker cards={CARDS} closed={CLOSED} feature={null} onFeature={onFeature} />);
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // When a feature is picked, then No feature, then All features
    await userEvent.click(screen.getByRole("option", { name: "beta 1" }));
    await userEvent.click(screen.getByRole("option", { name: "No feature 1" }));
    await userEvent.click(screen.getByRole("option", { name: "All features 4" }));
    // Then each pick hands its word up
    expect(onFeature).toHaveBeenNthCalledWith(1, "beta");
    expect(onFeature).toHaveBeenNthCalledWith(2, "-");
    expect(onFeature).toHaveBeenNthCalledWith(3, null);
  });

  it("Scenario: No feature stands in the list only when a card carries none", async () => {
    // Given cards that all carry a feature
    const clustered = CARDS.filter((c) => c.cluster !== "");
    render(<FeaturePicker cards={clustered} closed={CLOSED} feature={null} onFeature={() => undefined} />);
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // Then No feature is not offered
    expect(screen.queryByRole("option", { name: /^No feature/ })).toBeNull();
  });

  it("Scenario: the keyboard walks the list, Enter picks, Escape closes to the button", async () => {
    // Given the open picker with alpha picked, the focus starts on the picked entry
    const onFeature = vi.fn();
    render(<FeaturePicker cards={CARDS} closed={CLOSED} feature="alpha" onFeature={onFeature} />);
    const button = screen.getByRole("button", { name: "Feature" });
    await userEvent.click(button);
    await vi.waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("option", { name: "alpha 2" })),
    );
    // When the arrow walks down and Enter takes it
    await userEvent.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(screen.getByRole("option", { name: "beta 1" }));
    await userEvent.keyboard("{Enter}");
    // Then beta is handed up and the popover is gone
    expect(onFeature).toHaveBeenCalledWith("beta");
    expect(screen.queryByRole("listbox", { name: "Features" })).toBeNull();
    // Given the picker open again, when Escape is pressed
    await userEvent.click(button);
    await vi.waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("option", { name: "alpha 2" })),
    );
    await userEvent.keyboard("{Escape}");
    // Then the popover is gone and the button holds the focus
    expect(screen.queryByRole("listbox", { name: "Features" })).toBeNull();
    expect(document.activeElement).toBe(button);
  });

  it("Scenario: the arrows walk the whole list, Home takes the first, End the last, a stranger key leaves the walk alone", async () => {
    // Given the open picker with alpha picked, the focus on the picked entry
    render(<FeaturePicker cards={CARDS} closed={[]} feature="alpha" onFeature={() => undefined} />);
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    await vi.waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("option", { name: "alpha 2" })),
    );
    // When the walk steps up, then home, then end
    await userEvent.keyboard("{ArrowUp}");
    expect(document.activeElement).toBe(screen.getByRole("option", { name: "All features 4" }));
    await userEvent.keyboard("{Home}");
    expect(document.activeElement).toBe(screen.getByRole("option", { name: "All features 4" }));
    await userEvent.keyboard("{End}");
    expect(document.activeElement).toBe(screen.getByRole("option", { name: "No feature 1" }));
    // And a key the walk does not know leaves the focus where it stands
    await userEvent.keyboard("{PageUp}");
    expect(document.activeElement).toBe(screen.getByRole("option", { name: "No feature 1" }));
  });

  it("Scenario: an arrow from nowhere — the focus off every entry — walks from the top", async () => {
    // Given the open picker and the focus fallen off the list
    render(<FeaturePicker cards={CARDS} closed={[]} feature="alpha" onFeature={() => undefined} />);
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    await vi.waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("option", { name: "alpha 2" })),
    );
    (document.activeElement as HTMLElement).blur();
    // When the key lands on the list itself
    screen
      .getByRole("listbox", { name: "Features" })
      .dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }));
    // Then the walk starts from nowhere and takes the first entry
    expect(document.activeElement).toBe(screen.getByRole("option", { name: "All features 4" }));
  });

  it("Scenario: on a phone the same list is a sheet titled Feature", async () => {
    // Given a phone pointer
    stubMatchMedia((query) => query.includes("max-width"));
    const onFeature = vi.fn();
    render(<FeaturePicker cards={CARDS} closed={CLOSED} feature={null} onFeature={onFeature} />);
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // When the sheet is read
    const sheet = await vi.waitFor(() => screen.getByRole("dialog", { name: "Feature" }));
    const options = within(sheet).getAllByRole("option");
    // Then it lists the same rows in the same order
    expect(options.map((o) => o.getAttribute("aria-label"))).toEqual([
      "All features 4",
      "alpha 2",
      "beta 1",
      "No feature 1",
    ]);
    // When a row is picked, the word is handed up and the sheet is gone
    await userEvent.click(within(sheet).getByRole("option", { name: "beta 1" }));
    expect(onFeature).toHaveBeenCalledWith("beta");
    await vi.waitFor(() => expect(screen.queryByRole("dialog", { name: "Feature" })).toBeNull());
  });

  it("Scenario: on a phone the owner's Close and Reopen ride in the sheet", async () => {
    // Given a phone pointer and the owner's powers
    stubMatchMedia((query) => query.includes("max-width"));
    render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature={null}
        onFeature={() => undefined}
        canClose
        boardKey="k"
        toast={() => undefined}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // Then the sheet carries a Close beside the open rows and a Reopen beside the closed ones
    const sheet = await vi.waitFor(() => screen.getByRole("dialog", { name: "Feature" }));
    expect(within(sheet).getByRole("button", { name: "Close alpha" })).toBeTruthy();
    expect(within(sheet).getByRole("button", { name: "Reopen gamma" })).toBeTruthy();
  });
});

describe("Feature: the closed features", () => {
  it("Scenario: the closed list is read-only, each row its name, its ago and its card count", async () => {
    // Given the open picker over a board with two closed features
    render(<FeaturePicker cards={CARDS} closed={CLOSED} feature={null} onFeature={() => undefined} />);
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // When the Closed group is read
    const group = screen.getByRole("list", { name: "Closed" });
    const rows = within(group).getAllByRole("listitem");
    // Then it keeps the snapshot's order, each row two lines: the name, then Closed ago · cards
    expect(rows.map((r) => r.textContent)).toEqual([
      "gammaClosed 2 d ago · 7 cards",
      "deltaClosed — · 3 cards",
    ]);
    // And the rows are not picks: no button, no option, nothing in the feature list
    expect(within(group).queryAllByRole("button")).toEqual([]);
    expect(within(group).queryAllByRole("option")).toEqual([]);
    const list = screen.getByRole("listbox", { name: "Features" });
    expect(within(list).queryByText("gamma")).toBeNull();
  });

  it("Scenario: no closed features, no Closed group", async () => {
    // Given a board with nothing closed
    render(<FeaturePicker cards={CARDS} closed={[]} feature={null} onFeature={() => undefined} />);
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // Then the Closed group is not on the page
    expect(screen.queryByRole("list", { name: "Closed" })).toBeNull();
  });
});

describe("Feature: closing and reopening", () => {
  /** Fakes fetch for a feature action, recording url and init; answers `body` unless over says no. */
  function stubPost(
    body: unknown,
    over: { ok?: boolean; status?: number } = {},
  ): { url: string; init: RequestInit | undefined }[] {
    const calls: { url: string; init: RequestInit | undefined }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: URL | string, init?: RequestInit) => {
        calls.push({ url: String(input), init });
        return { ok: over.ok ?? true, status: over.status ?? 200, json: async () => body };
      }),
    );
    return calls;
  }

  it("Scenario: the owner sees Close on every open feature row and Reopen on every closed one", async () => {
    // Given the picker opened by the owner
    render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature={null}
        onFeature={() => undefined}
        canClose
        boardKey="k"
        toast={() => undefined}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // Then every open row carries its Close and every closed row its Reopen
    expect(screen.getByRole("button", { name: "Close alpha" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Close beta" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Reopen gamma" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Reopen delta" })).toBeTruthy();
    // And All features and No feature carry no action of their own
    expect(screen.queryByRole("button", { name: "Close All features" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Close No feature" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reopen All features" })).toBeNull();
    // And both actions are the design system's Button, ghost like the board's other quiet ones
    const closeAlpha = screen.getByRole("button", { name: "Close alpha" });
    expect(closeAlpha.classList.contains("ui-btn")).toBe(true);
    expect(closeAlpha.classList.contains("ui-btn-ghost")).toBe(true);
    const reopenGamma = screen.getByRole("button", { name: "Reopen gamma" });
    expect(reopenGamma.classList.contains("ui-btn")).toBe(true);
    expect(reopenGamma.classList.contains("ui-btn-ghost")).toBe(true);
  });

  it("Scenario: without the owner's powers the picker shows no action button", async () => {
    // Given the picker opened without canClose, the board key and the toast at hand all the same
    render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature={null}
        onFeature={() => undefined}
        boardKey="k"
        toast={() => undefined}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // Then no Close and no Reopen anywhere: the closed rows stay read-only
    expect(screen.queryByRole("button", { name: "Close alpha" })).toBeNull();
    const group = screen.getByRole("list", { name: "Closed" });
    expect(within(group).queryAllByRole("button")).toEqual([]);
  });

  it("Scenario: Close posts the action under the board's key and toasts the answer's first line", async () => {
    // Given the owner's picker open and a server answering two lines
    const onFeature = vi.fn();
    const toast = vi.fn();
    const calls = stubPost({ lines: ["Closed alpha: 2 cards archived", "a second line"] });
    render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature={null}
        onFeature={onFeature}
        canClose
        boardKey="k"
        toast={toast}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // When Close is taken on alpha
    await userEvent.click(screen.getByRole("button", { name: "Close alpha" }));
    // Then the post rides the board's key with the action and the feature, and the first line toasts
    await vi.waitFor(() => expect(toast).toHaveBeenCalledWith("Closed alpha: 2 cards archived"));
    expect(calls).toEqual([
      {
        url: "/api/kanban/b/k/feature",
        init: {
          method: "POST",
          headers: {
            "content-type": "application/json",
            "x-huddle-action": "/api/kanban/b/k/feature",
          },
          body: JSON.stringify({ action: "close", feature: "alpha" }),
        },
      },
    ]);
    // And the board word is not handed up: the next delta moves the rows, not the post
    expect(onFeature).not.toHaveBeenCalled();
  });

  it("Scenario: Reopen posts the reopen action and toasts the answer's line", async () => {
    const toast = vi.fn();
    const calls = stubPost({ lines: ["Reopened gamma: 7 cards back"] });
    render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature={null}
        onFeature={() => undefined}
        canClose
        boardKey="b2"
        toast={toast}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    await userEvent.click(screen.getByRole("button", { name: "Reopen gamma" }));
    await vi.waitFor(() => expect(toast).toHaveBeenCalledWith("Reopened gamma: 7 cards back"));
    expect(calls[0]?.url).toBe("/api/kanban/b/b2/feature");
    expect(calls[0]?.init?.body).toBe(JSON.stringify({ action: "reopen", feature: "gamma" }));
  });

  it("Scenario: a refusal toasts the error as bad news and hands no word up", async () => {
    // Given a server that refuses the close, as it does a member's
    const onFeature = vi.fn();
    const toast = vi.fn();
    stubPost({ error: "only the owner closes or reopens a feature" }, { ok: false, status: 403 });
    render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature={null}
        onFeature={onFeature}
        canClose
        boardKey="k"
        toast={toast}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    await userEvent.click(screen.getByRole("button", { name: "Close alpha" }));
    // Then the error toasts as bad news and the picked feature stands
    await vi.waitFor(() =>
      expect(toast).toHaveBeenCalledWith("only the owner closes or reopens a feature", { bad: true }),
    );
    expect(onFeature).not.toHaveBeenCalled();
  });

  it("Scenario: closing the picked feature hands All features up", async () => {
    // Given alpha picked and the picker open
    const onFeature = vi.fn();
    stubPost({ lines: ["Closed alpha: 2 cards archived"] });
    render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature="alpha"
        onFeature={onFeature}
        canClose
        boardKey="k"
        toast={() => undefined}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // When its own Close is taken and the server answers ok
    await userEvent.click(screen.getByRole("button", { name: "Close alpha" }));
    // Then the address moves to All features
    await vi.waitFor(() => expect(onFeature).toHaveBeenCalledWith(null));
  });

  it("Scenario: the keyboard walk skips the action buttons and Enter still picks an option", async () => {
    // Given the owner's picker open with alpha picked, the focus on the picked option
    const onFeature = vi.fn();
    render(
      <FeaturePicker
        cards={CARDS}
        closed={[]}
        feature="alpha"
        onFeature={onFeature}
        canClose
        boardKey="k"
        toast={() => undefined}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    await vi.waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("option", { name: "alpha 2" })),
    );
    // When the arrow walks down past alpha's Close button
    await userEvent.keyboard("{ArrowDown}");
    // Then it lands on the beta option, and Enter picks it
    expect(document.activeElement).toBe(screen.getByRole("option", { name: "beta 1" }));
    await userEvent.keyboard("{Enter}");
    expect(onFeature).toHaveBeenCalledWith("beta");
  });

  it("Scenario: with no board key, or no toast, the action is a quiet no-op", async () => {
    // Given the owner's powers but neither the board's key nor the toast — the standalone page
    const calls = stubPost({ lines: ["Closed alpha: 2 cards archived"] });
    const first = render(
      <FeaturePicker cards={CARDS} closed={CLOSED} feature={null} onFeature={() => undefined} canClose />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    await userEvent.click(screen.getByRole("button", { name: "Close alpha" }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    // Then nothing is posted
    expect(calls).toEqual([]);
    first.unmount();
    // Given the board's key but no toast, the same silence
    render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature={null}
        onFeature={() => undefined}
        canClose
        boardKey="k"
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    await userEvent.click(screen.getByRole("button", { name: "Close alpha" }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(calls).toEqual([]);
  });

  it("Scenario: a bare ok answer toasts the plain Closed or Reopened line", async () => {
    // Given a server that answers ok with no lines at all
    const toast = vi.fn();
    stubPost({ lines: [] });
    const first = render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature={null}
        onFeature={() => undefined}
        canClose
        boardKey="k"
        toast={toast}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    // When Close is taken, the plain line names the action and the feature
    await userEvent.click(screen.getByRole("button", { name: "Close alpha" }));
    await vi.waitFor(() => expect(toast).toHaveBeenCalledWith("Closed alpha"));
    first.unmount();
    // And a reopen of a closed feature reads the same way
    render(
      <FeaturePicker
        cards={CARDS}
        closed={CLOSED}
        feature={null}
        onFeature={() => undefined}
        canClose
        boardKey="k"
        toast={toast}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Feature" }));
    await userEvent.click(screen.getByRole("button", { name: "Reopen gamma" }));
    await vi.waitFor(() => expect(toast).toHaveBeenCalledWith("Reopened gamma"));
  });
});

describe("Feature: the pane around the picker", () => {
  const NOTE_CARDS = [makeCard({ id: "N-1", cluster: "alpha" }), makeCard({ id: "N-2", cluster: "beta" })];

  /** The pane's io: the snapshot answers at once, the stream never opens, the ticket is the fixture's. */
  function paneIo(snap: Snapshot): Io {
    return {
      fetchSnapshot: async () => clone(snap),
      fetchTicket: async () => clone(ticketUi),
      connectEvents: () => ({ close: () => undefined }),
      setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
      clearTimeout: (handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>),
    };
  }

  /** The pane over one board with `feature` in the address, read by the huddle kind `kind`. */
  function pane(kind: string, feature: string | null, snap: Snapshot): { unmount: () => void } {
    return render(
      <HuddleContext.Provider value={makeCtx(makeState(), { who: { kind } })}>
        <BoardPane
          io={paneIo(snap)}
          picker={{
            boards: [{ key: "k", root: "/repo/k", name: "k" }],
            boardKey: "k",
            onBoard: () => undefined,
            feature,
            onFeature: () => undefined,
          }}
        />
      </HuddleContext.Provider>,
    );
  }

  /** The picker's trigger, the listbox one — the toolbar's cluster chip is a menu also named Feature. */
  function trigger(): HTMLElement {
    const one = screen
      .getAllByRole("button", { name: "Feature" })
      .find((button) => button.getAttribute("aria-haspopup") === "listbox");
    if (one === undefined) throw new Error("the pane's feature trigger is not on the page");
    return one;
  }

  it("Scenario: the owner and the board's device may close; a member and the standalone page may not", async () => {
    // Given each kind reading the board, when the picker opens
    const snap = makeSnapshot(NOTE_CARDS);
    for (const kind of ["owner", "device", "member", ""]) {
      const page = pane(kind, null, snap);
      await userEvent.click(trigger());
      await vi.waitFor(() => expect(screen.queryByRole("option", { name: "alpha 1" })).not.toBeNull());
      // Then only the owner's and the device's list carries a Close
      const close = screen.queryByRole("button", { name: "Close alpha" });
      if (kind === "owner" || kind === "device") expect(close).toBeTruthy();
      else expect(close).toBeNull();
      page.unmount();
    }
  });

  it("Scenario: a closed word in the address says so, an unknown one says no card carries it, a known one says nothing", async () => {
    // Given a board with alpha and beta open and gamma closed
    const snap = makeSnapshot(NOTE_CARDS, {
      features: { closed: [{ name: "gamma", closedAt: AGO_2D, cards: 7 }] },
    });
    // When gamma is the word, the note under the head says the feature is closed
    const closed = pane("member", "gamma", snap);
    expect(await screen.findByText("gamma is closed.")).toBeTruthy();
    closed.unmount();
    // When a word no card carries, the note says so
    const unknown = pane("member", "zeta", snap);
    expect(await screen.findByText("No cards in zeta.")).toBeTruthy();
    unknown.unmount();
    // When a word the board knows and keeps open, the head says nothing
    const known = pane("member", "alpha", snap);
    await userEvent.click(trigger());
    await vi.waitFor(() => expect(screen.queryByRole("option", { name: "alpha 1" })).not.toBeNull());
    expect(screen.queryByText("gamma is closed.")).toBeNull();
    expect(screen.queryByText("No cards in zeta.")).toBeNull();
    known.unmount();
  });
});
