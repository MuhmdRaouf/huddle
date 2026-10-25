/**
 * The strip above the summary: the three loudest alerts with where they happened, a count, and a way to the
 * full list. Renders nothing while there is nothing to look at; the alerts tab reuses AlertRow.
 */

import { costText, fmtAgo, fmtCount, fmtNum } from "../fmt.ts";
import type { IconName } from "../icons.ts";
import { alertLabel, STATUS_COLOR, type StatusTone } from "../palette.ts";
import type { Alert, AlertKind } from "../shared/api.ts";
import { useApp } from "./context.ts";
import { Icon } from "./Icon.tsx";
import { Badge, IconTile, Panel } from "./kit.tsx";

export const ALERT_ICON: Record<AlertKind, IconName> = {
  stuck: "timer",
  loop: "repeat",
  retry_storm: "wifiOff",
  context: "gauge",
  budget: "dollar",
  upstream: "alert",
  quota_nearly_used: "clock",
};

/** Where the alerts that belong to no session happened, by kind; any other reads as the router. */
const SESSIONLESS_WHERE: Partial<Record<AlertKind, string>> = {
  budget: "All sessions",
  quota_nearly_used: "Claude",
};

/** Where an alert happened: the project and agent, the session id when the project is unknown, Claude's plan
 *  window, or the router. */
export function alertWhere(alert: Alert): string {
  if (alert.sessionId === "") return SESSIONLESS_WHERE[alert.kind] ?? "Router";
  const project = alert.project === "" ? alert.sessionId.slice(0, 8) : alert.project;
  return alert.agentId === null ? project : `${project}, ${alert.agentId.slice(0, 12)}`;
}

/** The classes an alert row's severity reads as: errors on the fail tone's soft fill, the rest on the
 *  warn tone's. */
export function alertClasses(tone: StatusTone): string {
  return tone === "err"
    ? "border-[var(--ui-fail)] bg-[var(--ui-fail-soft)]"
    : "border-[var(--ui-warn)] bg-[var(--ui-warn-soft)]";
}

/** One alert: the kind badge and detail, the where/since/cost facts, and a dismiss button. */
export function AlertRow({ alert }: { alert: Alert }) {
  const { now, act } = useApp();
  const tone: StatusTone = alert.severity === "err" ? "err" : "warn";
  return (
    <li
      role="alert"
      data-alert={tone}
      class={`grid grid-cols-[auto_minmax(0,1fr)] items-start gap-3 rounded-[var(--ui-radius)] border p-3 sm:grid-cols-[auto_minmax(0,1fr)_auto] ${alertClasses(tone)}`}
    >
      <IconTile name={ALERT_ICON[alert.kind]} color={STATUS_COLOR[tone]} sm />
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-2">
          <Badge text={alertLabel(alert.kind)} tone={tone} />
          <span class="min-w-0 truncate text-sm">{alert.detail}</span>
        </div>
        <div class="flex flex-wrap gap-x-3.5 text-xs text-[var(--ui-muted)]">
          <span>{alertWhere(alert)}</span>
          <span>{`since ${fmtAgo(alert.since, now)}`}</span>
          {alert.costUsd !== null && <span>{costText(alert.costUsd)}</span>}
        </div>
      </div>
      <button
        type="button"
        class="ui-btn ui-btn-ghost col-span-2 justify-self-end sm:col-span-1"
        data-action="dismiss-alert"
        data-value={alert.id}
        aria-label={`Dismiss: ${alert.detail}`}
        onClick={() => act("dismiss-alert", alert.id)}
      >
        <Icon name="close" class="ui-icon" />
        <span>Dismiss</span>
      </button>
    </li>
  );
}

/** The overview's alert strip: the first three alerts, their count, and a button to the alerts tab. */
export function AlertStrip() {
  const { state, act } = useApp();
  if (state.alerts.length === 0) return null;
  const shown = state.alerts.slice(0, 3);
  const more = state.alerts.length - shown.length;
  return (
    <Panel
      icon="bell"
      title="Needs a look"
      subtitle={fmtCount(state.alerts.length, "active alert")}
      actions={
        <button
          type="button"
          class="ui-btn ui-btn-secondary"
          data-action="tab"
          data-value="alerts"
          onClick={() => act("tab", "alerts")}
        >
          <span>{more > 0 ? `All ${fmtNum(state.alerts.length)} alerts` : "Alerts"}</span>
        </button>
      }
    >
      <ul class="m-0 grid list-none gap-2 p-0">
        {shown.map((alert) => (
          <AlertRow key={alert.id} alert={alert} />
        ))}
      </ul>
    </Panel>
  );
}
