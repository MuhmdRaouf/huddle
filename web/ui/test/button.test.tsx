import { fireEvent, render, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import { Button } from "../src/button.tsx";

describe("Button", () => {
  it("renders a primary button by default and calls onClick", () => {
    const onClick = vi.fn();
    render(<Button onClick={onClick}>Approve</Button>);
    const b = screen.getByRole("button", { name: "Approve" });
    expect(b.getAttribute("type")).toBe("button");
    expect(b.className).toBe("ui-btn ui-btn-primary");
    fireEvent.click(b);
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("takes a variant, an icon, a pressed state, a submit type and extra classes", () => {
    render(
      <Button variant="ghost" icon="check" pressed type="submit" class="wide">
        Done
      </Button>,
    );
    const b = screen.getByRole("button", { name: "Done" });
    expect(b.className).toBe("ui-btn ui-btn-ghost wide");
    expect(b.getAttribute("aria-pressed")).toBe("true");
    expect(b.getAttribute("type")).toBe("submit");
    expect(b.querySelector("svg.ui-icon")).toBeTruthy();
  });

  it("is an icon button named by its label when it has no text", () => {
    render(<Button variant="secondary" icon="x" label="Close" />);
    const b = screen.getByRole("button", { name: "Close" });
    expect(b.className).toBe("ui-btn ui-btn-secondary ui-btn-icon");
    expect(b.hasAttribute("aria-pressed")).toBe(false);
  });

  it("does nothing while disabled", () => {
    const onClick = vi.fn();
    render(
      <Button variant="danger" disabled onClick={onClick}>
        Deny
      </Button>,
    );
    const b = screen.getByRole("button", { name: "Deny" }) as HTMLButtonElement;
    expect(b.disabled).toBe(true);
    expect(b.className).toBe("ui-btn ui-btn-danger");
    fireEvent.click(b);
    expect(onClick).not.toHaveBeenCalled();
  });

  it("takes the thumb's 48 px size and the data attributes a page finds it by", () => {
    render(
      <Button variant="danger" size="lg" data-act="deny">
        Deny
      </Button>,
    );
    const b = screen.getByRole("button", { name: "Deny" });
    expect(b.className).toBe("ui-btn ui-btn-danger ui-btn-lg");
    expect(b.dataset.act).toBe("deny");
  });

  it("is a link that looks like a button when it has an href", () => {
    render(
      <Button href="#/now/s-1" size="lg" data-act="review" class="wide">
        Review
      </Button>,
    );
    const a = screen.getByRole("link", { name: "Review" });
    expect(a.getAttribute("href")).toBe("#/now/s-1");
    expect(a.className).toBe("ui-btn ui-btn-primary ui-btn-lg wide");
    expect(a.dataset.act).toBe("review");
    expect(a.hasAttribute("type")).toBe(false);
  });
});
