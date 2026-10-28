import { fireEvent, render, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import { Field } from "../src/field.tsx";

describe("Field", () => {
  it("labels its input, describes it with the hint and reports each edit", () => {
    const onInput = vi.fn();
    render(
      <Field
        label="Channel name"
        value="huddle"
        onInput={onInput}
        hint="Lowercase, no spaces"
        placeholder="e.g. api"
        mono
      />,
    );
    const input = screen.getByRole("textbox", { name: "Channel name" }) as HTMLInputElement;
    expect(input.value).toBe("huddle");
    expect(input.getAttribute("type")).toBe("text");
    expect(input.getAttribute("placeholder")).toBe("e.g. api");
    expect(input.className).toBe("ui-input ui-mono");
    expect(input.getAttribute("aria-invalid")).toBeNull();
    const hint = screen.getByText("Lowercase, no spaces");
    expect(input.getAttribute("aria-describedby")).toBe(hint.id);
    fireEvent.input(input, { target: { value: "api" } });
    expect(onInput).toHaveBeenCalledWith("api");
  });

  it("marks the input invalid and reads the error out", () => {
    render(<Field label="Budget" type="number" value="" onInput={() => {}} error="Enter a number" />);
    const input = screen.getByRole("spinbutton", { name: "Budget" });
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(input.className).toBe("ui-input");
    const err = screen.getByText("Enter a number");
    expect(err.className).toBe("ui-field-error");
    expect(input.getAttribute("aria-describedby")).toBe(err.id);
  });

  it("has no description when there is neither hint nor error", () => {
    render(<Field label="Search" type="search" value="" onInput={() => {}} />);
    expect(screen.getByRole("searchbox", { name: "Search" }).hasAttribute("aria-describedby")).toBe(false);
  });
});

describe("Field multiline", () => {
  it("renders a growing textarea wired like the input, and ignores the type", () => {
    const onInput = vi.fn();
    render(
      <Field
        label="Notes"
        type="search"
        value="first"
        onInput={onInput}
        multiline
        hint="Grows as you type"
        placeholder="Type away"
      />,
    );
    const box = screen.getByLabelText("Notes") as HTMLTextAreaElement;
    expect(box.tagName).toBe("TEXTAREA");
    expect(box.className).toBe("ui-input ui-input-multi");
    expect(box.getAttribute("rows")).toBe("1");
    expect(box.getAttribute("type")).toBeNull();
    expect(box.value).toBe("first");
    expect(box.getAttribute("placeholder")).toBe("Type away");
    expect(box.style.getPropertyValue("--ui-field-rows")).toBe("6");
    expect(box.getAttribute("aria-invalid")).toBeNull();
    const hint = screen.getByText("Grows as you type");
    expect(box.getAttribute("aria-describedby")).toBe(hint.id);
    fireEvent.input(box, { target: { value: "first\nsecond" } });
    expect(onInput).toHaveBeenCalledWith("first\nsecond");
  });

  it("marks the textarea invalid and reads the error out", () => {
    render(<Field label="Notes" value="" onInput={() => {}} multiline error="Too long" />);
    const box = screen.getByLabelText("Notes");
    expect(box.tagName).toBe("TEXTAREA");
    expect(box.getAttribute("aria-invalid")).toBe("true");
    const err = screen.getByText("Too long");
    expect(err.className).toBe("ui-field-error");
    expect(box.getAttribute("aria-describedby")).toBe(err.id);
  });

  it("caps the growth at the maxRows it is given", () => {
    render(<Field label="Notes" value="" onInput={() => {}} multiline maxRows={3} />);
    expect(screen.getByLabelText("Notes").style.getPropertyValue("--ui-field-rows")).toBe("3");
  });

  it("sets its own height on input where the browser lacks field-sizing", () => {
    // happy-dom answers its supports from the prototype, so the answer is swapped there
    const proto = Object.getPrototypeOf(CSS) as { supports: (p: string, v: string) => boolean };
    const real = proto.supports;
    proto.supports = () => false;
    try {
      render(<Field label="Notes" value="" onInput={() => {}} multiline />);
      const box = screen.getByLabelText("Notes") as HTMLTextAreaElement;
      fireEvent.input(box, { target: { value: "one\ntwo" } });
      expect(box.style.height.endsWith("px")).toBe(true);
    } finally {
      proto.supports = real;
    }
  });

  it("leaves the height to field-sizing where the browser has it", () => {
    render(<Field label="Notes" value="" onInput={() => {}} multiline />);
    const box = screen.getByLabelText("Notes") as HTMLTextAreaElement;
    expect(box.className).toBe("ui-input ui-input-multi");
    fireEvent.input(box, { target: { value: "one\ntwo" } });
    expect(box.style.height).toBe("");
  });
});
