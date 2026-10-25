// keyboard-hook.test.tsx — the keyboard inset a component reads through the hook: nothing in a
// browser without a visual viewport, the covered height once the visual viewport shrinks, and
// no listener left behind after the component is gone.

import { act, render } from "@testing-library/preact";
import { afterEach, describe, expect, it } from "vite-plus/test";
import { useKeyboardInset } from "../../src/chat/keyboard.ts";

function Probe() {
  return <output>{useKeyboardInset()}</output>;
}

type Listener = () => void;

/** A visual viewport the test drives: its height, and the resize listeners it holds. */
function fakeViewport(height: number) {
  const listeners = new Set<Listener>();
  const vv = {
    height,
    offsetTop: 0,
    addEventListener: (_: string, fn: Listener) => listeners.add(fn),
    removeEventListener: (_: string, fn: Listener) => listeners.delete(fn),
  };
  return { vv, listeners };
}

const original = Object.getOwnPropertyDescriptor(window, "visualViewport");

afterEach(() => {
  if (original) Object.defineProperty(window, "visualViewport", original);
  else Reflect.deleteProperty(window, "visualViewport");
});

describe("useKeyboardInset", () => {
  it("is nothing where the browser has no visual viewport", () => {
    Object.defineProperty(window, "visualViewport", { value: null, configurable: true });
    const { container } = render(<Probe />);
    expect(container.textContent).toBe("0");
  });

  it("follows the visual viewport as the keyboard opens, and lets go on unmount", async () => {
    const { vv, listeners } = fakeViewport(window.innerHeight);
    Object.defineProperty(window, "visualViewport", { value: vv, configurable: true });
    const { container, unmount } = render(<Probe />);
    expect(container.textContent).toBe("0");
    expect(listeners.size).toBeGreaterThan(0);

    vv.height = window.innerHeight - 300;
    await act(() => {
      listeners.forEach((fn) => fn());
    });
    expect(container.textContent).toBe("300");

    await act(() => {
      unmount();
    });
    expect(listeners.size).toBe(0);
  });
});
