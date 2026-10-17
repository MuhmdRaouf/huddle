/**
 * Writes the base records the dashboard's tests spread their overrides over (test/fixtures/ui.json), so
 * the tests load fixtures as data and never import the API's producers. Run with bun after changing a
 * base: `bun scripts/ui-fixtures.ts`. Node runs this file through type stripping, so it sticks to erasable
 * syntax and .ts import specifiers.
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type {
  Alert,
  AttributionNode,
  BudgetSpend,
  ProviderHealth,
  SessionListItem,
  TreeNodeRow,
} from "../src/shared/api.ts";
import { ZERO_TOKENS } from "../src/shared/model.ts";

const quietActivity = (): SessionListItem["activity"] => ({
  bucketMs: 18_750,
  counts: Array.from({ length: 48 }, () => 0),
  models: Array.from({ length: 48 }, () => ""),
});

const sessionListItem: SessionListItem = {
  id: "s1",
  project: null,
  cwd: null,
  name: null,
  branch: null,
  repo: null,
  parentSessionId: null,
  startedAt: null,
  endedAt: null,
  live: false,
  status: null,
  activity: quietActivity(),
  model: null,
  agentCount: 0,
  liveAgentCount: 0,
  requestCount: 0,
  tokens: 0,
  lastAt: 0,
  external: false,
  title: null,
};

const treeNodeRow: TreeNodeRow = {
  id: "s1/w1",
  kind: "subagent",
  parentId: "s1/main",
  sessionId: "s1",
  agentId: "w1",
  label: null,
  agentType: "Explore",
  description: null,
  project: null,
  cwd: null,
  model: null,
  provider: null,
  toolUseId: null,
  spawnDepth: 1,
  jobState: null,
  repo: null,
  branch: null,
  name: "Explore — find the flaky test",
  parentSessionId: null,
  startedAt: 1,
  endedAt: null,
  lastAt: 9,
  live: false,
  requests: 3,
  tokens: { ...ZERO_TOKENS },
};

const attributionNode: AttributionNode = {
  kind: "model",
  key: "m",
  label: "m",
  note: null,
  requests: 1,
  tokens: { ...ZERO_TOKENS },
  costUsd: null,
  unpriced: 0,
  mix: [],
  children: [],
};

const budgetSpend: BudgetSpend = {
  id: "b1",
  spentUsd: 0,
  limitUsd: 10,
  pct: 0,
  scope: "provider:zai",
  period: "day",
  action: "stop",
  periodStart: 0,
};

const providerHealth: ProviderHealth = {
  plugin: "zai",
  counts: { fallback: 0, refusal: 0, rate_limited: 0, budget_stop: 0, restart: 0 },
  series: [],
  kindSeries: [],
  lastAt: null,
  lastReason: null,
};

const alert: Alert = {
  id: "a0",
  kind: "loop",
  sessionId: "s1",
  agentId: "w1-long-agent-id",
  project: "app",
  since: 1_800_000_000_000 - 120_000,
  detail: "Bash called 5 times in a row with the same input",
  costUsd: 0.5,
  severity: "warn",
};

const fixtures = { alert, attributionNode, budgetSpend, providerHealth, sessionListItem, treeNodeRow };

const out = join(dirname(fileURLToPath(import.meta.url)), "..", "test", "fixtures", "ui.json");
mkdirSync(dirname(out), { recursive: true });
writeFileSync(out, `${JSON.stringify(fixtures, null, 2)}\n`);
console.log(`wrote ${out}`);
