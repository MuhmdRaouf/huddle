// Appearance.test.tsx — Settings › Appearance: the System, Light and Dark control checked as
// kept and light while the theme paints both sides, one preview card per registered theme with
// the kept one checked and "Dark only" on the one-sided themes, the pick that applies a theme
// at once (the key, the root and the status bar), a dark-only theme blocking Light, and the
// arrows that move and pick in both groups.

import { act, screen, within } from "@testing-library/preact";
import { THEMES } from "@huddle/ui/themes.ts";
import { afterEach, describe, expect, it } from "vite-plus/test";
import { stubMedia } from "../helpers.tsx";
import { renderHuddle } from "../render.tsx";
import { Appearance } from "../../src/app/Appearance.tsx";

describe("the Appearance page", () => {
  afterEach(() => {
    localStorage.clear();
    delete document.documentElement.dataset.theme;
    delete document.documentElement.dataset.scheme;
  });

  /** The two theme-color metas index.html ships, rebuilt clean for the test. */
  function metas(): HTMLMetaElement[] {
    for (const m of document.head.querySelectorAll('meta[name="theme-color"]')) m.remove();
    return (["dark", "light"] as const).map((s) => {
      const m = document.createElement("meta");
      m.name = "theme-color";
      m.media = `(prefers-color-scheme: ${s})`;
      m.content = "#000000";
      document.head.append(m);
      return m;
    });
  }

  /** Sends one arrow key to an element the way the keyboard does. */
  async function press(el: Element, key: string): Promise<void> {
    await act(async () => {
      el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    });
  }

  it("TestChecksSystemByDefaultAndOffersThreeSchemes", () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    renderHuddle(<Appearance />);
    const group = within(screen.getByRole("radiogroup", { name: "Appearance" }));
    const names = ["System", "Light", "Dark"];
    expect(group.getAllByRole("radio").map((r) => r.textContent?.trim())).toEqual(names);
    expect(
      names.map((n) => group.getByRole("radio", { name: n, exact: true }).getAttribute("aria-checked")),
    ).toEqual(["true", "false", "false"]);
    expect(group.getByRole("radio", { name: "Light", exact: true }).getAttribute("aria-disabled")).toBeNull();
    // one tab stop: the checked radio carries it
    expect(group.getAllByRole("radio").map((r) => r.getAttribute("tabindex"))).toEqual(["0", "-1", "-1"]);
    expect(screen.getByText("System follows this device's setting.")).toBeTruthy();
    expect(screen.queryByRole("switch")).toBeNull();
  });

  it("TestChecksTheKeptThemeAndNamesEveryRegisteredOne", () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    localStorage.setItem("huddle:theme", "nord");
    renderHuddle(<Appearance />);
    const group = within(screen.getByRole("radiogroup", { name: "Theme" }));
    const cards = group.getAllByRole("radio");
    expect(cards.map((c) => c.getAttribute("data-theme-value"))).toEqual(THEMES.map((t) => t.id));
    expect(
      cards
        .filter((c) => c.getAttribute("aria-checked") === "true")
        .map((c) => c.getAttribute("data-theme-value")),
    ).toEqual(["nord"]);
    expect(
      cards
        .filter((c) => (c.textContent ?? "").includes("Dark only"))
        .map((c) => c.getAttribute("data-theme-value")),
    ).toEqual(["monokai", "dracula", "one-dark"]);
    expect(screen.getByText(/Dark-only themes stay dark when Appearance is Light/)).toBeTruthy();
  });

  it("TestPickingACardAppliesTheThemeAtOnce", async () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    renderHuddle(<Appearance />);
    const ms = metas();
    await act(async () => {
      within(screen.getByRole("radiogroup", { name: "Theme" }))
        .getByRole("radio", { name: "Nord" })
        .click();
    });
    expect(localStorage.getItem("huddle:theme")).toBe("nord");
    expect(document.documentElement.dataset.theme).toBe("nord");
    // System over a two-sided theme keeps the OS-keyed pair
    expect(ms.map((m) => m.content)).toEqual(["#2e3440", "#e5e9f0"]);
    const nord = within(screen.getByRole("radiogroup", { name: "Theme" })).getByRole("radio", {
      name: "Nord",
    });
    expect(nord.getAttribute("aria-checked")).toBe("true");
  });

  it("TestPickingADarkOnlyThemeDisablesLight", async () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    renderHuddle(<Appearance />);
    const group = within(screen.getByRole("radiogroup", { name: "Appearance" }));
    const light = group.getByRole("radio", { name: "Light", exact: true });
    expect(light.getAttribute("aria-disabled")).toBeNull();
    await act(async () => {
      within(screen.getByRole("radiogroup", { name: "Theme" }))
        .getByRole("radio", { name: /Monokai/ })
        .click();
    });
    expect(document.documentElement.dataset.theme).toBe("monokai");
    expect(light.getAttribute("aria-disabled")).toBe("true");
    expect(light.className).toContain("opacity-50");
    // picking it does nothing: the scheme stays as it was and Monokai keeps painting dark
    await act(async () => {
      light.click();
    });
    expect(localStorage.getItem("huddle:scheme")).toBeNull();
    expect(document.documentElement.dataset.scheme).toBe("dark");
  });

  it("TestArrowsMoveThePick", async () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    renderHuddle(<Appearance />);
    const group = within(screen.getByRole("radiogroup", { name: "Appearance" }));
    await press(group.getByRole("radio", { name: "System", exact: true }), "ArrowRight");
    expect(group.getByRole("radio", { name: "Light", exact: true }).getAttribute("aria-checked")).toBe(
      "true",
    );
    expect(localStorage.getItem("huddle:scheme")).toBe("light");
    await press(group.getByRole("radio", { name: "Light", exact: true }), "ArrowRight");
    expect(group.getByRole("radio", { name: "Dark", exact: true }).getAttribute("aria-checked")).toBe("true");
    await press(group.getByRole("radio", { name: "Dark", exact: true }), "ArrowLeft");
    expect(group.getByRole("radio", { name: "Light", exact: true }).getAttribute("aria-checked")).toBe(
      "true",
    );
    const themes = within(screen.getByRole("radiogroup", { name: "Theme" }));
    await press(themes.getByRole("radio", { name: "Huddle" }), "ArrowDown");
    expect(themes.getByRole("radio", { name: "Catppuccin" }).getAttribute("aria-checked")).toBe("true");
    expect(localStorage.getItem("huddle:theme")).toBe("catppuccin");
  });

  it("TestPreviewsFollowTheDeviceWhileSystemHolds", () => {
    const media = stubMedia({ "(prefers-color-scheme: dark)": false });
    renderHuddle(<Appearance />);
    const side = (id: string): string | null =>
      document.querySelector(`[data-theme-value="${id}"] [data-theme]`)?.getAttribute("data-scheme") ?? null;
    expect(side("nord")).toBe("light");
    act(() => media.fire("(prefers-color-scheme: dark)"));
    // the device flipped while the page is open: the previews resolve the new side
    expect(side("nord")).toBe("dark");
    expect(side("monokai")).toBe("dark");
  });

  it("TestArrowsSkipABlockedLight", async () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    localStorage.setItem("huddle:theme", "monokai");
    renderHuddle(<Appearance />);
    const group = within(screen.getByRole("radiogroup", { name: "Appearance" }));
    await press(group.getByRole("radio", { name: "System", exact: true }), "ArrowRight");
    expect(group.getByRole("radio", { name: "Dark", exact: true }).getAttribute("aria-checked")).toBe("true");
    expect(group.getByRole("radio", { name: "Light", exact: true }).getAttribute("aria-checked")).toBe(
      "false",
    );
  });
});
