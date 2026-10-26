// Tokens.tsx — #/dev/tokens: every web/ui component and token on one page, in the scheme the OS or
// the switch at the top picks. The design system's own proof: the e2e checks it on the phone and the
// desktop project (no horizontal scroll, 44 px tap targets, axe in both schemes).

import { Badge } from "@huddle/ui/badge.tsx";
import { Button } from "@huddle/ui/button.tsx";
import { Card } from "@huddle/ui/card.tsx";
import { ChartFrame } from "@huddle/ui/chart-frame.tsx";
import { Field } from "@huddle/ui/field.tsx";
import { List, ListItem } from "@huddle/ui/list.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import { type Column, Table } from "@huddle/ui/table.tsx";
import { Tabs } from "@huddle/ui/tabs.tsx";
import {
  applyScheme,
  followScheme,
  readScheme,
  type SchemePref,
  type SchemeQuery,
  type SchemeStore,
  writeScheme,
} from "@huddle/ui/theme.ts";
import type { ComponentChildren, CSSProperties, JSX } from "preact";
import { useEffect, useId, useState } from "preact/hooks";

/** The address the page answers on. */
export const DEV_TOKENS_HASH = "#/dev/tokens";

/** Where the page keeps the scheme override, the OS query it follows, and the element it marks. */
export type TokensProps = { store: SchemeStore; query: SchemeQuery; root: HTMLElement };

const SCHEMES: readonly (readonly [SchemePref, string, "monitor" | "moon" | "sun"])[] = [
  ["system", "System", "monitor"],
  ["dark", "Dark", "moon"],
  ["light", "Light", "sun"],
];

const COLORS = [
  "bg",
  "surface",
  "surface-2",
  "line",
  "line-strong",
  "muted",
  "text",
  "accent",
  "ok",
  "warn",
  "fail",
  "info",
] as const;

const TYPE = [
  ["xl", "Needs you: 2"],
  ["lg", "Cost today"],
  ["md", "Body text reads at 16 px."],
  ["sm", "A muted line under a title."],
  ["xs", "A badge or a table header."],
] as const;

const SPACES = [1, 2, 3, 4, 5, 6] as const;

/** The stacking layers, low to high, with their values. */
const LAYERS: readonly (readonly [string, string])[] = [
  ["sticky", "10"],
  ["tabbar", "20"],
  ["scrim", "30"],
  ["popup", "40"],
  ["toast", "50"],
  ["tooltip", "60"],
];

const SHADOWS = [1, 2, 3] as const;

/** The motion tokens, each shown with the value it computes to. */
const MOTION = ["--ui-ease", "--ui-dur-fast", "--ui-dur", "--ui-dur-slow"] as const;

/** The tokens whose value depends on the device and its settings. */
const COMPUTED = [
  "--ui-font",
  "--ui-control-h",
  "--ui-ease",
  "--ui-dur-fast",
  "--ui-dur",
  "--ui-dur-slow",
] as const;

type Computed = Partial<Record<(typeof COMPUTED)[number], string>>;

type CostRow = { model: string; requests: number; cost: number };

const COST_ROWS: readonly CostRow[] = [
  { model: "glm-5.3", requests: 1204, cost: 3.42 },
  { model: "kimi-k3", requests: 310, cost: 0.88 },
  { model: "claude-opus-5-5", requests: 42, cost: 6.1 },
];

const COST_COLUMNS: readonly Column<CostRow>[] = [
  { key: "model", label: "Model", cell: (r) => r.model, mono: true },
  {
    key: "requests",
    label: "Requests",
    cell: (r) => r.requests.toLocaleString("en-US"),
    align: "end",
    mono: true,
  },
  { key: "cost", label: "Cost", cell: (r) => `$${r.cost.toFixed(2)}`, align: "end", mono: true },
];

const BARS = [4, 7, 5, 9, 12, 8, 11, 14, 10, 13, 16, 12] as const;

const RANGES = [
  { id: "day", label: "Day" },
  { id: "week", label: "Week" },
  { id: "month", label: "Month" },
] as const;

const grid = (min: string): CSSProperties => ({
  display: "grid",
  gap: "var(--ui-space-3)",
  gridTemplateColumns: `repeat(auto-fill, minmax(${min}, 1fr))`,
});

const row: CSSProperties = { display: "flex", flexWrap: "wrap", gap: "var(--ui-space-2)" };

/** One titled block of the page. */
function Block({ title, children }: { title: string; children: ComponentChildren }): JSX.Element {
  const id = useId();
  return (
    <section aria-labelledby={id} style={{ marginTop: "var(--ui-space-6)" }}>
      <h2 id={id} style={{ margin: "0 0 var(--ui-space-3)", fontSize: "var(--ui-text-lg)" }}>
        {title}
      </h2>
      {children}
    </section>
  );
}

/** The scheme switch: System, Dark, Light, the kept one pressed. */
function SchemeSwitch({ pref, onPick }: { pref: SchemePref; onPick: (p: SchemePref) => void }): JSX.Element {
  return (
    <div role="group" aria-label="Scheme" style={row}>
      {SCHEMES.map(([id, label, icon]) => (
        <Button key={id} variant="secondary" icon={icon} pressed={pref === id} onClick={() => onPick(id)}>
          {label}
        </Button>
      ))}
    </div>
  );
}

/** The bar chart the chart frame holds: twelve hours of tokens. */
function Bars(): JSX.Element {
  const top = Math.max(...BARS);
  return (
    <svg viewBox={`0 0 ${BARS.length * 10} 40`} aria-hidden="true">
      {BARS.map((v, i) => (
        <rect
          key={i}
          x={i * 10 + 2}
          y={40 - (v / top) * 40}
          width="6"
          height={(v / top) * 40}
          rx="1"
          style={{ fill: "var(--ui-accent)" }}
        />
      ))}
    </svg>
  );
}

/** The page. */
export function Tokens({ store, query, root }: TokensProps): JSX.Element {
  const [pref, setPref] = useState<SchemePref>(() => readScheme(store));
  const [range, setRange] = useState("day");
  const [name, setName] = useState("huddle");
  const [picked, setPicked] = useState(false);
  const [sheet, setSheet] = useState(false);
  const [computed, setComputed] = useState<Computed>({});

  useEffect(() => {
    const stop = followScheme(root, store, query);
    return () => {
      stop();
      delete root.dataset.scheme;
    };
  }, []);

  useEffect(() => {
    const style = getComputedStyle(document.documentElement);
    const values: Computed = {};
    for (const name of COMPUTED) values[name] = style.getPropertyValue(name).trim();
    setComputed(values);
  }, []);

  const pick = (p: SchemePref): void => {
    writeScheme(store, p);
    applyScheme(root, p, query.matches);
    setPref(p);
  };

  return (
    <main class="ui-page">
      <h1 style={{ margin: 0, fontSize: "var(--ui-text-xl)", lineHeight: 1.2 }}>Design tokens</h1>
      <p style={{ margin: "var(--ui-space-2) 0 var(--ui-space-4)", color: "var(--ui-muted)" }}>
        Every web/ui component on the tokens. The scheme follows the OS until you pick one here.
      </p>
      <SchemeSwitch pref={pref} onPick={pick} />

      <Block title="Colors">
        <div style={grid("140px")}>
          {COLORS.map((c) => (
            <div key={c} class="ui-card" style={{ overflow: "hidden" }}>
              <div style={{ height: "40px", background: `var(--ui-${c})` }} />
              <div class="ui-mono" style={{ padding: "var(--ui-space-2)", fontSize: "var(--ui-text-sm)" }}>
                --ui-{c}
              </div>
            </div>
          ))}
        </div>
      </Block>

      <Block title="Type">
        {TYPE.map(([size, text]) => (
          <p key={size} style={{ margin: "0 0 var(--ui-space-2)", fontSize: `var(--ui-text-${size})` }}>
            {text}
          </p>
        ))}
        <p class="ui-mono" style={{ margin: 0 }}>
          huddle up --port 18860
        </p>
        <p
          class="ui-mono"
          style={{
            margin: "var(--ui-space-2) 0 0",
            overflowWrap: "anywhere",
            color: "var(--ui-muted)",
            fontSize: "var(--ui-text-sm)",
          }}
        >
          --ui-font: {computed["--ui-font"]}
        </p>
      </Block>

      <Block title="Spacing">
        {SPACES.map((n) => (
          <div key={n} style={{ display: "flex", alignItems: "center", gap: "var(--ui-space-3)" }}>
            <span class="ui-mono" style={{ width: "6rem", fontSize: "var(--ui-text-sm)" }}>
              --ui-space-{n}
            </span>
            <span style={{ width: `var(--ui-space-${n})`, height: "12px", background: "var(--ui-accent)" }} />
          </div>
        ))}
      </Block>

      <Block title="Layers, shadows and motion">
        {LAYERS.map(([layer, value]) => (
          <div key={layer} style={{ display: "flex", alignItems: "center", gap: "var(--ui-space-3)" }}>
            <span class="ui-mono" style={{ width: "9rem", fontSize: "var(--ui-text-sm)" }}>
              --ui-layer-{layer}
            </span>
            <span class="ui-mono" style={{ fontSize: "var(--ui-text-sm)" }}>
              {value}
            </span>
          </div>
        ))}
        <div style={{ ...grid("150px"), marginTop: "var(--ui-space-3)" }}>
          {SHADOWS.map((n) => (
            <div key={n} class="ui-card" style={{ overflow: "hidden" }}>
              <div
                style={{
                  margin: "var(--ui-space-3)",
                  height: "48px",
                  borderRadius: "var(--ui-radius)",
                  background: "var(--ui-bg)",
                  boxShadow: `var(--ui-shadow-${n})`,
                }}
              />
              <div class="ui-mono" style={{ padding: "var(--ui-space-2)", fontSize: "var(--ui-text-sm)" }}>
                --ui-shadow-{n}
              </div>
            </div>
          ))}
        </div>
        <p style={{ margin: "var(--ui-space-3) 0 0", color: "var(--ui-muted)" }}>
          A control stands <span class="ui-mono">--ui-control-h</span> ={" "}
          <span class="ui-mono">{computed["--ui-control-h"]}</span> tall here.
        </p>
        {MOTION.map((name) => (
          <div
            key={name}
            style={{
              display: "flex",
              alignItems: "center",
              gap: "var(--ui-space-3)",
              marginTop: "var(--ui-space-2)",
            }}
          >
            <span class="ui-mono" style={{ width: "9rem", fontSize: "var(--ui-text-sm)" }}>
              {name}
            </span>
            <span class="ui-mono" style={{ fontSize: "var(--ui-text-sm)", overflowWrap: "anywhere" }}>
              {computed[name]}
            </span>
          </div>
        ))}
      </Block>

      <Block title="Buttons">
        <div style={row}>
          <Button>Approve</Button>
          <Button variant="secondary">Later</Button>
          <Button variant="ghost">Details</Button>
          <Button variant="danger">Deny</Button>
          <Button variant="secondary" icon="plus" label="Add" />
          <Button variant="secondary" disabled>
            Disabled
          </Button>
        </div>
      </Block>

      <Block title="Badges">
        <div style={row}>
          <Badge>idle</Badge>
          <Badge tone="ok">healthy</Badge>
          <Badge tone="warn">slow</Badge>
          <Badge tone="fail">down</Badge>
          <Badge tone="info">new</Badge>
        </div>
      </Block>

      <Block title="Card">
        <Card title="Cost today" meta="3 sessions" actions={<Button variant="ghost">Open</Button>}>
          <p class="ui-mono" style={{ margin: 0, fontSize: "var(--ui-text-xl)" }}>
            $10.40
          </p>
        </Card>
      </Block>

      <Block title="List">
        <List label="Sessions">
          <ListItem title="api-worker" meta="idle 2 min" trailing={<Badge tone="ok">healthy</Badge>} />
          <ListItem
            title="docs-writer"
            meta={picked ? "picked" : "tap to pick"}
            trailing={<Badge tone="warn">slow</Badge>}
            onClick={() => setPicked(!picked)}
          />
        </List>
      </Block>

      <Block title="Tabs">
        <Tabs tabs={RANGES} value={range} onPick={setRange} label="Range" panel="dev-range" class="ui-tabs" />
        <div
          id="dev-range"
          role="tabpanel"
          aria-labelledby={`wt-${range}`}
          style={{ padding: "var(--ui-space-3) 0" }}
        >
          Showing one {range}.
        </div>
      </Block>

      <Block title="Table">
        <Table label="Cost by model" columns={COST_COLUMNS} rows={COST_ROWS} rowKey={(r) => r.model} />
      </Block>

      <Block title="Field">
        <div style={{ display: "grid", gap: "var(--ui-space-4)", maxWidth: "28rem" }}>
          <Field label="Channel name" value={name} onInput={setName} hint="Lowercase, no spaces" mono />
          <Field label="Daily budget" type="number" value="" onInput={() => {}} error="Enter a number" />
        </div>
      </Block>

      <Block title="Chart frame">
        <ChartFrame
          title="Tokens"
          value="12,400"
          meta="last 12 h"
          label="Tokens per hour, rising through the day"
        >
          <Bars />
        </ChartFrame>
      </Block>

      <Block title="Sheet">
        <Button variant="secondary" onClick={() => setSheet(true)}>
          Open sheet
        </Button>
        <Sheet open={sheet} title="Task 42" onClose={() => setSheet(false)}>
          <p style={{ marginTop: 0 }}>A bottom sheet on a phone, a side panel on a wide screen.</p>
          <Button onClick={() => setSheet(false)}>Done</Button>
        </Sheet>
      </Block>
    </main>
  );
}
