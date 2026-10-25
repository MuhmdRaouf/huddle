// badge.test.tsx — one card badge: its kind picks the class, and a priority badge carries its
// own level too, so P0 and P3 read differently.

import { render } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { Badge } from "../../src/kanban/components/Badge.tsx";

describe("Badge", () => {
  it("names its kind and keeps the label as the text", () => {
    const { container } = render(<Badge badge={{ kind: "type", label: "feature" }} />);
    const li = container.querySelector("li");
    expect(li?.className).toBe("badge b-type");
    expect(li?.textContent).toBe("feature");
  });

  it("adds the level to a priority badge", () => {
    const { container } = render(<Badge badge={{ kind: "priority", label: "P0" }} />);
    expect(container.querySelector("li")?.className).toBe("badge b-priority p0");
  });
});
