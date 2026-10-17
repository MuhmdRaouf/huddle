// Icon.test.tsx — the icon's two special shapes: the logo, which is its own image, and the loud
// failure when a view reads radar's context without its provider.

import { render } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { AppContext, useApp } from "../../../src/radar/app/context.ts";
import { Icon } from "../../../src/radar/app/Icon.tsx";

function Probe(): null {
  useApp();
  return null;
}

describe("Icon", () => {
  it("renders the logo as its own image, labelled for the reader", () => {
    const { container } = render(<Icon name="logo" label="Radar" />);
    const image = container.querySelector("img[data-icon='logo']");
    expect(image).not.toBeNull();
    expect(image?.getAttribute("alt")).toBe("Radar");
    expect(image?.getAttribute("src")?.startsWith("data:image/svg+xml")).toBe(true);
  });
});

describe("useApp", () => {
  it("fails a view loudly when it reads radar's context without its provider", () => {
    let caught: string | null = null;
    try {
      render(
        <AppContext.Provider value={null}>
          <Probe />
        </AppContext.Provider>,
      );
    } catch (error) {
      caught = error instanceof Error ? error.message : String(error);
    }
    expect(caught).toBe("radar: useApp() outside <AppContext.Provider>");
  });
});
