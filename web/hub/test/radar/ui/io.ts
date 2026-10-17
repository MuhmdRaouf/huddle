import { vi } from "vite-plus/test";
import type { HttpResponse, Io } from "../../../src/radar/app/controller.ts";
import type { SessionListItem } from "../../../src/radar/shared/api.ts";
import type { Summary } from "../../../src/radar/shared/model.ts";
import { makeRequest, uiFixtures } from "../helpers.ts";

/** The answers and handles a Radar-tab render needs: the polled reads, the backfill and a stream
 *  the test pushes its snapshot through — the same shape the controller harness answers with. */
export type RadarIo = {
  io: Io;
  /** The stream instances the controller opened; push a JSON wire message through the newest. */
  streams: { push(data: string): void; open(): void; fail(): void; closed: boolean }[];
  calls: string[];
};

export const ok = (body: unknown): HttpResponse => ({ ok: true, status: 200, json: async () => body });

export const fail = (status: number, body: unknown = {}): HttpResponse => ({
  ok: false,
  status,
  json: async () => body,
});

export const SUMMARY: Summary = {
  sessions: 2,
  liveSessions: 1,
  agents: 3,
  requests: 9,
  tokens: { input: 100, output: 40, cacheRead: 0, cacheWrite: 0 },
  errors: 0,
  toolCalls: 4,
  latencyP50: null,
  latencyP95: null,
  startedAt: null,
  now: 0,
};

/** A session list item the snapshot carries; callers spread overrides over it. */
export function snapshotItem(id: string, over: Partial<SessionListItem> = {}): SessionListItem {
  return { ...uiFixtures().sessionListItem, id, lastAt: 20, ...over };
}

/** The wire snapshot the controller's stream brings first: two sessions, the summary, no models. */
export function snapshot(sessions = [snapshotItem("s1"), snapshotItem("s2")]): string {
  return JSON.stringify({
    type: "snapshot",
    summary: SUMMARY,
    sessions,
    models: { models: [], upstreams: [], tools: [] },
    catchingUp: false,
  });
}

/** The browser IO a test stands in for: every polled route answered, every fetch recorded. */
export function radarIo(over: Partial<Record<string, HttpResponse>> = {}): RadarIo {
  const calls: string[] = [];
  const streams: RadarIo["streams"] = [];
  const answers = new Map<string, HttpResponse>([
    ["/api/alerts", ok({ alerts: [] })],
    ["/api/budget-status", ok({ version: 1, updatedAt: 0, stopped: [], spend: [] })],
    ["/api/spend", ok({ todayUsd: 1.25 })],
    ["/api/health", ok({ ok: true, version: "0.0.1", startedAt: 5, port: 4000, sessions: 2, uptimeMs: 100 })],
    ["/api/budgets", ok({ budgets: [], providers: ["zai", "kimi"] })],
    ["/api/settings", ok({ settings: { notifications: true } })],
    ["/api/history/stats", ok({ bytes: 0, nodes: 0, requests: 0, roots: 0, retentionDays: 14 })],
    ["/api/requests?limit=1000", ok({ requests: [makeRequest()] })],
    ["/api/events?limit=500", ok({ events: [] })],
    ["/api/tools?limit=1000", ok({ tools: [] })],
  ]);
  for (const [path, response] of Object.entries(over)) {
    if (response !== undefined) answers.set(path, response);
  }
  class FakeStream {
    onopen: (() => void) | null = null;
    onerror: (() => void) | null = null;
    onmessage: ((message: { data: string }) => void) | null = null;
    closed = false;

    constructor() {
      streams.push(this);
    }

    close(): void {
      this.closed = true;
    }

    open(): void {
      this.onopen?.();
    }

    fail(): void {
      this.onerror?.();
    }

    push(data: string): void {
      this.onmessage?.({ data });
    }
  }
  const io: Io = {
    fetch: vi.fn((path: string): Promise<HttpResponse> => {
      const bare = path.replace(/^\/api\/radar/, "");
      calls.push(bare);
      return Promise.resolve(answers.get(bare) ?? fail(404, { error: "no such route: " + bare }));
    }),
    EventSource: FakeStream,
    storage: mapStorage(),
    location: { hash: "" },
    replaceHash: () => undefined,
    pushHash: () => undefined,
    now: () => Date.now(),
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    download: () => undefined,
    href: () => "http://127.0.0.1:47400/",
    clipboard: () => undefined,
    revealView: () => undefined,
    focusOpener: () => undefined,
  };
  return { io, streams, calls };
}

/** A Storage stand-in over a Map, so a test's preferences never touch the browser's own. */
function mapStorage(): Storage {
  const store = new Map<string, string>();
  return {
    get length(): number {
      return store.size;
    },
    clear: () => store.clear(),
    getItem: (key: string): string | null => store.get(key) ?? null,
    key: (index: number): string | null => [...store.keys()][index] ?? null,
    removeItem: (key: string): void => {
      store.delete(key);
    },
    setItem: (key: string, value: string): void => {
      store.set(key, value);
    },
  };
}

/** Let the controller's fetches and its stream attach land, then its throttled notification fire
 *  (a burst of stream messages becomes one within 250 ms) and the repaint with it. */
export async function settleRadar(): Promise<void> {
  for (let round = 0; round < 10; round += 1) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 260));
}
