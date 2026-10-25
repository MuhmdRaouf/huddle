// LinkedSessions.tsx — a channel's Settings › Linked sessions (SPEC 8): every session linked into
// the channel, the level it hears the channel at and the select that moves it, the age of its
// newest digest, and the unlink that ends the tie (the passkey a 401 asks for first). The
// link-another sheet starts with the channel's sessions picked and the channel chosen.

import { Button } from "@huddle/ui/button.tsx";
import { Card } from "@huddle/ui/card.tsx";
import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { ApiError } from "../api.ts";
import { errText, useHuddle } from "../app/context.tsx";
import { LinkSheet } from "../app/LinkSheet.tsx";
import { useStepUp } from "../app/Needs.tsx";
import { StateMark } from "../app/Now.tsx";
import { type ChannelLinks, LEVEL_LABEL, type LinkLevel, type LinkRow } from "../links.ts";
import { ageText, sessionName } from "../registry.ts";

/** The levels the level select offers, in the picker's own order. */
const LEVELS: readonly LinkLevel[] = ["overhear", "digest", "on-request"];

/** The channel's Linked sessions card: one row per session linked into it. */
export function LinkedSessions({ channel }: { channel: string }): JSX.Element {
  const { api, toast, now, registry } = useHuddle();
  const { ask, sheet } = useStepUp();
  const [links, setLinks] = useState<LinkRow[] | null>(null);
  const [error, setError] = useState("");
  const [linking, setLinking] = useState(false);

  const reload = (): void => {
    api
      .api("/api/links")
      .then((r) => {
        const all = (r as { channels?: ChannelLinks[] }).channels ?? [];
        setLinks(all.find((ch) => ch.channel === channel)?.links ?? []);
      })
      .catch((e: unknown) => setError(errText(e)));
  };
  useEffect(reload, [api, channel]);

  const titleOf = (id: string): string => {
    const row = registry.getState().sessions?.find((s) => s.session_id === id);
    return row ? sessionName(row) : id;
  };
  const stateOf = (id: string): string =>
    registry.getState().sessions?.find((s) => s.session_id === id)?.state ?? "idle";
  const pathOf = (session: string): string =>
    `/api/links/${encodeURIComponent(channel)}/${encodeURIComponent(session)}`;

  // the level move is no dangerous act: no step-up, the select answers at once
  const moveLevel = async (session: string, level: LinkLevel): Promise<void> => {
    try {
      await api.api(pathOf(session), { method: "PATCH", body: { level } });
    } catch (e) {
      toast(errText(e), { bad: true });
      return;
    }
    setLinks((ls) => (ls ?? []).map((l) => (l.session_id === session ? { ...l, level } : l)));
  };

  const unlink = async (session: string): Promise<void> => {
    // ending the tie is a dangerous act (SPEC 7.4): a 401 runs the passkey sheet once and
    // deletes again
    const drop = (): void => setLinks((ls) => (ls ?? []).filter((l) => l.session_id !== session));
    try {
      await api.api(pathOf(session), { method: "DELETE" });
      drop();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401 || err.message !== "step-up required") {
        toast(errText(e), { bad: true });
        return;
      }
      if (!(await ask({ action: "Unlink session", session: titleOf(session) }))) return;
      try {
        await api.api(pathOf(session), { method: "DELETE" });
        drop();
      } catch (e2) {
        toast(errText(e2), { bad: true });
      }
    }
  };

  return (
    <div data-linked-sessions="">
      {sheet}
      {linking && links ? (
        <LinkSheet
          sessions={links.map((l) => l.session_id)}
          channel={channel}
          onClose={() => setLinking(false)}
          onLinked={reload}
        />
      ) : null}
      <Card
        title="Linked sessions"
        meta={links && links.length > 0 ? String(links.length) : undefined}
        actions={
          links && links.length > 0 ? (
            <Button variant="secondary" data-link-another="" onClick={() => setLinking(true)}>
              Link another session
            </Button>
          ) : undefined
        }
      >
        {error ? (
          <p role="alert" class="m-0 text-sm text-[var(--ui-fail-ink)]">
            {error}
          </p>
        ) : null}
        {links === null ? (
          error ? null : (
            <p class="m-0 text-sm text-[var(--ui-muted)]">Loading…</p>
          )
        ) : links.length === 0 ? (
          <p class="m-0 text-sm text-[var(--ui-muted)]">
            No sessions linked into this channel. Pick two or more on Now and choose Link.
          </p>
        ) : (
          <ul class="m-0 flex list-none flex-col p-0">
            {links.map((l) => {
              const title = titleOf(l.session_id);
              return (
                <li
                  key={l.session_id}
                  data-link={l.session_id}
                  class="flex min-h-12 flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-[var(--ui-line)] py-1 last:border-b-0"
                >
                  <div class="flex min-w-0 items-center gap-2.5">
                    <StateMark state={stateOf(l.session_id)} />
                    <span class="truncate text-[15px]">{title}</span>
                  </div>
                  <div class="flex shrink-0 items-center gap-2">
                    {l.digest_at ? (
                      <span class="text-xs text-[var(--ui-muted)] tabular-nums">
                        digest {ageText(l.digest_at, now)}
                      </span>
                    ) : null}
                    <select
                      class="ui-input max-w-36"
                      aria-label={`Level for ${title}`}
                      value={l.level}
                      onChange={(e) => void moveLevel(l.session_id, e.currentTarget.value as LinkLevel)}
                    >
                      {LEVELS.map((lv) => (
                        <option key={lv} value={lv}>
                          {LEVEL_LABEL[lv]}
                        </option>
                      ))}
                    </select>
                    <Button variant="ghost" onClick={() => void unlink(l.session_id)}>
                      Unlink
                    </Button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </Card>
    </div>
  );
}
