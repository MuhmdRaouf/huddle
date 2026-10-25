import { render, screen } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { ChartFrame } from "../src/chart-frame.tsx";

describe("ChartFrame", () => {
  it("frames a chart under its title, headline value and meta, the chart named for screen readers", () => {
    render(
      <ChartFrame title="Tokens" value="12,400" meta="last 24 h" label="Tokens per hour, rising">
        <svg />
      </ChartFrame>,
    );
    const fig = screen.getByRole("figure", { name: "Tokens 12,400 last 24 h" });
    expect(fig.className).toBe("ui-chart");
    expect(screen.getByText("12,400").className).toBe("ui-chart-value ui-mono");
    const body = screen.getByRole("img", { name: "Tokens per hour, rising" });
    expect(body.className).toBe("ui-chart-body");
    expect(body.querySelector("svg")).toBeTruthy();
  });

  it("shows the title alone when there is no value or meta", () => {
    const view = render(
      <ChartFrame title="Cost" label="Cost per day">
        <svg />
      </ChartFrame>,
    );
    expect(view.container.querySelector(".ui-chart-value")).toBeNull();
    expect(view.container.querySelector(".ui-chart-meta")).toBeNull();
    expect(screen.getByRole("figure", { name: "Cost" })).toBeTruthy();
  });
});
