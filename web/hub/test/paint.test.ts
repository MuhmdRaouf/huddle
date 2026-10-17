// paint.test.ts — the root-paint rule: a paint answered while another runs waits for the stack
// to unwind and repaints once, so preact's render is never re-entered mid-tree.
import { describe, expect, it, vi } from "vite-plus/test";
import { paintGuard } from "../src/paint.ts";

describe("paintGuard", () => {
  it("runs the body at once when no paint is on the stack", () => {
    const body = vi.fn();
    const paint = paintGuard(body);
    paint();
    paint();
    expect(body).toHaveBeenCalledTimes(2);
  });

  it("defers a call that arrives mid-paint to the next microtask", async () => {
    let nested = false;
    const body = vi.fn(() => {
      if (!nested) {
        nested = true;
        paint(); // the controller's flush reaching the listener mid-render
        expect(body).toHaveBeenCalledTimes(1); // not run on the same stack
      }
    });
    const paint = paintGuard(body);
    paint();
    await Promise.resolve(); // the trailing paint's microtask
    await Promise.resolve();
    expect(body).toHaveBeenCalledTimes(2);
  });

  it("coalesces every mid-paint call into one trailing paint", async () => {
    let round = 0;
    const body = vi.fn(() => {
      round += 1;
      if (round === 1) {
        paint();
        paint();
        paint();
      }
    });
    const paint = paintGuard(body);
    paint();
    await Promise.resolve();
    await Promise.resolve();
    expect(body).toHaveBeenCalledTimes(2); // the outer paint plus one trailing
  });

  it("paints again when the trailing paint itself is answered mid-paint", async () => {
    let round = 0;
    const body = vi.fn(() => {
      round += 1;
      if (round <= 2) paint(); // a flush inside the first paint, and one inside the trailing
    });
    const paint = paintGuard(body);
    paint();
    for (let i = 0; i < 6; i++) await Promise.resolve();
    expect(body).toHaveBeenCalledTimes(3);
  });

  it("releases the guard when the body throws", () => {
    const paint = paintGuard(() => {
      throw new Error("boom");
    });
    expect(paint).toThrow("boom");
    expect(() => paint()).toThrow("boom"); // a second call paints again, not queued
  });
});

it("a paint answered inside the trailing paint is deferred too, never nested", async () => {
  let depth = 0;
  let maxDepth = 0;
  let rounds = 0;
  const paint = paintGuard(() => {
    depth++;
    maxDepth = Math.max(maxDepth, depth);
    rounds++;
    if (rounds <= 2) paint();
    depth--;
  });
  paint();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(maxDepth).toBe(1);
  expect(rounds).toBe(3);
});
