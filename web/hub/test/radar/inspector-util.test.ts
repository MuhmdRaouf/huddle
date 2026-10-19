// inspector-util.test.ts — the inspector's pure helpers: the HTTP status a stop reason carries,
// the retries around a failed call, and the JSON and capture shapes the Raw and Context tabs render.

import { describe, expect, it } from "vite-plus/test";
import {
  captureSystemText,
  captureTools,
  firstLine,
  httpStatusOf,
  nearbyRetries,
  prettyJson,
  rawDocument,
  retriesText,
} from "../../src/radar/app/inspector/util.ts";
import { makeRequest } from "./helpers.ts";

describe("httpStatusOf", () => {
  it("reads the recorded status a router line carried, success or failure", () => {
    expect(httpStatusOf(makeRequest({ status: "200" }))).toBe(200);
    expect(httpStatusOf(makeRequest({ status: "429" }))).toBe(429);
    expect(httpStatusOf(makeRequest({ stopReason: "end_turn" }))).toBeNull();
  });

  it("reads a bare three-digit stop reason as the status, anything else as none", () => {
    expect(httpStatusOf(makeRequest({ stopReason: "429" }))).toBe(429);
    expect(httpStatusOf(makeRequest({ stopReason: "end_turn" }))).toBeNull();
    expect(httpStatusOf(makeRequest({ stopReason: null }))).toBeNull();
    expect(httpStatusOf(makeRequest({ stopReason: "99" }))).toBeNull();
  });

  it("prefers the recorded status over a numeric stop reason, and never reads a real reason", () => {
    expect(httpStatusOf(makeRequest({ status: "200", stopReason: "502" }))).toBe(200);
    expect(httpStatusOf(makeRequest({ status: "ok" }))).toBeNull();
  });
});

describe("nearbyRetries", () => {
  const at = 1_000_000;

  it("holds the same chain's failed calls just before this one, newest first", () => {
    const request = makeRequest({ id: "req", sessionId: "s1", agentId: "main", ts: at });
    const retries = [
      makeRequest({
        id: "old-other-session",
        sessionId: "s2",
        agentId: "main",
        ts: at - 1000,
        stopReason: "429",
      }),
      makeRequest({ id: "old-ok", sessionId: "s1", agentId: "main", ts: at - 2000, stopReason: "end_turn" }),
      makeRequest({
        id: "old-far",
        sessionId: "s1",
        agentId: "main",
        ts: at - 3 * 60_000,
        stopReason: "500",
      }),
      makeRequest({ id: "old-b", sessionId: "s1", agentId: "main", ts: at - 20_000, stopReason: "500" }),
      makeRequest({ id: "old-a", sessionId: "s1", agentId: "main", ts: at - 10_000, stopReason: "429" }),
    ];
    const found = nearbyRetries([...retries, request], request);
    expect(found.map((retry) => retry.id)).toEqual(["old-a", "old-b"]);
  });

  it("never counts the request itself", () => {
    const request = makeRequest({ id: "req", sessionId: "s1", agentId: "main", ts: at, stopReason: "500" });
    expect(nearbyRetries([request], request)).toEqual([]);
  });
});

describe("retriesText", () => {
  it("says none when nothing is held, else the count with the statuses", () => {
    expect(retriesText([])).toBe("None recorded");
    const retries = [makeRequest({ stopReason: "429" }), makeRequest({ stopReason: "500" })];
    expect(retriesText(retries)).toBe("2 within 2 min (429, 500)");
    // a retry whose stop reason is a real word reads as an unknown status
    const worded = [makeRequest({ stopReason: "overloaded_error" })];
    expect(retriesText(worded)).toBe("1 within 2 min (?)");
  });
});

describe("prettyJson", () => {
  it("indents objects, quotes a string, and survives circular values", () => {
    expect(prettyJson({ a: 1 })).toBe('{\n  "a": 1\n}');
    expect(prettyJson("plain")).toBe('"plain"');
    const circular: { self?: unknown } = {};
    circular.self = circular;
    expect(prettyJson(circular)).toBe("[object Object]");
  });
});

describe("firstLine", () => {
  it("cuts at the first newline and trims", () => {
    expect(firstLine("  first\nsecond\n")).toBe("first");
    expect(firstLine("only")).toBe("only");
  });
});

describe("rawDocument", () => {
  it("names the request always and the sides once they are stored", () => {
    const request = makeRequest();
    expect(rawDocument(request, null)).toEqual({ request });
    expect(rawDocument(request, { input: [1], output: "out" })).toEqual({
      request,
      input: [1],
      output: "out",
    });
  });
});

describe("captureSystemText", () => {
  it("joins text blocks, keeps a plain string, empties on none, and prints anything else", () => {
    expect(captureSystemText("You are huddle.")).toBe("You are huddle.");
    expect(captureSystemText([{ text: "one" }, { type: "image" }, { text: "two" }])).toBe("one\n\ntwo");
    expect(captureSystemText(null)).toBe("");
    expect(captureSystemText(undefined)).toBe("");
    expect(captureSystemText({ odd: true })).toBe('{\n  "odd": true\n}');
  });
});

describe("captureTools", () => {
  it("keeps the tool objects the wire carried and drops the rest", () => {
    const tool = { name: "Bash", input_schema: {} };
    expect(captureTools([tool, "noise", 7, null, [1]])).toEqual([tool]);
    expect(captureTools("not a list")).toEqual([]);
  });
});
