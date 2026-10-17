// SettingsTab.test.tsx — the Devices section of Settings (SPEC 7.3): the paired devices with
// their added and last-seen ages, the removal that asks for the passkey when the server says
// step-up, the refusal said as the server spelled it, the empty list's word, and the copyable
// `huddle pair` line that pairs the next device.

import { act, screen, waitFor } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { FetchFn } from "../../src/api.ts";
import { Devices } from "../../src/app/SettingsTab.tsx";
import { flush, NOW, stubMedia } from "../helpers.tsx";
import { renderHuddle } from "../render.tsx";

/** One device row as the server lists it: added an hour ago, seen half a minute ago. */
const device = (over: Partial<{ name: string; added: string; seen: string }> = {}) => ({
  name: "iPhone",
  added: "2026-10-08T11:00:00Z",
  seen: "2026-10-08T11:59:30Z",
  ...over,
});

type Mount = {
  calls: Array<{ url: string; init?: RequestInit }>;
  toast: ReturnType<typeof renderHuddle>["toast"];
  copy: ReturnType<typeof renderHuddle>["copy"];
  /** Re-answers every later call with this list. */
  answerWith(status: number, body: unknown): void;
};

/** Renders the Devices section over one fetch answer per call, at a fixed paint time. */
function mount(
  answers: Array<{ status: number; body: unknown }>,
  seed: Array<ReturnType<typeof device>> = [device()],
): Mount {
  stubMedia();
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const queue = [...answers];
  const fetchFn: FetchFn = (url, init) => {
    calls.push(init ? { url, init } : { url });
    const a = queue.shift() ?? { status: 200, body: { devices: seed } };
    return Promise.resolve(
      new Response(JSON.stringify(a.body), {
        status: a.status,
        headers: { "content-type": "application/json" },
      }),
    );
  };
  const r = renderHuddle(<Devices />, {}, NOW, fetchFn);
  return {
    calls,
    toast: r.toast,
    copy: r.copy,
    answerWith: (status, body) => {
      queue.push({ status, body });
    },
  };
}

/** Mocks navigator.credentials.get with one assertion; the test reads back what it was asked. */
function stubGet(): ReturnType<typeof vi.fn> {
  const get = vi.fn(() =>
    Promise.resolve({
      id: "cred-9",
      rawId: new Uint8Array([4, 5]).buffer,
      type: "public-key",
      response: {
        authenticatorData: new Uint8Array([8]).buffer,
        clientDataJSON: new Uint8Array([7]).buffer,
        signature: new Uint8Array([6, 6]).buffer,
        userHandle: null,
      },
    }),
  );
  vi.stubGlobal("navigator", { ...navigator, credentials: { get } });
  return get;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the devices section", () => {
  it("TestTheListNamesEachDeviceAndItsAges", async () => {
    mount([
      { status: 200, body: { devices: [device(), device({ name: "iPad", added: "2026-10-05T09:00:00Z" })] } },
    ]);
    await waitFor(() => expect(screen.getByText("iPhone")).toBeDefined());
    expect(screen.getByText("iPad")).toBeDefined();
    expect(screen.getByText("iPhone").parentElement?.textContent).toContain("added 1 h");
    expect(screen.getByText("iPhone").parentElement?.textContent).toContain("last seen 30 s");
    expect(screen.getByText("iPad").parentElement?.textContent).toContain("added 3 d");
    expect(screen.getAllByRole("button", { name: "Remove" }).length).toBe(2);
  });

  it("TestARemovalNeedsThePasskeyWhenTheServerSaysSo", async () => {
    const h = mount([{ status: 200, body: { devices: [device()] } }]);
    await waitFor(() => expect(screen.getByText("iPhone")).toBeDefined());
    h.answerWith(401, { error: "step-up required" });
    h.answerWith(200, { options: { publicKey: { challenge: "aGk" } } });
    h.answerWith(200, { device: "iPhone", valid_for: 300 });
    h.answerWith(200, { ok: true });
    const get = stubGet();
    await act(async () => {
      screen.getByRole("button", { name: "Remove" }).click();
    });
    await flush();
    // the sheet names the removal before the passkey runs
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    expect(screen.getByText("Remove device")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(get).toHaveBeenCalledTimes(1);
    const deletes = h.calls.filter((c) => c.url === "/api/devices/iPhone");
    expect(deletes).toHaveLength(2);
    expect(deletes[0]?.init?.method).toBe("DELETE");
    // the removed device left the list
    await waitFor(() => expect(document.querySelector('[data-device="iPhone"]')).toBeNull());
  });

  it("TestARemovalWithoutTheCheckGoesStraightThrough", async () => {
    const h = mount([{ status: 200, body: { devices: [device()] } }]);
    await waitFor(() => expect(screen.getByText("iPhone")).toBeDefined());
    h.answerWith(200, { ok: true });
    await act(async () => {
      screen.getByRole("button", { name: "Remove" }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.url)).toEqual(["/api/devices", "/api/devices/iPhone"]);
    expect(document.querySelector("dialog[open]")).toBeNull();
    await waitFor(() => expect(screen.queryByText("iPhone")).toBeNull());
  });

  it("TestARemovedRefusalIsToasted", async () => {
    const h = mount([{ status: 200, body: { devices: [device()] } }]);
    await waitFor(() => expect(screen.getByText("iPhone")).toBeDefined());
    h.answerWith(403, { error: "only the owner removes devices" });
    await act(async () => {
      screen.getByRole("button", { name: "Remove" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("only the owner removes devices", { bad: true });
    expect(screen.getByText("iPhone")).toBeDefined();
  });

  it("TestAnEmptyListSaysSo", async () => {
    mount([{ status: 200, body: { devices: [] } }]);
    await waitFor(() => expect(screen.getByText("No devices paired yet.")).toBeDefined());
  });

  it("TestThePairLineNamesTheCommandAndCopiesIt", async () => {
    const h = mount([{ status: 200, body: { devices: [] } }]);
    await waitFor(() => expect(screen.getByText("No devices paired yet.")).toBeDefined());
    expect(screen.getByText("Pair a new device")).toBeDefined();
    expect(
      screen.getByText("Run this on the computer running huddle, then open the link on the device:"),
    ).toBeDefined();
    expect(screen.getByText("huddle pair").tagName).toBe("CODE");
    await act(async () => {
      screen.getByRole("button", { name: "Copy huddle pair" }).click();
    });
    expect(h.copy).toHaveBeenCalledWith("huddle pair");
  });
});
