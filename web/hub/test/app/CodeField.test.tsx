// CodeField.test.tsx — the six-box field's own behaviour: typing auto-advances and backspace
// steps back, a paste fills from the first box wherever it lands, the sixth digit submits the
// code by itself and only once, and a refused code clears the field and refocuses its start.

import { cleanup, fireEvent, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { CodeField } from "../../src/app/CodeField.tsx";

/** One box of the field, failing the test when it is not there. */
const at = (boxes: HTMLInputElement[], i: number): HTMLInputElement => {
  const el = boxes[i];
  if (!el) throw new Error(`no box ${i}`);
  return el;
};

/** Renders the field and answers its box inputs in order. */
function mount(over: { error?: string; busy?: boolean } = {}): HTMLInputElement[] {
  cleanup();
  render(<CodeField onCode={() => {}} error={over.error ?? ""} busy={over.busy ?? false} />);
  return screen.getAllByRole("textbox").map((el) => el as HTMLInputElement);
}

/** Types one character into a box the way a keyboard does. */
const type = (box: HTMLInputElement, ch: string): void => {
  fireEvent.input(box, { target: { value: ch } });
};

/** Pastes one string into a box: wherever it lands, the field fills from its first box. */
const paste = (box: HTMLInputElement, text: string): void => {
  fireEvent.paste(box, { clipboardData: { getData: () => text } });
};

/** The digits the boxes hold, as one word. */
const word = (boxes: HTMLInputElement[]): string[] => boxes.map((b) => b.value);

afterEach(cleanup);

describe("the code field", () => {
  it("TestAdvancesAsDigitsArriveAndStepsBackOnBackspace", () => {
    const boxes = mount();
    type(at(boxes, 0), "1");
    expect(at(boxes, 1)).toBe(document.activeElement);
    type(at(boxes, 1), "2");
    type(at(boxes, 2), "3");
    expect(word(boxes)).toEqual(["1", "2", "3", "", "", ""]);
    fireEvent.keyDown(at(boxes, 3), { key: "Backspace" });
    expect(word(boxes)).toEqual(["1", "2", "", "", "", ""]);
    expect(at(boxes, 2)).toBe(document.activeElement);
  });

  it("TestAPasteFillsFromTheFirstBox", () => {
    const boxes = mount();
    paste(at(boxes, 3), "654321");
    expect(word(boxes)).toEqual(["6", "5", "4", "3", "2", "1"]);
    expect(at(boxes, 5)).toBe(document.activeElement);
  });

  it("TestTheSixthDigitSubmitsByItself", () => {
    cleanup();
    const onCode = vi.fn();
    render(<CodeField onCode={onCode} />);
    const boxes = screen.getAllByRole("textbox").map((el) => el as HTMLInputElement);
    for (const [i, ch] of ["1", "2", "3", "4", "5"].entries()) {
      type(at(boxes, i), ch ?? "");
    }
    expect(onCode).not.toHaveBeenCalled();
    type(at(boxes, 5), "6");
    expect(onCode).toHaveBeenCalledTimes(1);
    expect(onCode).toHaveBeenCalledWith("123456");
  });

  it("TestARefusedCodeClearsAndRefocusesTheFirstBox", () => {
    cleanup();
    const view = render(<CodeField onCode={() => {}} error="" />);
    paste(
      at(
        screen.getAllByRole("textbox").map((el) => el as HTMLInputElement),
        0,
      ),
      "111111",
    );
    view.rerender(<CodeField onCode={() => {}} error="That code did not work." />);
    const boxes = screen.getAllByRole("textbox").map((el) => el as HTMLInputElement);
    expect(word(boxes)).toEqual(["", "", "", "", "", ""]);
    expect(screen.getByText("That code did not work.")).toBeDefined();
    expect(at(boxes, 0)).toBe(document.activeElement);
  });
});

describe("the code field's edges", () => {
  it("TestADigitBoxKeepsWhatIsNotADigit", () => {
    const boxes = mount();
    type(at(boxes, 0), "a");
    type(at(boxes, 1), "-");
    expect(word(boxes)).toEqual(["", "", "", "", "", ""]);
  });

  it("TestAPasteLongerThanTheFieldClampsAtSix", () => {
    const onCode = vi.fn();
    cleanup();
    render(<CodeField onCode={onCode} error="" />);
    const boxes = screen.getAllByRole("textbox").map((el) => el as HTMLInputElement);
    paste(at(boxes, 2), "12345678");
    expect(word(boxes)).toEqual(["1", "2", "3", "4", "5", "6"]);
    expect(onCode).toHaveBeenCalledWith("123456");
  });

  it("TestTheArrowsStopAtEitherEnd", () => {
    const boxes = mount();
    at(boxes, 0).focus();
    fireEvent.keyDown(at(boxes, 0), { key: "ArrowLeft" });
    expect(at(boxes, 0)).toBe(document.activeElement);
    at(boxes, 5).focus();
    fireEvent.keyDown(at(boxes, 5), { key: "ArrowRight" });
    expect(at(boxes, 5)).toBe(document.activeElement);
  });

  it("TestAPasteWithNothingOnTheClipboardChangesNothing", () => {
    const boxes = mount();
    type(at(boxes, 0), "9");
    fireEvent.paste(at(boxes, 0), {});
    expect(word(boxes)).toEqual(["9", "", "", "", "", ""]);
  });
});

describe("the code field's tokens", () => {
  it("paints the six boxes as ui fields and the refusal in the fail ink", () => {
    const boxes = mount({ error: "That code did not work." });
    for (const box of boxes) {
      expect(box.className.split(/\s+/)).toContain("ui-input");
      expect(box.className.split(/\s+/)).not.toContain("input");
    }
    expect(screen.getByRole("alert").className).toBe("m-0 text-sm text-[var(--ui-fail-ink)]");
  });
});
