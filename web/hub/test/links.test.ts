// links.test.ts — the link facts the sheets and the Settings section share: the channel name's
// rule and the line an invalid one carries, and the three levels' own descriptions.
import { describe, expect, it } from "vite-plus/test";
import { CHANNEL_RE, channelNameError, LEVEL_TEXT } from "../src/links.ts";

describe("channelNameError", () => {
  it("accepts a lowercase letter or digit to start, then letters, digits and dashes", () => {
    for (const name of ["a", "0lead", "kanban-release", "x".repeat(40)]) {
      expect(channelNameError(name), name).toBeNull();
      expect(CHANNEL_RE.test(name), name).toBe(true);
    }
  });

  it("refuses the empty name, capitals, a leading dash, other marks and 41 characters", () => {
    for (const name of ["", "Kanban", "-lead", "kanban_release", "kanban release", "x".repeat(41)]) {
      expect(channelNameError(name), JSON.stringify(name)).toBe(
        "Lowercase letters, digits and dashes, 40 at most, starting with a letter or digit.",
      );
    }
  });
});

describe("LEVEL_TEXT", () => {
  it("describes each of the three levels in its own words", () => {
    expect(LEVEL_TEXT.overhear).toBe("Hears every message posted in the channel.");
    expect(LEVEL_TEXT.digest).toBe("Gets each peer's newest digest once, on its next prompt.");
    expect(LEVEL_TEXT["on-request"]).toBe("Hears nothing until you brief it.");
  });
});
