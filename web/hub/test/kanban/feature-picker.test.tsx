// feature-picker.test.tsx — the feature picker: a button naming the picked feature or All features,
// a list of the board's features with their counts and the closed ones read-only under them,
// driven from the keyboard, a sheet on a phone.
import { render, screen, within } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { FeaturePicker } from "../../src/kanban/components/FeaturePicker.tsx";
import { makeCard } from "./helpers.ts";
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
