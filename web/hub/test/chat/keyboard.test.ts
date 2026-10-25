// keyboard.test.ts — the keyboard's height as the visual viewport tells it: what the layout
// viewport holds below the visible part, never less than nothing.

import { describe, expect, it } from "vite-plus/test";
import { keyboardInset } from "../../src/chat/keyboard.ts";

describe("keyboardInset", () => {
  it("is nothing while the visual viewport fills the layout viewport", () => {
    expect(keyboardInset({ innerHeight: 659, height: 659, offsetTop: 0 })).toBe(0);
  });

  it("is the height the keyboard covers below the visible part", () => {
    expect(keyboardInset({ innerHeight: 659, height: 359, offsetTop: 0 })).toBe(300);
  });

  it("leaves out what the page scrolled the visible part down by", () => {
    expect(keyboardInset({ innerHeight: 659, height: 359, offsetTop: 40 })).toBe(260);
  });

  it("is never below nothing, and rounds to whole pixels", () => {
    expect(keyboardInset({ innerHeight: 659, height: 700, offsetTop: 0 })).toBe(0);
    expect(keyboardInset({ innerHeight: 659.4, height: 359, offsetTop: 0 })).toBe(300);
  });
});
