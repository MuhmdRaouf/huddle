// SettingsTab.test.tsx — the Settings tab's six groups (Appearance, Notifications, Security,
// Spending, Tips, Hub): a card list with each row's current value on the phone that opens each
// group's page under a back link, a sidebar beside the page on the tablet and the desktop, the
// members each group page draws and the member addresses that open their group; the Security
// group: the paired devices oldest first with this browser's own chip and each device's
// freshness, the removal that asks for the passkey when the server says step-up, the refusal
// said as the server spelled it, the empty list's word; the Add-a-device section with the phone
// guide in its sheet and the copyable `huddle pair` line; the Sign-in rows whose sheets hold the
// passkey and authenticator panes; then Appearance's three choices, This hub's facts, and the
// hub's time zone with its picker.

import { act, screen, waitFor, within } from "@testing-library/preact";
import type { JSX } from "preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { FetchFn } from "../../src/api.ts";
import {
  Appearance,
  Devices,
  Phone,
  SettingsHead,
  type SettingsSection,
  SettingsTab,
  SignInMethods,
  settingsSectionOf,
  ThisHub,
  TimeZone,
} from "../../src/app/SettingsTab.tsx";
import { flush, legacyClasses, NOW, stubMedia } from "../helpers.tsx";
import { renderHuddle } from "../render.tsx";

/** One device row as the server lists it: added an hour ago, seen half a minute ago, not this
 * browser's own. */
const device = (over: Partial<{ name: string; added: string; seen: string; current: boolean }> = {}) => ({
  name: "iPhone",
  added: "2026-10-08T11:00:00Z",
  seen: "2026-10-08T11:59:30Z",
  current: false,
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
  it("TestTheListOrdersOldestFirstAndNamesEachFreshness", async () => {
    mount([
      {
        status: 200,
        body: {
          devices: [
            device({ name: "iPhone", added: "2026-10-08T11:00:00Z", seen: "2026-10-08T11:40:00Z" }),
            device({ name: "gtking", added: "2026-10-05T09:00:00Z", seen: "2026-10-05T09:00:00Z" }),
            device({
              name: "This Mac",
              added: "2026-09-01T09:00:00Z",
              seen: "2026-10-08T11:59:30Z",
              current: true,
            }),
          ],
        },
      },
    ]);
    await waitFor(() => expect(screen.getByText("This Mac")).toBeDefined());
    expect(screen.getByRole("heading", { name: "Devices · 3" })).toBeDefined();
    // oldest pairing first, and this browser's device leads the list
    const titles = [...document.querySelectorAll(".ui-list-title")].map((el) => el.textContent);
    expect(titles).toEqual(["This Mac", "gtking", "iPhone"]);
    // the caller's own device: the chip and the active-now line, and nothing to remove
    expect(screen.getByText("This device")).toBeDefined();
    expect(screen.getByText("This device · active now")).toBeDefined();
    expect(screen.getByText("This Mac").closest("li")?.querySelector("button")).toBeNull();
    // the others say how fresh they are and each carries a Remove
    expect(screen.getByText("active 20 min ago")).toBeDefined();
    expect(screen.getByText("last used 3 d")).toBeDefined();
    expect(screen.getAllByRole("button", { name: "Remove" })).toHaveLength(2);
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

  it("TestTheSectionIsOnTheTokens", async () => {
    mount([{ status: 200, body: { devices: [device()] } }]);
    await waitFor(() => expect(screen.getByText("iPhone")).toBeDefined());
    expect(screen.getByRole("button", { name: "Remove" }).className).toContain("ui-btn");
    expect(screen.getByText("A device signs out on its own after 7 days unused.")).toBeDefined();
    // the passkey sheet a removal opens is the Needs page's own
    expect(legacyClasses(document.body, "dialog")).toEqual([]);
  });

  it("TestAnEmptyListSaysSo", async () => {
    mount([{ status: 200, body: { devices: [] } }]);
    await waitFor(() => expect(screen.getByText("No devices paired yet.")).toBeDefined());
  });
});

/** Renders one section over one fetch answer per call, the calls kept for asserting. */
function mountSection(
  ui: JSX.Element,
  answers: Array<{ status: number; body: unknown }>,
): {
  calls: Array<{ url: string; init?: RequestInit }>;
  toast: ReturnType<typeof renderHuddle>["toast"];
  copy: ReturnType<typeof renderHuddle>["copy"];
} {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const queue = [...answers];
  const fetchFn: FetchFn = (url, init) => {
    calls.push(init ? { url, init } : { url });
    const a = queue.shift() ?? { status: 500, body: {} };
    return Promise.resolve(
      new Response(JSON.stringify(a.body), {
        status: a.status,
        headers: { "content-type": "application/json" },
      }),
    );
  };
  const r = renderHuddle(ui, {}, NOW, fetchFn);
  return { calls, toast: r.toast, copy: r.copy };
}

describe("the appearance section", () => {
  afterEach(() => {
    localStorage.clear();
  });

  it("TestPressesTheKeptChoiceAndPaintsAPick", async () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    localStorage.setItem("huddle:scheme", "dark");
    renderHuddle(<Appearance />);
    const pressed = (): string[] =>
      ["System", "Dark", "Light"].map(
        (name) => screen.getByRole("button", { name }).getAttribute("aria-pressed") ?? "",
      );
    expect(pressed()).toEqual(["false", "true", "false"]);
    await act(async () => {
      screen.getByRole("button", { name: "System" }).click();
    });
    expect(pressed()).toEqual(["true", "false", "false"]);
    expect(localStorage.getItem("huddle:scheme")).toBe("system");
    expect(document.documentElement.dataset.scheme).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("huddle");
  });

  it("TestOffersTheThreeSchemesAndTheCatppuccinFlavour", async () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    renderHuddle(<Appearance />);
    const scheme = screen.getByRole("group", { name: "Scheme" });
    expect([...scheme.querySelectorAll("button")].map((b) => b.textContent?.trim())).toEqual([
      "System",
      "Dark",
      "Light",
    ]);
    const flavour = screen.getByRole("switch", { name: "Catppuccin colours" });
    expect(flavour.getAttribute("aria-checked")).toBe("false");
    await act(async () => {
      flavour.click();
    });
    expect(flavour.getAttribute("aria-checked")).toBe("true");
    expect(localStorage.getItem("huddle:flavour")).toBe("catppuccin");
    expect(document.documentElement.dataset.flavour).toBe("catppuccin");
    await act(async () => {
      flavour.click();
    });
    expect(flavour.getAttribute("aria-checked")).toBe("false");
    expect(localStorage.getItem("huddle:flavour")).toBe("plain");
    expect(document.documentElement.hasAttribute("data-flavour")).toBe(false);
  });

  it("TestTheFlavourIsATokenSwitchInATapTallRow", () => {
    stubMedia({ "(prefers-color-scheme: dark)": false });
    renderHuddle(<Appearance />);
    const flavour = screen.getByRole("switch", { name: "Catppuccin colours" });
    expect(flavour.className).toBe("ui-switch");
    expect(flavour.closest("label")?.className).toContain("min-h-[var(--ui-tap)]");
    expect(legacyClasses()).toEqual([]);
  });
});

/** The phone's viewport, then the tablet's and the desktop's. */
const PHONE = { "(min-width: 640px)": false, "(min-width: 1024px)": false };
const TABLET = { "(min-width: 640px)": true, "(min-width: 1024px)": false };
const DESK = { "(min-width: 640px)": true, "(min-width: 1024px)": true };

describe("the phone's grouped list", () => {
  afterEach(() => {
    document.body.textContent = "";
  });

  /** Answers the reads the phone list makes, by path; a name in `fail` is refused instead. */
  function mountList(over: Record<string, unknown> = {}, fail: string[] = []): Array<{ url: string }> {
    stubMedia(PHONE);
    const answers: Record<string, unknown> = {
      "/health": { name: "studio", machine: "mac", version: "0.4.0" },
      "/api/whoami": { kind: "owner", device: "" },
      "/api/devices": { devices: [device()] },
      "/api/insights/settings": { tips_for_me: true },
      "/api/settings": { zone: "Africa/Cairo", zone_origin: "owner" },
      ...over,
    };
    const calls: Array<{ url: string }> = [];
    const fetchFn: FetchFn = (url) => {
      calls.push({ url });
      return Promise.resolve(
        new Response(JSON.stringify(fail.includes(url) ? {} : (answers[url] ?? {})), {
          status: fail.includes(url) ? 500 : 200,
          headers: { "content-type": "application/json" },
        }),
      );
    };
    renderHuddle(<SettingsTab section={null} />, {}, NOW, fetchFn);
    return calls;
  }

  it("TestThePhoneListShowsSixGroupsUnderThreeHeadingsWithValues", async () => {
    mountList();
    await waitFor(() =>
      expect(document.querySelector('[data-value="hub"]')?.textContent).toBe("Africa/Cairo"),
    );
    const rows = [
      ...document.querySelectorAll<HTMLAnchorElement>('[aria-label="Settings sections"] a[data-section]'),
    ];
    expect(rows.map((a) => a.dataset.section)).toEqual([
      "appearance",
      "notifications",
      "security",
      "spending",
      "tips",
      "hub",
    ]);
    expect(rows.map((a) => a.getAttribute("href"))).toEqual([
      "#/settings/appearance",
      "#/settings/notifications",
      "#/settings/security",
      "#/settings/spending",
      "#/settings/tips",
      "#/settings/hub",
    ]);
    expect(rows.map((a) => a.querySelector("[data-label]")?.textContent)).toEqual([
      "Appearance",
      "Notifications",
      "Security",
      "Spending",
      "Tips",
      "Hub",
    ]);
    expect(rows.map((a) => a.querySelector("[data-value]")?.textContent)).toEqual([
      "Huddle · System",
      "",
      "1 device",
      "",
      "On",
      "Africa/Cairo",
    ]);
    expect(
      [...document.querySelectorAll<HTMLElement>("[data-card-heading]")].map((h) => h.textContent),
    ).toEqual(["Appearance and notifications", "Security and spending", "Tips and hub"]);
    // every row carries its group's icon and opens the group's page; the list paints no panes
    expect(rows.every((a) => a.querySelector("svg"))).toBe(true);
    expect(document.querySelectorAll("h1")).toHaveLength(0);
    expect(document.querySelectorAll("[data-setting]")).toHaveLength(0);
    expect(legacyClasses()).toEqual([]);
  });

  it("TestThePhoneListSaysTheFootnote", () => {
    mountList();
    expect(document.querySelector("[data-footnote]")?.textContent).toBe(
      "Theme and appearance are kept in this browser. Everything else is kept on the hub.",
    );
  });

  it("TestThePhoneListLeadsWithTheHubCard", async () => {
    mountList({ "/api/whoami": { kind: "device", device: "Pixel 9" } });
    await waitFor(() => expect(document.querySelector("[data-hub-name]")?.textContent).toBe("studio"));
    const card = document.querySelector("a[data-hub-card]");
    expect(card?.getAttribute("href")).toBe("#/settings/hub");
    expect(card?.querySelector("[data-hub-initial]")?.textContent).toBe("S");
    expect(document.querySelector("[data-hub-subtitle]")?.textContent).toBe(
      "huddle 0.4.0 · signed in on Pixel 9",
    );
    expect(card?.textContent).toContain("Healthy");
  });

  it("TestARefusedReadLeavesTheRowEmptyAndTheCardQuiet", async () => {
    const calls = mountList({}, ["/api/devices", "/api/insights/settings", "/api/settings", "/health"]);
    await waitFor(() => expect(calls.length).toBeGreaterThanOrEqual(4));
    await flush();
    const values = [...document.querySelectorAll<HTMLElement>("[data-value]")];
    expect(values.map((v) => v.textContent)).toEqual(["Huddle · System", "", "", "", "", ""]);
    expect(document.querySelector("[data-hub-subtitle]")?.textContent).toBe("");
    expect(document.querySelector("a[data-hub-card]")?.textContent).not.toContain("Healthy");
  });

  it("TestAPhoneGroupPageDrawsItsMembersWithoutTheList", () => {
    stubMedia(PHONE);
    mountSection(<SettingsTab section="security" />, []);
    expect(document.querySelector('[aria-label="Settings sections"]')).toBeNull();
    expect(
      [...document.querySelectorAll<HTMLElement>("[data-setting]")].map((el) => el.dataset.setting),
    ).toEqual(["devices", "phone", "signin", "signout"]);
  });

  it("TestTheHeaderBacksOutToTheListAndNamesTheGroup", () => {
    renderHuddle(<SettingsHead section="hub" />);
    const back = screen.getByRole("link", { name: "Back to Settings" });
    expect(back.getAttribute("href")).toBe("#/settings");
    expect(back.className).toContain("min-h-[var(--ui-tap)]");
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Hub");
    expect(legacyClasses()).toEqual([]);
    document.body.textContent = "";
    renderHuddle(<SettingsHead section="security" />);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Security");
  });
});

describe("the wide layouts' sidebar", () => {
  afterEach(() => {
    document.body.textContent = "";
  });

  it("TestTabletAndDeskKeepTheSidebarBesideThePage", async () => {
    for (const media of [TABLET, DESK]) {
      stubMedia(media);
      mountSection(<SettingsTab section="hub" />, [{ status: 200, body: { name: "studio" } }]);
      await waitFor(() => expect(screen.getByText("studio")).toBeDefined());
      const links = [...document.querySelectorAll<HTMLAnchorElement>('[aria-label="Settings sections"] a')];
      expect(links.map((a) => a.getAttribute("aria-current"))).toEqual([
        null,
        null,
        null,
        null,
        null,
        "page",
      ]);
      expect(links.every((a) => a.className.includes("min-h-[var(--ui-tap)]"))).toBe(true);
      // each sidebar entry names its group beside its icon
      expect(links.map((a) => a.textContent)).toEqual([
        "Appearance",
        "Notifications",
        "Security",
        "Spending",
        "Tips",
        "Hub",
      ]);
      expect(links.every((a) => a.querySelector("svg"))).toBe(true);
      const nav = document.querySelector('[aria-label="Settings sections"]');
      expect(nav?.nextElementSibling?.querySelector("[data-settings]")).not.toBeNull();
      expect(legacyClasses()).toEqual([]);
      document.body.textContent = "";
    }
  });

  it("TestNoAddressOpensAppearanceBesideTheSidebar", () => {
    stubMedia(DESK);
    mountSection(<SettingsTab section={null} />, []);
    expect(document.querySelector('a[data-section="appearance"]')?.getAttribute("aria-current")).toBe("page");
    expect(
      [...document.querySelectorAll<HTMLElement>("[data-setting]")].map((el) => el.dataset.setting),
    ).toEqual(["appearance"]);
  });
});

describe("the six groups", () => {
  /** Every member a group page draws, in page order. */
  const members = (): string[] =>
    [...document.querySelectorAll<HTMLElement>("[data-setting]")].map((el) => el.dataset.setting ?? "");

  it("TestTheRowNamesTheSixGroupsInOrder", () => {
    stubMedia(DESK);
    mountSection(<SettingsTab section="appearance" />, []);
    const links = [...document.querySelectorAll<HTMLAnchorElement>('[aria-label="Settings sections"] a')];
    expect(links.map((a) => a.textContent)).toEqual([
      "Appearance",
      "Notifications",
      "Security",
      "Spending",
      "Tips",
      "Hub",
    ]);
    expect(links.map((a) => a.dataset.section)).toEqual([
      "appearance",
      "notifications",
      "security",
      "spending",
      "tips",
      "hub",
    ]);
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      "#/settings/appearance",
      "#/settings/notifications",
      "#/settings/security",
      "#/settings/spending",
      "#/settings/tips",
      "#/settings/hub",
    ]);
  });

  it("TestEachGroupPageDrawsItsMembersInOrder", () => {
    const groups: Array<[SettingsSection, string[]]> = [
      ["appearance", ["appearance"]],
      ["notifications", ["notifications"]],
      ["security", ["devices", "phone", "signin", "signout"]],
      ["spending", ["radar"]],
      ["tips", ["insights"]],
      ["hub", ["hub", "zone", "capture", "selfcheck"]],
    ];
    for (const [group, want] of groups) {
      mountSection(<SettingsTab section={group} />, []);
      expect(members(), group).toEqual(want);
      document.body.textContent = "";
    }
  });

  it("TestAMemberAddressOpensTheGroupThatHoldsIt", () => {
    expect(settingsSectionOf(undefined)).toBe("appearance");
    expect(settingsSectionOf("appearance")).toBe("appearance");
    expect(settingsSectionOf("notifications")).toBe("notifications");
    expect(settingsSectionOf("devices")).toBe("security");
    expect(settingsSectionOf("phone")).toBe("security");
    expect(settingsSectionOf("signin")).toBe("security");
    expect(settingsSectionOf("signout")).toBe("security");
    expect(settingsSectionOf("radar")).toBe("spending");
    expect(settingsSectionOf("insights")).toBe("tips");
    expect(settingsSectionOf("hub")).toBe("hub");
    expect(settingsSectionOf("zone")).toBe("hub");
    expect(settingsSectionOf("capture")).toBe("hub");
    expect(settingsSectionOf("selfcheck")).toBe("hub");
    // Linked channels live under each channel's own Settings view
    expect(settingsSectionOf("channels")).toBe("appearance");
    expect(settingsSectionOf("somewhere-else")).toBe("appearance");
  });
});

describe("the hub section", () => {
  it("TestNamesTheHubItsAddressAndVersion", async () => {
    mountSection(<ThisHub />, [{ status: 200, body: { name: "studio", machine: "mac", version: "0.0.2" } }]);
    await waitFor(() => expect(screen.getByText("studio")).toBeDefined());
    expect(screen.getByText("0.0.2")).toBeDefined();
    expect(screen.getByText(location.origin)).toBeDefined();
  });

  it("TestFallsBackToTheMachineAndSaysWhatItCannotRead", async () => {
    mountSection(<ThisHub />, [{ status: 200, body: { machine: "mac" } }]);
    await waitFor(() => expect(screen.getByText("mac")).toBeDefined());
    expect(screen.getByText("Unknown")).toBeDefined();
    mountSection(<ThisHub />, [{ status: 500, body: {} }]);
    await waitFor(() => expect(screen.getAllByText("Unknown").length).toBeGreaterThan(1));
  });
});

describe("the self-check", () => {
  afterEach(() => {
    document.body.textContent = "";
  });

  it("TestTheHubGroupSaysTheSelfCheckAndBothSidesAsText", async () => {
    const counts = { requests: 3, input: 2400, output: 600, cacheRead: 5000, cacheWrite: 2000 };
    const answer = {
      day: "2026-10-25",
      radar: { ...counts, requests: 4 },
      transcripts: counts,
      driftPct: 12.5,
    };
    const fetchFn: FetchFn = (url) =>
      Promise.resolve(
        new Response(url === "/api/radar/api/selfcheck" ? JSON.stringify(answer) : "{}", {
          status: url === "/api/radar/api/selfcheck" ? 200 : 404,
          headers: { "content-type": "application/json" },
        }),
      );
    for (const media of [PHONE, DESK]) {
      stubMedia(media);
      renderHuddle(<SettingsTab section="hub" />, {}, NOW, fetchFn);
      const pane = document.querySelector('[data-setting="selfcheck"]') as HTMLElement;
      await waitFor(() => expect(pane.querySelector("[data-selfcheck]")).not.toBeNull());
      const check = pane.querySelector("[data-selfcheck]") as HTMLElement;
      expect(check.dataset.tone).toBe("warn");
      expect(check.textContent).toContain("12.5% off the transcripts");
      expect(check.textContent).toContain(
        "Self-check for 2026-10-25 (UTC). " +
          "Radar: 4 requests, 2,400 in, 600 out, 5,000 cache read, 2,000 cache write. " +
          "Transcripts: 3 requests, 2,400 in, 600 out, 5,000 cache read, 2,000 cache write.",
      );
      // the sentence is on the page, not behind a hover
      expect(pane.querySelector("[title]")).toBeNull();
      expect(legacyClasses()).toEqual([]);
      document.body.textContent = "";
    }
  });
});

describe("the hub's time zone", () => {
  const ZONES = ["UTC", "Asia/Tokyo", "Europe/Istanbul"];

  it("TestNamesTheZoneAndThatItCameFromTheFirstSignIn", async () => {
    mountSection(<TimeZone zones={ZONES} />, [
      { status: 200, body: { notify: true, zone: "Europe/Istanbul", zone_origin: "first-sign-in" } },
    ]);
    await waitFor(() =>
      expect(screen.getByText("Time zone: Europe/Istanbul (from your first sign-in)")).toBeDefined(),
    );
    const picker = screen.getByRole("combobox", { name: "Time zone" }) as HTMLSelectElement;
    expect(picker.value).toBe("Europe/Istanbul");
    expect([...picker.options].map((o) => o.value)).toEqual(ZONES);
    expect(picker.className).toContain("ui-input");
    expect(legacyClasses()).toEqual([]);
  });

  it("TestAPickIsPutAndTheNoteGoes", async () => {
    const h = mountSection(<TimeZone zones={ZONES} />, [
      { status: 200, body: { zone: "Europe/Istanbul", zone_origin: "first-sign-in" } },
      { status: 200, body: { zone: "Asia/Tokyo", zone_origin: "owner" } },
    ]);
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Time zone" })).toBeDefined());
    const picker = screen.getByRole("combobox", { name: "Time zone" }) as HTMLSelectElement;
    await act(async () => {
      picker.value = "Asia/Tokyo";
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    const put = h.calls.find((c) => c.init?.method === "PUT");
    expect(put?.url).toBe("/api/settings");
    expect(JSON.parse(put?.init?.body as string)).toEqual({ zone: "Asia/Tokyo" });
    expect(screen.getByText("Time zone: Asia/Tokyo")).toBeDefined();
    expect(screen.queryByText(/from your first sign-in/)).toBeNull();
  });

  it("TestARefusedPickIsToastedAndTheZoneStays", async () => {
    const h = mountSection(<TimeZone zones={ZONES} />, [
      { status: 200, body: { zone: "UTC", zone_origin: "owner" } },
      { status: 400, body: { error: "time zone is not known" } },
    ]);
    await waitFor(() => expect(screen.getByText("Time zone: UTC")).toBeDefined());
    const picker = screen.getByRole("combobox", { name: "Time zone" }) as HTMLSelectElement;
    await act(async () => {
      picker.value = "Asia/Tokyo";
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("time zone is not known", { bad: true });
    expect(screen.getByText("Time zone: UTC")).toBeDefined();
  });

  it("TestAHubWithoutAZoneSaysSoAndABrowserWithoutAListTypesIt", async () => {
    const h = mountSection(<TimeZone zones={[]} />, [
      { status: 200, body: { zone: "", zone_origin: "" } },
      { status: 200, body: { zone: "UTC", zone_origin: "owner" } },
    ]);
    await waitFor(() => expect(screen.getByText("Time zone: not set yet")).toBeDefined());
    const field = screen.getByRole("textbox", { name: "Time zone" }) as HTMLInputElement;
    expect(field.value).toBe("UTC");
    await act(async () => {
      screen.getByRole("button", { name: "Save" }).click();
    });
    await flush();
    const put = h.calls.find((c) => c.init?.method === "PUT");
    expect(JSON.parse(put?.init?.body as string)).toEqual({ zone: "UTC" });
    expect(screen.getByText("Time zone: UTC")).toBeDefined();
  });
});

describe("the phone section", () => {
  it("TestAddAPhoneStartsTheGuideAndShowsItsQR", async () => {
    const h = mountSection(<Phone />, [
      {
        status: 200,
        body: { url: "http://127.0.0.1:51111/", fingerprint: "AA:BB:CC", expires_in: 600 },
      },
    ]);
    await act(async () => {
      screen.getByRole("button", { name: "Add a phone" }).click();
    });
    await flush();
    // the start names its action the way the server demands
    const post = h.calls.find((c) => c.url === "/api/phone");
    expect(post?.init?.method).toBe("POST");
    expect(JSON.parse(post?.init?.body as string)).toEqual({ action: "start" });
    // the guide stands in an open sheet: the QR the hub serves beside the fingerprint to compare
    expect(document.querySelector("dialog[open]")).not.toBeNull();
    const qr = document.querySelector("img[data-phone-qr]");
    expect(qr?.getAttribute("src")).toBe("/api/phone/qr.svg");
    expect(qr?.getAttribute("alt")).toBe("QR code: the phone's trust guide");
    expect(screen.getByText("AA:BB:CC")).toBeDefined();
    expect(screen.getByText(/compare before installing/)).toBeDefined();
  });

  it("TestAddAPhoneSaysARefusedStart", async () => {
    const h = mountSection(<Phone />, [
      { status: 500, body: { error: "could not start the phone guide: no address" } },
    ]);
    await act(async () => {
      screen.getByRole("button", { name: "Add a phone" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("could not start the phone guide: no address", {
      bad: true,
    });
    expect(document.querySelector("img[data-phone-qr]")).toBeNull();
    expect(document.querySelector("dialog[open]")).toBeNull();
  });

  it("TestThePairCommandIsSatelliteAndCopiesIt", async () => {
    const h = mountSection(<Phone />, []);
    expect(screen.getByText("Pair a machine")).toBeDefined();
    const cmd = "huddle pair --satellite";
    expect(screen.getByText(cmd).tagName).toBe("CODE");
    await act(async () => {
      screen.getByRole("button", { name: "Copy the pair command" }).click();
    });
    expect(h.copy).toHaveBeenCalledWith(cmd);
  });
});

/** Renders the Sign-in section over one whoami answer (and whatever the test queues after it),
 * with one passkey creation the stubbed browser always answers. */
function mountSignin(
  whoami: { status: number; body: unknown },
  later: Array<{ status: number; body: unknown }> = [],
): {
  toast: ReturnType<typeof renderHuddle>["toast"];
  create: ReturnType<typeof vi.fn>;
  calls: string[];
} {
  const queue = [whoami, ...later];
  const calls: string[] = [];
  const fetchFn: FetchFn = (url) => {
    calls.push(url);
    const a = queue.shift() ?? whoami;
    return Promise.resolve(
      new Response(JSON.stringify(a.body), {
        status: a.status,
        headers: { "content-type": "application/json" },
      }),
    );
  };
  const create = vi.fn(() =>
    Promise.resolve({
      id: "cred-9",
      rawId: new Uint8Array([9]).buffer,
      type: "public-key",
      response: {
        attestationObject: new Uint8Array([5]).buffer,
        clientDataJSON: new Uint8Array([6]).buffer,
      },
    }),
  );
  vi.stubGlobal("navigator", { ...navigator, credentials: { create } });
  const r = renderHuddle(<SignInMethods />, {}, NOW, fetchFn);
  return { toast: r.toast, create, calls };
}

describe("the sign-in section", () => {
  /** The section's one list; the sheets repeat the row titles, so row queries stay inside it. */
  const list = (): HTMLElement => document.querySelector(".ui-list") as HTMLElement;
  /** A row's own button, found by its title inside the list. */
  const row = (title: string): HTMLElement =>
    within(list()).getByText(title).closest("button") as HTMLElement;
  /** The sheet a row opened. */
  const sheet = (): HTMLElement => document.querySelector("dialog[open]") as HTMLElement;

  it("TestABrowserWithItsPasskeyNeedsNoOffer", async () => {
    mountSignin({
      status: 200,
      body: { kind: "device", totp: true, device_passkey: true, device: "This Mac" },
    });
    await flush();
    expect(within(list()).getByText("On · This Mac")).toBeDefined();
    expect(within(list()).getByText("On")).toBeDefined();
    await act(async () => {
      row("Passkey").click();
    });
    expect(within(sheet()).getByText("This browser signs in with its passkey.")).toBeDefined();
    expect(within(sheet()).queryByRole("button", { name: "Save passkey" })).toBeNull();
  });

  it("TestAnOwnerIsToldToSignInByCodeFirst", async () => {
    mountSignin({
      status: 200,
      body: { kind: "owner", totp: true, device_passkey: false, device: "This Mac" },
    });
    await flush();
    await act(async () => {
      row("Passkey").click();
    });
    expect(
      within(sheet()).getByText(
        "A passkey is saved per browser: sign in with a code first, then accept the offer.",
      ),
    ).toBeDefined();
    expect(within(sheet()).queryByRole("button", { name: "Save passkey" })).toBeNull();
  });

  it("TestSavePasskeyRunsTheWholeRegistrationAndReloads", async () => {
    const h = mountSignin(
      { status: 200, body: { kind: "device", totp: true, device_passkey: false, device: "This Mac" } },
      [
        { status: 200, body: { options: { publicKey: { challenge: "aGk", rp: { id: "localhost" } } } } },
        { status: 200, body: {} },
        { status: 200, body: { kind: "device", totp: true, device_passkey: true, device: "This Mac" } },
      ],
    );
    await flush();
    await act(async () => {
      row("Passkey").click();
    });
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Save passkey" }).click();
    });
    await flush();
    expect(h.create).toHaveBeenCalledTimes(1);
    expect(h.toast).toHaveBeenCalledWith("Passkey saved for this browser");
    expect(within(list()).getByText("On · This Mac")).toBeDefined();
    expect(h.calls.filter((u) => u.endsWith("/api/whoami"))).toHaveLength(2);
  });

  it("TestThePasskeyRowIsOnTheTokens", async () => {
    mountSignin({
      status: 200,
      body: { kind: "device", totp: true, device_passkey: false, device: "This Mac" },
    });
    await flush();
    await act(async () => {
      row("Passkey").click();
    });
    const save = within(sheet()).getByRole("button", { name: "Save passkey" });
    expect(save.className).toBe("ui-btn ui-btn-primary");
    const pane = document.querySelector("[data-passkey-row]") as HTMLElement;
    expect(pane).not.toBeNull();
    expect(legacyClasses(pane)).toEqual([]);
    // the panes live in dialogs; everything outside them is on the tokens
    expect(legacyClasses(document.body, "dialog")).toEqual([]);
  });

  it("TestACreationTheBrowserRefusedIsSaidInline", async () => {
    const h = mountSignin(
      { status: 200, body: { kind: "device", totp: true, device_passkey: false, device: "This Mac" } },
      [{ status: 200, body: { options: { publicKey: { challenge: "aGk", rp: { id: "localhost" } } } } }],
    );
    await flush();
    await act(async () => {
      row("Passkey").click();
    });
    h.create.mockRejectedValueOnce(new Error("cancelled"));
    await act(async () => {
      within(sheet()).getByRole("button", { name: "Save passkey" }).click();
    });
    await flush();
    expect(within(sheet()).getByText("The passkey was not created. Try again.")).toBeDefined();
    expect(within(sheet()).getByRole("button", { name: "Save passkey" })).toBeDefined();
  });

  it("TestAWhoamiTheServerRefusedIsSaidInline", async () => {
    mountSignin({ status: 500, body: {} });
    await flush();
    expect(screen.getByRole("alert")).toBeDefined();
    expect(screen.queryByRole("button", { name: "Save passkey" })).toBeNull();
  });

  it("TestTheAuthenticatorRowOpensItsPane", async () => {
    mountSignin({
      status: 200,
      body: { kind: "device", totp: false, device_passkey: true, device: "This Mac" },
    });
    await flush();
    expect(within(list()).getByText("Off")).toBeDefined();
    await act(async () => {
      row("Authenticator app").click();
    });
    expect(
      within(sheet()).getByText(/Not set up\. A code from your app would sign any browser in\./),
    ).toBeDefined();
    expect(within(sheet()).getByRole("button", { name: "Set up" })).toBeDefined();
  });
});
