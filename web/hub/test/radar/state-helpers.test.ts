// state-helpers.test.ts — the client state's pure helpers the views' tests reach only sometimes:
// the picked set's folds, the sorts' defaults, the history node ids, and the block shorthands the
// inspector renders from.

import { describe, expect, it } from "vite-plus/test";
import type { RootSummary, TreeNodeRow } from "../../src/radar/shared/api.ts";
import { ZERO_TOKENS } from "../../src/radar/shared/model.ts";
import {
  agentDisplayName,
  agentNodeId,
  blocksOf,
  blockText,
  budgetFromDraft,
  type ClientState,
  clearSelected,
  detailTargets,
  draftOf,
  initialClientState,
  newDraft,
  nextSort,
  nodeAgentOf,
  nodeLabel,
  requestNodeId,
  resultBlocks,
  rootName,
  scopeCrumb,
  tableTools,
  toggleSelected,
  transcriptAgentOf,
  visibleEvents,
  visibleTools,
  withBudget,
} from "../../src/radar/state.ts";
import { makeAgentView, makeRequest, makeSessionView, makeTool, uiFixtures } from "./helpers.ts";

const fixtures = uiFixtures();

function state(over: Partial<ClientState> = {}): ClientState {
  return { ...initialClientState(), ...over };
}

function treeRow(over: Partial<TreeNodeRow> = {}): TreeNodeRow {
  return { ...fixtures.treeNodeRow, ...over };
}

describe("the picked set", () => {
  it("folds picks in and out, the empty id clearing the whole set", () => {
    let current = toggleSelected(state(), "s1");
    expect(current.selected).toEqual(["s1"]);
    expect(current.session).toBe("s1");
    current = toggleSelected(current, "s2");
    expect(current.session).toBeNull(); // several picked: no single one
    current = toggleSelected(current, "s1");
    expect(current.selected).toEqual(["s2"]);
    current = toggleSelected(current, "");
    expect(current.selected).toEqual([]);
    expect(current.session).toBeNull();
  });

  it("clearSelected empties the set; a pick resets the agent and model filters with it", () => {
    const current = toggleSelected({ ...state(), agent: "w1", model: "glm-5.3" }, "s1");
    expect(current.agent).toBeNull();
    expect(current.model).toBeNull();
    expect(clearSelected(current).selected).toEqual([]);
  });

  it("details worth showing: the picked sessions in order, else the most recent few", () => {
    const items = [
      { ...fixtures.sessionListItem, id: "a", lastAt: 1 },
      { ...fixtures.sessionListItem, id: "b", lastAt: 3 },
      { ...fixtures.sessionListItem, id: "c", lastAt: 2 },
    ];
    expect(detailTargets(state({ sessions: items })).map((item) => item.id)).toEqual(["b", "c", "a"]);
    expect(detailTargets(state({ sessions: items, selected: ["c", "a"] })).map((item) => item.id)).toEqual([
      "c",
      "a",
    ]);
  });
});

describe("sorts and tables", () => {
  it("a new column starts at its natural side, the same column flips", () => {
    expect(nextSort({ key: "time", dir: "desc" }, "model").dir).toBe("asc"); // text starts A→Z
    expect(nextSort({ key: "time", dir: "desc" }, "latency").dir).toBe("desc"); // numbers start biggest
    expect(nextSort({ key: "model", dir: "asc" }, "model").dir).toBe("desc");
  });

  it("the tools table reads a call's project and main agent, failed only when asked", () => {
    const current = state({
      toolFilter: "failed",
      sessions: [{ ...fixtures.sessionListItem, id: "s1", project: "app" }],
      tools: [
        makeTool({ id: "t1", sessionId: "s1", agentId: "", ok: true }),
        makeTool({ id: "t2", sessionId: "s1", agentId: "w1", ok: false, startedAt: 2000 }),
      ],
    });
    const rows = tableTools(current);
    expect(rows.map((tool) => tool.id)).toEqual(["t2"]);
  });

  it("the event and tool lists stay under a picked agent; an unattributed row stays, it is the session's own", () => {
    const current = state({
      agent: "main",
      events: [
        { seq: 1, ts: 2, kind: "Stop", sessionId: "s1", agentId: null, label: null, payload: null },
        { seq: 2, ts: 3, kind: "Stop", sessionId: "s1", agentId: "w1", label: null, payload: null },
        { seq: 3, ts: 4, kind: "Stop", sessionId: "s2", agentId: null, label: null, payload: null },
      ],
      tools: [
        makeTool({ id: "t1", sessionId: "s1", agentId: null, startedAt: 2 }),
        makeTool({ id: "t2", sessionId: "s1", agentId: "w1", startedAt: 3 }),
      ],
    });
    // newest first; the other session's unattributed event stays, but its session is out of the filter
    expect(visibleEvents(current).map((event) => event.seq)).toEqual([3, 1]);
    expect(visibleTools(current).map((tool) => tool.id)).toEqual(["t1"]);
  });
});

describe("names the inspector reads", () => {
  it("an agent's display name: Main for the lead, the live name, else the history node's", () => {
    const live = state({
      details: {
        s1: makeSessionView({
          id: "s1",
          agents: [makeAgentView({ sessionId: "s1", id: "w1", name: "Watcher" })],
        }),
      },
    });
    const history = state({
      historyScope: { rootId: "s1", nodeId: "s1/w1" },
      historyTrees: {
        s1: [treeRow({ id: "s1/w1", sessionId: "s1", agentId: "w1", name: "Explore — find it" })],
      },
    });
    expect(agentDisplayName(state(), "s1", "main")).toBe("Main");
    expect(agentDisplayName(state(), "s1", "")).toBe("Main");
    expect(agentDisplayName(live, "s1", "w1")).toBe("Watcher");
    expect(agentDisplayName(history, "s1", "w1")).toBe("Explore — find it");
    expect(agentDisplayName(state(), "s9", "w9")).toBe("w9");
  });

  it("node ids: a job's own agent, everyone else under their session", () => {
    expect(agentNodeId("job-1", { id: "", kind: "external" })).toBe("job:job-1");
    expect(agentNodeId("s1", { id: "main", kind: "main" })).toBe("s1/main");
    expect(agentNodeId("s1", { id: "w1", kind: "subagent" })).toBe("s1/w1");
    expect(nodeAgentOf("job:job-1")).toEqual({ sessionId: "job-1", agentId: "" });
    expect(nodeAgentOf("s1/w1")).toEqual({ sessionId: "s1", agentId: "w1" });
    expect(nodeAgentOf("/rootless")).toBeNull();
  });

  it("the node a live request lands on: the job's lead, else the session's agent", () => {
    const external = state({
      details: {
        job1: makeSessionView({
          id: "job1",
          external: true,
          agents: [makeAgentView({ sessionId: "job1", id: "job1", kind: "external" })],
        }),
      },
    });
    expect(requestNodeId(state(), makeRequest({ sessionId: "s1", agentId: "" }))).toBe("s1/main");
    expect(requestNodeId(external, makeRequest({ sessionId: "job1", agentId: "main" }))).toBe("job:job1");
    expect(requestNodeId(external, makeRequest({ sessionId: "job1", agentId: "other" }))).toBe("job1/other");
  });

  it("the agent a transcript node answers, found through the held detail", () => {
    const current = state({
      details: {
        s1: makeSessionView({
          id: "s1",
          agents: [makeAgentView({ sessionId: "s1", id: "w1" })],
        }),
      },
    });
    expect(transcriptAgentOf(current, "s1/w1")?.agent.id).toBe("w1");
    expect(transcriptAgentOf(current, "s9/w1")).toBeNull();
    expect(transcriptAgentOf(current, "s1/none")).toBeNull();
  });
});

describe("history words", () => {
  it("a root's name: its own, else its label, project, or the id's head", () => {
    expect(rootName(undefined)).toBeNull();
    const root = (over: Partial<TreeNodeRow>): RootSummary => ({
      ...treeRow({ id: "abcdef1234", kind: "main", ...over }),
      requests: 0,
      nodes: 1,
      liveNodes: 0,
      tokens: { ...ZERO_TOKENS },
      lastAt: 9,
      activity: {
        bucketMs: 18_750,
        counts: Array.from({ length: 48 }, () => 0),
        models: Array.from({ length: 48 }, () => ""),
      },
    });
    expect(rootName(root({ name: "lead" }))).toBe("lead");
    expect(rootName({ ...root({ name: null }), label: "the label" })).toBe("the label");
    expect(rootName({ ...root({ name: null }), project: "app" })).toBe("app");
    expect(rootName(root({ name: null, label: null, project: null }))).toBe("abcdef12");
  });

  it("a node's word: Main for the root, else its name, label, description, or id", () => {
    expect(nodeLabel(undefined)).toBeNull();
    expect(nodeLabel(treeRow({ kind: "main" }))).toBe("Main");
    expect(nodeLabel(treeRow({ name: null, label: "the label" }))).toBe("the label");
    expect(nodeLabel(treeRow({ name: null, label: null, description: "the description" }))).toBe(
      "the description",
    );
  });

  it("the breadcrumb: the root's word and the picked node's, ids when nothing else is held", () => {
    expect(scopeCrumb(state())).toBeNull();
    const current = state({
      historyScope: { rootId: "r1", nodeId: "r1/w1" },
      historyRoots: [
        {
          ...treeRow({ id: "r1", kind: "main", parentId: null, name: "lead" }),
          requests: 0,
          nodes: 1,
          liveNodes: 1,
          tokens: { ...ZERO_TOKENS },
          lastAt: 9,
          activity: {
            bucketMs: 18_750,
            counts: Array.from({ length: 48 }, () => 0),
            models: Array.from({ length: 48 }, () => ""),
          },
        },
      ],
      historyTrees: { r1: [treeRow({ id: "r1/w1", name: "Watcher" })] },
    });
    expect(scopeCrumb(current)).toEqual({ root: "lead", node: "Watcher" });
    const plain = state({ historyScope: { rootId: "r1", nodeId: "r1/w1" } });
    expect(scopeCrumb(plain)).toEqual({ root: "r1", node: "r1/w1" });
    expect(scopeCrumb(state({ historyScope: { rootId: "r1", nodeId: null } }))).toEqual({
      root: "r1",
      node: null,
    });
  });
});

describe("stored sides as blocks", () => {
  it("an array stands as it is, plain text becomes one block, nothing else reads as none", () => {
    const blocks = [{ type: "text", text: "hi" }];
    expect(blocksOf(blocks)).toBe(blocks);
    expect(blocksOf([])).toBeNull();
    expect(blocksOf("plain")).toEqual([{ type: "text", text: "plain" }]);
    expect(blocksOf("")).toBeNull();
    expect(blocksOf(7)).toBeNull();
  });

  it("a block's text: its text, its thinking, else its JSON", () => {
    expect(blockText({ type: "text", text: "hi" })).toBe("hi");
    expect(blockText({ type: "thinking", thinking: "hmm" })).toBe("hmm");
    expect(blockText({ type: "tool_use", name: "Bash" })).toContain('"Bash"');
  });

  it("a tool result's blocks: the nested content, the content string, the flattened text, else none", () => {
    const nested = [{ type: "text", text: "in" }];
    expect(resultBlocks({ type: "tool_result", content: nested })).toEqual(nested);
    expect(resultBlocks({ type: "tool_result", content: "flat" })).toEqual([{ type: "text", text: "flat" }]);
    expect(resultBlocks({ type: "tool_result", text: "kept" })).toEqual([{ type: "text", text: "kept" }]);
    expect(resultBlocks({ type: "tool_result" })).toBeNull();
  });
});

describe("budget drafts", () => {
  it("a new draft: the first provider, this month, $10, warn only", () => {
    expect(newDraft(["zai", "kimi"])).toEqual({
      id: null,
      scope: "provider:zai",
      period: "month",
      limit: "10",
      action: "warn",
    });
    expect(newDraft([]).scope).toBe("total");
  });

  it("a saved budget reads back as its draft; a draft becomes a budget or a sentence", () => {
    const budget = {
      id: "b1",
      scope: "total",
      period: "day" as const,
      limitUsd: 12.5,
      action: "stop" as const,
    };
    expect(draftOf(budget)).toEqual({
      id: "b1",
      scope: "total",
      period: "day",
      limit: "12.5",
      action: "stop",
    });
    expect(budgetFromDraft({ ...draftOf(budget), limit: " 7.50 " }, "new")).toEqual({
      id: "b1",
      scope: "total",
      period: "day",
      limitUsd: 7.5,
      action: "stop",
    });
    expect(budgetFromDraft({ ...draftOf(budget), limit: "0" }, "new")).toContain("Enter a limit above $0");
    expect(budgetFromDraft({ ...draftOf(budget), limit: "2000000" }, "new")).toContain("too large");
  });

  it("withBudget replaces the one it edits and appends the rest", () => {
    const first = { id: "b1", scope: "total", period: "day" as const, limitUsd: 1, action: "warn" as const };
    const second = { ...first, id: "b2" };
    expect(withBudget([first], { ...first, limitUsd: 2 })).toEqual([{ ...first, limitUsd: 2 }]);
    expect(withBudget([first], second)).toEqual([first, second]);
  });
});
