// ThemePicker.test.tsx — the Settings picker: the mode segment and the family grid as radios,
// the swatch halves each variant's own tokens paint, a pick writing the pref and landing on
// <html>, and the arrows walking both groups.
import { act, fireEvent, render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { ThemePicker } from "../../src/app/ThemePicker.tsx";
import { stubMatchMedia } from "../helpers.tsx";

beforeEach(() => {
  localStorage.clear();
  stubMatchMedia(false);
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.documentElement.dataset.theme = "";
});

/** The family cards, in grid order. */
const cards = (): HTMLButtonElement[] => [...document.querySelectorAll<HTMLButtonElement>("[data-family]")];

/** One family's card. */
const card = (family: string): HTMLButtonElement =>
  document.querySelector<HTMLButtonElement>(`[data-family="${family}"]`) as HTMLButtonElement;

/** One mode's segment button. */
const mode = (m: string): HTMLButtonElement =>
  document.querySelector<HTMLButtonElement>(`[data-mode="${m}"]`) as HTMLButtonElement;

/** The mode segment's buttons, in order. */
const modes = (): HTMLButtonElement[] => [...document.querySelectorAll<HTMLButtonElement>("[data-mode]")];

describe("the picker", () => {
  it("offers the six families as cards with their variant sublines, the current one checked", () => {
    render(<ThemePicker />);
    expect(cards().map((c) => c.textContent)).toEqual([
      "CatppuccinMocha · Latte",
      "GruvboxDark · Light",
      "TokyoNight · Day",
      "Rosé PineMain · Dawn",
      "NordPolar · Snow",
      "SolarizedDark · Light",
    ]);
    expect(cards()[0]?.getAttribute("aria-checked")).toBe("true");
    expect(
      cards()
        .slice(1)
        .every((c) => c.getAttribute("aria-checked") === "false"),
    ).toBe(true);
    expect(screen.getByText("Saved on this device. Each paired device keeps its own theme.")).toBeDefined();
  });

  it("checks the one card and draws the check icon on it alone", () => {
    render(<ThemePicker />);
    const chosen = cards()[0] as HTMLButtonElement;
    expect(chosen.querySelector("svg")).not.toBeNull();
    expect((cards()[1] as HTMLButtonElement).querySelector("svg")).toBeNull();
    expect(chosen.className).toContain("border-primary");
  });

  it("paints each half with its variant's own tokens: base, ink bar, primary, success, warning", () => {
    render(<ThemePicker />);
    const dark = document.querySelector<HTMLElement>('[data-variant="gruvbox-dark"]');
    const bar = dark?.querySelector("span");
    expect(dark).not.toBeNull();
    expect(dark?.style.background).toBe("#32302f"); // gruvbox-dark base-100
    expect(bar).toBeDefined();
    expect((bar as HTMLElement).style.background).toBe("#ebdbb2"); // its ink
    expect([...(dark?.querySelectorAll("i") ?? [])].map((i) => (i as HTMLElement).style.background)).toEqual([
      "#83a598",
      "#b8bb26",
      "#fe8019",
    ]); // primary, success, warning
    const light = document.querySelector<HTMLElement>('[data-variant="gruvbox-light"]');
    expect(light).not.toBeNull();
    expect(light?.style.background).toBe("#fbf1c7");
  });

  it("offers System, Dark and Light in the segment, the current mode checked", () => {
    render(<ThemePicker />);
    expect(modes().map((m) => m.textContent)).toEqual(["System", "Dark", "Light"]);
    expect(modes().map((m) => m.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
  });
});

describe("picking", () => {
  it("choosing a card keeps the mode, writes the pref and lands the variant on <html>", async () => {
    render(<ThemePicker />);
    await act(async () => {
      fireEvent.click(card("tokyo"));
    });
    expect(localStorage.getItem("huddle:theme")).toBe('{"family":"tokyo","mode":"dark"}');
    expect(document.documentElement.dataset.theme).toBe("tokyo-night");
    expect(cards()[2]?.getAttribute("aria-checked")).toBe("true");
  });

  it("choosing a mode keeps the family and resolves system against the system's answer", async () => {
    render(<ThemePicker />);
    await act(async () => {
      fireEvent.click(card("gruvbox"));
    });
    await act(async () => {
      fireEvent.click(mode("light"));
    });
    expect(localStorage.getItem("huddle:theme")).toBe('{"family":"gruvbox","mode":"light"}');
    expect(document.documentElement.dataset.theme).toBe("gruvbox-light");
    await act(async () => {
      fireEvent.click(mode("system"));
    });
    expect(document.documentElement.dataset.theme).toBe("gruvbox-light"); // the stubbed system is light
  });
});

describe("the keyboard", () => {
  it("walks the family grid with the arrows, selecting and focusing as it goes", async () => {
    render(<ThemePicker />);
    const first = cards()[0] as HTMLButtonElement;
    first.focus();
    expect(document.activeElement).toBe(first);
    await act(async () => {
      fireEvent.keyDown(first, { key: "ArrowRight" });
    });
    const second = cards()[1] as HTMLButtonElement;
    expect(document.activeElement).toBe(second);
    expect(second.getAttribute("aria-checked")).toBe("true");
    expect(localStorage.getItem("huddle:theme")).toBe('{"family":"gruvbox","mode":"dark"}');
    await act(async () => {
      fireEvent.keyDown(second, { key: "ArrowLeft" });
    });
    expect((cards()[0] as HTMLButtonElement).getAttribute("aria-checked")).toBe("true");
    await act(async () => {
      fireEvent.keyDown(cards()[0] as HTMLButtonElement, { key: "ArrowUp" });
    });
    expect((cards()[5] as HTMLButtonElement).getAttribute("aria-checked")).toBe("true"); // wraps
    await act(async () => {
      fireEvent.keyDown(cards()[5] as HTMLButtonElement, { key: "End" });
    });
    expect(document.activeElement).toBe(cards()[5]);
    await act(async () => {
      fireEvent.keyDown(cards()[5] as HTMLButtonElement, { key: "Home" });
    });
    expect(document.activeElement).toBe(cards()[0]);
    expect((cards()[0] as HTMLButtonElement).getAttribute("aria-checked")).toBe("true");
  });

  it("walks the mode segment with the arrows", async () => {
    render(<ThemePicker />);
    const dark = modes()[1] as HTMLButtonElement;
    dark.focus();
    await act(async () => {
      fireEvent.keyDown(dark, { key: "ArrowRight" });
    });
    expect(localStorage.getItem("huddle:theme")).toBe('{"family":"catppuccin","mode":"light"}');
    expect(document.documentElement.dataset.theme).toBe("latte");
    expect(document.activeElement).toBe(modes()[2]);
  });

  it("keeps only the checked radio in the tab order", () => {
    render(<ThemePicker />);
    expect(cards().map((c) => c.tabIndex)).toEqual([0, -1, -1, -1, -1, -1]);
    expect(modes().map((m) => m.tabIndex)).toEqual([-1, 0, -1]);
  });
});
