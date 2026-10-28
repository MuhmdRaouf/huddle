import { render, screen } from "@testing-library/preact";
import { h } from "preact";
import type { JSX } from "preact";
import { describe, expect, it } from "vite-plus/test";
import { useUiId } from "../src/id.ts";

/** A component whose heading carries a useUiId id. */
function Titled(props: { prefix: string; text: string }): JSX.Element {
  const id = useUiId(props.prefix);
  return h("h2", { id }, props.text);
}

describe("useUiId", () => {
  it("gives two components mounted at different times different ids", () => {
    render(h(Titled, { prefix: "ui-probe", text: "first" }));
    const first = screen.getByText("first");
    render(h(Titled, { prefix: "ui-probe", text: "second" }));
    const second = screen.getByText("second");
    expect(first.id).not.toBe(second.id);
    expect(second.id.startsWith("ui-probe-")).toBe(true);
  });

  it("keeps one id across re-renders", () => {
    const view = render(h(Titled, { prefix: "ui-probe", text: "same" }));
    const before = screen.getByText("same").id;
    view.rerender(h(Titled, { prefix: "ui-probe", text: "same" }));
    expect(screen.getByText("same").id).toBe(before);
  });
});
