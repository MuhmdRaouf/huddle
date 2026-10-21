// Home.tsx — the channels page: a card per channel with what needs the owner on top, the New
// channel dialog, and the start-a-session card. The list is read again every 10 s while the
// page shows and the tab is visible (app.js homeView/homeData/paintChannels/newChannel); the
// poll's timers are injected, so tests step it.

import { Dialog } from "@huddle/ui/dialog.tsx";
import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Api } from "../api.ts";
import { ConnectCard } from "../app/ConnectCard.tsx";
import { errText, useHuddle } from "../app/context.tsx";
import { IntroActions } from "../app/intro.tsx";
import { Icon } from "../icons.tsx";
import { Avatar, Empty, Pill } from "../kit.tsx";
import type { Attention, ChannelSummary } from "../store.ts";

/** One row of GET /api/channels, with what the card shows: description, who is online, the
 *  plan's progress and the last event, plus the needs count the list sorts by. */
export type ChannelRow = ChannelSummary & {
  description?: string;
  sessions?: { name: string }[];
  stats?: { tasks?: number; done?: number; last?: number };
  needs?: number;
};

/** The slice of setInterval the poll needs; the browser's by default, fakes in tests. */
export type PollTimers = { interval(fn: () => void, ms: number): unknown; stop(h: unknown): void };

const realTimers: PollTimers = {
  interval: (fn, ms) => setInterval(fn, ms),
  stop: (h) => clearInterval(h as number),
};

/** How many things on a channel need the owner: the attention snapshot's four kinds. */
export const needsOf = (a: Attention | null): number =>
  a ? (a.asks?.length ?? 0) + (a.gates?.length ?? 0) + (a.paused?.length ?? 0) + (a.blocked?.length ?? 0) : 0;

/** The channel list with each channel's needs count, sorted so the ones that need the owner come
 *  first, then the most recently active, then by name (app.js homeData). */
export async function homeData(api: Api): Promise<ChannelRow[]> {
  const list = ((await api.api("/api/channels").catch(() => [])) as ChannelRow[] | undefined) ?? [];
  await Promise.all(
    list.map(async (c) => {
      const a = (await api.api(api.channelPath(c.name, "/attention")).catch(() => null)) as Attention | null;
      c.needs = needsOf(a);
    }),
  );
  return list.sort(
    (a, b) =>
      (b.needs ?? 0) - (a.needs ?? 0) ||
      (b.stats?.last ?? 0) - (a.stats?.last ?? 0) ||
      a.name.localeCompare(b.name),
  );
}

/** The card's head: icon, name and title, description, and the needs pill (paintChannels' card). */
function ChannelHead({ c }: { c: ChannelRow }): JSX.Element {
  return (
    <div class="flex items-start gap-3">
      <span class="tinted ink inline-flex size-7 shrink-0 items-center justify-center rounded-lg c-blue size-9">
        <Icon name="hash" class="size-4" />
      </span>
      <div class="min-w-0 flex-1">
        <div class="flex min-w-0 items-center gap-2">
          <h2 class="truncate text-[15px] font-semibold">{c.title || c.name}</h2>
          {c.title && c.title !== c.name ? (
            <span class="badge badge-ghost badge-sm font-mono">{c.name}</span>
          ) : null}
        </div>
        <p class="mt-0.5 line-clamp-2 text-sm muted">
          {c.description ? c.description : "No description yet."}
        </p>
      </div>
      {c.needs ? (
        <Pill status="needs" label={`${c.needs} need${c.needs === 1 ? "s" : ""} you`} />
      ) : (
        <Pill status="done" label="All clear" />
      )}
    </div>
  );
}

/** The card's online cell: up to five avatars, a "+n more", and the count — or nobody (paintChannels). */
function OnlineCell({ on }: { on: { name: string }[] }): JSX.Element {
  const more = on.length - 5;
  return (
    <span class="flex items-center gap-2">
      {on.length ? (
        <>
          <span class="flex items-center -space-x-1.5">
            {on.slice(0, 5).map((s) => (
              <Avatar key={s.name} name={s.name} small />
            ))}
            {more > 0 ? (
              <span class="avatar avatar-placeholder c-idle" title={`${more} more online`}>
                <span class="w-6 rounded-full tinted">
                  <span class="ink text-xs font-semibold">+{more}</span>
                </span>
              </span>
            ) : null}
          </span>
          {on.length} online
        </>
      ) : (
        <>
          <Icon name="users" class="size-4 text-base-content/50" />
          Nobody online
        </>
      )}
    </span>
  );
}

/** The card's plan cell: a bar and "done of tasks", or "No tasks yet" (paintChannels). */
function TasksCell({ st }: { st: ChannelRow["stats"] }): JSX.Element {
  const pct = st?.tasks ? Math.round(((st.done ?? 0) / st.tasks) * 100) : 0;
  return (
    <span class="flex min-w-36 flex-1 items-center gap-2">
      {st?.tasks ? (
        <>
          <span
            class="segbar max-w-40 flex-1"
            role="img"
            aria-label={`${st.done ?? 0} of ${st.tasks} tasks done`}
          >
            <i class="segbar-seg" style={`width:${pct}%`} />
          </span>
          <span class="tabular-nums">
            {st.done ?? 0} of {st.tasks} tasks
          </span>
        </>
      ) : (
        <>
          <Icon name="list" class="size-4 text-base-content/50" />
          No tasks yet
        </>
      )}
    </span>
  );
}

/** The card's activity cell: the last event's seq, or "No events yet" (paintChannels). */
function LastCell({ st }: { st: ChannelRow["stats"] }): JSX.Element {
  return (
    <span class="flex items-center gap-1.5 tabular-nums" title="Last event">
      <Icon name="activity" class="size-4 text-base-content/50" />
      {st?.last ? `Last event #${st.last}` : "No events yet"}
    </span>
  );
}

/** One channel's card: name and title, description, the needs pill, who is online, the plan's
 *  progress and the last event (app.js paintChannels' card). The creator's browser gets the
 *  Remove channel action beside the cells; the link's own navigation never fires for it. */
export function ChannelCard({
  c,
  onRemove,
}: {
  c: ChannelRow;
  onRemove?: ((c: ChannelRow) => void) | undefined;
}): JSX.Element {
  const st = c.stats ?? {};
  return (
    <div class="relative">
      <a
        class="panel flex flex-col gap-4 p-5 transition-colors hover:bg-base-content/5"
        href={`#/channels/${c.name}`}
        data-ch={c.name}
      >
        <ChannelHead c={c} />
        <div class="mt-auto flex flex-wrap items-center gap-x-5 gap-y-3 border-t hairline pt-4 text-xs muted">
          <OnlineCell on={c.sessions ?? []} />
          <TasksCell st={st} />
          <LastCell st={st} />
        </div>
      </a>
      {onRemove ? (
        <button
          type="button"
          class="btn btn-ghost btn-xs absolute right-4 bottom-5"
          data-remove={c.name}
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onRemove(c);
          }}
        >
          <Icon name="trash" class="size-3.5" /> Remove channel
        </button>
      ) : null}
    </div>
  );
}

/** The Remove channel dialog: the channel's name, the kept history on disk, the server's
 *  refusal (a session online) in the dialog itself. */
function RemoveChannel({
  c,
  onClose,
  onRemoved,
}: {
  c: ChannelRow | null;
  onClose: () => void;
  onRemoved: () => void;
}): JSX.Element {
  const { api, toast } = useHuddle();
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => setErr(""), [c]);
  const submit = async (): Promise<void> => {
    if (!c || busy) return;
    setErr("");
    setBusy(true);
    try {
      await api.api(`/api/channels/${c.name}`, { method: "DELETE" });
      onClose();
      toast(`Removed ${c.name}. Its history is kept under channels/.removed/.`);
      onRemoved();
    } catch (x) {
      setErr(errText(x));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open={!!c} onClose={onClose} title="Remove channel">
      <div class="flex flex-col gap-3 p-4">
        <p class="text-sm">
          Remove <strong>{c?.name}</strong>? Sessions can no longer join it. Its history is kept on disk under{" "}
          <code>channels/.removed/</code>; stop the hub and move the file back to{" "}
          <code>channels/{c?.name}.db</code> to undo.
        </p>
        <p class="text-xs text-error" role="alert">
          {err}
        </p>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-ghost" onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            class="btn btn-error"
            data-confirm-remove
            disabled={busy}
            onClick={() => void submit()}
          >
            Remove channel
          </button>
        </div>
      </div>
    </Dialog>
  );
}

/** The New channel dialog: name, title, description, allowed members; a bad name is said in the
 *  form, a refused create too (app.js newChannel). */
function NewChannel({ open, onClose }: { open: boolean; onClose: () => void }): JSX.Element {
  const { api, go, toast } = useHuddle();
  const [err, setErr] = useState("");
  const [badName, setBadName] = useState(false);
  const nameRef = useRef<HTMLInputElement>(null);
  const submit = async (e: Event): Promise<void> => {
    e.preventDefault();
    setErr("");
    const f = e.target as HTMLFormElement;
    const name = (f.elements.namedItem("nc-name") as HTMLInputElement).value.trim();
    const members = (f.elements.namedItem("nc-members") as HTMLInputElement).value
      .split(/[\s,]+/)
      .filter(Boolean);
    if (!/^[a-z0-9][a-z0-9-]{0,39}$/.test(name)) {
      setErr("Use 1 to 40 lowercase letters, digits or dashes, starting with a letter or digit.");
      setBadName(true);
      nameRef.current?.focus();
      return;
    }
    const title = (f.elements.namedItem("nc-title") as HTMLInputElement).value.trim();
    const description = (f.elements.namedItem("nc-desc") as HTMLTextAreaElement).value.trim();
    try {
      await api.api("/api/channels", {
        body: {
          name,
          title: title || undefined,
          description: description || undefined,
          ...(members.length ? { members } : {}),
        },
      });
      onClose();
      toast(`Created ${name}`);
      go(`#/channels/${name}`);
    } catch (x) {
      setErr(errText(x));
    }
  };
  return (
    <Dialog open={open} onClose={onClose} title="New channel">
      <form class="flex flex-col gap-3 p-4" noValidate onSubmit={(e) => void submit(e)}>
        <label class="flex flex-col gap-1.5 text-sm font-medium">
          Name{" "}
          <input
            ref={nameRef}
            class="input input-sm"
            id="nc-name"
            name="nc-name"
            required
            pattern="[a-z0-9][a-z0-9\-]{0,39}"
            placeholder="checkout-v2"
            autoComplete="off"
            aria-describedby="nc-nh nc-err"
            aria-invalid={badName ? "true" : undefined}
          />
          <span class="text-xs font-normal muted" id="nc-nh">
            Lowercase letters, digits and dashes. Sessions use it in <code>.agents/huddle/huddle.json</code>.
          </span>
        </label>
        <label class="flex flex-col gap-1.5 text-sm font-medium">
          <span>
            Title <span class="text-xs font-normal muted">(optional)</span>
          </span>
          <input class="input input-sm" id="nc-title" name="nc-title" placeholder="Checkout, version 2" />
        </label>
        <label class="flex flex-col gap-1.5 text-sm font-medium">
          <span>
            What the team is doing <span class="text-xs font-normal muted">(optional)</span>
          </span>
          <textarea class="input input-sm" id="nc-desc" name="nc-desc" rows={2} />
        </label>
        <label class="flex flex-col gap-1.5 text-sm font-medium">
          <span>
            Allowed sessions <span class="text-xs font-normal muted">(optional)</span>
          </span>
          <input
            class="input input-sm"
            id="nc-members"
            name="nc-members"
            placeholder="api, web, docs"
            aria-describedby="nc-mh"
          />
          <span class="text-xs font-normal muted" id="nc-mh">
            Leave empty to let any local session join.
          </span>
        </label>
        <p class="text-xs text-error" id="nc-err" role="alert">
          {err}
        </p>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-ghost" onClick={onClose}>
            Cancel
          </button>
          <button class="btn btn-primary" type="submit">
            Create channel
          </button>
        </div>
      </form>
    </Dialog>
  );
}

/** The channels page. The list loads at once and again every 10 s while the page shows and the
 *  tab is visible (app.js homeView + HOMEPOLL). The creator's browser (an owner link session)
 *  gets Remove channel on every card. */
export function Home({ timers = realTimers }: { timers?: PollTimers }): JSX.Element {
  const { api, who } = useHuddle();
  const [list, setList] = useState<readonly ChannelRow[]>([]);
  const [open, setOpen] = useState(false);
  const [rm, setRm] = useState<ChannelRow | null>(null);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let on = true;
    const read = (): void => {
      void homeData(api).then((l) => {
        if (on) setList(l);
      });
    };
    read();
    const h = timers.interval(() => {
      if (document.hidden) return;
      read();
    }, 10_000);
    return () => {
      on = false;
      timers.stop(h);
    };
  }, [api, timers, tick]);
  const first = list[0]?.name || "my-channel";
  return (
    <div class="flex min-w-0 flex-col gap-6">
      <IntroActions>
        <button type="button" class="btn btn-primary" id="newch" onClick={() => setOpen(true)}>
          <Icon name="plus" class="size-4" /> New channel
        </button>
      </IntroActions>
      {/* biome-ignore lint/a11y/useAriaPropsSupportedByRole: the list region's label */}
      <div id="chlist" aria-label="Channels">
        {list.length ? (
          <div class="grid gap-4 md:grid-cols-2">
            {list.map((c) => (
              <ChannelCard key={c.name} c={c} onRemove={who.kind === "owner" ? setRm : undefined} />
            ))}
          </div>
        ) : (
          <Empty
            text="No channels yet"
            hint="Create one with New channel, or let a session's first join create it."
            icon="layers"
          />
        )}
      </div>
      <ConnectCard ch={first} />
      <NewChannel open={open} onClose={() => setOpen(false)} />
      {rm ? (
        <RemoveChannel c={rm} onClose={() => setRm(null)} onRemoved={() => setTick((n) => n + 1)} />
      ) : null}
    </div>
  );
}
