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

  it("shows its title as the tooltip on the button and on the link form", () => {
    render(<Button title="Approve the card">Approve</Button>);
    expect(screen.getByRole("button", { name: "Approve" }).getAttribute("title")).toBe("Approve the card");
    render(
      <Button href="#/now/s-1" title="Review the card">
        Review
      </Button>,
    );
    expect(screen.getByRole("link", { name: "Review" }).getAttribute("title")).toBe("Review the card");
  });

  it("carries no title when none is asked", () => {
    render(<Button>Approve</Button>);
    expect(screen.getByRole("button", { name: "Approve" }).hasAttribute("title")).toBe(false);
    render(<Button href="#/now/s-1">Review</Button>);
    expect(screen.getByRole("link", { name: "Review" }).hasAttribute("title")).toBe(false);
  });

  it("takes a menu item's role on the button, never on the link form", () => {
    render(<Button role="menuitem">Move</Button>);
    expect(screen.getByRole("menuitem", { name: "Move" }).getAttribute("role")).toBe("menuitem");
    render(
      <Button href="#/now/s-1" role="menuitem">
        Review
      </Button>,
    );
    expect(screen.getByRole("link", { name: "Review" }).hasAttribute("role")).toBe(false);
  });

  it("names what it opens as aria-haspopup on the button and on the link form", () => {
    render(<Button hasPopup="menu">Move</Button>);
    expect(screen.getByRole("button", { name: "Move" }).getAttribute("aria-haspopup")).toBe("menu");
    render(
      <Button href="#/now/s-1" hasPopup="menu">
        Pick
      </Button>,
    );
    expect(screen.getByRole("link", { name: "Pick" }).getAttribute("aria-haspopup")).toBe("menu");
  });

  it("reads what it opens as open or closed in aria-expanded on both forms", () => {
    render(<Button expanded>Move</Button>);
    expect(screen.getByRole("button", { name: "Move" }).getAttribute("aria-expanded")).toBe("true");
    render(<Button expanded={false}>Closed</Button>);
    expect(screen.getByRole("button", { name: "Closed" }).getAttribute("aria-expanded")).toBe("false");
    render(
      <Button href="#/now/s-1" expanded>
        Pick
      </Button>,
    );
    expect(screen.getByRole("link", { name: "Pick" }).getAttribute("aria-expanded")).toBe("true");
    render(
      <Button href="#/now/s-1" expanded={false}>
        Shut
      </Button>,
    );
    expect(screen.getByRole("link", { name: "Shut" }).getAttribute("aria-expanded")).toBe("false");
  });

  it("carries no menu attribute when none is asked", () => {
    render(<Button>Approve</Button>);
    const b = screen.getByRole("button", { name: "Approve" });
    expect(b.hasAttribute("role")).toBe(false);
    expect(b.hasAttribute("aria-haspopup")).toBe(false);
    expect(b.hasAttribute("aria-expanded")).toBe(false);
    render(<Button href="#/now/s-1">Review</Button>);
    const a = screen.getByRole("link", { name: "Review" });
    expect(a.hasAttribute("role")).toBe(false);
    expect(a.hasAttribute("aria-haspopup")).toBe(false);
    expect(a.hasAttribute("aria-expanded")).toBe(false);
  });
});
