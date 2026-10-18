// boot.test.ts — the shell's start: the one question that answers 200 signed in and signed out
// alike decides which of the two ways the entry takes, a question that cannot be asked leaves
// the shell its chance, and an answer that says nothing counts as signed out.

import { describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../src/api.ts";
import { boot } from "../src/boot.ts";

/** A client that answers the one question with the given value, or refuses it with the error. */
function api(answer: unknown): Pick<Api, "api"> {
  return {
    api: () => (answer instanceof Error ? Promise.reject(answer) : Promise.resolve(answer)),
  };
}

/** Lets the boot's promise chain run out, the way a tick of the entry's host would. */
const settled = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0));

describe("boot", () => {
  it("starts the shell only once the answer says this browser holds a session", async () => {
    const start = vi.fn();
    const signedOut = vi.fn();
    boot({ api: api({ signedIn: true }), start, signedOut });
    expect(start).not.toHaveBeenCalled();
    await settled();
    expect(start).toHaveBeenCalledTimes(1);
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("paints the landing when the answer says no session stands", async () => {
    const start = vi.fn();
    const signedOut = vi.fn();
    boot({ api: api({ signedIn: false, passkeys: true }), start, signedOut });
    await settled();
    expect(signedOut).toHaveBeenCalledTimes(1);
    expect(start).not.toHaveBeenCalled();
  });

  it("paints the landing for an answer that says nothing about a session", async () => {
    const start = vi.fn();
    boot({ api: api({ passkeys: true, totp: false }), start, signedOut: vi.fn() });
    await settled();
    expect(start).not.toHaveBeenCalled();
  });

  it("starts the shell when the question itself cannot be asked", async () => {
    const start = vi.fn();
    const signedOut = vi.fn();
    boot({ api: api(new Error("hub unreachable")), start, signedOut });
    await settled();
    expect(start).toHaveBeenCalledTimes(1);
    expect(signedOut).not.toHaveBeenCalled();
  });
});
