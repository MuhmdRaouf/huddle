import { screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import {
  Badge,
  EmptyState,
  IconTile,
  ModelChip,
  Panel,
  Notice,
  SEGMENT,
  Segmented,
  Status,
  shortModelName,
} from "../../../src/radar/app/kit.tsx";
import { renderApp } from "./render.tsx";
import { expectTokensOnly } from "./tokens.ts";

// every render in this file paints only token classes and labels its table cells for the phone
afterEach(() => expectTokensOnly());

describe("shortModelName", () => {
  it("reads families by their short names and keeps unknown ids raw", () => {
    expect(shortModelName("claude-opus-5-5")).toBe("Opus 5.5");
    expect(shortModelName("Claude Sonnet 5.5")).toBe("Sonnet 5.5");
    expect(shortModelName("glm-5.3")).toBe("GLM 5.3");
    expect(shortModelName("zai:glm-5.3-flash")).toBe("GLM 5.3 Flash");
    expect(shortModelName("kimi-k2-thinking")).toBe("kimi-k2-thinking");
    expect(shortModelName("claude-haiku-4-5-20251001")).toBe("Haiku 4.5");
    expect(shortModelName("claude-fable-5-1")).toBe("Fable 5.1");
    expect(shortModelName("claude-sonnet-4-20250514")).toBe("Sonnet 4");
  });
});

describe("ModelChip", () => {
  it("shows a dot coloured by the model's family and the short name, the raw id as its title", () => {
    const { container } = renderApp(<ModelChip model="claude-opus-5-5" />);
    const chip = container.querySelector(".model-chip");
    expect(chip?.getAttribute("title")).toBe("claude-opus-5-5");
    expect(chip?.textContent).toBe("Opus 5.5");
    const dot = chip?.querySelector("[data-dot]");
    expect(dot?.getAttribute("style")).toContain("var(--model-opus)");
    expect(dot?.className).toContain("rounded-full");
  });
});

describe("Status", () => {
  it("sets the dot beside its word, pulsing only when live", () => {
    const { container } = renderApp(
      <>
        <Status tone="ok" word="Live" pulse />
        <Status tone="err" word="Down" />
      </>,
    );
    const [live, down] = [...container.querySelectorAll("[data-status]")];
    expect(live?.getAttribute("data-status")).toBe("ok");
    expect(live?.textContent).toBe("Live");
    expect(live?.querySelector("[data-dot]")?.className).toContain("motion-safe:animate-pulse");
    expect(down?.querySelector("[data-dot]")?.className).not.toContain("animate-pulse");
  });
});

describe("Panel", () => {
  it("draws the token card with a titled head, subtitle and right-aligned actions", () => {
    const { container } = renderApp(
      <Panel icon="bot" title="Agents" subtitle="3 running" actions={<button type="button">Do</button>}>
        <p>body</p>
      </Panel>,
    );
    const panel = container.querySelector("section");
    expect(panel?.className).toBe("ui-card min-w-0");
    expect(panel?.querySelector(".ui-card-head h2.ui-card-title")?.textContent).toBe("Agents");
    expect(panel?.getAttribute("aria-labelledby")).toBe(panel?.querySelector("h2")?.id);
    expect(panel?.querySelector(".ui-card-meta")?.textContent).toBe("3 running");
    expect(panel?.querySelector(".ui-card-actions button")?.textContent).toBe("Do");
    expect(panel?.querySelector("svg")?.getAttribute("class")).toContain("text-[var(--ui-accent)]");
    expect(container.textContent).toContain("body");
  });

  it("renders headless and carries extra classes when given", () => {
    const { container } = renderApp(<Panel class="tree-block">just the box</Panel>);
    const panel = container.querySelector("section");
    expect(panel?.className).toBe("ui-card min-w-0 tree-block");
    expect(panel?.querySelector("h2")).toBeNull();
    expect(panel?.textContent).toBe("just the box");
  });

  it("pads its body by default and leaves flush bodies to run edge to edge", () => {
    const padded = renderApp(<Panel title="T">body</Panel>);
    expect(padded.container.querySelector("section .ui-card-body")).toBeTruthy();
    const flush = renderApp(
      <Panel title="T" flush>
        body
      </Panel>,
    );
    expect(flush.container.querySelectorAll("section")[0]?.className).toContain("overflow-hidden");
    expect(flush.container.querySelector("section .ui-card-body")).toBeNull();
  });
});

describe("EmptyState", () => {
  it("offers the one verb button when the view has somewhere to go", async () => {
    const run = vi.fn();
    const { container } = renderApp(
      <EmptyState title="No budgets" hint="Set one to watch spend." action={{ label: "New budget", run }} />,
    );
    expect(screen.getByText("No budgets")).toBeTruthy();
    expect(container.querySelector("[data-empty] p")?.className).toContain("font-semibold");
    const button = screen.getByRole("button", { name: "New budget" });
    expect(button.className).toBe("ui-btn ui-btn-secondary mt-2");
    await userEvent.click(button);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("renders without a button when there is nowhere to go", () => {
    renderApp(<EmptyState title="Nothing here" hint="Wait for traffic." />);
    expect(document.querySelector("button")).toBeNull();
  });
});

describe("Notice", () => {
  it("says a failure on the fail tone and news on the info tone, with its role", () => {
    const { container } = renderApp(
      <>
        <Notice tone="err">It broke</Notice>
        <Notice tone="ok" role="status">
          Saved
        </Notice>
      </>,
    );
    const [err, ok] = [...container.querySelectorAll("[data-notice]")];
    expect(err?.getAttribute("role")).toBe("alert");
    expect(err?.className).toContain("bg-[var(--ui-fail-soft)]");
    expect(err?.className).toContain("text-[var(--ui-fail-ink)]");
    expect(ok?.getAttribute("role")).toBe("status");
    expect(ok?.className).toContain("bg-[var(--ui-ok-soft)]");
  });
});

describe("IconTile", () => {
  it("tints the tile with the given token, smaller when asked", () => {
    const { container } = renderApp(
      <>
        <IconTile name="bell" color="var(--ui-warn)" />
        <IconTile name="close" color="var(--ui-fail)" sm />
      </>,
    );
    const [full, small] = [...container.querySelectorAll("span")];
    expect(full?.getAttribute("style")).toContain("var(--ui-warn)");
    expect(full?.className).toContain("size-8");
    expect(full?.className).toContain("rounded-[var(--ui-radius-sm)]");
    expect(small?.className).toContain("size-[26px]");
    expect(small?.querySelector("svg[data-icon='close']")).toBeTruthy();
  });
});

describe("Segmented", () => {
  it("runs the action with the segment's value and marks the pressed one on the token buttons", async () => {
    const { container, act } = renderApp(
      <Segmented
        action="tool-filter"
        label="Filter"
        segments={[
          { label: "All", value: "all", on: true, count: "4" },
          { label: "Failed", value: "failed", on: false, count: "1", dot: "var(--ui-fail)" },
        ]}
      />,
    );
    expect(container.querySelector("[role=group]")?.className).toContain("bg-[var(--ui-surface-2)]");
    const [on, off] = [...container.querySelectorAll("button")];
    expect(on?.getAttribute("aria-pressed")).toBe("true");
    expect(off?.getAttribute("aria-pressed")).toBe("false");
    expect(on?.className).toBe(SEGMENT);
    expect(off?.className).toBe(SEGMENT);
    expect(SEGMENT).toContain("ui-btn ui-btn-ghost");
    expect(on?.textContent).toBe("All4");
    expect(container.querySelector(".segment-count")?.className).toContain("num");
    expect(off?.querySelector("[data-dot]")?.getAttribute("style")).toContain("var(--ui-fail)");
    await userEvent.click(off as HTMLElement);
    expect(act).toHaveBeenCalledWith("tool-filter", "failed");
  });
});

describe("Badge", () => {
  it("draws the design system's badge in the tone's status colour with its optional icon", () => {
    const { container } = renderApp(
      <>
        <Badge text="Live" tone="ok" />
        <Badge text="Failed" tone="err" icon="close" />
      </>,
    );
    const [plain, withIcon] = [...container.querySelectorAll(".ui-badge")];
    expect(plain?.className).toBe("ui-badge ui-badge-ok gap-1");
    expect(withIcon?.className).toBe("ui-badge ui-badge-fail gap-1");
    expect(withIcon?.querySelector("svg[data-icon='close']")).toBeTruthy();
  });
});
