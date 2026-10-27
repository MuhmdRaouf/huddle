// InsightsSettings.tsx — Settings › Tips › Insights, the group's own member with the
// address #/settings/insights: the owner's two switches. "Tips for me" shows the Insights card's
// tips; "Tips to my sessions" lets a session take one tip line as context and spends its tokens.
// Both live in one file in the huddle home; the tips come from what huddle already records and
// nothing here leaves the machine.

import { List, ListItem } from "@huddle/ui/list.tsx";
import { Switch } from "@huddle/ui/switch.tsx";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { errText, useHuddle } from "./context.tsx";

/** The switches' route. */
const SWITCHES_PATH = "/api/insights/settings";

/** A section's heading, the same small caps on every section. */
const HEAD = "mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase text-[var(--ui-muted)]";

/** Muted body copy under a heading. */
const NOTE = "m-0 text-sm text-[var(--ui-muted)]";

/** What the route answers: the two switches as the hub holds them. */
type InsightsSwitches = {
  tips_for_me: boolean;
  tips_to_sessions: boolean;
};

/** Reads the route's answer into the switches; a field the answer leaves out reads as off. */
function switchesOf(r: unknown): InsightsSwitches {
  const o = (r ?? {}) as { tips_for_me?: unknown; tips_to_sessions?: unknown };
  return { tips_for_me: o.tips_for_me === true, tips_to_sessions: o.tips_to_sessions === true };
}

/** One switch row: its title and help line name and describe the switch, a full tap target.
 *  A click asks onFlip; the row's state comes back through `on`. `disabled` holds the switch
 *  still — until the hub's first answer, the row's state is not the hub's to flip. */
function SwitchRow({
  id,
  title,
  help,
  on,
  disabled = false,
  onFlip,
}: {
  id: string;
  title: string;
  help: ComponentChildren;
  on: boolean;
  disabled?: boolean;
  onFlip: () => void;
}): JSX.Element {
  return (
    <ListItem
      title={
        <span id={`${id}-l`} class="whitespace-normal">
          {title}
        </span>
      }
      meta={<span id={`${id}-h`}>{help}</span>}
      trailing={
        <Switch
          id={`set-${id}`}
          labelledBy={`${id}-l`}
          describedBy={`${id}-h`}
          checked={on}
          disabled={disabled}
          onChange={onFlip}
        />
      }
    />
  );
}

/** This hub's Insights section: the two switches. The section reads them once on mount, a flip
 *  PUTs its one field alone and shows the switches the hub kept, and a refusal flips the switch
 *  back and says the error. */
export function InsightsSettings(): JSX.Element {
  const { api, toast } = useHuddle();
  const [held, setHeld] = useState<InsightsSwitches | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .api(SWITCHES_PATH)
      .then((r) => {
        if (alive) setHeld(switchesOf(r));
      })
      .catch((e: unknown) => {
        if (alive) toast(errText(e), { bad: true });
      });
    return () => {
      alive = false;
    };
  }, [api]);

  const flip = (field: "tips_for_me" | "tips_to_sessions"): void => {
    if (held === null) return;
    const next: InsightsSwitches =
      field === "tips_for_me"
        ? { ...held, tips_for_me: !held.tips_for_me }
        : { ...held, tips_to_sessions: !held.tips_to_sessions };
    setHeld(next); // the switch answers the tap at once; the hub's answer decides whether it holds
    api
      .api(SWITCHES_PATH, { method: "PUT", body: { [field]: next[field] } })
      .then((r) => setHeld(switchesOf(r)))
      .catch((e: unknown) => {
        setHeld(held); // the hub did not take the change, so the switch goes back
        toast(errText(e), { bad: true });
      });
  };

  return (
    <section aria-labelledby="insights-h" class="flex flex-col gap-3">
      <h2 id="insights-h" class={HEAD}>
        Insights
      </h2>
      <p class={NOTE}>Tips come from what huddle already records. Nothing leaves this machine.</p>
      <List label="Insights">
        <SwitchRow
          id="tips-for-me"
          title="Tips for me"
          help="The Insights card on Sessions"
          on={held?.tips_for_me ?? false}
          disabled={held === null}
          onFlip={() => flip("tips_for_me")}
        />
        <SwitchRow
          id="tips-to-sessions"
          title="Tips to my sessions"
          help="One tip line in a session's context at start and on a prompt. Spends tokens."
          on={held?.tips_to_sessions ?? false}
          disabled={held === null}
          onFlip={() => flip("tips_to_sessions")}
        />
      </List>
    </section>
  );
}
