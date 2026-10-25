// badge.test.tsx — one card badge: its kind names the list item, and a priority badge reads its
// own level's tone, so P0 and P3 read differently.

import { render } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { Badge } from "../../src/kanban/components/Badge.tsx";

describe("Badge", () => {
  it("names its kind and keeps the label as the text", () => {
    const { container } = render(<Badge badge={{ kind: "type", label: "feature" }} />);
    const li = container.querySelector("li");
    expect(li?.className).toBe("card-badge b-type");
    expect(li?.textContent).toBe("feature");
  });

  it("reads a priority badge's tone from its level", () => {
    const { container } = render(<Badge badge={{ kind: "priority", label: "P0" }} />);
    expect(container.querySelector("li")?.className).toBe("card-badge b-priority");
    expect(container.querySelector("li .ui-badge")?.className).toBe("ui-badge ui-badge-fail");
  });
});
