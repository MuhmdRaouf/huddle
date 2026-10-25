import { render, screen } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { Badge } from "../src/badge.tsx";

describe("Badge", () => {
  it("renders neutral by default and one class per status tone", () => {
    render(
      <>
        <Badge>idle</Badge>
        <Badge tone="ok">healthy</Badge>
        <Badge tone="warn">slow</Badge>
        <Badge tone="fail">down</Badge>
        <Badge tone="info">new</Badge>
      </>,
    );
    expect(screen.getByText("idle").className).toBe("ui-badge ui-badge-neutral");
    expect(screen.getByText("healthy").className).toBe("ui-badge ui-badge-ok");
    expect(screen.getByText("slow").className).toBe("ui-badge ui-badge-warn");
    expect(screen.getByText("down").className).toBe("ui-badge ui-badge-fail");
    expect(screen.getByText("new").className).toBe("ui-badge ui-badge-info");
  });

  it("paints a count in the needs-you and turn-done state roles", () => {
    render(
      <>
        <Badge tone="needs-you">3</Badge>
        <Badge tone="turn-done">1</Badge>
      </>,
    );
    expect(screen.getByText("3").className).toBe("ui-badge ui-badge-needs-you");
    expect(screen.getByText("1").className).toBe("ui-badge ui-badge-turn-done");
  });
});
