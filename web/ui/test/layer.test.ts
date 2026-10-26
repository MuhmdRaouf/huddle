// layer.test.ts — the one stacking rule for the kit's modal <dialog>s: an opening dialog numbers
// itself above every open layer, and every open layer but the newest wears data-ui-covered until
// the ones above it close. The rule lives in the DOM attributes, so the tests read them back.
import { describe, expect, it } from "vite-plus/test";
import { restack, showLayer } from "../src/layer.ts";

/** A fresh dialog appended to the body. */
function layer(): HTMLDialogElement {
  const d = document.createElement("dialog");
  document.body.append(d);
  return d;
}

describe("the layer rule", () => {
  it("numbers each opening above the open ones and covers every layer but the newest", () => {
    const a = layer();
    const b = layer();
    const c = layer();
    showLayer(a);
    expect(a.getAttribute("data-ui-layer")).toBe("1");
    expect(a.hasAttribute("data-ui-covered")).toBe(false);
    showLayer(b);
    expect(b.getAttribute("data-ui-layer")).toBe("2");
    expect(a.hasAttribute("data-ui-covered")).toBe(true);
    expect(b.hasAttribute("data-ui-covered")).toBe(false);
    showLayer(c);
    expect(c.getAttribute("data-ui-layer")).toBe("3");
    expect(b.hasAttribute("data-ui-covered")).toBe(true);
    expect(c.hasAttribute("data-ui-covered")).toBe(false);
    for (const d of [a, b, c]) d.remove();
  });

  it("uncovers the layer below when the newest closes and restack runs", () => {
    const a = layer();
    const b = layer();
    showLayer(a);
    showLayer(b);
    expect(a.hasAttribute("data-ui-covered")).toBe(true);
    b.close();
    restack(document);
    expect(a.hasAttribute("data-ui-covered")).toBe(false);
    expect(b.hasAttribute("data-ui-covered")).toBe(false);
    for (const d of [a, b]) d.remove();
  });

  it("keeps the newest uncovered when a middle layer closes", () => {
    const a = layer();
    const b = layer();
    const c = layer();
    showLayer(a);
    showLayer(b);
    showLayer(c);
    b.close();
    restack(document);
    expect(a.hasAttribute("data-ui-covered")).toBe(true);
    expect(c.hasAttribute("data-ui-covered")).toBe(false);
    for (const d of [a, b, c]) d.remove();
  });
});
