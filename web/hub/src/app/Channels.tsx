// Channels.tsx — Settings › Linked channels (SPEC 8): every link of every channel, the level
// each session hears its channel at and the select that moves it, the age of a session's
// newest digest, and the unlink that ends the tie (the passkey a 401 asks for first). The
// channel's own link-another sheet starts with its sessions picked and the channel chosen.

import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { ApiError } from "../api.ts";
import { type ChannelLinks, LEVEL_LABEL, type LinkLevel } from "../links.ts";
import { ageText, sessionName } from "../registry.ts";
import { errText, useHuddle } from "./context.tsx";
import { LinkSheet } from "./LinkSheet.tsx";
import { useStepUp } from "./Needs.tsx";
import { StateMark } from "./Now.tsx";

/** The levels the level select offers, in the picker's own order. */
const LEVELS: readonly LinkLevel[] = ["overhear", "digest", "on-request"];

/** The linked sessions of one channel, as the section links another into it. */
const sessionsOf = (ch: ChannelLinks): string[] => ch.links.map((l) => l.session_id);

/** Settings › Linked channels: one group per channel, one row per link. */
export function Channels(): JSX.Element {
  const { api, toast, now, registry } = useHuddle();
  const { ask, sheet } = useStepUp();
  const [channels, setChannels] = useState<ChannelLinks[] | null>(null);
  const [error, setError] = useState("");
  const [linking, setLinking] = useState<ChannelLinks | null>(null);

  const reload = (): void => {
    api
      .api("/api/links")
      .then((r) => setChannels((r as { channels?: ChannelLinks[] }).channels ?? []))
      .catch((e: unknown) => setError(errText(e)));
  };
  useEffect(reload, [api]);

  const titleOf = (id: string): string => {
    const row = registry.getState().sessions?.find((s) => s.session_id === id);
    return row ? sessionName(row) : id;
  };
  const stateOf = (id: string): string =>
    registry.getState().sessions?.find((s) => s.session_id === id)?.state ?? "idle";

  // the level move is no dangerous act: no step-up, the select answers at once
  const moveLevel = async (channel: string, session: string, level: LinkLevel): Promise<void> => {
    try {
      await api.api(`/api/links/${encodeURIComponent(channel)}/${encodeURIComponent(session)}`, {
        method: "PATCH",
        body: { level },
      });
    } catch (e) {
      toast(errText(e), { bad: true });
      return;
    }
    setChannels((chs) =>
      (chs ?? []).map((ch) =>
        ch.channel !== channel
          ? ch
          : {
              ...ch,
              links: ch.links.map((l) => (l.session_id === session ? { ...l, level } : l)),
            },
      ),
    );
  };

  const unlink = async (channel: string, session: string): Promise<void> => {
    // ending the tie is a dangerous act (SPEC 7.4): a 401 runs the passkey sheet once and
    // deletes again
    const path = `/api/links/${encodeURIComponent(channel)}/${encodeURIComponent(session)}`;
    const drop = (): void =>
      setChannels((chs) =>
        (chs ?? [])
          .map((ch) =>
            ch.channel !== channel ? ch : { ...ch, links: ch.links.filter((l) => l.session_id !== session) },
          )
          .filter((ch) => ch.links.length > 0),
      );
    try {
      await api.api(path, { method: "DELETE" });
      drop();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401 || err.message !== "step-up required") {
        toast(errText(e), { bad: true });
        return;
      }
      if (!(await ask({ action: "Unlink session", session: titleOf(session) }))) return;
      try {
        await api.api(path, { method: "DELETE" });
        drop();
      } catch (e2) {
        toast(errText(e2), { bad: true });
      }
    }
  };

  return (
    <section aria-labelledby="linked-channels-h" data-linked-channels="" class="flex flex-col gap-1">
      {sheet}
      {linking ? (
        <LinkSheet
          sessions={sessionsOf(linking)}
          channel={linking.channel}
          onClose={() => setLinking(null)}
          onLinked={reload}
        />
      ) : null}
      <h2 id="linked-channels-h" class="mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase muted">
        Linked channels{channels && channels.length > 0 ? ` · ${channels.length}` : ""}
      </h2>
      {error ? (
        <p role="alert" class="m-0 text-sm text-error">
          {error}
        </p>
      ) : null}
      {channels === null ? (
        <p class="m-0 text-sm muted">Loading…</p>
      ) : channels.length === 0 ? (
        <p class="m-0 text-sm muted">No linked sessions yet. Pick two or more on Now and choose Link.</p>
      ) : (
        <div class="flex flex-col gap-4">
          {channels.map((ch) => (
            <div key={ch.channel} data-channel={ch.channel} class="flex flex-col gap-1">
              <h3 class="m-0 text-[15px] font-semibold">#{ch.channel}</h3>
              <ul class="m-0 flex list-none flex-col p-0">
                {ch.links.map((l) => {
                  const title = titleOf(l.session_id);
                  return (
                    <li
                      key={l.session_id}
                      data-link={l.session_id}
                      class="flex min-h-12 items-center justify-between gap-3 border-b hairline py-1"
                    >
                      <div class="flex min-w-0 items-center gap-2.5">
                        <StateMark state={stateOf(l.session_id)} />
                        <span class="truncate text-[15px]">{title}</span>
                      </div>
                      <div class="flex shrink-0 items-center gap-2">
                        {l.digest_at ? (
                          <span class="text-xs muted tnum">digest {ageText(l.digest_at, now)}</span>
                        ) : null}
                        <select
                          class="select max-w-36"
                          aria-label={`Level for ${title}`}
                          value={l.level}
                          onChange={(e) =>
                            void moveLevel(ch.channel, l.session_id, e.currentTarget.value as LinkLevel)
                          }
                        >
                          {LEVELS.map((lv) => (
                            <option key={lv} value={lv}>
                              {LEVEL_LABEL[lv]}
                            </option>
                          ))}
                        </select>
                        <button
                          type="button"
                          class="btn btn-ghost btn-sm text-error"
                          onClick={() => void unlink(ch.channel, l.session_id)}
                        >
                          Unlink
                        </button>
                      </div>
                    </li>
                  );
                })}
              </ul>
              <div>
                <button
                  type="button"
                  data-link-another=""
                  class="btn btn-ghost btn-sm"
                  onClick={() => setLinking(ch)}
                >
                  Link another session
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
