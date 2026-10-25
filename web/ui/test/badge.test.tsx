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
});
