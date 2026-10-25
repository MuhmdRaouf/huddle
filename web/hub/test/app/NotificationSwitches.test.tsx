// NotificationSwitches.test.tsx — Settings › Notifications' two switches: the page's own
// notifications with the browser's permission, and the push subscription with the permission it
// runs under, iOS's install rule, a browser that cannot receive push and a refused turn-on.
import { act } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api } from "../../src/api.ts";
import type { HuddleContextValue, NotifySwitch } from "../../src/app/context.tsx";
import { NotificationsCard } from "../../src/app/Notifications.tsx";
import type { PushStatus, PushSwitch } from "../../src/pwa.ts";
import { flush, makeCtx, makeState, renderIn } from "../helpers.tsx";

afterEach(() => {
  vi.unstubAllGlobals();
});

const notifyFake = (on = false): NotifySwitch & { toggles: number } => {
  const sw = { on: () => on, toggles: 0, toggle: async () => {} };
  return Object.assign(sw, {
    toggle: () => {
      sw.toggles += 1;
      return Promise.resolve();
    },
  });
};

/** A push switch fake: the status it answers, and the turns it took. */
const pushFake = (
  start: Partial<PushStatus> = {},
  over: { enable?: () => Promise<void> } = {},
): PushSwitch & { enables: number; disables: number } => {
  const status: PushStatus = {
    supported: true,
    permission: "default",
    on: false,
    standalone: true,
    ...start,
  };
  const sw = {
    enables: 0,
    disables: 0,
    status: async () => ({ ...status }),
    enable: async () => {
      sw.enables += 1;
      if (over.enable) return over.enable();
      status.on = true;
      status.permission = "granted";
    },
    disable: async () => {
      sw.disables += 1;
      status.on = false;
    },
  };
  return sw;
};

/** An api that answers the device's notification choice. */
const api: Api = {
  api: () => Promise.resolve({ notify: "needs" }),
  op: () => Promise.resolve({}),
  channelPath: (ch, p) => `/api/c/${ch}${p}`,
  channelHref: (ch, p) => `#/channels/${ch}${p}`,
};

/** Renders the card over the switches the test hands in. */
function mount(
  over: { notify?: NotifySwitch; push?: PushSwitch; toast?: HuddleContextValue["toast"] } = {},
): void {
  renderIn(
    <NotificationsCard />,
    makeCtx(makeState(), {
      api,
      notify: over.notify ?? notifyFake(),
      push: over.push ?? pushFake(),
      toast: over.toast ?? vi.fn(),
    }),
  );
}

describe("this browser's switches", () => {
  it("flips the notify switch and reads the browser's permission", async () => {
    const notify = notifyFake(false);
    vi.stubGlobal("Notification", { permission: "granted" });
    mount({ notify });
    await flush();
    const sw = document.getElementById("set-notif") as HTMLButtonElement;
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(document.body.textContent).toContain("Browser permission: granted");
    await act(async () => {
      sw.click();
    });
    await flush();
    expect(notify.toggles).toBe(1);
    expect(sw.getAttribute("aria-checked")).toBe("false"); // the fake still says off
  });

  it("turns push on and off and reads the permission it runs under", async () => {
    const push = pushFake();
    mount({ push });
    await flush();
    const sw = document.getElementById("set-push") as HTMLInputElement;
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(document.body.textContent).toContain("Push permission: default");
    await act(async () => {
      sw.click();
    });
    await flush();
    expect(push.enables).toBe(1);
    expect(sw.getAttribute("aria-checked")).toBe("true");
    expect(document.body.textContent).toContain("Push permission: granted");
    await act(async () => {
      sw.click();
    });
    await flush();
    expect(push.disables).toBe(1);
    expect(sw.getAttribute("aria-checked")).toBe("false");
  });

  it("says iOS pushes only to the app added to the home screen", async () => {
    mount({ push: pushFake({ standalone: false }) });
    await flush();
    expect(document.body.textContent).toContain("On iPhone and iPad, add Huddle to the Home Screen");
    mount({ push: pushFake({ standalone: true }) });
    await flush();
    expect(document.getElementById("set-push")).not.toBeNull();
  });

  it("disables push where the browser cannot receive it", async () => {
    mount({ push: pushFake({ supported: false }) });
    await flush();
    expect((document.getElementById("set-push") as HTMLInputElement).disabled).toBe(true);
    expect(document.body.textContent).toContain("This browser cannot receive push.");
  });

  it("says a refused turn-on in a toast and stays off", async () => {
    const toast = vi.fn();
    const push = pushFake(
      {},
      { enable: () => Promise.reject(new Error("only a paired device holds a push subscription")) },
    );
    mount({ push, toast });
    await flush();
    const sw = document.getElementById("set-push") as HTMLInputElement;
    await act(async () => {
      sw.click();
    });
    await flush();
    expect(toast).toHaveBeenCalledWith("only a paired device holds a push subscription", { bad: true });
    expect(sw.getAttribute("aria-checked")).toBe("false");
  });

  it("says when this browser cannot show notifications", async () => {
    const saved = (window as { Notification?: unknown }).Notification;
    Reflect.deleteProperty(window, "Notification");
    mount();
    await flush();
    expect(document.body.textContent).toContain("This browser cannot show notifications.");
    if (saved !== undefined) (window as { Notification?: unknown }).Notification = saved;
  });
});
