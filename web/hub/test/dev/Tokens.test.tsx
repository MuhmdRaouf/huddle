import { fireEvent, render, screen, waitFor } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import type { SchemeQuery, SchemeStore } from "@huddle/ui/theme.ts";
import { Tokens } from "../../src/dev/Tokens.tsx";

/** An in-memory scheme store. */
function memory(): SchemeStore & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return { data, getItem: (k) => data.get(k) ?? null, setItem: (k, v) => void data.set(k, v) };
}

/** A prefers-color-scheme query fixed at `dark`, counting its listeners. */
function query(dark: boolean): SchemeQuery & { listeners: number } {
  const q = {
    matches: dark,
    listeners: 0,
    addEventListener: () => {
      q.listeners += 1;
    },
    removeEventListener: () => {
      q.listeners -= 1;
    },
  };
  return q;
}

const setup = (dark = true) => {
  const store = memory();
  const q = query(dark);
  const root = document.createElement("html");
  const view = render(<Tokens store={store} query={q} root={root} />);
  return { store, q, root, view };
};

describe("Tokens", () => {
  it("renders every component block under the page title", () => {
    setup();
    expect(screen.getByRole("heading", { level: 1, name: "Design tokens" })).toBeTruthy();
    for (const name of [
      "Colors",
      "Type",
      "Spacing",
      "Buttons",
      "Badges",
      "Card",
      "List",
      "Tabs",
      "Table",
      "Field",
      "Switch",
      "Chart frame",
      "Sheet",
      "Dialog",
    ])
      expect(screen.getByRole("heading", { level: 2, name })).toBeTruthy();
    expect(screen.getByRole("table", { name: "Cost by model" }).textContent).toContain("$6.10");
    expect(screen.getByRole("img", { name: /Tokens per hour/ }).querySelectorAll("rect")).toHaveLength(12);
  });

  it("shows the mono type step, the scrim, the sheet shadow and accent-soft", () => {
    setup();
    const mono = screen.getByText("huddle provider zai setup");
    expect(mono.classList.contains("ui-mono")).toBe(true);
    expect(mono.getAttribute("style")).toContain("var(--ui-text-mono)");
    for (const name of ["--ui-accent-soft", "--ui-scrim", "--ui-shadow-sheet"])
      expect(screen.getByText(name)).toBeTruthy();
  });

  it("shows the hero card and the sheet's grab handle", () => {
    setup();
    expect(document.querySelector(".ui-card.ui-card-lg")).toBeTruthy();
    const handle = document.querySelector(".ui-sheet-handle");
    expect(handle).toBeTruthy();
    expect(handle?.getAttribute("aria-hidden")).toBe("true");
  });

  it("follows the OS, and the switch overrides it, keeps it and presses its button", () => {
    const { store, root } = setup(true);
    expect(root.dataset.scheme).toBe("dark");
    expect(screen.getByRole("button", { name: "System" }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "Light" }));
    expect(root.dataset.scheme).toBe("light");
    expect(store.data.get("huddle:scheme")).toBe("light");
    expect(screen.getByRole("button", { name: "Light" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "System" }).getAttribute("aria-pressed")).toBe("false");
  });

  it("stops following and clears the mark when it leaves", async () => {
    const { q, root, view } = setup(false);
    expect(q.listeners).toBe(1);
    expect(root.dataset.scheme).toBe("light");
    view.unmount();
    // preact runs an unmounted tree's effect cleanups after the frame, not inside unmount()
    await waitFor(() => expect(q.listeners).toBe(0));
    expect(root.dataset.scheme).toBeUndefined();
  });

  it("picks a tab, a list row and a field edit, and opens and closes the sheet", () => {
    const { view } = setup();
    fireEvent.click(screen.getByRole("tab", { name: "Week" }));
    expect(screen.getByRole("tabpanel").textContent).toBe("Showing one week.");
    fireEvent.click(screen.getByRole("button", { name: /docs-writer/ }));
    expect(screen.getByText("picked")).toBeTruthy();
    const input = screen.getByRole("textbox", { name: "Channel name" }) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "api" } });
    expect(input.value).toBe("api");
    const dlg = view.baseElement.querySelector("dialog") as HTMLDialogElement;
    fireEvent.click(screen.getByRole("button", { name: "Open sheet" }));
    expect(dlg.open).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(dlg.open).toBe(false);
  });
});
