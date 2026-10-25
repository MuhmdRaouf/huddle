import { describe, expect, it, vi } from "vite-plus/test";
import { type Api, type ApiError, createApi, type FetchFn } from "../src/api.ts";

/** A Response with a JSON body (or any body under a content-type). */
const respond = (body: string, status = 200, ct = "application/json"): Response =>
  new Response(body, { status, headers: ct ? { "content-type": ct } : {} });

const jsonOf = (j: unknown, status = 200): Response => respond(JSON.stringify(j), status);

/** A fetch that records every call and answers from one handler. */
function fakeFetch(handler: (url: string, init?: RequestInit) => Response): {
  calls: { url: string; init?: RequestInit | undefined }[];
  fetchFn: FetchFn;
} {
  const calls: { url: string; init?: RequestInit | undefined }[] = [];
  const fetchFn: FetchFn = async (url, init) => {
    calls.push({ url, init });
    return handler(url, init);
  };
  return { calls, fetchFn };
}

const expectFailure = async (p: Promise<unknown>, message: string, status: number): Promise<void> => {
  const e = await p.then(
    () => {
      throw new Error("expected a rejection");
    },
    (err: ApiError) => err,
  );
  expect(e.message).toBe(message);
  expect(e.status).toBe(status);
};

describe("api", () => {
  it("gets and parses a JSON body", async () => {
    const { calls, fetchFn } = fakeFetch(() => jsonOf({ ok: 1 }));
    const onSignedOut = vi.fn();
    const { api } = createApi(fetchFn, onSignedOut);
    await expect(api("/x")).resolves.toEqual({ ok: 1 });
    expect(calls[0]?.url).toBe("/x");
    expect(calls[0]?.init?.method).toBe("GET");
    const headers = calls[0]?.init?.headers as Record<string, string> | undefined;
    expect(headers?.["content-type"]).toBeUndefined();
    expect(onSignedOut).not.toHaveBeenCalled();
  });

  it("sends the browser's zone until an answer names the hub's zone back", async () => {
    let heard = false;
    const { calls, fetchFn } = fakeFetch(
      () =>
        new Response("{}", {
          status: 200,
          headers: { "content-type": "application/json", ...(heard ? { "x-huddle-zone": "" } : {}) },
        }),
    );
    const { api } = createApi(fetchFn, () => {}, "Europe/Istanbul");
    const zoneOf = (i: number): string | undefined =>
      (calls[i]?.init?.headers as Record<string, string> | undefined)?.["x-huddle-zone"];
    // the sign-in page's own calls answer without a credential: nothing heard the zone
    await api("/api/login/available");
    heard = true;
    await api("/api/whoami");
    await api("/api/settings");
    expect(zoneOf(0)).toBe("Europe/Istanbul");
    expect(zoneOf(1)).toBe("Europe/Istanbul");
    expect(zoneOf(2)).toBeUndefined();
  });

  it("sends no zone when the browser names none", async () => {
    const { calls, fetchFn } = fakeFetch(() => jsonOf({}));
    const { api } = createApi(fetchFn, () => {});
    await api("/x");
    const headers = calls[0]?.init?.headers as Record<string, string> | undefined;
    expect(headers?.["x-huddle-zone"]).toBeUndefined();
  });

  it("posts a JSON body by default and keeps an explicit method", async () => {
    const { calls, fetchFn } = fakeFetch(() => jsonOf({ done: true }));
    const { api } = createApi(fetchFn, () => {});
    await api("/x", { body: { a: 1 } });
    await api("/y", { method: "DELETE", body: {} });
    const posted = calls[0]?.init?.headers as Record<string, string> | undefined;
    expect(calls[0]?.init?.method).toBe("POST");
    expect(posted?.["content-type"]).toBe("application/json");
    expect(calls[0]?.init?.body).toBe('{"a":1}');
    expect(calls[1]?.init?.method).toBe("DELETE");
  });

  it("names the action on every write: the route's path, without its query", async () => {
    const { calls, fetchFn } = fakeFetch(() => jsonOf({ ok: true }));
    const { api, op } = createApi(fetchFn, () => {});
    await api("/api/settings", { body: { notify: true } });
    await api("/api/tokens/abc123", { method: "DELETE" });
    await op("My Ch", "resume", { target: "a.b" });
    const headers = (i: number): Record<string, string> | undefined =>
      calls[i]?.init?.headers as Record<string, string> | undefined;
    expect(headers(0)?.["x-huddle-action"]).toBe("/api/settings");
    expect(headers(1)?.["x-huddle-action"]).toBe("/api/tokens/abc123");
    expect(headers(2)?.["x-huddle-action"]).toBe("/api/c/My%20Ch/op/resume");
  });

  it("sends no action on a read", async () => {
    const { calls, fetchFn } = fakeFetch(() => jsonOf({ ok: 1 }));
    const { api } = createApi(fetchFn, () => {});
    await api("/api/channels");
    const headers = calls[0]?.init?.headers as Record<string, string> | undefined;
    expect(headers?.["x-huddle-action"]).toBeUndefined();
  });

  it("gives the raw text when the body is not JSON, JSON-like but unlabelled, or unlabelled entirely", async () => {
    const { fetchFn } = fakeFetch(() => respond("nope", 200, "text/plain"));
    const { api } = createApi(fetchFn, () => {});
    await expect(api("/x")).resolves.toBe("nope");

    const bare = fakeFetch(() => respond("bare", 200, "")); // no content-type at all
    await expect(createApi(bare.fetchFn, () => {}).api("/x")).resolves.toBe("bare");

    const none = fakeFetch(() => new Response("silent"));
    await expect(createApi(none.fetchFn, () => {}).api("/x")).resolves.toBe("silent");

    const bodyless = fakeFetch(() => new Response(null)); // no body, no content-type
    await expect(createApi(bodyless.fetchFn, () => {}).api("/x")).resolves.toBe("");
  });

  it("signs the browser out on a 401 that says signin, and still fails the call", async () => {
    const { fetchFn } = fakeFetch(() => jsonOf({ signin: true }, 401));
    const onSignedOut = vi.fn();
    const { api } = createApi(fetchFn, onSignedOut);
    await expectFailure(api("/x"), "HTTP 401", 401);
    expect(onSignedOut).toHaveBeenCalledTimes(1);
  });

  it("stays signed in on a 401 without signin, or with a body that will not parse", async () => {
    const onSignedOut = vi.fn();
    const no = fakeFetch(() => jsonOf({ error: "gone" }, 401));
    await expectFailure(createApi(no.fetchFn, onSignedOut).api("/x"), "gone", 401);
    expect(onSignedOut).not.toHaveBeenCalled();

    const junk = fakeFetch(() => respond("<html>", 401));
    await expectFailure(createApi(junk.fetchFn, onSignedOut).api("/x"), "HTTP 401", 401);
    expect(onSignedOut).not.toHaveBeenCalled();
  });

  it("builds the error message from the body's error, a non-string error, the text, or the status", async () => {
    const onSignedOut = vi.fn();
    const jsonError = fakeFetch(() => jsonOf({ error: "waits on t2" }, 409));
    await expectFailure(createApi(jsonError.fetchFn, onSignedOut).api("/x"), "waits on t2", 409);

    const oddError = fakeFetch(() => jsonOf({ error: 7 }, 400));
    await expectFailure(createApi(oddError.fetchFn, onSignedOut).api("/x"), "7", 400);

    const textError = fakeFetch(() => respond("boom", 500, "text/plain"));
    await expectFailure(createApi(textError.fetchFn, onSignedOut).api("/x"), "boom", 500);

    const emptyText = fakeFetch(() => respond("", 500, "text/plain"));
    await expectFailure(createApi(emptyText.fetchFn, onSignedOut).api("/x"), "HTTP 500", 500);

    const emptyJson = fakeFetch(() => jsonOf({}, 503));
    await expectFailure(createApi(emptyJson.fetchFn, onSignedOut).api("/x"), "HTTP 503", 503);
  });

  it("ops a channel: POST to /api/c/<ch>/op/<name>?as=owner with the args as the body", async () => {
    const { calls, fetchFn } = fakeFetch(() => jsonOf({ result: true }));
    const { op } = createApi(fetchFn, () => {});
    await expect(op("My Ch", "resume", { target: "a.b" })).resolves.toEqual({ result: true });
    expect(calls[0]?.url).toBe("/api/c/My%20Ch/op/resume?as=owner");
    expect(calls[0]?.init?.method).toBe("POST");
    expect(calls[0]?.init?.body).toBe('{"target":"a.b"}');
    await op("c", "digest");
    expect(calls[1]?.url).toBe("/api/c/c/op/digest?as=owner");
    expect(calls[1]?.init?.body).toBe("{}");
  });

  it("builds channel API paths (encoded) and channel routes (as the address bar spells them)", () => {
    const client: Api = createApi(
      async () => jsonOf({}),
      () => {},
    );
    expect(client.channelPath("a b", "/board")).toBe("/api/c/a%20b/board");
    expect(client.channelPath("c", "")).toBe("/api/c/c");
    expect(client.channelHref("a b", "/inbox")).toBe("#/channels/a b/inbox");
    expect(client.channelHref("c", "")).toBe("#/channels/c");
  });
});
