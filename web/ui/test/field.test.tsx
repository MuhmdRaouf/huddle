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
