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

describe("Field inputMode", () => {
  it("passes the input mode to the one-line input", () => {
    render(<Field label="Limit" value="" onInput={() => {}} inputMode="decimal" />);
    expect(screen.getByRole("textbox", { name: "Limit" }).getAttribute("inputmode")).toBe("decimal");
  });

  it("passes the input mode to the textarea", () => {
    render(<Field label="Notes" value="" onInput={() => {}} multiline inputMode="decimal" />);
    expect(screen.getByLabelText("Notes").getAttribute("inputmode")).toBe("decimal");
  });

  it("carries no inputmode when none is asked", () => {
    render(<Field label="Name" value="" onInput={() => {}} />);
    expect(screen.getByRole("textbox", { name: "Name" }).hasAttribute("inputmode")).toBe(false);
    render(<Field label="Notes" value="" onInput={() => {}} multiline />);
    expect(screen.getByLabelText("Notes").hasAttribute("inputmode")).toBe(false);
  });
});

describe("Field native attributes", () => {
  it("passes the autocomplete hint, the spelling check and the form name to the input", () => {
    render(
      <Field label="Title" value="" onInput={() => {}} autoComplete="off" spellCheck={false} name="title" />,
    );
    const input = screen.getByRole("textbox", { name: "Title" });
    expect(input.getAttribute("autocomplete")).toBe("off");
    expect(input.getAttribute("spellcheck")).toBe("false");
    expect(input.getAttribute("name")).toBe("title");
  });

  it("passes the autocomplete hint, the spelling check and the form name to the textarea", () => {
    render(
      <Field
        label="Body"
        value=""
        onInput={() => {}}
        multiline
        autoComplete="off"
        spellCheck={false}
        name="body"
      />,
    );
    const box = screen.getByLabelText("Body");
    expect(box.getAttribute("autocomplete")).toBe("off");
    expect(box.getAttribute("spellcheck")).toBe("false");
    expect(box.getAttribute("name")).toBe("body");
  });

  it("carries no autocomplete, spellcheck or name when none is asked", () => {
    render(<Field label="Name" value="" onInput={() => {}} />);
    const input = screen.getByRole("textbox", { name: "Name" });
    expect(input.hasAttribute("autocomplete")).toBe(false);
    expect(input.hasAttribute("spellcheck")).toBe(false);
    expect(input.hasAttribute("name")).toBe(false);
    render(<Field label="Notes" value="" onInput={() => {}} multiline />);
    const box = screen.getByLabelText("Notes");
    expect(box.hasAttribute("autocomplete")).toBe(false);
    expect(box.hasAttribute("spellcheck")).toBe(false);
    expect(box.hasAttribute("name")).toBe(false);
  });
});

describe("Field error note", () => {
  it("announces the error as an alert", () => {
    render(<Field label="Budget" value="" onInput={() => {}} error="Enter a number" />);
    expect(screen.getByRole("alert").textContent).toBe("Enter a number");
  });

  it("gives the hint no role", () => {
    render(<Field label="Channel" value="" onInput={() => {}} hint="Lowercase, no spaces" />);
    expect(screen.getByText("Lowercase, no spaces").getAttribute("role")).toBeNull();
  });
});

describe("Field hint", () => {
  it("renders a marked-up hint inside the hint node", () => {
    render(<Field label="Token" value="" onInput={() => {}} hint={<code>x</code>} />);
    const input = screen.getByRole("textbox", { name: "Token" });
    const code = screen.getByText("x");
    expect(code.tagName).toBe("CODE");
    const hint = code.closest("p") as HTMLParagraphElement;
    expect(hint.className).toBe("ui-field-hint");
    expect(input.getAttribute("aria-describedby")).toBe(hint.id);
  });

  it("renders a plain string hint as its text", () => {
    render(<Field label="Channel" value="" onInput={() => {}} hint="Lowercase, no spaces" />);
    const input = screen.getByRole("textbox", { name: "Channel" });
    const hint = screen.getByText("Lowercase, no spaces");
    expect(hint.className).toBe("ui-field-hint");
    expect(input.getAttribute("aria-describedby")).toBe(hint.id);
  });
});

describe("Field onKeyDown", () => {
  it("a multiline field hands each key press to onKeyDown, and onInput still works", () => {
    const onInput = vi.fn();
    const onKeyDown = vi.fn();
    render(<Field label="Reply" value="" onInput={onInput} onKeyDown={onKeyDown} multiline />);
    const box = screen.getByLabelText("Reply");
    fireEvent.keyDown(box, { key: "Enter" });
    expect(onKeyDown).toHaveBeenCalledTimes(1);
    expect(onKeyDown.mock.calls[0]?.[0]).toMatchObject({ key: "Enter" });
    fireEvent.input(box, { target: { value: "hi" } });
    expect(onInput).toHaveBeenCalledWith("hi");
  });

  it("a one-line field hands each key press to onKeyDown", () => {
    const onKeyDown = vi.fn();
    render(<Field label="Name" value="" onInput={() => {}} onKeyDown={onKeyDown} />);
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Name" }), { key: "Enter" });
    expect(onKeyDown).toHaveBeenCalledTimes(1);
    expect(onKeyDown.mock.calls[0]?.[0]).toMatchObject({ key: "Enter" });
  });
});

describe("Field id", () => {
  it("a field given an id puts it on its textarea, and the label points at it", () => {
    render(<Field label="Reply" id="ar-7" value="" onInput={() => {}} multiline hint="Enter sends" />);
    const box = screen.getByLabelText("Reply");
    expect(box.id).toBe("ar-7");
    expect(document.querySelector('label[for="ar-7"]')).not.toBeNull();
    expect(box.getAttribute("aria-describedby")).toBe("ar-7-note");
  });

  it("a field given an id puts it on its input, and the label points at it", () => {
    render(<Field label="Channel" id="ch-2" value="" onInput={() => {}} hint="Lowercase, no spaces" />);
    const input = screen.getByRole("textbox", { name: "Channel" });
    expect(input.id).toBe("ch-2");
    expect(document.querySelector('label[for="ch-2"]')).not.toBeNull();
    expect(input.getAttribute("aria-describedby")).toBe("ch-2-note");
  });

  it("a field without an id keeps its generated one", () => {
    render(<Field label="Notes" value="" onInput={() => {}} multiline />);
    const box = screen.getByLabelText("Notes");
    expect(box.id).toMatch(/^ui-field-/);
    expect(document.querySelector(`label[for="${box.id}"]`)).not.toBeNull();
  });
});

describe("Field labelHidden", () => {
  it("a hidden label stays readable", () => {
    render(<Field label="Reply" value="" onInput={() => {}} multiline labelHidden />);
    // happy-dom lays nothing out, so hiding is held structurally: the label stays in the DOM, for
    // the control and under its accessible name, and carries the shared visually-hidden class
    const box = screen.getByLabelText("Reply");
    const label = document.querySelector(`label[for="${box.id}"]`);
    expect(label).not.toBeNull();
    expect(label?.textContent).toBe("Reply");
    expect(label?.className).toContain("sr-only");
  });
});

describe("Field minRows", () => {
  it("stands minRows lines open and still grows to maxRows", () => {
    render(<Field label="Notes" value="" onInput={() => {}} multiline minRows={3} maxRows={5} />);
    const box = screen.getByLabelText("Notes") as HTMLTextAreaElement;
    expect(box.getAttribute("rows")).toBe("3");
    expect(box.style.getPropertyValue("--ui-field-rows")).toBe("5");
    expect(box.style.getPropertyValue("--ui-field-min-rows")).toBe("3");
  });

  it("carries the floor to the stylesheet clamped, and none without minRows", () => {
    render(<Field label="Notes" value="" onInput={() => {}} multiline minRows={9} maxRows={4} />);
    expect(screen.getByLabelText("Notes").style.getPropertyValue("--ui-field-min-rows")).toBe("4");
    render(<Field label="Body" value="" onInput={() => {}} multiline />);
    expect(screen.getByLabelText("Body").style.getPropertyValue("--ui-field-min-rows")).toBe("");
  });

  it("clamps a minRows above maxRows", () => {
    render(<Field label="Notes" value="" onInput={() => {}} multiline minRows={9} maxRows={4} />);
    expect(screen.getByLabelText("Notes").getAttribute("rows")).toBe("4");
  });

  it("stands one row open when neither minRows nor maxRows is given", () => {
    render(<Field label="Notes" value="" onInput={() => {}} multiline />);
    expect(screen.getByLabelText("Notes").getAttribute("rows")).toBe("1");
  });
});
