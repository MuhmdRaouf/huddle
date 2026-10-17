// helpers.tsx — what the app and pages tests share: a full store state, a context value over it,
// a matchMedia stub, and a channel api fake.
import { act, type RenderResult, render } from "@testing-library/preact";
import type { ComponentChildren } from "preact";
import { vi } from "vite-plus/test";
import type { Api } from "../src/api.ts";
import { type HuddleContextValue, HuddleProvider } from "../src/app/context.tsx";
import { createRegistry, type RegistryState, type RegistryStore } from "../src/registry.ts";
import type {
  AskItem,
  Attention,
  BlockedItem,
  GateItem,
  HuddleState,
  PausedItem,
  PlanStep,
  RosterSession,
} from "../src/store.ts";

/** One deterministic paint time. */
export const NOW = Date.parse("2026-10-08T12:00:00Z");

/** A full store state with everything empty; the test overrides what it looks at. */
export function makeState(over: Partial<HuddleState> = {}): HuddleState {
  return {
    ch: "ch",
    info: { name: "ch", config: { title: "Checkout" } },
    board: null,
    byId: new Map<string, PlanStep>(),
    sessions: null,
    timeline: null,
    attention: null,
    channels: null,
    replies: new Map<number, string[]>(),
    extras: null,
    live: "connecting",
    ...over,
  };
}

/** A registry store over an API that answers nothing; the test seeds its state directly. An api
 *  names the reads the store's own refills make (an action's re-read after its write). */
export function makeRegistry(
  over: Partial<RegistryState> = {},
  api: Pick<Api, "api"> = { api: () => Promise.reject(new Error("no canned answer")) },
): RegistryStore {
  const registry = createRegistry({ api });
  Object.assign(registry.getState(), { sessions: [], ...over });
  return registry;
}

/** The context value the tests run under; `go` really moves the address bar. */
export function makeCtx(state: HuddleState, over: Partial<HuddleContextValue> = {}): HuddleContextValue {
  return {
    state,
    registry: makeRegistry(),
    api: {
      api: () => Promise.resolve({}),
      op: () => Promise.resolve({}),
      channelPath: (ch, p) => `/api/c/${ch}${p}`,
      channelHref: (ch, p) => `#/channels/${ch}${p}`,
    },
    now: NOW,
    updatedAt: NOW,
    go: (h) => {
      location.hash = h;
    },
    act: async () => true,
    toast: vi.fn(),
    copy: vi.fn(),
    ...over,
  };
}

/** Renders a component under a HuddleProvider built from `ctx`. */
export function renderIn(
  ui: ComponentChildren,
  ctx: HuddleContextValue = makeCtx(makeState()),
): RenderResult {
  return render(<HuddleProvider value={ctx}>{ui}</HuddleProvider>);
}

/** Replaces matchMedia with one fake query whose `matches` the test flips through `fire`. */
export function stubMatchMedia(matches = false): { fire(): void } {
  let listener: (() => void) | null = null;
  const mq = {
    matches,
    addEventListener: (_t: string, fn: () => void) => {
      listener = fn;
    },
    removeEventListener: () => {
      listener = null;
    },
  };
  vi.stubGlobal("matchMedia", () => mq);
  return {
    fire: () => listener?.(),
  };
}

/** Replaces matchMedia with one answer per query text, any other query answering false. `fire`
 *  flips one query and calls its listeners, the way a resize crossing a breakpoint does. */
export function stubMedia(over: Record<string, boolean> = {}): { fire(query: string): void } {
  const state = new Map(Object.entries(over));
  const listeners = new Map<string, (() => void)[]>();
  vi.stubGlobal(
    "matchMedia",
    (q: string) =>
      ({
        get matches(): boolean {
          return state.get(q) ?? false;
        },
        addEventListener: (_t: string, fn: () => void) => {
          listeners.set(q, [...(listeners.get(q) ?? []), fn]);
        },
        removeEventListener: (_t: string, fn: () => void) => {
          listeners.set(
            q,
            (listeners.get(q) ?? []).filter((f) => f !== fn),
          );
        },
      }) as MediaQueryList,
  );
  return {
    fire: (query: string): void => {
      state.set(query, !(state.get(query) ?? false));
      for (const fn of listeners.get(query) ?? []) fn();
    },
  };
}

/** A clipboard whose writes the test can fail or watch. */
export function stubClipboard(impl: () => Promise<void> = () => Promise.resolve()): { writes: string[] } {
  const writes: string[] = [];
  vi.stubGlobal("navigator", {
    ...navigator,
    clipboard: {
      writeText: (t: string) => {
        writes.push(t);
        return impl();
      },
    },
  });
  return { writes };
}

/** A session of the roster, with the fields the nav reads. */
export function sess(over: Partial<RosterSession> & { name: string }): RosterSession {
  return { state: "idle", unread: 0, open: 0, holds_turn: false, parent: null, ...over };
}

/** An attention snapshot: the kinds a test names; the rest are empty. */
export function attention(n: {
  asks?: AskItem[];
  gates?: GateItem[];
  paused?: PausedItem[];
  blocked?: BlockedItem[];
}): Attention {
  return {
    asks: n.asks ?? [],
    gates: n.gates ?? [],
    paused: n.paused ?? [],
    blocked: n.blocked ?? [],
  };
}

/** Lets a promise-driven update land inside act: the chain resolves and the rerender flushes. */
export function flush(): Promise<void> {
  return act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}

/** A wider flush for teardown-time work (an unmount's effect cleanup lands late in happy-dom). */
export function settle(): Promise<void> {
  return act(async () => {
    await new Promise((r) => setTimeout(r, 10));
  });
}
