// layout.test.tsx — the viewport seam: the three layouts the 768 and 1200 px lines cut, and the
// hook that follows a viewport across them through matchMedia's change events.
import { act } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { DESKTOP_QUERY, layoutOf, panesOf, TABLET_QUERY, useLayout } from "../../src/app/layout.ts";
import { renderIn, stubMedia } from "../helpers.tsx";

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Whether a "(min-width: N)" query in px or rem holds at a viewport width in px; a rem is 16 px in a media query. */
function holdsAt(query: string, width: number): boolean {
  const m = /^\(min-width: (\d+(?:\.\d+)?)(px|rem)\)$/.exec(query);
  if (m === null) throw new Error(`want a min-width query in px or rem, got ${query}`);
  return width >= Number(m[1]) * (m[2] === "rem" ? 16 : 1);
}

/** The layout a viewport width gets from the two queries. */
const layoutAt = (width: number) => layoutOf(holdsAt(TABLET_QUERY, width), holdsAt(DESKTOP_QUERY, width));

describe("layoutOf", () => {
  it("cuts phone under 768, tablet to 1199, desktop from 1200", () => {
    expect(layoutOf(false, false)).toBe("phone");
    expect(layoutOf(true, false)).toBe("tablet");
    expect(layoutOf(true, true)).toBe("desktop");
    expect(layoutOf(false, true)).toBe("desktop"); // a viewport past 1200 is past 768 too
  });

  it("puts 767 on the phone, 768 and 1199 on the tablet, 1200 on the desk", () => {
    expect(layoutAt(767)).toBe("phone");
    expect(layoutAt(768)).toBe("tablet");
    expect(layoutAt(1199)).toBe("tablet");
    expect(layoutAt(1200)).toBe("desktop");
  });
});

describe("panesOf", () => {
  it("is one on the phone and two on the tablet and the desktop", () => {
    expect(panesOf("phone")).toBe("one");
    expect(panesOf("tablet")).toBe("two");
    expect(panesOf("desktop")).toBe("two");
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
