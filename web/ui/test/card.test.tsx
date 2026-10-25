import { render, screen } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { Card } from "../src/card.tsx";

describe("Card", () => {
  it("renders a titled section with its meta, actions and body", () => {
    render(
      <Card title="Cost today" meta="3 sessions" actions={<button type="button">Open</button>}>
        <p>$4.20</p>
      </Card>,
    );
    const card = screen.getByRole("region", { name: "Cost today" });
    expect(card.className).toBe("ui-card");
    expect(screen.getByRole("heading", { level: 2, name: "Cost today" })).toBeTruthy();
    expect(screen.getByText("3 sessions").className).toBe("ui-card-meta");
    expect(screen.getByRole("button", { name: "Open" })).toBeTruthy();
    expect(card.querySelector(".ui-card-body")?.textContent).toBe("$4.20");
  });

  it("renders a bare body named by its label when it has no title", () => {
    const view = render(
      <Card label="Health">
        <p>ok</p>
      </Card>,
    );
    const card = screen.getByRole("region", { name: "Health" });
    expect(card.querySelector(".ui-card-head")).toBeNull();
    expect(view.container.textContent).toBe("ok");
  });

  it("shows a title with no meta and no actions", () => {
    const view = render(
      <Card title="Health">
        <p>ok</p>
      </Card>,
    );
    expect(screen.getByRole("region", { name: "Health" })).toBeTruthy();
    expect(view.container.querySelector(".ui-card-meta")).toBeNull();
    expect(view.container.querySelector(".ui-card-actions")).toBeNull();
  });
});
