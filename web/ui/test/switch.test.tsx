import { fireEvent, render, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import { Switch } from "../src/switch.tsx";

describe("Switch", () => {
  it("is a named switch on the tokens that asks for the other state", () => {
    const onChange = vi.fn();
    render(<Switch label="Catppuccin colours" checked={false} onChange={onChange} />);
    const sw = screen.getByRole("switch", { name: "Catppuccin colours" });
    expect(sw.tagName).toBe("BUTTON");
    expect(sw.getAttribute("type")).toBe("button");
    expect(sw.className).toBe("ui-switch");
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(sw.querySelector(".ui-switch-track > .ui-switch-thumb")).toBeTruthy();
    fireEvent.click(sw);
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it("shows the state its owner holds, named and described by the row around it", () => {
    const onChange = vi.fn();
    render(
      <div>
        <span id="push-l">Push to this device</span>
        <span id="push-h">Push permission: granted.</span>
        <Switch id="set-push" labelledBy="push-l" describedBy="push-h" checked onChange={onChange} />
      </div>,
    );
    const sw = screen.getByRole("switch", { name: "Push to this device" });
    expect(sw.id).toBe("set-push");
    expect(sw.getAttribute("aria-checked")).toBe("true");
    expect(sw.getAttribute("aria-describedby")).toBe("push-h");
    fireEvent.click(sw);
    expect(onChange).toHaveBeenCalledWith(false);
    // the owner did not change its state, so the switch still reads on
    expect(sw.getAttribute("aria-checked")).toBe("true");
  });

  it("does nothing while disabled", () => {
    const onChange = vi.fn();
    render(<Switch label="Push" checked={false} disabled onChange={onChange} />);
    const sw = screen.getByRole("switch", { name: "Push" }) as HTMLButtonElement;
    expect(sw.disabled).toBe(true);
    fireEvent.click(sw);
    expect(onChange).not.toHaveBeenCalled();
  });
});
