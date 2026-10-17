// sw.test.ts — the service worker's own work, run against the exact bytes that ship
// (public/sw.js, a classic worker script served after the build stamps its cache revision): the
// app shell is cached under the revision's name and live answers — the API, the MCP bridge, the
// SSE stream — stay the network's; navigations prefer the network and fall back to the cached
// shell offline; activate deletes every cache an older revision left; a push shows the
// notification its payload names and its actions open the app at the item or deny it at once.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it, vi } from "vite-plus/test";

// import.meta.url is an http URL under happy-dom, so the worker's bytes come by directory name
const source = readFileSync(join(import.meta.dirname, "../public/sw.js"), "utf8");

const ORIGIN = "http://127.0.0.1:18860";

/** What the worker reads of a Response: its ok, and the clone a cache fill keeps. */
type FakeResponse = {
  url: string;
  ok: boolean;
  status: number;
  body: string;
  clone(): FakeResponse;
};

const reply = (href: string, body: string, ok = true): FakeResponse => {
  const r: FakeResponse = { url: href, ok, status: ok ? 200 : 500, body, clone: () => r };
  return r;
};

/** What the worker reads of a Request. */
type FakeRequest = { url: string; method: string; mode?: string };

const get = (path: string): FakeRequest => ({ url: `${ORIGIN}${path}`, method: "GET" });
const navigate = (path: string): FakeRequest => ({
  url: `${ORIGIN}${path}`,
  method: "GET",
  mode: "navigate",
});

/** The cache key the browser's Cache API matches on: the URL, fragment aside. */
const href = (input: string | FakeRequest): string => {
  const u = new URL(typeof input === "string" ? input : input.url, ORIGIN);
  u.hash = "";
  return u.href;
};

/** The slice of the Cache API the worker uses, over one map. */
class FakeCache {
  entries = new Map<string, FakeResponse>();

  async match(request: string | FakeRequest): Promise<FakeResponse | undefined> {
    return this.entries.get(href(request));
  }

  async put(request: string | FakeRequest, response: FakeResponse): Promise<void> {
    this.entries.set(href(request), response);
  }
}

/** The slice of `caches` the worker uses. */
class FakeCaches {
  private readonly store = new Map<string, FakeCache>();

  async open(name: string): Promise<FakeCache> {
    let cache = this.store.get(name);
    if (!cache) {
      cache = new FakeCache();
      this.store.set(name, cache);
    }
    return cache;
  }

  async keys(): Promise<string[]> {
    return [...this.store.keys()];
  }

  async delete(name: string): Promise<boolean> {
    return this.store.delete(name);
  }
}

/** One worker run: the fakes its script runs against, and the listeners it registered. */
type WorkerRun = {
  caches: FakeCaches;
  fetch: ReturnType<typeof vi.fn<(input: string | FakeRequest, init?: RequestInit) => Promise<FakeResponse>>>;
  /** The notifications the worker asked the registration to show, newest last. */
  shown: Array<{ title: string; options: Record<string, unknown> }>;
  /** The paths the worker opened in a new window, newest last. */
  opened: string[];
  /** The window clients the worker saw, in matchAll's answer. */
  windowClients: Array<{
    url: string;
    focus: ReturnType<typeof vi.fn<() => Promise<null>>>;
    navigate: ReturnType<typeof vi.fn<(path: string) => Promise<null>>>;
  }>;
  install(): Promise<void>;
  activate(): Promise<void>;
  /** Delivers one push message's payload to the worker. */
  pushed(payload: unknown, torn?: boolean): Promise<void>;
  /** Delivers one notification click: the action's name and the notification's data. */
  clicked(action: string, data: unknown): Promise<void>;
  fetchHandled(request: FakeRequest): { answered: boolean; response(): Promise<FakeResponse> };
};

/** Runs public/sw.js against a fake worker scope; network answers every URL with its own href. */
const worker = (network: (input: string | FakeRequest) => Promise<FakeResponse> = net): WorkerRun => {
  const listeners = new Map<string, Array<(event: unknown) => void>>();
  const shown: WorkerRun["shown"] = [];
  const opened: string[] = [];
  const windowClients: WorkerRun["windowClients"] = [];
  const scope = {
    location: { origin: ORIGIN },
    skipWaiting: () => Promise.resolve(),
    registration: {
      showNotification: (title: string, options: Record<string, unknown>) => {
        shown.push({ title, options });
        return Promise.resolve();
      },
    },
    clients: {
      claim: () => Promise.resolve(),
      matchAll: () => Promise.resolve(windowClients),
      openWindow: (path: string) => {
        opened.push(path);
        return Promise.resolve(null);
      },
    },
    addEventListener: (kind: string, fn: (event: unknown) => void) => {
      listeners.set(kind, [...(listeners.get(kind) ?? []), fn]);
    },
  };
  const fetchStub = vi.fn(network);
  const cachesApi = new FakeCaches();
  // the worker ships as a classic script with no module form, so the test runs its exact bytes
  // oxlint-disable-next-line typescript/no-implied-eval -- the Function constructor is the only way to run those exact bytes
  new Function("self", "caches", "fetch", source)(scope, cachesApi, fetchStub);
  const fired = async (kind: string, event: Record<string, unknown>): Promise<void> => {
    const waited: Array<Promise<unknown>> = [];
    for (const fn of listeners.get(kind) ?? [])
      fn({ ...event, waitUntil: (p: Promise<unknown>) => waited.push(p) });
    await Promise.all(waited);
  };
  return {
    caches: cachesApi,
    fetch: fetchStub,
    shown,
    opened,
    windowClients,
    install: () => fired("install", {}),
    activate: () => fired("activate", {}),
    pushed: (payload, torn) =>
      fired("push", {
        data: {
          json: () => {
            if (torn) throw new Error("not JSON");
            return payload;
          },
        },
      }),
    clicked: (action, data) =>
      fired("notificationclick", { action, notification: { close: () => {}, data } }),
    fetchHandled: (request: FakeRequest) => {
      const holder: { answer: { p: Promise<FakeResponse> } | null } = { answer: null };
      for (const fn of listeners.get("fetch") ?? [])
        fn({
          request,
          waitUntil: () => {},
          respondWith: (p: Promise<FakeResponse>) => {
            holder.answer = { p };
          },
        });
      return {
        answered: holder.answer !== null,
        response: () => {
          if (!holder.answer) throw new Error("the worker passed this request through");
          return holder.answer.p;
        },
      };
    },
  };
};

const net = (input: string | FakeRequest): Promise<FakeResponse> =>
  Promise.resolve(reply(href(input), `net:${href(input)}`));

const theCache = async (w: WorkerRun): Promise<FakeCache> => w.caches.open((await w.caches.keys())[0] ?? "");

describe("install", () => {
  it("precaches the shell into a cache named for the revision", async () => {
    const w = worker();
    await w.install();
    const names = await w.caches.keys();
    expect(names).toHaveLength(1);
    expect(names[0]).toMatch(/^huddle-shell-.+$/);
    const cache = await theCache(w);
    expect([...cache.entries.keys()]).toEqual([
      href("/"),
      href("/index.html"),
      href("/static/theme-boot.js"),
      href("/static/app.css"),
      href("/static/app.js"),
      href("/manifest.webmanifest"),
      href("/icons/icon-192.png"),
      href("/icons/icon-512.png"),
      href("/icons/icon-maskable-512.png"),
    ]);
  });
});

describe("fetch routing", () => {
  it("never answers live paths: /api, /mcp and the SSE stream stay the network's", async () => {
    const w = worker();
    await w.install();
    w.fetch.mockClear();
    for (const path of ["/api/channels", "/api/c/team/attention", "/api/c/team/live", "/mcp", "/mcp/team"]) {
      expect(w.fetchHandled(get(path)).answered, path).toBe(false);
    }
    expect(w.fetch).not.toHaveBeenCalled();
  });

  it("never answers a write or another origin's request", async () => {
    const w = worker();
    await w.install();
    w.fetch.mockClear();
    expect(w.fetchHandled({ url: `${ORIGIN}/static/app.js`, method: "POST" }).answered).toBe(false);
    expect(w.fetchHandled({ url: "https://cdn.example/framework.js", method: "GET" }).answered).toBe(false);
    expect(w.fetch).not.toHaveBeenCalled();
  });

  it("serves the precached shell from the cache, without the network", async () => {
    const w = worker();
    await w.install();
    w.fetch.mockClear();
    const handled = w.fetchHandled(get("/static/app.js"));
    expect(handled.answered).toBe(true);
    await expect(handled.response()).resolves.toHaveProperty("body", `net:${href("/static/app.js")}`);
    expect(w.fetch).not.toHaveBeenCalled();
  });

  it("fills a shell file it meets for the first time, then serves it cache-first", async () => {
    const w = worker();
    await w.install();
    w.fetch.mockClear();
    const first = w.fetchHandled(get("/fonts/plex.woff2"));
    expect(first.answered).toBe(true);
    await expect(first.response()).resolves.toHaveProperty("body", `net:${href("/fonts/plex.woff2")}`);
    expect(w.fetch).toHaveBeenCalledTimes(1);
    const again = w.fetchHandled(get("/fonts/plex.woff2"));
    await expect(again.response()).resolves.toHaveProperty("body", `net:${href("/fonts/plex.woff2")}`);
    expect(w.fetch).toHaveBeenCalledTimes(1);
    expect(await (await theCache(w)).match("/fonts/plex.woff2")).toBeDefined();
  });

  it("passes a same-origin path that is neither live nor shell to the network untouched", async () => {
    const w = worker();
    await w.install();
    w.fetch.mockClear();
    expect(w.fetchHandled(get("/connect.md")).answered).toBe(false);
    expect(w.fetch).not.toHaveBeenCalled();
  });

  it("answers navigations from the network while it answers, and from the cached shell offline", async () => {
    const w = worker();
    await w.install();
    w.fetch.mockClear();
    const online = w.fetchHandled(navigate("/#/now"));
    expect(online.answered).toBe(true);
    await expect(online.response()).resolves.toHaveProperty("body", `net:${href("/#/now")}`);
    w.fetch.mockImplementation(() => Promise.reject<FakeResponse>(new Error("offline")));
    const offline = w.fetchHandled(navigate("/#/now"));
    expect(offline.answered).toBe(true);
    await expect(offline.response()).resolves.toHaveProperty("body", `net:${href("/")}`);
  });
});

describe("sign-in links", () => {
  it("never keeps a one-time sign-in page as the offline shell", async () => {
    const w = worker();
    await w.install();
    const online = w.fetchHandled(navigate("/?code=abc123"));
    await expect(online.response()).resolves.toHaveProperty("body", `net:${href("/?code=abc123")}`);
    await Promise.resolve();
    w.fetch.mockImplementation(() => Promise.reject<FakeResponse>(new Error("offline")));
    const offline = w.fetchHandled(navigate("/#/now"));
    await expect(offline.response()).resolves.toHaveProperty("body", `net:${href("/")}`);
  });
});

describe("activate", () => {
  it("deletes every cache an older revision left, and only those", async () => {
    const w = worker();
    await w.install();
    const current = (await w.caches.keys())[0] ?? "";
    await w.caches.open("huddle-shell-v0");
    await w.caches.open("not-ours");
    await w.activate();
    expect(await w.caches.keys()).toEqual([current, "not-ours"]);
  });
});

// The push (SPEC 7.6): the payload carries who, what kind and the tool name, and the
// notification shows no more than that. Allow and Open land in the app at the item — where
// deciding asks for the passkey — and Deny decides at once with the device cookie the fetch
// itself carries.
describe("push", () => {
  const permission = { item: 7, session: "s-1", machine: "laptop", kind: "permission", tool: "Bash" };

  it("shows a permission item's notification with Allow, Deny and Open", async () => {
    const w = worker();
    await w.pushed(permission);
    expect(w.shown).toHaveLength(1);
    const shown = w.shown[0];
    if (!shown) throw new Error("nothing shown");
    const { title, options } = shown;
    expect(title).toBe("Bash");
    expect(options.body).toBe("s-1 on laptop");
    expect(options.data).toEqual(permission);
    expect(options.tag).toBe("huddle-item-7");
    expect(options.actions).toEqual([
      { action: "allow", title: "Allow" },
      { action: "deny", title: "Deny" },
      { action: "open", title: "Open" },
    ]);
  });

  it("shows every other kind with Open alone", async () => {
    const w = worker();
    await w.pushed({ item: 8, session: "s-2", machine: "", kind: "question" });
    expect(w.shown).toHaveLength(1);
    const shown = w.shown[0];
    if (!shown) throw new Error("nothing shown");
    expect(shown.title).toBe("question");
    expect(shown.options.body).toBe("s-2");
    expect(shown.options.actions).toEqual([{ action: "open", title: "Open" }]);
  });

  it("shows a session's needs-you entry as Needs you, tagged by its session", async () => {
    const w = worker();
    await w.pushed({ kind: "session", session: "s-3", machine: "studio" });
    expect(w.shown).toHaveLength(1);
    const shown = w.shown[0];
    if (!shown) throw new Error("nothing shown");
    expect(shown.title).toBe("Needs you");
    expect(shown.options.body).toBe("s-3 on studio");
    expect(shown.options.tag).toBe("huddle-session-s-3");
    expect(shown.options.actions).toEqual([{ action: "open", title: "Open" }]);
  });

  it("shows nothing for a message it cannot read", async () => {
    const w = worker();
    await w.pushed(null);
    await w.pushed({}, true);
    expect(w.shown).toHaveLength(0);
  });
});

describe("notificationclick", () => {
  const permission = { item: 7, session: "s-1", machine: "laptop", kind: "permission", tool: "Bash" };

  it("opens the app at the item for Allow and Open", async () => {
    for (const action of ["allow", "open", ""]) {
      const w = worker();
      await w.install();
      await w.clicked(action, permission);
      expect(w.opened).toEqual(["/#/now/s-1?item=7"]);
    }
  });

  it("opens a session's entry at the session itself, with no item query", async () => {
    const w = worker();
    await w.install();
    await w.clicked("open", { kind: "session", session: "s-3", machine: "studio" });
    expect(w.opened).toEqual(["/#/now/s-3"]);
  });

  it("brings the window that is already there to the item", async () => {
    const w = worker();
    await w.install();
    w.windowClients.push({
      url: `${ORIGIN}/`,
      focus: vi.fn(() => Promise.resolve(null)),
      navigate: vi.fn(() => Promise.resolve(null)),
    });
    await w.clicked("allow", permission);
    const client = w.windowClients[0];
    if (!client) throw new Error("the window client is gone");
    expect(client.focus).toHaveBeenCalledTimes(1);
    expect(client.navigate).toHaveBeenCalledWith("/#/now/s-1?item=7");
    expect(w.opened).toEqual([]);
  });

  it("decides deny at once over the device's own cookie, and opens nothing", async () => {
    const w = worker();
    await w.install();
    w.fetch.mockClear();
    await w.clicked("deny", permission);
    expect(w.fetch).toHaveBeenCalledTimes(1);
    const call = w.fetch.mock.calls[0];
    if (!call) throw new Error("no fetch");
    const [path, init] = call;
    expect(path).toBe("/api/needs/7/decide");
    expect(init?.method).toBe("POST");
    expect(init?.credentials).toBe("include");
    expect(init?.headers).toEqual({
      "content-type": "application/json",
      "x-huddle-action": "/api/needs/7/decide",
    });
    expect(init?.body).toBe(JSON.stringify({ decision: "deny" }));
    expect(w.opened).toEqual([]);
  });

  it("lets a deny that cannot reach the server pass quietly", async () => {
    const w = worker();
    await w.install();
    w.fetch.mockImplementation(() => Promise.reject(new Error("offline")));
    await expect(w.clicked("deny", permission)).resolves.toBeUndefined();
  });
});
