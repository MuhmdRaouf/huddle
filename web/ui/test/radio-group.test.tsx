import { fireEvent, render, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import { RadioGroup } from "../src/radio-group.tsx";

type Wake = "needs" | "all" | "off";

const OPTIONS = [
  { value: "needs", label: "Needs you only", help: "A permission or a question." },
  { value: "all", label: "Needs you and alerts" },
  { value: "off", label: "Off", help: "Never woken." },
] as const;

const inputs = (): HTMLInputElement[] => [
  ...document.querySelectorAll<HTMLInputElement>('input[name="wake"]'),
];

describe("RadioGroup", () => {
  it("is a named group of native radios on the tokens, the held value checked", () => {
    render(
      <RadioGroup<Wake>
        label="What wakes this device"
        name="wake"
        value="all"
        options={OPTIONS}
        onChange={() => {}}
      />,
    );
    const group = screen.getByRole("radiogroup", { name: "What wakes this device" });
    expect(group.className).toBe("ui-radios");
    expect(inputs().map((x) => x.value)).toEqual(["needs", "all", "off"]);
    expect(inputs().map((x) => x.checked)).toEqual([false, true, false]);
    expect(inputs().every((x) => x.className === "ui-radio-input" && x.type === "radio")).toBe(true);
    const row = screen.getByRole("radio", { name: /Needs you only/ }).closest("label");
    expect(row?.className).toBe("ui-radio");
    expect(screen.getByText("A permission or a question.").className).toBe("ui-radio-help");
    expect(screen.getByText("Needs you and alerts").className).toBe("ui-radio-label");
  });

  it("reports a pick and checks nothing while it holds no value", () => {
    const onChange = vi.fn();
    render(<RadioGroup<Wake> label="Wake" name="wake" value={null} options={OPTIONS} onChange={onChange} />);
    expect(inputs().some((x) => x.checked)).toBe(false);
    fireEvent.click(inputs()[2] as HTMLInputElement);
    expect(onChange).toHaveBeenCalledWith("off");
  });

  it("disables every radio while disabled", () => {
    render(
      <RadioGroup<Wake>
        label="Wake"
        name="wake"
        value="needs"
        disabled
        options={OPTIONS}
        onChange={() => {}}
      />,
    );
    expect(inputs().every((x) => x.disabled)).toBe(true);
  });
});
