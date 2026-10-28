// InsightsSettings.tsx — Settings › Tips, the pane the address #/settings/insights opens:
// the owner's two switches, and Start over. "Tips for me" shows the
// Insights card's tips; "Tips to my sessions" lets a session take one tip line as context and
// spends its tokens. Start over's Forget asks once, then posts to the forget route, re-reads the
// switches and says it forgot; a refusal says the error and keeps the confirm up. Both live in one
// file in the huddle home; the tips come from what huddle already records and nothing here leaves
// the machine.

import { Button } from "@huddle/ui/button.tsx";
import { Card } from "@huddle/ui/card.tsx";
import { List, ListItem } from "@huddle/ui/list.tsx";
import { Switch } from "@huddle/ui/switch.tsx";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { errText, useHuddle } from "./context.tsx";

/** The switches' route. */
const SWITCHES_PATH = "/api/insights/settings";

/** The forget route. */
const FORGET_PATH = "/api/insights/forget";

/** What a kept forget says. */
const FORGOT_TOAST = "Insights forgot what it learned.";

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

/** This hub's Insights section: the two switches, and Start over. The section reads the switches
 *  once on mount, a flip PUTs its one field alone and shows the switches the hub kept, and a
 *  refusal flips the switch back and says the error. The forget button swaps for the confirm; a
 *  kept forget posts to the forget route, re-reads the switches and puts the button back, and a
 *  refusal says the error and keeps the confirm up. */
export function InsightsSettings(): JSX.Element {
  const { api, toast } = useHuddle();
  const [held, setHeld] = useState<InsightsSwitches | null>(null);
  const [confirming, setConfirming] = useState(false);

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

  const forget = (): void => {
    api
      // the forget takes no fields; an empty body is still JSON, the hub refusing writes without
      // the content-type
      .api(FORGET_PATH, { method: "POST", body: {} })
      .then(() => api.api(SWITCHES_PATH))
      .then((r) => {
        setHeld(switchesOf(r));
        setConfirming(false);
        toast(FORGOT_TOAST);
      })
      .catch((e: unknown) => toast(errText(e), { bad: true }));
  };

  return (
    <section aria-labelledby="insights-h" class="flex flex-col gap-3">
      <h2 id="insights-h" class={HEAD}>
        Tips
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
      <section aria-labelledby="insights-so-h">
        <h2 id="insights-so-h" class={HEAD}>
          Start over
        </h2>
        <Card>
          {confirming ? (
            <>
              <p class={NOTE}>
                Huddle forgets the muted tips and turns both switches back to their defaults.
              </p>
              <div class="mt-3 flex gap-2">
                <Button variant="secondary" class="flex-1" onClick={() => setConfirming(false)}>
                  Cancel
                </Button>
                <Button variant="danger" class="flex-1" onClick={forget}>
                  Forget
                </Button>
              </div>
            </>
          ) : (
            <Button variant="danger" class="w-full" onClick={() => setConfirming(true)}>
              Forget what you learned
            </Button>
          )}
        </Card>
        <p data-footnote="" class="m-0 mt-2 text-[length:var(--ui-text-xs)] text-[var(--ui-muted)]">
          Removes insights.json on this machine.
        </p>
      </section>
    </section>
  );
}
