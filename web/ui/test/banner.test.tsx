import { fireEvent, render, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import { Banner } from "../src/banner.tsx";

describe("Banner", () => {
  it("renders one class per tone and speaks status for info, alert for warn and fail", () => {
    const info = render(<Banner>Deploy finished.</Banner>);
    const root = info.container.firstElementChild as HTMLElement;
    expect(root.className).toBe("ui-banner ui-banner-info");
    expect(root.getAttribute("role")).toBe("status");
    expect(root.querySelector("svg")).toBeTruthy();
    info.unmount();

    const warn = render(<Banner tone="warn">The budget is nearly spent.</Banner>);
    expect(warn.container.firstElementChild?.className).toBe("ui-banner ui-banner-warn");
    expect(screen.getByRole("alert")).toBe(warn.container.firstElementChild);
    warn.unmount();

    const fail = render(<Banner tone="fail">The router is unreachable.</Banner>);
    expect(fail.container.firstElementChild?.className).toBe("ui-banner ui-banner-fail");
    expect(screen.getByRole("alert")).toBe(fail.container.firstElementChild);
    fail.unmount();
  });

  it("leads with the bold title before the message", () => {
    const view = render(<Banner title="Heads up">The budget is nearly spent.</Banner>);
    const root = view.container.firstElementChild as HTMLElement;
    const title = root.querySelector(".ui-banner-title");
    expect(title?.tagName).toBe("STRONG");
    expect(title?.textContent).toBe("Heads up");
    expect(root.textContent?.indexOf("Heads up")).toBeLessThan(root.textContent?.indexOf("The budget") ?? -1);
  });

  it("renders the action as a named button and runs it once on click", () => {
    const onClick = vi.fn();
    render(<Banner action={{ label: "Reload", onClick }}>Huddle updated in the background.</Banner>);
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("renders no button without an action", () => {
    const view = render(<Banner>All quiet.</Banner>);
    expect(view.container.querySelector("button")).toBeNull();
  });
});
