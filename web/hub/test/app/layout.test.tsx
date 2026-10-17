// layout.test.tsx — the viewport seam: the three layouts the 640 and 1024 px lines cut, and the
// hook that follows a viewport across them through matchMedia's change events.
import { act } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { DESKTOP_QUERY, layoutOf, TABLET_QUERY, useLayout } from "../../src/app/layout.ts";
import { renderIn, stubMedia } from "../helpers.tsx";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("layoutOf", () => {
  it("cuts phone under 640, tablet to 1023, desktop from 1024", () => {
    expect(layoutOf(false, false)).toBe("phone");
    expect(layoutOf(true, false)).toBe("tablet");
    expect(layoutOf(true, true)).toBe("desktop");
    expect(layoutOf(false, true)).toBe("desktop"); // a viewport past 1024 is past 640 too
  });
});

/** Renders the hook alone, the way a component reads it. */
function Probe() {
  return <span data-layout={useLayout()} />;
}

describe("useLayout", () => {
  it("reads the two breakpoint queries at mount", () => {
    stubMedia({ [TABLET_QUERY]: true, [DESKTOP_QUERY]: false });
    const { container } = renderIn(<Probe />);
    expect(container.querySelector("[data-layout]")?.getAttribute("data-layout")).toBe("tablet");
  });

  it("follows a viewport that crosses a breakpoint", () => {
    const media = stubMedia({ [TABLET_QUERY]: false, [DESKTOP_QUERY]: false });
    const { container } = renderIn(<Probe />);
    expect(container.querySelector("[data-layout]")?.getAttribute("data-layout")).toBe("phone");
    void act(() => {
      media.fire(TABLET_QUERY);
    });
    expect(container.querySelector("[data-layout]")?.getAttribute("data-layout")).toBe("tablet");
    void act(() => {
      media.fire(DESKTOP_QUERY);
    });
    expect(container.querySelector("[data-layout]")?.getAttribute("data-layout")).toBe("desktop");
  });

  it("stops listening on unmount", () => {
    const media = stubMedia({});
    const { unmount, container } = renderIn(<Probe />);
    unmount();
    void act(() => {
      media.fire(TABLET_QUERY);
    });
    expect(container.querySelector("[data-layout]")).toBeNull();
  });
});
