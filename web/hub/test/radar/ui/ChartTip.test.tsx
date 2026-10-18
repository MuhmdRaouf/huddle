// ChartTip.test.tsx — the shared hover tooltip: where the box flips to the pointer's left, and the
// box's own rendering (the head and one row per series).
import { render } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { ChartTipBox, tipLeft } from "../../../src/radar/app/ChartTip.tsx";

const TIP = { index: 3, x: 400, top: 120, height: 240 };

describe("tipLeft", () => {
  it("sits right of the pointer with a gap", () => {
    window.innerWidth = 1200;
    expect(tipLeft(400, 190)).toBe(414);
  });

  it("flips left of the pointer when the box would run off the window, never under a margin", () => {
    window.innerWidth = 500;
    expect(tipLeft(460, 190)).toBe(256);
    window.innerWidth = 200;
    expect(tipLeft(50, 190)).toBe(8);
  });
});

describe("ChartTipBox", () => {
  it("shows the head and one line per series, each with its colour and value", () => {
    window.innerWidth = 1200;
    const { container } = render(
      <ChartTipBox
        tip={TIP}
        head="17 Oct"
        lines={[
          { color: "#123456", label: "GLM 5.3", value: "10,000" },
          { color: "#654321", label: "Opus 5", value: "0" },
        ]}
      />,
    );
    expect(container.querySelector(".tip-head")?.textContent).toBe("17 Oct");
    const lines = [...container.querySelectorAll(".tip-line")];
    expect(lines.map((line) => line.textContent)).toEqual(["GLM 5.310,000", "Opus 50"]);
    expect(lines[0]?.querySelector(".swatch")?.getAttribute("style")).toContain("#123456");
    expect(container.querySelector(".chart-cross")).toBeTruthy();
  });
});
