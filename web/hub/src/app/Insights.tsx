// Insights.tsx — the Insights card on Now: the tips GET /api/insights answers from the needs-you
// history, each one a sentence with the text between backticks drawn as code, over the line that
// names the rule's source and links the tip's session. Read on mount and once every five minutes
// through the owner's own credential. A body that cannot be read, or one without a tip, hides the
// card; the card never notifies and never interrupts.
import { Card } from "@huddle/ui/card.tsx";
import { Fragment, type JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { Api } from "../api.ts";
import { useHuddle } from "./context.tsx";

const INSIGHTS_PATH = "/api/insights";
const REFRESH_MS = 300_000;

/** One tip the insights route answers: what it noticed, how often, and — when it names one — the
 *  session it saw it in. */
export type Tip = {
  id: string;
  rule: string;
  session?: string;
  sentence: string;
  count: number;
  wait_ms?: number;
};

const isStr = (v: unknown): v is string => typeof v === "string";
const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null;
const isCount = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);

/** One wire tip as a Tip, or null when its id, rule or sentence is not a string. */
function tipOf(v: unknown): Tip | null {
  if (!isRecord(v) || !isStr(v.id) || !isStr(v.rule) || !isStr(v.sentence)) return null;
  return {
    id: v.id,
    rule: v.rule,
    sentence: v.sentence,
    count: isCount(v.count) ? v.count : 0,
    ...(isStr(v.session) ? { session: v.session } : {}),
    ...(isCount(v.wait_ms) ? { wait_ms: v.wait_ms } : {}),
  };
}

/** Reads the tips once; a refusal, a network error or a body that is not { tips: Tip[] } reads as
 *  null. A tip without a string id, rule and sentence is dropped. */
export async function readInsights(api: Api): Promise<Tip[] | null> {
  let body: unknown;
  try {
    body = await api.api(INSIGHTS_PATH);
  } catch {
    return null;
  }
  if (!isRecord(body) || !Array.isArray(body.tips)) return null;
  const tips: Tip[] = [];
  for (const v of body.tips) {
    const tip = tipOf(v);
    if (tip) tips.push(tip);
  }
  return tips;
}

/** The line naming where a rule's tip comes from. */
function sourceOf(rule: string): string {
  if (rule === "always_allowed") return "From your permission asks, last 7 days";
  if (rule === "longest_wait") return "From Needs you, last 7 days";
  if (rule === "same_command" || rule === "tool_failing") return "From your sessions' tool runs";
  if (rule === "lane_failing") return "From the provider lanes, today";
  return "From huddle";
}

/** One sentence as text: the span between a pair of backticks draws as code with the backticks
 *  themselves hidden, and a last backtick without its pair leaves the tail as text. */
function Sentence({ text }: { text: string }): JSX.Element {
  const parts = text.split("`");
  const last = parts.length - 1;
  return (
    <p class="m-0 text-sm">
      {parts.map((part, i) =>
        i % 2 === 1 && i !== last ? (
          <code key={i} class="ui-mono">
            {part}
          </code>
        ) : (
          <Fragment key={i}>{part}</Fragment>
        ),
      )}
    </p>
  );
}

/** The card itself, over tips already read. */
export function InsightsView({ tips }: { tips: readonly Tip[] }): JSX.Element {
  return (
    <div data-insights="">
      <Card title="Insights" meta={String(tips.length)}>
        <ul class="m-0 flex list-none flex-col gap-3 p-0">
          {tips.map((tip) => (
            <li key={tip.id} data-tip={tip.rule}>
              <Sentence text={tip.sentence} />
              <p class="m-0 text-xs muted">
                {sourceOf(tip.rule)}
                {tip.session !== undefined ? (
                  <>
                    {" · "}
                    <a href={`#/sessions/${tip.session}`}>open the session</a>
                  </>
                ) : null}
              </p>
            </li>
          ))}
        </ul>
      </Card>
    </div>
  );
}

/** The card as Now mounts it: read on mount and once every five minutes; nothing while there is
 *  nothing to show. */
export function InsightsCard(): JSX.Element | null {
  const { api } = useHuddle();
  const [tips, setTips] = useState<Tip[] | null>(null);
  useEffect(() => {
    let alive = true;
    const read = (): void => {
      void readInsights(api).then((t) => {
        if (alive) setTips(t);
      });
    };
    read();
    const timer = window.setInterval(read, REFRESH_MS);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [api]);
  return tips !== null && tips.length > 0 ? <InsightsView tips={tips} /> : null;
}
