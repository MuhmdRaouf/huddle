// ConnectCard.test.tsx — the start-a-session card paints from the tokens: its card, the numbered
// steps and their rail, each command box with its Copy button, the Invite button and its refusal,
// and every harness tab. The card's behaviour lives in pages/Home.test.tsx.

import { act, cleanup, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import { ConnectCard } from "../../src/app/ConnectCard.tsx";
import { flush, legacyClasses, makeCtx, makeState, renderIn } from "../helpers.tsx";

/** Every daisyUI or legacy class the card paints right now. */
const leftovers = (): string[] => {
  const daisy = [...document.querySelectorAll("[class]")]
    .flatMap((el) => (el.getAttribute("class") ?? "").split(/\s+/))
    .filter((c) => /^(bg-line|before:bg-line)$/.test(c));
  return [...legacyClasses(), ...daisy];
};

afterEach(() => {
  cleanup();
});

describe("the connect card's tokens", () => {
  it("paints the card, the steps, the commands and every tab from the tokens", async () => {
    const api: Api = {
      api: () => Promise.reject(new Error("rate limited")),
      op: () => Promise.resolve({}),
      channelPath: (ch, p) => `/api/c/${ch}${p}`,
      channelHref: (ch, p) => `#/channels/${ch}${p}`,
    };
    renderIn(<ConnectCard ch="ch" />, makeCtx(makeState(), { api }));
    expect(document.querySelector("section")?.className).toBe("ui-card");
    expect(screen.getByRole("region", { name: "Start a session" })).toBeDefined();
    expect(screen.getByRole("button", { name: /Invite a session/ }).className).toContain(
      "ui-btn ui-btn-secondary",
    );
    expect(screen.getByRole("link", { name: /Full guide/ }).className).toContain("ui-btn ui-btn-ghost");
    await act(async () => {
      screen.getByRole("button", { name: /Invite a session/ }).click();
    });
    await flush();
    expect(screen.getByText("rate limited").className).toContain("text-[var(--ui-fail-ink)]");
    expect(leftovers()).toEqual([]);
    for (const tab of ["Codex", "Gemini", "Other MCP"]) {
      await act(async () => {
        screen.getByRole("tab", { name: tab }).click();
      });
      expect(leftovers(), tab).toEqual([]);
    }
  });
});
