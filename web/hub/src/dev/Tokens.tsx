// Tokens.tsx — #/dev/tokens: every web/ui component and token on one page, in the scheme the OS or
// the switch at the top picks. The design system's own proof: the e2e checks it on the phone and the
// desktop project (no horizontal scroll, 44 px tap targets, axe in both schemes).

import { Badge } from "@huddle/ui/badge.tsx";
import { Banner } from "@huddle/ui/banner.tsx";
import { Button } from "@huddle/ui/button.tsx";
import { Card } from "@huddle/ui/card.tsx";
import { ChartFrame } from "@huddle/ui/chart-frame.tsx";
import { Chip } from "@huddle/ui/chip.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import { Empty } from "@huddle/ui/empty.tsx";
import { Field } from "@huddle/ui/field.tsx";
import { Icon } from "@huddle/ui/icons.tsx";
import { useUiId } from "@huddle/ui/id.ts";
import { List, ListItem } from "@huddle/ui/list.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import { SlideOver } from "@huddle/ui/slide-over.tsx";
import { Switch } from "@huddle/ui/switch.tsx";
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
import { useEffect, useState } from "preact/hooks";

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
  "accent-soft",
] as const;

/** The four status tones; the gallery shows each beside the ink its text is written in. */
const TONES = ["ok", "warn", "fail", "info"] as const;

/** The filters the gallery's toggle chips pick from. */
const FILTERS = ["Ready", "Waiting", "Overdue", "Mine"] as const;

const TYPE = [
  ["xl", "Needs you: 2"],
  ["lg", "Cost today"],
  ["md", "Body text reads at 15 px."],
  ["sm", "A muted line under a title."],
  ["xs", "A badge or a table header."],
  ["mono", "huddle provider zai setup"],
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

/** The shadow tokens and the scrim that sits under a sheet, each on its own swatch. */
const SHADOWS: readonly (readonly [string, CSSProperties])[] = [
  ["--ui-shadow-1", { boxShadow: "var(--ui-shadow-1)" }],
  ["--ui-shadow-2", { boxShadow: "var(--ui-shadow-2)" }],
  ["--ui-shadow-3", { boxShadow: "var(--ui-shadow-3)" }],
  ["--ui-shadow-sheet", { boxShadow: "var(--ui-shadow-sheet)" }],
  ["--ui-scrim", { background: "var(--ui-scrim)" }],
];

/** The motion tokens, each shown with the value it computes to. */
const MOTION = ["--ui-ease", "--ui-dur-fast", "--ui-dur", "--ui-dur-slow"] as const;

/** The tokens whose value depends on the device and its settings. */
const COMPUTED = [
  "--ui-font",
  "--ui-text-mono",
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

/** The colour and tone swatch cards share one grid and one swatch bar: the design guard ratchets the
 *  file's px literals, so the two blocks write them once. */
const SWATCH_GRID = grid("140px");
const SWATCH_BAR = { height: "40px" } as const;

/** One titled block of the page. */
function Block({ title, children }: { title: string; children: ComponentChildren }): JSX.Element {
  const id = useUiId("dev-block");
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
  const [notes, setNotes] = useState("");
  const [picks, setPicks] = useState<readonly string[]>([]);
  const [picked, setPicked] = useState(false);
  const [push, setPush] = useState(true);
  const [sheet, setSheet] = useState(false);
  const [over, setOver] = useState(false);
  const [dlg, setDlg] = useState(false);
  const [overDlg, setOverDlg] = useState(false);
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
    applyScheme(root, p, query.matches, store);
    setPref(p);
  };

  const flip = (f: string): void =>
    setPicks(picks.includes(f) ? picks.filter((x) => x !== f) : [...picks, f]);

  return (
    <main class="ui-page">
      <h1 style={{ margin: 0, fontSize: "var(--ui-text-xl)", lineHeight: 1.2 }}>Design tokens</h1>
      <p style={{ margin: "var(--ui-space-2) 0 var(--ui-space-4)", color: "var(--ui-muted)" }}>
        Every web/ui component on the tokens. The scheme follows the OS until you pick one here.
      </p>
      <SchemeSwitch pref={pref} onPick={pick} />

      <Block title="Colors">
        <div style={SWATCH_GRID}>
          {COLORS.map((c) => (
            <div key={c} class="ui-card" style={{ overflow: "hidden" }}>
              <div style={{ ...SWATCH_BAR, background: `var(--ui-${c})` }} />
              <div class="ui-mono" style={{ padding: "var(--ui-space-2)", fontSize: "var(--ui-text-sm)" }}>
                --ui-{c}
              </div>
            </div>
          ))}
        </div>
      </Block>

      <Block title="Status tones">
        <div style={SWATCH_GRID}>
          {TONES.map((t) => (
            <div key={t} class="ui-card" style={{ overflow: "hidden" }}>
              <div style={{ display: "flex", ...SWATCH_BAR }}>
                <div style={{ flex: 1, background: `var(--ui-${t})` }} />
                <div style={{ flex: 1, background: `var(--ui-${t}-ink)` }} />
              </div>
              <div class="ui-mono" style={{ padding: "var(--ui-space-2)", fontSize: "var(--ui-text-sm)" }}>
                --ui-{t} · --ui-{t}-ink
              </div>
            </div>
          ))}
        </div>
      </Block>

      <Block title="Type">
        {TYPE.map(([size, text]) => (
          <p
            key={size}
            class={size === "mono" ? "ui-mono" : undefined}
            style={{ margin: "0 0 var(--ui-space-2)", fontSize: `var(--ui-text-${size})` }}
          >
            {text}
          </p>
        ))}
        <p
          class="ui-mono"
          style={{
            margin: "var(--ui-space-2) 0 0",
            overflowWrap: "anywhere",
            color: "var(--ui-muted)",
            fontSize: "var(--ui-text-sm)",
          }}
        >
          --ui-text-mono: {computed["--ui-text-mono"]}
        </p>
        <p
          class="ui-mono"
          style={{
            margin: 0,
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
          {SHADOWS.map(([name, lift]) => (
            <div key={name} class="ui-card" style={{ overflow: "hidden" }}>
              <div
                style={{
                  margin: "var(--ui-space-3)",
                  height: "48px",
                  borderRadius: "var(--ui-radius)",
                  background: "var(--ui-bg)",
                  ...lift,
                }}
              />
              <div class="ui-mono" style={{ padding: "var(--ui-space-2)", fontSize: "var(--ui-text-sm)" }}>
                {name}
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

      <Block title="Chips">
        <div style={row}>
          <Chip>idle</Chip>
          <Chip tone="ok" dot>
            healthy
          </Chip>
          <Chip tone="warn">slow</Chip>
          <Chip tone="fail">down</Chip>
          <Chip tone="info" dot>
            new
          </Chip>
          <Chip tone="accent" icon={<Icon name="plus" />}>
            pinned
          </Chip>
        </div>
        <div class="ui-chip-row" style={{ marginTop: "var(--ui-space-3)" }}>
          {FILTERS.map((f) => (
            <Chip key={f} tone="accent" pressed={picks.includes(f)} onClick={() => flip(f)}>
              {f}
            </Chip>
          ))}
        </div>
      </Block>

      <Block title="Banners">
        <div style={{ display: "grid", gap: "var(--ui-space-3)" }}>
          <Banner title="Update ready" action={{ label: "Reload", onClick: () => {} }}>
            Huddle updated in the background; reload to pick it up.
          </Banner>
          <Banner tone="warn" title="Budget low" action={{ label: "Review", onClick: () => {} }}>
            The zai budget is at 90% for today.
          </Banner>
          <Banner tone="fail" title="Router down" action={{ label: "Retry", onClick: () => {} }}>
            The hub cannot reach huddle-router.
          </Banner>
        </div>
      </Block>

      <Block title="Empty states">
        <div style={{ display: "grid", gap: "var(--ui-space-3)" }}>
          <Empty icon={<Icon name="radar" class="ui-icon" />}>No sessions are running.</Empty>
          <Empty action={{ label: "Add a channel", onClick: () => {} }}>
            This huddle has no channels yet.
          </Empty>
        </div>
      </Block>

      <Block title="Card">
        <Card title="Cost today" meta="3 sessions" actions={<Button variant="ghost">Open</Button>}>
          <p class="ui-mono" style={{ margin: 0, fontSize: "var(--ui-text-xl)" }}>
            $10.40
          </p>
        </Card>
        {/* the hero card: the page's one large raised panel */}
        <section
          class="ui-card ui-card-lg"
          style={{ marginTop: "var(--ui-space-4)", display: "grid", gap: "var(--ui-space-2)" }}
        >
          <h2 style={{ margin: 0, fontSize: "var(--ui-text-md)" }}>Cost this week</h2>
          <p class="ui-mono" style={{ margin: 0, fontSize: "var(--ui-text-xl)" }}>
            $64.90
          </p>
        </section>
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
          <Field label="Notes" value={notes} onInput={setNotes} multiline hint="Grows as you type" />
        </div>
      </Block>

      <Block title="Switch">
        <div style={row}>
          <Switch label="Push to this device" checked={push} onChange={setPush} />
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
          <Button onClick={() => setDlg(true)}>Open dialog</Button>
          <Button onClick={() => setSheet(false)}>Done</Button>
        </Sheet>
        {/* the grab handle, drawn by every sheet above its head */}
        <div class="ui-sheet-handle" aria-hidden="true" style={{ marginTop: "var(--ui-space-2)" }} />
      </Block>

      <Block title="Slide-over">
        <Button variant="secondary" onClick={() => setOver(true)}>
          Open slide-over
        </Button>
        <SlideOver
          open={over}
          label="Request details"
          onClose={() => setOver(false)}
          header={
            <p class="m-0" style={{ fontSize: "var(--ui-text-md)", fontWeight: 600 }}>
              Request details
            </p>
          }
          actions={<Button variant="ghost" icon="plus" label="Add" onClick={() => {}} />}
        >
          <p style={{ marginTop: 0 }}>A bottom sheet on a phone, a right-hand pane on a wide screen.</p>
          <Button onClick={() => setOverDlg(true)}>Open dialog</Button>
          <Button onClick={() => setOver(false)}>Done</Button>
        </SlideOver>
        <Dialog open={overDlg} title="Detach session" onClose={() => setOverDlg(false)}>
          <p style={{ marginTop: 0 }}>This detaches the session and keeps its history.</p>
          <Button onClick={() => setOverDlg(false)}>Done</Button>
        </Dialog>
      </Block>

      <Block title="Dialog">
        <Button variant="secondary" onClick={() => setDlg(true)}>
          Open dialog
        </Button>
        <Dialog open={dlg} title="Remove channel" onClose={() => setDlg(false)}>
          <p style={{ marginTop: 0 }}>This removes #huddle and its history for everyone.</p>
          <Button onClick={() => setDlg(false)}>Done</Button>
        </Dialog>
      </Block>
    </main>
  );
}
