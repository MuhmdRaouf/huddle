/**
 * The settings tab: appearance, budgets (the add-or-edit form and the saved rows with their spend bars),
 * desktop notifications, the history store with its retention, and where Radar reads its data.
 */

import { Switch } from "@huddle/ui/switch.tsx";
import { useId } from "preact/hooks";
import { ESTIMATE_NOTE, fmtBytes, fmtNum, fmtUsd } from "../../fmt.ts";
import { budgetTone, STATUS_COLOR } from "../../palette.ts";
import type { Budget } from "../../shared/api.ts";
import { pluginLabel, scopeLabel } from "../../shared/provider.ts";
import type { BudgetDraft } from "../../state.ts";
import { StackedBar } from "../Chart.tsx";
import { useApp } from "../context.ts";
import { Icon } from "../Icon.tsx";
import { Badge, EmptyState, Notice, Panel, Segmented, Status } from "../kit.tsx";

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

/** The add-or-edit budget form: scope, period, limit and the action at 100%, plus save and cancel. */
function BudgetForm({ draft }: { draft: BudgetDraft }) {
  const { state, act } = useApp();
  const scopes: [string, string][] = [
    ["total", "Total (every provider)"],
    ...state.providers.map((p): [string, string] => [`provider:${p}`, pluginLabel(p)]),
  ];
  if (!scopes.some(([v]) => v === draft.scope)) scopes.push([draft.scope, scopeLabel(draft.scope)]);
  return (
    <div class="-mx-4 -mt-4 mb-4 grid gap-3.5 border-b border-[var(--ui-line)] bg-[var(--ui-surface-2)] p-4">
      <fieldset class="m-0 grid min-w-0 gap-3 border-0 p-0">
        <legend class="mb-1 p-0 text-sm font-semibold">
          {draft.id === null ? "New budget" : "Edit budget"}
        </legend>
        <div class="grid gap-3 [grid-template-columns:repeat(auto-fit,minmax(min(190px,100%),1fr))]">
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
        </div>
      </fieldset>
      <div class="flex flex-wrap gap-2">
        <button
          type="button"
          class="ui-btn ui-btn-primary"
          data-action="save-budget"
          onClick={() => act("save-budget")}
        >
          <Icon name="check" class="ui-icon" />
          <span>{draft.id === null ? "Add budget" : "Save"}</span>
        </button>
        <button
          type="button"
          class="ui-btn ui-btn-secondary"
          data-action="cancel-budget"
          onClick={() => act("cancel-budget")}
        >
          <span>Cancel</span>
        </button>
      </div>
    </div>
  );
}

/** One saved budget: its label and action badge, spend so far, and edit/remove buttons. */
function SavedBudget({ budget }: { budget: Budget }) {
  const { state, act } = useApp();
  const spend = state.budgetStatus?.spend.find((s) => s.id === budget.id);
  const pct = spend?.pct ?? 0;
  const tone = budgetTone(pct);
  return (
    <div class="budget-row grid gap-2 py-4 first:pt-0 last:pb-0">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span class="text-sm font-medium">
          {`${scopeLabel(budget.scope)}, ${fmtUsd(budget.limitUsd)} per ${budget.period}`}
        </span>
        <Badge
          text={budget.action === "stop" ? "Stops at 100%" : "Warns"}
          tone={budget.action === "stop" ? "err" : "info"}
        />
        <span class="num ml-auto text-sm text-[var(--ui-muted)]">
          {spend === undefined ? "–" : `${fmtUsd(spend.spentUsd)} so far (${Math.round(pct)}%)`}
        </span>
        <span class="inline-flex gap-1">
          <button
            type="button"
            class="ui-btn ui-btn-ghost ui-btn-icon"
            data-action="edit-budget"
            data-value={budget.id}
            aria-label={`Edit the ${scopeLabel(budget.scope)} budget`}
            onClick={() => act("edit-budget", budget.id)}
          >
            <Icon name="pencil" class="ui-icon" />
          </button>
          <button
            type="button"
            class="ui-btn ui-btn-ghost ui-btn-icon"
            data-action="remove-budget"
            data-value={budget.id}
            aria-label={`Remove the ${scopeLabel(budget.scope)} budget`}
            onClick={() => act("remove-budget", budget.id)}
          >
            <Icon name="trash" class="ui-icon" />
          </button>
        </span>
      </div>
      <StackedBar segments={[{ value: Math.min(pct, 100), color: STATUS_COLOR[tone] }]} max={100} />
    </div>
  );
}

/** The budgets panel: add button, save messages, the form when open, and the saved rows. */
export function BudgetsPanel() {
  const { state, act } = useApp();
  const budgets = state.budgets;
  return (
    <Panel
      icon="dollar"
      title="Budgets"
      subtitle="Estimated spend per day, week or month"
      actions={
        state.draft === null ? (
          <button
            type="button"
            class="ui-btn ui-btn-secondary"
            data-action="new-budget"
            onClick={() => act("new-budget")}
          >
            <Icon name="plus" class="ui-icon" />
            <span>Add budget</span>
          </button>
        ) : undefined
      }
    >
      {state.formMessage !== null && (
        <Notice
          tone={state.formMessage.tone === "err" ? "err" : "ok"}
          role={state.formMessage.tone === "err" ? "alert" : "status"}
          class="mb-4"
        >
          <Status tone={state.formMessage.tone} word={state.formMessage.text} />
        </Notice>
      )}
      {state.draft !== null && <BudgetForm draft={state.draft} />}
      {budgets === null ? (
        <div class="skeleton h-24 w-full" role="status" aria-label="Loading budgets" />
      ) : budgets.length === 0 && state.draft === null ? (
        <EmptyState
          title="No budgets yet"
          hint="Set a limit for everything or for one provider. A warn budget raises an alert at 80% and 100%; a stop budget also makes that provider's router refuse requests until the period ends. Claude requests are never stopped."
          icon="dollar"
        />
      ) : (
        <div class="divide-y divide-[var(--ui-line)]">
          {budgets.map((budget) => (
            <SavedBudget key={budget.id} budget={budget} />
          ))}
        </div>
      )}
      <p class="mt-4 mb-0 text-xs text-[var(--ui-muted)]">{ESTIMATE_NOTE}</p>
    </Panel>
  );
}

/** The desktop-notifications panel with its on/off switch. */
export function NotificationsPanel() {
  const { state, act } = useApp();
  const helpId = useId();
  const on = state.settings?.notifications ?? true;
  return (
    <Panel
      icon="bell"
      title="Desktop notifications"
      subtitle="Budgets at 80% and 100%, new stuck sessions and loops"
    >
      <div class="flex items-center justify-between gap-6">
        <div class="grid">
          <p class="m-0 mb-2 font-semibold">{on ? "On" : "Off"}</p>
          <p class="m-0 text-sm text-[var(--ui-muted)]" id={helpId}>
            macOS Notification Center, or notify-send on Linux when it is installed. Each alert notifies once;
            nothing in them but the project name and what happened.
          </p>
        </div>
        <Switch
          checked={on}
          label="Desktop notifications"
          describedBy={helpId}
          onChange={() => act("toggle-notifications")}
        />
      </div>
    </Panel>
  );
}

/** The history panel: how big the store is, how long it keeps things, and the button that wipes it. */
export function HistoryPanel() {
  const { state, act } = useApp();
  const stats = state.historyStats;
  return (
    <Panel icon="history" title="History" subtitle="Sessions, requests and their content, on disk">
      {state.historyOff ? (
        <p class="m-0 text-sm text-[var(--ui-muted)]">
          History is off on this machine; nothing is being kept.
        </p>
      ) : stats === null ? (
        <div class="skeleton h-16 w-full" role="status" aria-label="Reading the history store" />
      ) : (
        <div class="mb-5">
          <p class="m-0 mb-2 font-semibold">
            {`${fmtBytes(stats.bytes)}, ${fmtNum(stats.nodes)} sessions, ${fmtNum(stats.requests)} requests`}
          </p>
          <p class="m-0 text-sm text-[var(--ui-muted)]">
            Kept in ~/.agents/radar/history.db. Older rows are pruned hourly; clearing cannot be undone.
          </p>
        </div>
      )}
      <div class="flex flex-wrap items-center justify-between gap-4">
        <div class="grid">
          <p class="m-0 mb-2 font-semibold">Keep history for</p>
          <p class="m-0 text-sm text-[var(--ui-muted)]">
            A session older than this disappears from the History tab with its content.
          </p>
        </div>
        <Segmented
          action="retention"
          label="History retention"
          segments={[
            { label: "7d", value: "7", on: stats?.retentionDays === 7 },
            { label: "30d", value: "30", on: stats?.retentionDays === 30 },
            { label: "90d", value: "90", on: stats?.retentionDays === 90 },
            { label: "Forever", value: "0", on: stats?.retentionDays === 0 },
          ]}
        />
      </div>
      {state.clearConfirm ? (
        <Notice tone="warn" class="mt-5">
          <span>Delete every stored session, request and its content?</span>
          <button
            type="button"
            class="ui-btn ui-btn-danger"
            data-action="clear-history"
            data-value="confirm"
            onClick={() => act("clear-history", "confirm")}
          >
            Clear now
          </button>
          <button
            type="button"
            class="ui-btn ui-btn-secondary"
            data-action="clear-history"
            data-value="cancel"
            onClick={() => act("clear-history", "cancel")}
          >
            Keep it
          </button>
        </Notice>
      ) : (
        <div class="mt-5">
          <button
            type="button"
            class="ui-btn ui-btn-danger"
            data-action="clear-history"
            onClick={() => act("clear-history")}
          >
            <Icon name="trash" class="ui-icon" />
            <span>Clear history</span>
          </button>
        </div>
      )}
    </Panel>
  );
}

/** The data panel: where Radar reads its sessions, requests and settings from. */
export function DataPanel() {
  return (
    <Panel icon="folder" title="Data" subtitle="Where Radar reads and keeps what it shows">
      <dl class="grid gap-x-5 gap-y-3 max-sm:grid-cols-1 sm:grid-cols-[max-content_1fr]">
        <dt class="text-sm font-semibold">Live sessions</dt>
        <dd class="m-0 text-sm text-[var(--ui-muted)]">
          Claude Code's own session files under ~/.claude/projects, read as they are written.
        </dd>
        <dt class="text-sm font-semibold">History store</dt>
        <dd class="m-0 text-sm text-[var(--ui-muted)]">
          ~/.agents/radar/history.db keeps past sessions once their live files close.
        </dd>
        <dt class="text-sm font-semibold">Settings</dt>
        <dd class="m-0 text-sm text-[var(--ui-muted)]">
          Budgets and notification settings live in ~/.agents/radar.
        </dd>
      </dl>
    </Panel>
  );
}

/** The settings view huddle's Settings › Radar section shows: budgets, desktop notifications, the
 *  history store with its retention, and where Radar reads its data. */
export function SettingsView() {
  return (
    <div class="grid gap-5">
      <BudgetsPanel />
      <NotificationsPanel />
      <HistoryPanel />
      <DataPanel />
    </div>
  );
}
