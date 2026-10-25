// TopBar.test.tsx — the desktop top bar: the jump field with its ⌘K hint, the machines line with
// its live dot, the theme menu, the shortcuts key, and the server's version the sidebar's brand reads.
import { act, render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { DesktopBar, MachinesLine, ThemeMenu, useVersion } from "../../src/app/TopBar.tsx";
import { flush, legacyClasses, stubMatchMedia } from "../helpers.tsx";

beforeEach(() => {
  localStorage.clear();
  stubMatchMedia(false);
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ json: () => Promise.resolve({ version: "1.2.3" }) })),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the server's version", () => {
  it("shows the server's version once /health answers, and the placeholder when it fails", async () => {
    const probe = render(<VersionProbe />);
    await flush();
    expect(probe.container.textContent).toContain("1.2.3");
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new Error("down"))),
    );
    const fallback = render(<VersionProbe />);
    await flush();
    expect(fallback.container.textContent).toContain("0.0.1");
  });
});

/** The hook alone, so the /health read is testable without the whole bar. */
function VersionProbe() {
  return <span>{useVersion()}</span>;
}

describe("the jump field", () => {
  it("names what it does, spells the shortcut and opens the palette", () => {
    const onPalette = vi.fn();
    render(<DesktopBar live="live" onPalette={onPalette} />);
    const jump = screen.getByRole("button", { name: "Jump to a session" });
    expect(jump.getAttribute("aria-keyshortcuts")).toBe("Meta+K Control+K /");
    expect(jump.querySelector("kbd")?.textContent).toMatch(/⌘K|Ctrl K/);
    void act(() => {
      (jump as HTMLElement).click();
    });
    expect(onPalette).toHaveBeenCalledTimes(1);
  });
});

describe("the machines line", () => {
  it("answers its label and carries the live dot's state", () => {
    const { rerender } = render(<MachinesLine live="live" />);
    const line = screen.getByLabelText("Machines");
    const dot = (): string => line.querySelector("[data-live]")?.className ?? "";
    expect(dot()).toContain("bg-[var(--ui-ok)]");
    expect(line.textContent).toContain("This machine");
    rerender(<MachinesLine live="offline" />);
    expect(dot()).toContain("bg-[var(--ui-warn)]");
    rerender(<MachinesLine live="connecting" />);
    expect(dot()).toContain("bg-[var(--ui-idle)]");
  });
});

describe("the theme menu", () => {
  it("opens with the three choices, checks the kept one, and keeps a pick", () => {
    localStorage.setItem("huddle:scheme", "dark");
    render(<ThemeMenu />);
    void act(() => {
      (screen.getByLabelText("Theme: Dark") as HTMLButtonElement).click();
    });
    const menu = document.querySelector('[role="menu"]') as HTMLElement;
    expect(menu.textContent).toContain("System");
    expect(menu.textContent).toContain("Dark");
    expect(menu.textContent).toContain("Light");
    expect(menu.querySelector('[aria-checked="true"]')?.textContent).toContain("Dark");
    void act(() => {
      [...menu.querySelectorAll("button")].find((b) => b.textContent?.includes("Light"))?.click();
    });
    expect(localStorage.getItem("huddle:scheme")).toBe("light");
    expect(document.documentElement.dataset.scheme).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("latte");
    expect(screen.getByLabelText("Theme: Light")).toBeDefined();
    expect(document.querySelector('[role="menu"]')).toBeNull();
  });

  it("shows System when nothing is kept, and a choice Settings made since on the next open", () => {
    render(<ThemeMenu />);
    expect(screen.getByLabelText("Theme: System")).toBeDefined();
    localStorage.setItem("huddle:scheme", "light");
    void act(() => {
      (screen.getByLabelText("Theme: System") as HTMLButtonElement).click();
    });
    expect(screen.getByLabelText("Theme: Light")).toBeDefined();
  });

  it("paints System as the scheme the OS prefers", () => {
    localStorage.setItem("huddle:scheme", "light");
    stubMatchMedia(true);
    render(<ThemeMenu />);
    void act(() => {
      (screen.getByLabelText("Theme: Light") as HTMLButtonElement).click();
    });
    void act(() => {
      [...(document.querySelector('[role="menu"]')?.querySelectorAll("button") ?? [])]
        .find((b) => b.textContent?.includes("System"))
        ?.click();
    });
    expect(localStorage.getItem("huddle:scheme")).toBe("system");
    expect(document.documentElement.dataset.scheme).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("mocha");
  });
});

describe("the bar's keys", () => {
  it("runs the shortcuts key", () => {
    const onHelp = vi.fn();
    render(<DesktopBar live="live" onHelp={onHelp} />);
    void act(() => {
      screen.getByLabelText("Keyboard shortcuts").click();
    });
    expect(onHelp).toHaveBeenCalledTimes(1);
  });
});

describe("the tokens", () => {
  it("paints the bar with the ui buttons and no daisyUI or legacy class", () => {
    render(<DesktopBar live="live" />);
    const daisy = /^(status(-.+)?|kbd(-.+)?)$/;
    const all = [...document.querySelectorAll("[class]")].flatMap((el) =>
      (el.getAttribute("class") ?? "").split(/\s+/),
    );
    expect(legacyClasses()).toEqual([]);
    expect(all.filter((c) => daisy.test(c))).toEqual([]);
    for (const name of [/^Theme:/, "Keyboard shortcuts"])
      expect(screen.getByRole("button", { name }).className).toContain("ui-btn ui-btn-ghost ui-btn-icon");
  });
});
