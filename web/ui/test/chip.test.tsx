import { fireEvent, render, screen } from "@testing-library/preact";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { describe, expect, it, vi } from "vite-plus/test";
import { Chip } from "../src/chip.tsx";
import { Icon } from "../src/icons.tsx";

describe("Chip", () => {
  it("renders a span with one class per tone and no button role", () => {
    render(
      <>
        <Chip>idle</Chip>
        <Chip tone="info">new</Chip>
        <Chip tone="ok">healthy</Chip>
        <Chip tone="warn">slow</Chip>
        <Chip tone="fail">down</Chip>
        <Chip tone="accent">pinned</Chip>
      </>,
    );
    expect(screen.getByText("idle").className).toBe("ui-chip ui-chip-neutral");
    expect(screen.getByText("new").className).toBe("ui-chip ui-chip-info");
    expect(screen.getByText("healthy").className).toBe("ui-chip ui-chip-ok");
    expect(screen.getByText("slow").className).toBe("ui-chip ui-chip-warn");
    expect(screen.getByText("down").className).toBe("ui-chip ui-chip-fail");
    expect(screen.getByText("pinned").className).toBe("ui-chip ui-chip-accent");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("leads with the dot when it asks for one, and with no dot when it does not", () => {
    render(
      <>
        <Chip tone="ok" dot>
          healthy
        </Chip>
        <Chip tone="ok">plain</Chip>
      </>,
    );
    expect(screen.getByText("healthy").querySelector(".ui-chip-dot")?.tagName).toBe("SPAN");
    expect(screen.getByText("plain").querySelector(".ui-chip-dot")).toBeNull();
  });

  it("leads with the icon it is given", () => {
    render(
      <Chip tone="accent" icon={<Icon name="plus" />}>
        add-on
      </Chip>,
    );
    expect(screen.getByText("add-on").querySelector("svg")).toBeTruthy();
  });

  it("toggles as a button with aria-pressed and answers each click once", () => {
    const onClick = vi.fn();
    function Row(): JSX.Element {
      const [on, setOn] = useState(false);
      return (
        <Chip
          tone="accent"
          pressed={on}
          onClick={() => {
            setOn(!on);
            onClick();
          }}
        >
          Mine
        </Chip>
      );
    }
    render(<Row />);
    const chip = screen.getByRole("button", { name: "Mine" });
    expect(chip.className).toBe("ui-chip ui-chip-accent ui-chip-toggle");
    expect(chip.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(chip);
    expect(chip.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(chip);
    expect(onClick).toHaveBeenCalledTimes(2);
  });

  it("carries the data attributes a page or a test finds it by", () => {
    render(<Chip data-k="tag">x</Chip>);
    const chip = screen.getByText("x");
    expect(chip.tagName).toBe("SPAN");
    expect(chip.className).toBe("ui-chip ui-chip-neutral");
    expect(chip.getAttribute("data-k")).toBe("tag");
  });

  it("carries the data attributes on the filter button too", () => {
    render(
      <Chip pressed={true} onClick={() => {}} data-k="fact">
        Fact
      </Chip>,
    );
    const chip = screen.getByRole("button", { name: "Fact" });
    expect(chip.className).toBe("ui-chip ui-chip-neutral ui-chip-toggle");
    expect(chip.getAttribute("aria-pressed")).toBe("true");
    expect(chip.getAttribute("data-k")).toBe("fact");
  });
});
