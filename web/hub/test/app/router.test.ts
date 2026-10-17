// router.test.ts — every route, redirect, alias, drawer and builder the hash router answers: the
// five tabs' own addresses, the channel pages under #/channels/<name>, the radar views, and the
// old #/c/… addresses rewritten into them.
import { describe, expect, it } from "vite-plus/test";
import {
  channelHref,
  DESTS,
  type PrefReader,
  parseHash,
  pathOf,
  RADAR_VIEWS,
  redirect,
  sessHref,
  TABS,
  tabHref,
  tabLabel,
  taskHref,
} from "../../src/app/router.ts";

/** Reads "wview:<ch>" from a small table, so the t/ alias test can pick the last Work view. */
const reader =
  (views: Record<string, string> = {}): PrefReader =>
  (k, fb) =>
    views[k] ?? fb;

describe("parseHash: the five tabs", () => {
  it("reads Now, with or without a session id", () => {
    expect(parseHash("#/now", reader())).toMatchObject({ tab: "now", ch: null, sub: [] });
    expect(parseHash("#/now/s-1", reader())).toMatchObject({ tab: "now", sub: ["s-1"] });
    for (const h of ["#/now", "#/now/s-1"]) expect(parseHash(h, reader()).replace, h).toBeNull();
  });

  it("reads Settings, with or without a section", () => {
    expect(parseHash("#/settings", reader())).toMatchObject({ tab: "settings", sub: [] });
    expect(parseHash("#/settings/theme", reader())).toMatchObject({ tab: "settings", sub: ["theme"] });
    expect(parseHash("#/settings/theme", reader()).replace).toBeNull();
  });

  it("reads Radar's views, and names the missing or unknown one Overview with a rewrite", () => {
    expect(RADAR_VIEWS).toEqual([
      "overview",
      "costs",
      "models",
      "usage",
      "requests",
      "tools",
      "agents",
      "routers",
      "alerts",
      "timeline",
    ]);
    for (const v of RADAR_VIEWS) {
      const r = parseHash(`#/radar/${v}`, reader());
      expect(r.tab, v).toBe("radar");
      expect(r.view, v).toBe(v);
      expect(r.replace, v).toBeNull();
    }
    expect(parseHash("#/radar", reader())).toMatchObject({ view: "overview", replace: "#/radar/overview" });
    expect(parseHash("#/radar/nope", reader())).toMatchObject({
      view: "overview",
      replace: "#/radar/overview",
    });
  });

  it("reads Kanban, with or without a board key, and lists five tabs in order", () => {
    expect(parseHash("#/kanban", reader())).toMatchObject({ tab: "kanban", ch: null, sub: [] });
    expect(parseHash("#/kanban/a1a1", reader())).toMatchObject({ tab: "kanban", sub: ["a1a1"] });
    for (const h of ["#/kanban", "#/kanban/a1a1"]) expect(parseHash(h, reader()).replace, h).toBeNull();
    expect(TABS).toEqual([
      ["now", "Now", "#/now"],
      ["channels", "Channels", "#/channels"],
      ["radar", "Radar", "#/radar/overview"],
      ["kanban", "Kanban", "#/kanban"],
      ["settings", "Settings", "#/settings"],
    ]);
    expect(tabHref("kanban")).toBe("#/kanban");
    expect(tabLabel("kanban")).toBe("Kanban");
  });

  it("sends an unknown address to Now with a rewrite", () => {
    expect(parseHash("#/nope", reader())).toMatchObject({ tab: "now", replace: "#/now" });
    expect(parseHash("#/nope/deep", reader()).replace).toBe("#/now");
  });
});

describe("parseHash: the channels tab and today's pages", () => {
  it("reads the channels list with no name", () => {
    const r = parseHash("#/channels", reader());
    expect(r).toMatchObject({ tab: "channels", ch: null, dest: "home", sub: [] });
    expect(r.replace).toBeNull();
  });

  it("opens a channel on Overview when the hash names no page", () => {
    for (const hash of ["#/channels/ch", "#/channels/ch/"]) {
      const r = parseHash(hash, reader());
      expect(r.dest, hash).toBe("overview");
      expect(r.ch, hash).toBe("ch");
      expect(r.replace, hash).toBe("#/channels/ch/overview");
      expect(r.key, hash).toBe("channels/ch/overview/");
    }
  });

  it("accepts every page, with or without a sub path", () => {
    const expected: readonly (readonly [string, string, string[]])[] = [
      ["#/channels/ch/overview", "overview", []],
      ["#/channels/ch/today", "today", []],
      ["#/channels/ch/inbox", "inbox", []],
      ["#/channels/ch/team", "team", []],
      ["#/channels/ch/work", "work", []],
      ["#/channels/ch/work/list", "work", ["list"]],
      ["#/channels/ch/work/board", "work", ["board"]],
      ["#/channels/ch/work/graph", "work", ["graph"]],
      ["#/channels/ch/work/map", "work", ["map"]],
      ["#/channels/ch/work/repo/code", "work", ["repo", "code"]],
      ["#/channels/ch/knowledge", "knowledge", []],
      ["#/channels/ch/knowledge/12", "knowledge", ["12"]],
      ["#/channels/ch/settings", "settings", []],
    ];
    for (const [hash, dest, sub] of expected) {
      const r = parseHash(hash, reader());
      expect(r.dest, hash).toBe(dest);
      expect(r.sub, hash).toEqual(sub);
      expect(r.replace, hash).toBeNull();
      expect(r.key, hash).toBe(`channels/ch/${dest}/${sub.join("/")}`);
    }
  });

  it("reads an unknown page as Overview and rewrites the address, keeping the query", () => {
    expect(parseHash("#/channels/ch/nope", reader()).replace).toBe("#/channels/ch/overview");
    expect(parseHash("#/channels/ch/nope?t=7", reader())).toMatchObject({
      dest: "overview",
      replace: "#/channels/ch/overview?t=7",
      task: "7",
    });
  });

  it("decodes path parts and keeps a broken escape as written", () => {
    expect(parseHash("#/channels/my%20ch/inbox", reader()).ch).toBe("my ch");
    expect(parseHash("#/channels/ch/%zz", reader()).dest).toBe("overview");
  });

  it("carries the two drawers, both of them when both are in the query", () => {
    expect(parseHash("#/channels/ch/inbox?t=t1", reader()).task).toBe("t1");
    expect(parseHash("#/channels/ch/team?s=api", reader()).sess).toBe("api");
    const both = parseHash("#/channels/ch/work?t=a&s=b", reader());
    expect(both.task).toBe("a");
    expect(both.sess).toBe("b");
  });

  it("names every page the channel nav offers", () => {
    expect(DESTS.map((d) => d[0])).toEqual(["inbox", "team", "work", "knowledge", "settings"]);
    expect(DESTS.map((d) => d[1])).toEqual(["Inbox", "Team", "Work", "Knowledge", "Settings"]);
  });
});

describe("parseHash: the alias redirects", () => {
  it("sends needs to Inbox and live to Team", () => {
    expect(parseHash("#/channels/ch/needs", reader())).toMatchObject({
      dest: "inbox",
      replace: "#/channels/ch/inbox",
    });
    expect(parseHash("#/channels/ch/live", reader())).toMatchObject({
      dest: "team",
      replace: "#/channels/ch/team",
    });
  });

  it("sends plan to Work's list, carrying the phase when one is named", () => {
    expect(parseHash("#/channels/ch/plan", reader())).toMatchObject({
      dest: "work",
      sub: ["list"],
      replace: "#/channels/ch/work/list",
      prefs: {},
    });
    expect(parseHash("#/channels/ch/plan/p/3", reader()).prefs).toEqual({ "wphase:ch": 3 });
    expect(parseHash("#/channels/ch/plan/p/-1", reader()).prefs).toEqual({ "wphase:ch": -1 });
    expect(parseHash("#/channels/ch/plan/elsewhere", reader()).prefs).toEqual({});
  });

  it("sends board and graph to their Work views", () => {
    expect(parseHash("#/channels/ch/board", reader()).replace).toBe("#/channels/ch/work/board");
    expect(parseHash("#/channels/ch/graph", reader()).replace).toBe("#/channels/ch/work/graph");
  });

  it("sends review to Work's list filtered to notes", () => {
    const r = parseHash("#/channels/ch/review", reader());
    expect(r.replace).toBe("#/channels/ch/work/list");
    expect(r.prefs).toEqual({ "wfilter:ch": "notes" });
  });

  it("sends kb to Knowledge, onto an entry when one is named", () => {
    expect(parseHash("#/channels/ch/kb", reader()).replace).toBe("#/channels/ch/knowledge");
    const r = parseHash("#/channels/ch/kb/12", reader());
    expect(r.replace).toBe("#/channels/ch/knowledge/12");
    expect(r).toMatchObject({ dest: "knowledge", sub: ["12"] });
  });

  it("sends repo to Work's repo views", () => {
    expect(parseHash("#/channels/ch/repo", reader()).replace).toBe("#/channels/ch/work/repo");
    expect(parseHash("#/channels/ch/repo/code", reader()).sub).toEqual(["repo", "code"]);
  });

  it("sends t/<id> to the task drawer over the channel's last Work view", () => {
    const plain = parseHash("#/channels/ch/t/t1", reader());
    expect(plain.replace).toBe("#/channels/ch/work/list?t=t1");
    expect(plain.task).toBe("t1");
    const boarded = parseHash("#/channels/ch/t/t1", reader({ "wview:ch": "board" }));
    expect(boarded.replace).toBe("#/channels/ch/work/board?t=t1");
    expect(parseHash("#/channels/ch/t", reader()).replace).toBe("#/channels/ch/overview");
  });
});

describe("parseHash: the old addresses", () => {
  it("rewrites Home into the channels list", () => {
    for (const hash of ["", "#", "#/"]) {
      expect(parseHash(hash, reader()).replace, hash).toBe("#/channels");
    }
  });

  it("rewrites every #/c/… address into its #/channels/… one", () => {
    expect(parseHash("#/c/ch", reader())).toMatchObject({
      tab: "channels",
      ch: "ch",
      dest: "overview",
      replace: "#/channels/ch/overview",
    });
    expect(parseHash("#/c/ch/inbox", reader())).toMatchObject({
      dest: "inbox",
      replace: "#/channels/ch/inbox",
    });
    expect(parseHash("#/c/ch/work/list?t=3", reader())).toMatchObject({
      dest: "work",
      sub: ["list"],
      task: "3",
      replace: "#/channels/ch/work/list?t=3",
    });
    expect(parseHash("#/c/my%20ch/inbox", reader()).replace).toBe("#/channels/my%20ch/inbox");
  });

  it("rewrites an old alias through its canonical address, prefs included", () => {
    expect(parseHash("#/c/ch/needs", reader())).toMatchObject({
      dest: "inbox",
      replace: "#/channels/ch/inbox",
    });
    expect(parseHash("#/c/ch/plan/p/3", reader()).prefs).toEqual({ "wphase:ch": 3 });
    expect(parseHash("#/c/ch/plan/p/3", reader()).replace).toBe("#/channels/ch/work/list");
  });
});

describe("redirect", () => {
  it("gives null for an empty tail or a word that is no alias", () => {
    expect(redirect("ch", [], reader())).toBeNull();
    expect(redirect("ch", ["nope"], reader())).toBeNull();
    expect(redirect("ch", ["t"], reader())).toBeNull();
  });

  it("encodes the task id", () => {
    expect(redirect("ch", ["t", "a b"], reader())?.to).toBe("work/list?t=a%20b");
  });
});

describe("the builders", () => {
  it("splits the drawer off the path", () => {
    expect(pathOf("#/channels/ch/inbox?t=1")).toBe("#/channels/ch/inbox");
    expect(pathOf("")).toBe("#/");
    expect(pathOf("#/channels/ch/inbox")).toBe("#/channels/ch/inbox");
  });

  it("builds the task drawer over the open view, or over Work", () => {
    expect(taskHref("ch", "#/channels/ch/inbox", "t 1")).toBe("#/channels/ch/inbox?t=t%201");
    expect(taskHref("ch", "#/other/x", "t1")).toBe("#/channels/ch/work?t=t1");
  });

  it("builds the session drawer over the open view, or over Team", () => {
    expect(sessHref("ch", "#/channels/ch/work/list", "a.pi")).toBe("#/channels/ch/work/list?s=a.pi");
    expect(sessHref("ch", "#/other/x", "api")).toBe("#/channels/ch/team?s=api");
  });

  it("builds a channel link", () => {
    expect(channelHref("ch")).toBe("#/channels/ch");
    expect(channelHref("ch", "/inbox")).toBe("#/channels/ch/inbox");
  });
});
