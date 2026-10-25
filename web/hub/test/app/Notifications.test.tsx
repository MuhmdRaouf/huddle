// Notifications.test.tsx — the settings card of this device's push notifications: the choice
// read from the server, a pick saved at once, the default when the server names none, the
// finished-turn switch off by default and saved at once, the refusal of a browser that holds no paired device said in the card,
// and the Home Screen hint an iPhone or iPad gets in a Safari tab instead of the two browser switches.

import { fireEvent, screen, waitFor } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { NotificationsCard, readNotifyChoice, readTurnChoice } from "../../src/app/Notifications.tsx";
import type { PushStatus } from "../../src/pwa.ts";
import { legacyClasses } from "../helpers.tsx";
import { renderHuddle } from "../render.tsx";

/** Renders the card over one fetch answer per call, with the calls kept for asserting, and the
 *  push status this browser answers. */
function card(
  answers: Array<{ status: number; body: unknown }>,
  pushStatus: PushStatus = { supported: false, permission: "default", on: false, standalone: false },
): {
  r: ReturnType<typeof renderHuddle>;
  calls: Array<{ url: string; init?: RequestInit }>;
} {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  let n = 0;
  const r = renderHuddle(
    <NotificationsCard />,
    {},
    1,
    (url, init) => {
      calls.push(init ? { url, init } : { url });
      const a = answers[n] ?? { status: 500, body: {} };
      n++;
      const headers = { "content-type": "application/json" };
      return Promise.resolve(new Response(JSON.stringify(a.body), { status: a.status, headers }));
    },
    undefined,
    { status: async () => pushStatus, enable: async () => {}, disable: async () => {} },
  );
  return { r, calls };
}

/** The three radios, in card order. */
const radios = (): HTMLInputElement[] => [
  ...document.querySelectorAll<HTMLInputElement>('input[name="notify"]'),
];

/** One radio by its place in the card, the way the cases click them. */
const radio = (at: number): HTMLInputElement => radios()[at] as HTMLInputElement;

const IPHONE =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1";
const DESKTOP =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36";
const HINT =
  "Web push on iPhone and iPad works only from the Home Screen app. In Safari, tap Share, then Add to Home Screen, open Huddle from there and turn notifications on.";

/** Sets this browser's user agent for one case. */
const userAgent = (ua: string): void => {
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue(ua);
};

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("the notifications card on an iPhone or iPad", () => {
  it("points a Safari tab at the Home Screen instead of the two browser switches", async () => {
    userAgent(IPHONE);
    card([{ status: 200, body: { notify: "needs" } }]);
    await waitFor(() => expect(screen.getByText(HINT)).toBeDefined());
    expect(screen.queryByRole("switch", { name: "Notify me when a session asks me something" })).toBeNull();
    expect(screen.queryByRole("switch", { name: "Push to this device when a session needs me" })).toBeNull();
    expect(screen.getByRole("switch", { name: "Push when a turn finishes" })).toBeDefined();
    expect(legacyClasses()).toEqual([]);
  });

  it("points an iPad's Safari tab, which names itself a Mac, at the Home Screen", async () => {
    userAgent(DESKTOP.replace("Chrome/141.0.0.0 Safari/537.36", "Version/18.5 Safari/605.1.15"));
    vi.spyOn(navigator, "maxTouchPoints", "get").mockReturnValue(5);
    card([{ status: 200, body: { notify: "needs" } }]);
    await waitFor(() => expect(screen.getByText(HINT)).toBeDefined());
  });

  it("keeps the two switches in the installed app", async () => {
    userAgent(IPHONE);
    vi.stubGlobal("Notification", { permission: "default" });
    card([{ status: 200, body: { notify: "needs" } }], {
      supported: true,
      permission: "default",
      on: false,
      standalone: true,
    });
    await waitFor(() => expect(screen.getByText("Push permission: default.")).toBeDefined());
    expect(screen.getByRole("switch", { name: "Notify me when a session asks me something" })).toBeDefined();
    expect(screen.getByRole("switch", { name: "Push to this device when a session needs me" })).toBeDefined();
    expect(screen.queryByText(HINT)).toBeNull();
  });

  it("keeps today's text on a desktop browser", async () => {
    userAgent(DESKTOP);
    card([{ status: 200, body: { notify: "needs" } }]);
    await waitFor(() => expect(screen.getByText("This browser cannot receive push.")).toBeDefined());
    expect(screen.getByText("This browser cannot show notifications.")).toBeDefined();
    expect(screen.queryByText(HINT)).toBeNull();
  });
});

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
    expect(
      (screen.getByRole("switch", { name: "Push when a turn finishes" }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("reads the finished-turn switch off by default and saves a flip at once", async () => {
    const { r, calls } = card([
      { status: 200, body: { notify: "needs" } },
      { status: 200, body: { ok: true } },
    ]);
    const turn = (): HTMLElement => screen.getByRole("switch", { name: "Push when a turn finishes" });
    await waitFor(() => expect((turn() as HTMLButtonElement).disabled).toBe(false));
    expect(turn().getAttribute("aria-checked")).toBe("false");
    fireEvent.click(turn());
    await waitFor(() => expect(turn().getAttribute("aria-checked")).toBe("true"));
    await waitFor(() => expect(r.toast).toHaveBeenCalledWith("Notifications saved"));
    expect(JSON.parse(String(calls[1]?.init?.body))).toEqual({ turn: true });
    expect(radio(0).checked).toBe(true);
  });

  it("turns the finished-turn switch back when the save is refused", async () => {
    card([
      { status: 200, body: { notify: "needs", turn: true } },
      { status: 400, body: { error: "turn: true or false" } },
    ]);
    const turn = (): HTMLElement => screen.getByRole("switch", { name: "Push when a turn finishes" });
    await waitFor(() => expect(turn().getAttribute("aria-checked")).toBe("true"));
    fireEvent.click(turn());
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("turn"));
    expect(turn().getAttribute("aria-checked")).toBe("true");
  });

  it("paints the card, its three switches and its three radios on the tokens", async () => {
    card([{ status: 200, body: { notify: "needs" } }]);
    await waitFor(() => expect(radio(0).checked).toBe(true));
    expect(screen.getByRole("region", { name: "Notifications" }).className).toBe("ui-card");
    const switches = screen.getAllByRole("switch");
    expect(switches.map((x) => x.className)).toEqual(["ui-switch", "ui-switch", "ui-switch"]);
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

  it("reads a finished-turn choice of any shape as off", () => {
    expect(readTurnChoice({ turn: true })).toBe(true);
    expect(readTurnChoice({ turn: false })).toBe(false);
    expect(readTurnChoice({ turn: "yes" })).toBe(false);
    expect(readTurnChoice({})).toBe(false);
    expect(readTurnChoice(null)).toBe(false);
  });
});
