// Notifications.test.tsx — the settings card of this device's push notifications: the choice
// read from the server, a pick saved at once, the default when the server names none, and the
// refusal of a browser that holds no paired device said in the card.

import { fireEvent, screen, waitFor } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { NotificationsCard, readNotifyChoice } from "../../src/app/Notifications.tsx";
import { legacyClasses } from "../helpers.tsx";
import { renderHuddle } from "../render.tsx";

/** Renders the card over one fetch answer per call, with the calls kept for asserting. */
function card(answers: Array<{ status: number; body: unknown }>): {
  r: ReturnType<typeof renderHuddle>;
  calls: Array<{ url: string; init?: RequestInit }>;
} {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  let n = 0;
  const r = renderHuddle(<NotificationsCard />, {}, 1, (url, init) => {
    calls.push(init ? { url, init } : { url });
    const a = answers[n] ?? { status: 500, body: {} };
    n++;
    const headers = { "content-type": "application/json" };
    return Promise.resolve(new Response(JSON.stringify(a.body), { status: a.status, headers }));
  });
  return { r, calls };
}

/** The three radios, in card order. */
const radios = (): HTMLInputElement[] => [
  ...document.querySelectorAll<HTMLInputElement>('input[name="notify"]'),
];

/** One radio by its place in the card, the way the cases click them. */
const radio = (at: number): HTMLInputElement => radios()[at] as HTMLInputElement;

describe("the notifications card", () => {
  it("reads the device's choice and marks it", async () => {
    card([{ status: 200, body: { notify: "all" } }]);
    await waitFor(() => expect(radios()[1]?.checked).toBe(true));
    expect(radios().map((x) => x.value)).toEqual(["needs", "all", "off"]);
    expect(screen.getByText("Needs you and alerts")).toBeDefined();
  });

  it("marks needs-you only when the server names nothing", async () => {
    card([{ status: 200, body: {} }]);
    await waitFor(() => expect(radios()[0]?.checked).toBe(true));
  });

  it("saves a pick at once and says it did", async () => {
    const { r } = card([
      { status: 200, body: { notify: "needs" } },
      { status: 200, body: { ok: true } },
    ]);
    await waitFor(() => expect(radio(0).checked).toBe(true));
    fireEvent.click(radio(2));
    await waitFor(() => expect(radio(2).checked).toBe(true));
    await waitFor(() => expect(r.toast).toHaveBeenCalledWith("Notifications saved"));
  });

  it("keeps the old choice when the save is refused", async () => {
    card([
      { status: 200, body: { notify: "needs" } },
      { status: 400, body: { error: 'notify: "needs", "all" or "off"' } },
    ]);
    await waitFor(() => expect(radio(0).checked).toBe(true));
    fireEvent.click(radio(2));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("notify"));
    expect(radio(0).checked).toBe(true);
  });

  it("says what a browser without a paired device may not have", async () => {
    card([{ status: 403, body: { error: "only a paired device holds a push subscription" } }]);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("only a paired device"));
    expect(radios().every((x) => x.disabled)).toBe(true);
  });

  it("paints the card, its two switches and its three radios on the tokens", async () => {
    card([{ status: 200, body: { notify: "needs" } }]);
    await waitFor(() => expect(radio(0).checked).toBe(true));
    expect(screen.getByRole("region", { name: "Notifications" }).className).toBe("ui-card");
    const switches = screen.getAllByRole("switch");
    expect(switches.map((x) => x.className)).toEqual(["ui-switch", "ui-switch"]);
    expect(screen.getByRole("radiogroup", { name: "Notifications" }).className).toBe("ui-radios");
    expect(radios().every((x) => x.className === "ui-radio-input")).toBe(true);
    expect(legacyClasses()).toEqual([]);
  });

  it("reads a choice of any shape as the default", () => {
    expect(readNotifyChoice({ notify: "all" })).toBe("all");
    expect(readNotifyChoice({ notify: "off" })).toBe("off");
    expect(readNotifyChoice({ notify: "sometimes" })).toBe("needs");
    expect(readNotifyChoice({})).toBe("needs");
    expect(readNotifyChoice(null)).toBe("needs");
  });
});
