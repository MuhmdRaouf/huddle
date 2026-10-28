// Needs.test.tsx — the owner's actions on a needs-you item: allow once and deny on a permission
// ask, the passkey sheet the 401 "step-up required" answer opens (and its retry), the refusals
// the server answers (a cut input, an ask answered elsewhere, a switch's reason), the answer
// composer with its counter and delivery-route line, send to many from the Now list's selection
// (checkbox on the desktop, long-press on the phone), snooze and dismiss with their rollback,
// and the desktop keys a, d and r.

import type { ToastFn } from "@huddle/ui/toast.tsx";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/preact";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Api, ApiError } from "../../src/api.ts";
import { HuddleProvider } from "../../src/app/context.tsx";
import type { Layout } from "../../src/app/layout.ts";
import { NeedsBar, routeLine, routesLine, SnoozeMenu } from "../../src/app/Needs.tsx";
import { SessionsPane } from "../../src/app/Sessions.tsx";
import type { NeedItem, RegistrySession, RegistryStore } from "../../src/registry.ts";
import { flush, legacyClasses, makeCtx, makeRegistry, makeState, NOW, stubMedia } from "../helpers.tsx";

/** A fake browser credential of the assertion shape, as the mocked navigator.credentials.get
 *  resolves with it. */
const fakeAssertion = {
  id: "cred-9",
  rawId: new Uint8Array([4, 5]).buffer,
  type: "public-key",
  response: {
    authenticatorData: new Uint8Array([8]).buffer,
    clientDataJSON: new Uint8Array([7]).buffer,
    signature: new Uint8Array([6, 6]).buffer,
    userHandle: null,
  },
};

/** What credentialToJSON makes of fakeAssertion: the finish body the server reads. */
const fakeAssertionJSON = {
  id: "cred-9",
  rawId: "BAU",
  type: "public-key",
  response: {
    authenticatorData: "CA",
    clientDataJSON: "Bw",
    signature: "BgY",
    userHandle: null,
  },
};

/** Replaces navigator.credentials with one mocked get; the test reads back what it was asked. */
function stubGet(): ReturnType<typeof vi.fn> {
  const get = vi.fn(() => Promise.resolve(fakeAssertion));
  vi.stubGlobal("navigator", { ...navigator, credentials: { get } });
  return get;
}

/** The challenge the mocked get was asked with, as bytes. */
const askedChallenge = (get: ReturnType<typeof vi.fn>): number[] => {
  const asked = get.mock.calls[0]?.[0] as { publicKey: { challenge: ArrayBuffer } };
  return [...new Uint8Array(asked.publicKey.challenge)];
};

afterEach(() => {
  vi.unstubAllGlobals();
});

/** One registry row, with the fields the list and the bar read. */
const sess = (over: Partial<RegistrySession> & { session_id: string }): RegistrySession => ({
  state: "needs-you",
  title: "kanban-e2e",
  machine: "laptop",
  project: "/work/claude-code-plugins",
  branch: "port/KBN-E2E",
  last_event: "Notification",
  last_event_at: "2026-10-08T11:59:30Z",
  ...over,
});

/** One open permission ask on s-1. */
const ask = (over: Partial<NeedItem> = {}): NeedItem => ({
  id: 7,
  session: "s-1",
  kind: "permission",
  summary: "Bash: npm test -- --run e2e",
  created_at: "2026-10-08T11:59:30Z",
  state: "open",
  tool: "Bash",
  input: "npm test -- --run e2e",
  ...over,
});

/** One working row, for the selection and the keys to reach past the needs-you card. */
const working = (): RegistrySession =>
  sess({ session_id: "s-2", state: "working", title: "lead", task: "Release kanban" });

/** An HTTP failure the API client would hand over: message plus status. */
const httpError = (status: number, message: string): Error =>
  Object.assign(new Error(message), { status }) as ApiError;

/** A recording API: every call kept with its body, the answers a function of the path. The raw
 *  `call` backs the registry store's own reads, so a refill lands in the same record. */
function apiFake(answer: (path: string, n: number) => unknown = () => ({})): {
  api: Api;
  call: (path: string) => Promise<unknown>;
  calls: { path: string; body: unknown }[];
} {
  const calls: { path: string; body: unknown }[] = [];
  const call = (path: string, body?: unknown): Promise<unknown> => {
    // the list's Radar and Insights cards read their own routes, and each row reads its
    // sessions tasks for the feature chips; these are no act of the list's
    if (path.startsWith("/api/radar/") || path === "/api/insights" || path.endsWith("/tasks")) {
      return Promise.resolve({});
    }
    calls.push({ path, body });
    const r = answer(path, calls.length);
    return r instanceof Error ? Promise.reject(r) : Promise.resolve(r ?? {});
  };
  return {
    calls,
    call: (path) => call(path),
    api: {
      api: (path, o) => call(path, o?.body),
      op: () => Promise.resolve({}),
      channelPath: (ch, p) => `/api/c/${ch}${p}`,
      channelHref: (ch, p) => `#/channels/${ch}${p}`,
    } satisfies Api,
  };
}

/** Repaints on every registry change, the way the app's boot does, so a dropped item unmounts
 *  the bar that showed it. */
function Follow({ make, registry }: { make: () => ComponentChildren; registry: RegistryStore }): JSX.Element {
  const [, set] = useState(0);
  useEffect(() => registry.subscribe(() => set((n) => n + 1)), [registry]);
  return <>{make()}</>;
}

type Mount = {
  registry: RegistryStore;
  calls: { path: string; body: unknown }[];
  /** The calls that changed something: the two reads the store refills with are left out. */
  writes(): { path: string; body: unknown }[];
  toast: ReturnType<typeof vi.fn>;
  canned: { sessions: RegistrySession[]; needs: NeedItem[] };
};

/** Mounts one piece under a context whose registry repaints it, over a recording API whose
 *  reads answer the canned lists (a write path the test names answers first). */
function mount(
  make: (registry: RegistryStore) => ComponentChildren,
  over: {
    sessions?: RegistrySession[];
    needs?: NeedItem[];
    layout?: Layout;
    onWrite?: (path: string, n: number) => unknown;
  } = {},
): Mount {
  cleanup();
  stubMedia({
    "(min-width: 640px)": over.layout !== undefined && over.layout !== "phone",
    "(min-width: 1024px)": over.layout === "desktop",
  });
  const canned: Mount["canned"] = {
    sessions: over.sessions ?? [sess({ session_id: "s-1" }), working()],
    needs: over.needs ?? [ask()],
  };
  const { api, call, calls } = apiFake((path, n) => {
    if (path === "/api/registry/sessions") return { sessions: canned.sessions };
    if (path === "/api/needs") return { items: canned.needs };
    return over.onWrite?.(path, n) ?? {};
  });
  const toast = vi.fn();
  const registry = makeRegistry(
    { sessions: canned.sessions, needs: canned.needs },
    { api: (path) => call(path) },
  );
  render(
    <HuddleProvider
      value={makeCtx(makeState(), {
        registry,
        api,
        toast: toast as unknown as ToastFn,
        now: NOW,
      })}
    >
      <Follow make={() => make(registry)} registry={registry} />
    </HuddleProvider>,
  );
  return {
    registry,
    calls,
    writes: () => calls.filter((c) => c.path !== "/api/needs" && c.path !== "/api/registry/sessions"),
    toast,
    canned,
  };
}

describe("the delivery-route lines", () => {
  it("reads the now route as Delivers now and anything else as the next prompt", () => {
    expect(routeLine("now")).toBe("Delivers now");
    expect(routeLine("next_prompt")).toBe("Delivers on the next prompt");
    expect(routeLine(undefined)).toBe("Delivers on the next prompt");
    expect(routesLine(["now", "now"])).toBe("Delivers now");
    expect(routesLine(["now", "next_prompt"])).toBe("Delivers on the next prompt");
    expect(routesLine([undefined, undefined])).toBe("Delivers on the next prompt");
    expect(routesLine([])).toBe("Delivers on the next prompt");
  });
});

describe("the permission bar", () => {
  const mountBar = (
    over: {
      item?: NeedItem;
      route?: string;
      layout?: Layout;
      onWrite?: (path: string, n: number) => unknown;
    } = {},
  ): Mount =>
    mount(
      (registry) => {
        const s = registry.getState().sessions?.[0];
        const item = registry.getState().needs?.[0];
        return s && item ? <NeedsBar item={item} session={s} layout={over.layout ?? "phone"} /> : null;
      },
      {
        sessions: [
          sess({ session_id: "s-1", ...(over.route ? { delivery_route: over.route } : {}) }),
          working(),
        ],
        needs: over.item ? [over.item] : [ask()],
        ...(over.layout ? { layout: over.layout } : {}),
        ...(over.onWrite ? { onWrite: over.onWrite } : {}),
      },
    );

  it("shows the tool ask, the input, where it runs and the two answers, as the phone sheet", () => {
    mountBar();
    const sheet = document.querySelector("dialog[open].ui-sheet");
    expect(sheet).not.toBeNull();
    expect(sheet?.querySelector("[data-needs-sheet]")).not.toBeNull();
    expect(screen.getByRole("heading", { name: /kanban-e2e wants to run a command/ })).toBeDefined();
    expect(screen.getByText("npm test -- --run e2e").tagName).toBe("CODE");
    expect(
      screen.getByText(/Bash · claude-code-plugins · Runs in port\/KBN-E2E on laptop\. Waiting 30 s\./),
    ).toBeDefined();
    expect(document.querySelector("[data-needs-bar]")).toBeNull();
    const foot = sheet?.querySelector("[data-needs-foot]");
    expect(foot?.querySelector("[data-act='deny']")).not.toBeNull();
    expect(foot?.querySelector("[data-act='allow']")).not.toBeNull();
    expect(screen.getByRole("button", { name: "Deny" })).toBeDefined();
    expect(screen.getByRole("button", { name: /Allow once/ })).toBeDefined();
    expect(screen.getByRole("button", { name: "Reply instead" })).toBeDefined();
  });

  it("keeps the ask open, docked, when the owner closes the sheet without deciding", async () => {
    const h = mountBar();
    await act(async () => {
      screen.getByRole("button", { name: "Close" }).click();
    });
    await flush();
    expect(document.querySelector("dialog[open]")).toBeNull();
    expect(h.registry.getState().needs?.[0]?.id).toBe(7);
    expect(screen.getByText("Needs you · kanban-e2e wants to run a command")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Review" }).click();
    });
    await flush();
    expect(document.querySelector("dialog[open].ui-sheet")).not.toBeNull();
    expect(screen.getByRole("button", { name: /Allow once/ })).toBeDefined();
  });

  it("lays the same ask out as the wide inline bar, with the keys named on the buttons", () => {
    mountBar({ layout: "desktop" });
    expect(document.querySelector("[data-needs-bar]")).not.toBeNull();
    expect(screen.getByRole("button", { name: /Deny/ }).textContent).toContain("d");
    expect(screen.getByRole("button", { name: /Allow once/ }).textContent).toContain("a");
  });

  it("posts deny at once and drops the item", async () => {
    const h = mountBar();
    await act(async () => {
      screen.getByRole("button", { name: "Deny" }).click();
    });
    await flush();
    expect(h.calls).toEqual([{ path: "/api/needs/7/decide", body: { decision: "deny" } }]);
    expect(h.registry.getState().needs).toEqual([]);
  });

  it("posts allow once, opens the passkey sheet on 401, and retries once it resolves", async () => {
    let decides = 0;
    const h = mountBar({
      onWrite: (path) => {
        if (path === "/api/needs/7/decide") {
          decides++;
          return decides === 1 ? httpError(401, "step-up required") : {};
        }
        if (path === "/api/stepup/begin") return { options: { publicKey: { challenge: "aGk" } } };
        return {};
      },
    });
    const get = stubGet();
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/needs/7/decide"]);
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual([
      "/api/needs/7/decide",
      "/api/stepup/begin",
      "/api/stepup/finish",
      "/api/needs/7/decide",
    ]);
    expect(h.calls[1]?.body).toEqual({});
    expect(h.calls[2]?.body).toEqual({ response: fakeAssertionJSON });
    // the browser ran the server's challenge, decoded out of its base64url
    expect(askedChallenge(get)).toEqual([...new TextEncoder().encode("hi")]);
    expect(h.registry.getState().needs).toEqual([]);
    expect(screen.queryByText("Confirm with your passkey")).toBeNull();
  });

  it("posts nothing more when the owner steps out of the passkey sheet", async () => {
    const h = mountBar({
      onWrite: () => httpError(401, "step-up required"),
    });
    stubGet();
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "Cancel" }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/needs/7/decide"]);
    expect(h.registry.getState().needs?.[0]?.id).toBe(7);
    // the passkey dialog is gone; the ask sheet stays open beneath it
    expect(document.querySelector("dialog[open].ui-dialog")).toBeNull();
  });

  it("toasts a passkey check that cannot finish and keeps the sheet open", async () => {
    const h = mountBar({
      onWrite: (path) =>
        path === "/api/needs/7/decide"
          ? httpError(401, "step-up required")
          : path === "/api/stepup/finish"
            ? httpError(403, "the passkey did not verify")
            : {},
    });
    stubGet();
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("the passkey did not verify", { bad: true });
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    expect(h.calls.map((c) => c.path)).toEqual([
      "/api/needs/7/decide",
      "/api/stepup/begin",
      "/api/stepup/finish",
    ]);
  });

  it("TestTheSheetShowsWhatItConfirms", async () => {
    const h = mountBar({
      onWrite: (path) => (path === "/api/needs/7/decide" ? httpError(401, "step-up required") : {}),
    });
    stubGet();
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/needs/7/decide"]);
    const read = (term: string): string | undefined => screen.getByText(term).nextElementSibling?.textContent;
    expect(read("Action")).toBe("Allow Bash once");
    expect(read("Command")).toBe("npm test -- --run e2e");
    expect(read("Session")).toBe("kanban-e2e");
    expect(read("Machine")).toBe("laptop");
    expect(
      screen.getByText(/Confirming keeps you signed in for actions like this for 5 minutes\./),
    ).toBeDefined();
    // the question's Reply names no command and no machine, so those rows stay out
    await act(async () => {
      screen.getByRole("button", { name: "Cancel" }).click();
    });
    await flush();
    cleanup();
    const other = mountBar({
      item: ask({ kind: "question", summary: "Keep the timeout?", tool: "", input: "" }),
      onWrite: (path) =>
        path === "/api/sessions/s-1/answer"
          ? httpError(401, "step-up required")
          : path === "/api/stepup/begin"
            ? { options: { publicKey: { challenge: "aGk" } } }
            : {},
    });
    stubGet();
    await act(async () => {
      screen.getByRole("button", { name: "Answer" }).click();
    });
    fireEvent.input(screen.getByLabelText("Message"), { target: { value: "keep it" } });
    await act(async () => {
      screen.getByRole("button", { name: "Send" }).click();
    });
    await flush();
    const read2 = (term: string): string | undefined =>
      screen.getByText(term).nextElementSibling?.textContent;
    expect(read2("Action")).toBe("Reply");
    expect(read2("Session")).toBe("s-1");
    expect(screen.queryByText("Command")).toBeNull();
    expect(screen.queryByText("Machine")).toBeNull();
    // the passkey passes, and the composer posts the answer again on its own
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(other.calls.map((c) => c.path)).toEqual([
      "/api/sessions/s-1/answer",
      "/api/stepup/begin",
      "/api/stepup/finish",
      "/api/sessions/s-1/answer",
    ]);
  });

  it("keeps the sheet open when the browser's passkey prompt is dismissed", async () => {
    const h = mountBar({
      onWrite: (path) => (path === "/api/needs/7/decide" ? httpError(401, "step-up required") : {}),
    });
    vi.stubGlobal("navigator", {
      ...navigator,
      credentials: { get: vi.fn(() => Promise.reject(new DOMException("no", "NotAllowedError"))) },
    });
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(screen.getByText("The passkey was not used. Try again.")).toBeDefined();
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/needs/7/decide", "/api/stepup/begin"]);
  });

  it("shows the cut-input refusal exactly and keeps the ask", async () => {
    const h = mountBar({
      onWrite: () => httpError(409, "the input was cut; allow it in the terminal"),
    });
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("the input was cut; allow it in the terminal", { bad: true });
    expect(h.registry.getState().needs?.[0]?.id).toBe(7);
  });

  it("drops an ask the terminal answered first, and says so without the alarm", async () => {
    const h = mountBar({
      onWrite: () => httpError(409, "already answered: the terminal or the app got there first"),
    });
    await act(async () => {
      screen.getByRole("button", { name: "Deny" }).click();
    });
    await flush();
    expect(h.toast.mock.calls[0]).toEqual(["already answered: the terminal or the app got there first"]);
    expect(h.registry.getState().needs).toEqual([]);
  });

  it("shows the reason a 403 carries", async () => {
    const h = mountBar({
      onWrite: () => httpError(403, "remote permissions are off on this machine"),
    });
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("remote permissions are off on this machine", { bad: true });
    expect(h.registry.getState().needs?.[0]?.id).toBe(7);
  });

  it("replies instead: the composer counts to 4000, names the route, and posts the answer", async () => {
    const h = mountBar({ route: "now" });
    await act(async () => {
      screen.getByRole("button", { name: "Reply instead" }).click();
    });
    expect(screen.getByLabelText("Message")).toBeDefined();
    expect(screen.getByText("Delivers now")).toBeDefined();
    const box = screen.getByLabelText("Message") as HTMLTextAreaElement;
    expect((screen.getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.input(box, { target: { value: "ship it" } });
    expect(screen.getByText("7/4000")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Send" }).click();
    });
    await flush();
    expect(h.calls).toEqual([{ path: "/api/sessions/s-1/answer", body: { text: "ship it" } }]);
    expect(h.toast).toHaveBeenCalledWith("Sent");
    expect(screen.getByRole("button", { name: "Reply instead" })).toBeDefined();
  });

  it("names the next prompt when the session has no push bridge", async () => {
    mountBar();
    await act(async () => {
      screen.getByRole("button", { name: "Reply instead" }).click();
    });
    expect(screen.getByText("Delivers on the next prompt")).toBeDefined();
  });

  it("keeps the composer open and toasts the server's refusal", async () => {
    const h = mountBar({
      onWrite: () => httpError(400, "text is required: 1 to 4000 characters"),
    });
    await act(async () => {
      screen.getByRole("button", { name: "Reply instead" }).click();
    });
    fireEvent.input(screen.getByLabelText("Message"), { target: { value: "ship it" } });
    await act(async () => {
      screen.getByRole("button", { name: "Send" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("text is required: 1 to 4000 characters", { bad: true });
    expect(screen.getByLabelText("Message")).toBeDefined();
  });

  it("stops the send past 4000 characters", () => {
    mountBar();
    void act(() => {
      screen.getByRole("button", { name: "Reply instead" }).click();
    });
    fireEvent.input(screen.getByLabelText("Message"), { target: { value: "x".repeat(4001) } });
    expect(screen.getByText("4001/4000")).toBeDefined();
    expect((screen.getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("answers a question from its summary, with no Deny and no Allow once", async () => {
    mountBar({
      item: ask({
        kind: "question",
        summary: "Keep the 30 s upstream timeout?",
        tool: "",
        input: "",
      }),
    });
    expect(screen.getByRole("heading", { name: "Keep the 30 s upstream timeout?" })).toBeDefined();
    expect(screen.queryByRole("button", { name: "Deny" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Allow once/ })).toBeNull();
    await act(async () => {
      screen.getByRole("button", { name: "Answer" }).click();
    });
    expect(screen.getByLabelText("Message")).toBeDefined();
  });

  it("offers a finished turn Snooze and Dismiss under its digest line, and no Answer", async () => {
    const h = mountBar({
      item: ask({ kind: "turn", summary: "Goal: fix the login bug", tool: "", input: "" }),
    });
    expect(screen.getByRole("heading", { name: "Goal: fix the login bug" })).toBeDefined();
    expect(screen.queryByRole("button", { name: "Answer" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Deny" })).toBeNull();
    expect(screen.getByRole("button", { name: "Dismiss" }).getAttribute("class")).toContain("ui-btn-lg");
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    await act(async () => {
      screen.getByRole("menuitem", { name: "Snooze 1 hour" }).click();
    });
    await flush();
    expect(h.writes()).toEqual([{ path: "/api/needs/7/snooze", body: { until: "1h" } }]);
    h.canned.needs = [];
    await act(async () => {
      screen.getByRole("button", { name: "Dismiss" }).click();
    });
    await flush();
    expect(h.writes()[1]).toEqual({ path: "/api/needs/7/dismiss", body: {} });
    expect(legacyClasses()).toEqual([]);
  });
});

describe("the Now list's actions", () => {
  const press = (key: string): void => {
    void act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key }));
    });
  };

  it("enables the card's Deny and posts the session's permission ask", async () => {
    const h = mount(() => <SessionsPane layout="phone" />);
    const card = document.querySelector('[data-row="s-1"]');
    expect(card).not.toBeNull();
    const deny = card?.querySelector<HTMLButtonElement>('[data-act="deny"]');
    expect(deny?.disabled).toBe(false);
    await act(async () => {
      deny?.click();
    });
    await flush();
    expect(h.calls).toEqual([{ path: "/api/needs/7/decide", body: { decision: "deny" } }]);
  });

  it("posts the allow from the card's Allow once, at the card's foot", async () => {
    const h = mount(() => <SessionsPane layout="phone" />);
    const card = document.querySelector('[data-row="s-1"]');
    const allow = card?.querySelector<HTMLButtonElement>('[data-needs-actions] [data-act="allow"]');
    expect(allow?.textContent).toContain("Allow once");
    expect(allow?.className).toContain("ui-btn-lg");
    await act(async () => {
      allow?.click();
    });
    await flush();
    expect(h.calls).toEqual([{ path: "/api/needs/7/decide", body: { decision: "allow" } }]);
  });

  it("offers Review instead of Deny and Allow when the session carries no permission ask", () => {
    mount(() => <SessionsPane layout="phone" />, {
      needs: [ask({ kind: "question", tool: "", input: "", summary: "Keep the timeout?" })],
    });
    expect(document.querySelector('[data-act="deny"]')).toBeNull();
    expect(document.querySelector('[data-act="allow"]')).toBeNull();
    expect(screen.getByRole("link", { name: "Review" }).getAttribute("href")).toBe("#/sessions/s-1");
  });

  it("snoozes from the card's menu, optimistically, and rolls back when the server refuses", async () => {
    // the POST's resolution the test holds back, so the optimistic moment can be seen
    const gate: { open: ((v: unknown) => void) | null } = { open: null };
    const h = mount(() => <SessionsPane layout="phone" />, {
      onWrite: (path) =>
        path === "/api/needs/7/snooze"
          ? new Promise((r) => {
              gate.open = r;
            })
          : {},
    });
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    expect(screen.getByRole("menu", { name: "Snooze or dismiss" })).toBeDefined();
    await act(async () => {
      screen.getByRole("menuitem", { name: "Snooze 15 minutes" }).click();
    });
    // the item reads snoozed before the server has said a word
    expect(h.registry.getState().needs?.[0]?.state).toBe("snoozed");
    // the server takes the span; the snooze pushes no event, so the action rereads both lists
    gate.open?.(undefined);
    h.canned.needs = [ask({ state: "snoozed", snoozed_until: "2026-10-08T12:15:00Z" })];
    await flush();
    expect(h.writes()).toEqual([{ path: "/api/needs/7/snooze", body: { until: "15m" } }]);
    expect(h.registry.getState().needs?.[0]?.state).toBe("snoozed");
    expect(h.registry.getState().needs?.[0]?.snoozed_until).toBe("2026-10-08T12:15:00Z");

    // a refusal puts the item back and says so
    const refused = mount(() => <SessionsPane layout="phone" />, {
      onWrite: () => httpError(409, "the ask expired a moment ago"),
    });
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    await act(async () => {
      screen.getByRole("menuitem", { name: "Snooze 1 hour" }).click();
    });
    await flush();
    expect(refused.writes()).toEqual([{ path: "/api/needs/7/snooze", body: { until: "1h" } }]);
    expect(refused.registry.getState().needs?.[0]?.state).toBe("open");
    expect(refused.toast).toHaveBeenCalledWith("the ask expired a moment ago", { bad: true });
  });

  it("snoozes until tomorrow nine and dismisses, each once", async () => {
    const h = mount(() => <SessionsPane layout="phone" />);
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    await act(async () => {
      screen.getByRole("menuitem", { name: "Snooze until tomorrow 09:00" }).click();
    });
    await flush();
    expect(h.writes()).toEqual([{ path: "/api/needs/7/snooze", body: { until: "tomorrow" } }]);
    // the server's list after the dismissal: the item is gone
    h.canned.needs = [];
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    await act(async () => {
      screen.getByRole("menuitem", { name: "Dismiss" }).click();
    });
    // the optimistic dismissal takes the item away at once
    expect(h.registry.getState().needs).toEqual([]);
    await flush();
    expect(h.writes()[1]).toEqual({ path: "/api/needs/7/dismiss", body: {} });
    expect(h.registry.getState().needs).toEqual([]);
  });

  it("puts a dismissed item back when the server refuses", async () => {
    const h = mount(() => <SessionsPane layout="phone" />, {
      onWrite: () => httpError(500, "the registry's database is unavailable"),
    });
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    await act(async () => {
      screen.getByRole("menuitem", { name: "Dismiss" }).click();
    });
    await flush();
    // the refusal put the item back and said so
    expect(h.registry.getState().needs?.[0]?.id).toBe(7);
    expect(h.toast).toHaveBeenCalledWith("the registry's database is unavailable", { bad: true });
  });

  it("keeps an item gone, with no alarm, when the server says it no longer waits", async () => {
    for (const [name, path] of [
      ["Dismiss", "/api/needs/7/dismiss"],
      ["Snooze 1 hour", "/api/needs/7/snooze"],
    ] as const) {
      const h = mount(() => <SessionsPane layout="phone" />, {
        onWrite: (p) => (p === path ? httpError(404, "no open item 7") : {}),
      });
      // the server's own list: opening the session already answered the item
      h.canned.needs = [];
      await act(async () => {
        screen.getByRole("button", { name: "More actions" }).click();
      });
      await act(async () => {
        screen.getByRole("menuitem", { name }).click();
      });
      await flush();
      expect(h.writes()).toEqual([{ path, body: expect.anything() }]);
      expect(h.registry.getState().needs).toEqual([]);
      expect(h.toast).not.toHaveBeenCalled();
    }
  });

  it("denies the selected row's ask with d", async () => {
    const h = mount(() => <SessionsPane layout="desktop" />, { layout: "desktop" });
    press("d");
    await flush();
    expect(h.calls).toEqual([{ path: "/api/needs/7/decide", body: { decision: "deny" } }]);
  });

  it("allows the selected row's ask with a, through the passkey sheet when it is due", async () => {
    let decides = 0;
    const h = mount(() => <SessionsPane layout="desktop" />, {
      layout: "desktop",
      onWrite: (path) => {
        if (path === "/api/needs/7/decide") {
          decides++;
          return decides === 1 ? httpError(401, "step-up required") : {};
        }
        if (path === "/api/stepup/begin") return { options: { publicKey: { challenge: "aGk" } } };
        return {};
      },
    });
    stubGet();
    press("a");
    await flush();
    expect(screen.getByText("Confirm with your passkey")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Use passkey" }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual([
      "/api/needs/7/decide",
      "/api/stepup/begin",
      "/api/stepup/finish",
      "/api/needs/7/decide",
    ]);
  });

  it("opens the reply composer for the selected row with r and posts the answer", async () => {
    const h = mount(() => <SessionsPane layout="desktop" />, { layout: "desktop" });
    press("r");
    expect(screen.getByText("Reply to kanban-e2e")).toBeDefined();
    fireEvent.input(screen.getByLabelText("Message"), { target: { value: "keep it" } });
    await act(async () => {
      screen.getByRole("button", { name: "Send" }).click();
    });
    await flush();
    expect(h.calls).toEqual([{ path: "/api/sessions/s-1/answer", body: { text: "keep it" } }]);
    expect(screen.queryByText("Reply to kanban-e2e")).toBeNull();
  });

  it("acts only when there is something to act on", async () => {
    const h = mount(() => <SessionsPane layout="desktop" />, {
      layout: "desktop",
      needs: [ask({ kind: "question", tool: "", input: "" })],
    });
    press("a");
    press("d");
    await flush();
    expect(h.calls).toEqual([]);
  });

  it("sends to the sessions the desktop checkboxes pick", async () => {
    const h = mount(() => <SessionsPane layout="desktop" />, { layout: "desktop" });
    await act(async () => {
      screen.getByRole("button", { name: "Select" }).click();
    });
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select kanban-e2e" }).click();
      screen.getByRole("checkbox", { name: "Select lead" }).click();
    });
    expect(screen.getByText("2 selected")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Send to 2 sessions" }).click();
    });
    expect(screen.getByText("Send to 2 sessions", { selector: "dialog *" })).toBeDefined();
    fireEvent.input(screen.getByLabelText("Message"), { target: { value: "run the release" } });
    await act(async () => {
      screen.getByRole("button", { name: "Send" }).click();
    });
    await flush();
    expect(h.calls).toEqual([
      { path: "/api/send", body: { sessions: ["s-1", "s-2"], text: "run the release" } },
    ]);
    expect(h.toast).toHaveBeenCalledWith("Sent to 2 sessions");
    expect(screen.queryByLabelText("Message")).toBeNull();
  });

  it("toggles a picked row off by clicking it, and clears the bar", async () => {
    mount(() => <SessionsPane layout="desktop" />, { layout: "desktop" });
    await act(async () => {
      screen.getByRole("button", { name: "Select" }).click();
    });
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select kanban-e2e" }).click();
    });
    expect(screen.getByText("1 selected")).toBeDefined();
    await act(async () => {
      document.querySelector<HTMLElement>('[data-row="s-1"]')?.click();
    });
    expect(screen.queryByText("1 selected")).toBeNull();
    await act(async () => {
      screen.getByRole("checkbox", { name: "Select kanban-e2e" }).click();
    });
    await act(async () => {
      screen.getByRole("button", { name: "Clear" }).click();
    });
    expect(screen.queryByText("1 selected")).toBeNull();
  });

  it("picks with a long-press on the phone and sends to the pick", async () => {
    const h = mount(() => <SessionsPane layout="phone" />);
    const held = document.querySelector<HTMLElement>("[data-hold-row]");
    expect(held).not.toBeNull();
    // a press that moves or lifts early is not a hold
    await act(async () => {
      held?.dispatchEvent(new Event("pointerdown", { bubbles: true }));
      held?.dispatchEvent(new Event("pointerup", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 560));
    });
    expect(screen.queryByRole("button", { name: "Send to 1 session" })).toBeNull();
    await act(async () => {
      held?.dispatchEvent(new Event("pointerdown", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 560));
    });
    expect(screen.getByText("1 selected")).toBeDefined();
    await act(async () => {
      screen.getByRole("button", { name: "Send to 1 session" }).click();
    });
    fireEvent.input(screen.getByLabelText("Message"), { target: { value: "run the release" } });
    await act(async () => {
      screen.getByRole("button", { name: "Send" }).click();
    });
    await flush();
    expect(h.calls).toEqual([{ path: "/api/send", body: { sessions: ["s-1"], text: "run the release" } }]);
  });

  it("toggles a picked row by tapping it while a pick is on", async () => {
    mount(() => <SessionsPane layout="phone" />);
    const held = document.querySelector<HTMLElement>("[data-hold-row]");
    await act(async () => {
      held?.dispatchEvent(new Event("pointerdown", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 560));
    });
    expect(screen.getByText("1 selected")).toBeDefined();
    // the tap never opens the session while the pick is on
    await act(async () => {
      document.querySelector<HTMLElement>('[data-row="s-1"]')?.click();
    });
    expect(screen.queryByText("1 selected")).toBeNull();
    expect(location.hash).toBe("");
  });
});

describe("the needs sheet on the tokens", () => {
  /** Mounts the session screen's bar for one ask on a session the test shapes. */
  const mountAsk = (
    item: NeedItem,
    over: { layout?: Layout; session?: Partial<RegistrySession> } = {},
  ): Mount =>
    mount(
      (registry) => {
        const s = registry.getState().sessions?.[0];
        const it = registry.getState().needs?.[0];
        return s && it ? <NeedsBar item={it} session={s} layout={over.layout ?? "phone"} /> : null;
      },
      {
        sessions: [sess({ session_id: "s-1", ...over.session }), working()],
        needs: [item],
        ...(over.layout ? { layout: over.layout } : {}),
      },
    );

  const bash = (command: string): NeedItem =>
    ask({ input: JSON.stringify({ command, description: "Build the bundle" }) });

  it("shows a Bash ask's command, not its raw JSON", () => {
    mountAsk(bash("bun run build"));
    expect(screen.getByText("bun run build").tagName).toBe("CODE");
    expect(screen.queryByText(/"command"/)).toBeNull();
  });

  it("names the command in the passkey sheet an allow opens", async () => {
    const h = mount(
      (registry) => {
        const s = registry.getState().sessions?.[0];
        const it = registry.getState().needs?.[0];
        return s && it ? <NeedsBar item={it} session={s} layout="phone" /> : null;
      },
      {
        needs: [bash("bun run build")],
        onWrite: (path) => (path === "/api/needs/7/decide" ? httpError(401, "step-up required") : {}),
      },
    );
    stubGet();
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    expect(h.calls.map((c) => c.path)).toEqual(["/api/needs/7/decide"]);
    expect(screen.getByText("Command").nextElementSibling?.textContent).toBe("bun run build");
  });

  it("keeps another tool's input, and a cut Bash input, as they came", () => {
    mountAsk(ask({ tool: "Edit", input: JSON.stringify({ file_path: "/work/a.go" }) }));
    expect(screen.getByText("file_path")).toBeDefined();
    expect(screen.getByText("/work/a.go")).toBeDefined();
    const cut = '{"command":"bun run bu';
    mountAsk(ask({ input: cut, truncated: true }));
    expect(screen.getByText(cut).tagName).toBe("CODE");
    expect(screen.getByText(/the input was cut; allow it in the terminal/)).toBeDefined();
  });

  it("leaves Runs in out when the session reports no branch", () => {
    mountAsk(ask(), { session: { branch: "" } });
    expect(screen.queryByText(/Runs in/)).toBeNull();
    expect(screen.getByText(/Bash · claude-code-plugins · On laptop\. Waiting 30 s\./)).toBeDefined();
  });

  it("paints the phone sheet with real 48 px buttons and no legacy class", () => {
    mountAsk(bash("bun run build"));
    expect(screen.getByRole("button", { name: "Deny" }).getAttribute("class")).toContain(
      "ui-btn ui-btn-danger ui-btn-lg",
    );
    expect(screen.getByRole("button", { name: /Allow once/ }).getAttribute("class")).toContain(
      "ui-btn ui-btn-primary ui-btn-lg",
    );
    expect(screen.getByRole("button", { name: "Reply instead" }).getAttribute("class")).toContain("ui-btn");
    expect(legacyClasses()).toEqual([]);
  });

  it("paints the wide bar and its keys with no legacy class", () => {
    mountAsk(bash("bun run build"), { layout: "desktop" });
    expect(screen.getByRole("button", { name: /Deny/ }).getAttribute("class")).toContain(
      "ui-btn ui-btn-danger",
    );
    expect(screen.getByRole("button", { name: /Allow once/ }).getAttribute("class")).toContain(
      "ui-btn ui-btn-primary",
    );
    expect(legacyClasses()).toEqual([]);
  });

  it("paints the reply composer and the passkey sheet with no legacy class", async () => {
    mount(
      (registry) => {
        const s = registry.getState().sessions?.[0];
        const it = registry.getState().needs?.[0];
        return s && it ? <NeedsBar item={it} session={s} layout="phone" /> : null;
      },
      { onWrite: (path) => (path === "/api/needs/7/decide" ? httpError(401, "step-up required") : {}) },
    );
    stubGet();
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    expect(screen.getByRole("button", { name: "Use passkey" }).getAttribute("class")).toContain(
      "ui-btn ui-btn-primary",
    );
    expect(legacyClasses()).toEqual([]);
    await act(async () => {
      screen.getByRole("button", { name: "Cancel" }).click();
    });
    await flush();
    await act(async () => {
      screen.getByRole("button", { name: "Reply instead" }).click();
    });
    const box = screen.getByLabelText("Message").getAttribute("class") ?? "";
    expect(box).toContain("border-[var(--ui-line-strong)]");
    expect(box.split(/\s+/)).not.toContain("textarea");
    expect(legacyClasses()).toEqual([]);
  });

  it("paints the open snooze menu with no legacy class", async () => {
    mount(() => <SnoozeMenu item={ask()} />);
    await act(async () => {
      screen.getByRole("button", { name: "More actions" }).click();
    });
    expect(screen.getByRole("menu", { name: "Snooze or dismiss" })).toBeDefined();
    expect(legacyClasses()).toEqual([]);
  });
});

describe("the AskUserQuestion answer sheet", () => {
  /** The two questions as the ask's raw input: one single choice, one multi-select. */
  const askInput = JSON.stringify({
    questions: [
      {
        question: "Which database should this project use?",
        header: "Database",
        options: [
          { label: "Postgres", description: "The managed default" },
          { label: "SQLite", description: "One file on disk" },
        ],
        multiSelect: false,
      },
      {
        question: "Which linters should run?",
        header: "Lint",
        options: [
          { label: "ESLint", description: "The usual rules" },
          { label: "Biome", description: "One binary" },
        ],
        multiSelect: true,
      },
    ],
  });

  /** One AskUserQuestion ask carrying those questions. */
  const asks = (over: Partial<NeedItem> = {}): NeedItem =>
    ask({ tool: "AskUserQuestion", summary: "AskUserQuestion: 2 questions", input: askInput, ...over });

  /** Mounts the session screen's bar for one ask, the way the sheet describe does. */
  const mountAsk = (
    item: NeedItem,
    over: { layout?: Layout; onWrite?: (path: string, n: number) => unknown } = {},
  ): Mount =>
    mount(
      (registry) => {
        const s = registry.getState().sessions?.[0];
        const it = registry.getState().needs?.[0];
        return s && it ? <NeedsBar item={it} session={s} layout={over.layout ?? "phone"} /> : null;
      },
      {
        sessions: [sess({ session_id: "s-1" }), working()],
        needs: [item],
        ...(over.layout ? { layout: over.layout } : {}),
        ...(over.onWrite ? { onWrite: over.onWrite } : {}),
      },
    );

  /** Picks Postgres for the single choice and ESLint on the multi-select. */
  const answerBoth = async () => {
    await act(async () => {
      screen.getByRole("radio", { name: /Postgres/ }).click();
    });
    await act(async () => {
      screen.getByRole("checkbox", { name: /ESLint/ }).click();
    });
  };

  it("sends the owner's answers with the allow", async () => {
    const h = mountAsk(asks());
    await answerBoth();
    await act(async () => {
      screen.getByRole("button", { name: "Send answers" }).click();
    });
    await flush();
    expect(h.calls).toEqual([
      {
        path: "/api/needs/7/decide",
        body: {
          decision: "allow",
          answers: {
            "Which database should this project use?": "Postgres",
            "Which linters should run?": "ESLint",
          },
        },
      },
    ]);
  });

  it("a Bash ask still allows with no answers in the call", async () => {
    const h = mountAsk(
      ask({ input: JSON.stringify({ command: "bun test", description: "Run the unit tests" }) }),
    );
    await act(async () => {
      screen.getByRole("button", { name: /Allow once/ }).click();
    });
    await flush();
    expect(h.calls).toEqual([{ path: "/api/needs/7/decide", body: { decision: "allow" } }]);
  });

  it("a refusal speaks the server's line and keeps the sheet", async () => {
    const h = mountAsk(asks(), {
      onWrite: (path) =>
        path === "/api/needs/7/decide" ? httpError(400, "answers: name an answer for each question") : {},
    });
    await answerBoth();
    await act(async () => {
      screen.getByRole("button", { name: "Send answers" }).click();
    });
    await flush();
    expect(h.toast).toHaveBeenCalledWith("answers: name an answer for each question", { bad: true });
    expect(screen.getByRole("button", { name: "Send answers" })).toBeDefined();
  });
});
