/**
 * The Spending page, one grouped scroll: today's spend against a per-day budget, the saved budgets
 * with their add-or-edit sheet, desktop notifications, the history store with its retention, and
 * where Radar reads its data.
 */

import { Button } from "@huddle/ui/button.tsx";
import { List, ListItem } from "@huddle/ui/list.tsx";
import { RadioGroup } from "@huddle/ui/radio-group.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import { Switch } from "@huddle/ui/switch.tsx";
import type { ComponentChildren } from "preact";
import { ESTIMATE_NOTE, fmtUsd } from "../../fmt.ts";
import { budgetTone, STATUS_COLOR } from "../../palette.ts";
import type { Budget } from "../../shared/api.ts";
import { pluginLabel, scopeLabel } from "../../shared/provider.ts";
import type { BudgetDraft } from "../../state.ts";
import { useApp } from "../context.ts";
import { Icon } from "../Icon.tsx";
import { Badge, EmptyState, IconTile, Notice, Panel, Status } from "../kit.tsx";

/** A section of the page: its heading over one surface card, and the quiet note under the card. */
const HEAD = "m-0 text-xs font-semibold tracking-[0.06em] uppercase text-[var(--ui-muted)]";

function Section({
  id,
  title,
  note,
  children,
}: {
  id: string;
  title: string;
  note?: string;
  children: ComponentChildren;
}) {
  return (
    <section aria-labelledby={`${id}-h`} class="flex flex-col gap-3">
      <h2 id={`${id}-h`} class={HEAD}>
        {title}
      </h2>
      {children}
      {note !== undefined && <p class="m-0 text-xs text-[var(--ui-muted)]">{note}</p>}
    </section>
  );
}

/** Today's spend so far against the one budget that caps everything per day; absent without one. */
function TodaySection() {
  const { state } = useApp();
  const budget = state.budgets?.find((b) => b.scope === "total" && b.period === "day");
  if (budget === undefined) return null;
  const pct = Math.min(state.budgetStatus?.spend.find((s) => s.id === budget.id)?.pct ?? 0, 100);
  return (
    <Section id="spending-today" title="Today" note="Resets at midnight.">
      <Panel>
        <div class="flex items-baseline gap-2">
          <span class="num text-2xl font-bold">{fmtUsd(state.spendToday)}</span>
          <span class="text-sm text-[var(--ui-muted)]">{`of ${fmtUsd(budget.limitUsd)}`}</span>
        </div>
        <div
          role="progressbar"
          aria-label="Today's spend"
          aria-valuenow={Math.round(pct)}
          aria-valuemin={0}
          aria-valuemax={100}
          class="mt-3 h-2 w-full overflow-hidden rounded-full bg-[var(--ui-surface-2)]"
        >
          <div
            class="h-full rounded-full"
            style={`width:${pct}%;background:${STATUS_COLOR[budgetTone(pct)]}`}
          />
        </div>
      </Panel>
    </Section>
  );
}

/** One saved budget as a row: its plate, label, period line, Stop chip, limit, and a chevron. */
function BudgetRow({ budget }: { budget: Budget }) {
  const { act } = useApp();
  return (
    <ListItem
      leading={<IconTile name={budget.scope === "total" ? "gauge" : "bot"} color="var(--ui-muted)" />}
      title={scopeLabel(budget.scope)}
      meta={
        budget.action === "stop"
          ? `Per ${budget.period} · stop at 100%`
          : `Per ${budget.period} · warn at 80% and 100%`
      }
      trailing={
        <>
          {budget.action === "stop" && <Badge text="Stop" tone="warn" />}
          <span class="num text-sm">{fmtUsd(budget.limitUsd)}</span>
          <Icon name="chevronRight" class="size-4 shrink-0 text-[var(--ui-muted)]" />
        </>
      }
      onClick={() => act("edit-budget", budget.id)}
    />
  );
}

/** A labelled select bound to one draft field; changing it reports `draft-field` as `field=value`. */
function SelectField({
  label,
  field,
  value,
  options,
}: {
  label: string;
  field: string;
  value: string;
  options: [string, string][];
}) {
  const { act } = useApp();
  return (
    <label class="ui-field min-w-0">
      <span class="ui-field-label">{label}</span>
      <select
        class="ui-input w-full"
        aria-label={label}
        value={value}
        data-field={field}
        data-key={`field-${field}`}
        onChange={(event) => act("draft-field", `${field}=${event.currentTarget.value}`)}
      >
        {options.map(([v, l]) => (
          <option key={v} value={v}>
            {l}
          </option>
        ))}
      </select>
    </label>
  );
}

/** The add-or-edit budget form the sheet hosts: scope, period, limit, the action at 100%, and the
 *  save, cancel and (when editing) remove buttons. */
function BudgetForm({ draft }: { draft: BudgetDraft }) {
  const { state, act } = useApp();
  const scopes: [string, string][] = [
    ["total", "Total (every provider)"],
    ...state.providers.map((p): [string, string] => [`provider:${p}`, pluginLabel(p)]),
  ];
  if (!scopes.some(([v]) => v === draft.scope)) scopes.push([draft.scope, scopeLabel(draft.scope)]);
  return (
    <div class="grid gap-4">
      <SelectField label="Scope" field="scope" value={draft.scope} options={scopes} />
      <SelectField
        label="Period"
        field="period"
        value={draft.period}
        options={[
          ["day", "Per day"],
          ["week", "Per week (from Monday)"],
          ["month", "Per month"],
        ]}
      />
      <label class="ui-field min-w-0">
        <span class="ui-field-label">Limit (USD)</span>
        <input
          key={draft.id ?? "new"}
          class="ui-input w-full"
          type="text"
          inputmode="decimal"
          defaultValue={draft.limit}
          data-field="limit"
          data-key="field-limit"
          aria-label="Limit in US dollars"
          onInput={(event) => act("draft-field", `limit=${event.currentTarget.value}`)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              act("save-budget");
            }
          }}
        />
      </label>
      <SelectField
        label="At 100%"
        field="action"
        value={draft.action}
        options={[
          ["warn", "Warn only"],
          ["stop", "Stop that provider's requests"],
        ]}
      />
      <div class="flex flex-wrap gap-2">
        <Button variant="primary" icon="check" data-action="save-budget" onClick={() => act("save-budget")}>
          {draft.id === null ? "Add budget" : "Save"}
        </Button>
        <Button variant="secondary" data-action="cancel-budget" onClick={() => act("cancel-budget")}>
          Cancel
        </Button>
      </div>
      {draft.id !== null && (
        <Button
          variant="danger"
          class="w-full"
          data-action="remove-budget"
          data-value={draft.id}
          onClick={() => act("remove-budget", draft.id ?? undefined)}
        >
          Remove budget
        </Button>
      )}
    </div>
  );
}

/** The budgets sheet: the form rises over the page while a draft is open, titled by what it edits.
 *  A close with a draft still open cancels it; a close the save itself caused (the draft already
 *  gone) leaves the confirmation standing. */
function BudgetSheet() {
  const { state, act } = useApp();
  const draft = state.draft;
  return (
    <Sheet
      open={draft !== null}
      onClose={() => {
        if (state.draft !== null) act("cancel-budget");
      }}
      title={draft === null || draft.id === null ? "Add a budget" : "Edit budget"}
    >
      {draft !== null && <BudgetForm draft={draft} />}
    </Sheet>
  );
}

/** The budgets section: the saved rows, the add row, the sheet, and the two notes under the card. */
function BudgetsSection() {
  const { state, act } = useApp();
  const budgets = state.budgets;
  return (
    <Section id="spending-budgets" title="Budgets">
      {state.formMessage !== null && (
        <Notice
          tone={state.formMessage.tone === "err" ? "err" : "ok"}
          role={state.formMessage.tone === "err" ? "alert" : "status"}
        >
          <Status tone={state.formMessage.tone} word={state.formMessage.text} />
        </Notice>
      )}
      {budgets === null ? (
        <div class="skeleton h-24 w-full" role="status" aria-label="Loading budgets" />
      ) : (
        <>
          <List label="Budgets">
            {budgets.map((budget) => (
              <BudgetRow key={budget.id} budget={budget} />
            ))}
            <ListItem
              leading={<IconTile name="plus" color="var(--ui-accent)" />}
              title={<span class="font-medium text-[var(--ui-accent)]">Add a budget</span>}
              onClick={() => act("new-budget")}
            />
          </List>
          {budgets.length === 0 && (
            <EmptyState
              title="No budgets yet"
              hint="Set a limit for everything or for one provider. A warn budget raises an alert at 80% and 100%; a stop budget also makes that provider's router refuse requests until the period ends. Claude requests are never stopped."
              icon="dollar"
            />
          )}
        </>
      )}
      <p class="m-0 text-xs text-[var(--ui-muted)]">
        A stop budget makes that provider's requests wait until the period ends. Claude requests are never
        stopped.
      </p>
      <p class="m-0 text-xs text-[var(--ui-muted)]">{ESTIMATE_NOTE}</p>
    </Section>
  );
}

/** The alerts section: the one desktop-notifications row, and what fires an alert. */
function AlertsSection() {
  const { state, act } = useApp();
  return (
    <Section id="spending-alerts" title="Alerts" note="Budgets at 80% and 100%, stuck sessions and loops.">
      <List label="Alerts">
        <ListItem
          title="Desktop notifications"
          trailing={
            <Switch
              checked={state.settings?.notifications ?? true}
              label="Desktop notifications"
              onChange={() => act("toggle-notifications")}
            />
          }
        />
      </List>
    </Section>
  );
}

/** How long the history store keeps things; the retention values the settings route takes. */
const RETENTION_OPTIONS = [
  { value: "7", label: "7 days" },
  { value: "30", label: "30 days" },
  { value: "90", label: "90 days" },
  { value: "0", label: "Forever" },
] as const;

/** The history section: the retention choice while the store is on, and the wipe button. */
function HistorySection() {
  const { state, act } = useApp();
  const store = state.historyStats;
  const current =
    store === null
      ? null
      : (RETENTION_OPTIONS.find((o) => o.value === String(store.retentionDays))?.value ?? null);
  return (
    <Section
      id="spending-history"
      title="History"
      note="Kept in ~/.agents/radar/history.db. Older rows are pruned hourly; clearing cannot be undone."
    >
      {state.historyOff ? (
        <p class="m-0 text-sm text-[var(--ui-muted)]">
          History is off on this machine; nothing is being kept.
        </p>
      ) : store === null ? (
        <div class="skeleton h-16 w-full" role="status" aria-label="Reading the history store" />
      ) : (
        <Panel>
          <p class="m-0 text-sm font-medium">Keep history</p>
          <RadioGroup
            label="Keep history"
            name="history-retention"
            value={current}
            options={RETENTION_OPTIONS}
            onChange={(value) => act("retention", value)}
          />
          <p class="m-0 mt-2 text-xs text-[var(--ui-muted)]">
            A session older than this disappears from History with its content.
          </p>
        </Panel>
      )}
      {state.clearConfirm ? (
        <Notice tone="warn">
          <span>Delete every stored session, request and its content?</span>
          <Button
            variant="danger"
            data-action="clear-history"
            data-value="confirm"
            onClick={() => act("clear-history", "confirm")}
          >
            Clear now
          </Button>
          <Button
            variant="secondary"
            data-action="clear-history"
            data-value="cancel"
            onClick={() => act("clear-history", "cancel")}
          >
            Keep it
          </Button>
        </Notice>
      ) : (
        <Button
          variant="danger"
          class="w-full"
          data-action="clear-history"
          onClick={() => act("clear-history")}
        >
          Clear history
        </Button>
      )}
    </Section>
  );
}

/** The data section: where Radar reads its sessions, requests and settings from. */
function DataSection() {
  return (
    <Section id="spending-data" title="Data">
      <List label="Data">
        <ListItem
          title="Live sessions"
          meta="Claude Code's own session files under ~/.claude/projects, read as they are written."
        />
        <ListItem
          title="History store"
          meta="~/.agents/radar/history.db keeps past sessions once their live files close."
        />
        <ListItem title="Settings" meta="Budgets and notification settings live in ~/.agents/radar." />
      </List>
    </Section>
  );
}

/** The Spending view huddle's Settings › Radar section shows: today, budgets, alerts, history and
 *  where the data lives. */
export function SettingsView() {
  return (
    <div class="grid gap-6">
      <TodaySection />
      <BudgetsSection />
      <AlertsSection />
      <HistorySection />
      <DataSection />
      <BudgetSheet />
    </div>
  );
}
