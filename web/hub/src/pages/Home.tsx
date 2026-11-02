// Home.tsx — the channels page: a card per channel with what needs the owner on top, the New
// channel dialog, and the start-a-session card. The list is read again every 10 s while the
// page shows and the tab is visible (app.js homeView/homeData/paintChannels/newChannel); the
// poll's timers are injected, so tests step it.

import { Badge } from "@huddle/ui/badge.tsx";
import { Button } from "@huddle/ui/button.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import { Empty } from "@huddle/ui/empty.tsx";
import { Field } from "@huddle/ui/field.tsx";
import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { Api } from "../api.ts";
import { ConnectCard } from "../app/ConnectCard.tsx";
import { errText, useHuddle } from "../app/context.tsx";
import { IntroActions } from "../app/intro.tsx";
import { Icon } from "../icons.tsx";
import { Avatar } from "../kit.tsx";
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
      <span class="inline-flex size-9 shrink-0 items-center justify-center rounded-[var(--ui-radius-sm)] bg-[var(--ui-accent-soft)] text-[var(--ui-accent)]">
        <Icon name="hash" class="size-4" />
      </span>
      <div class="min-w-0 flex-1">
        <div class="flex min-w-0 items-center gap-2">
          <h2 class="truncate text-[length:var(--ui-text-md)] font-semibold">{c.title || c.name}</h2>
          {c.title && c.title !== c.name ? (
            <Badge tone="neutral">
              <span class="font-mono">{c.name}</span>
            </Badge>
          ) : null}
        </div>
        <p class="mt-0.5 line-clamp-2 text-sm text-[var(--ui-muted)]">
          {c.description ? c.description : "No description yet."}
        </p>
      </div>
      {c.needs ? (
        <Badge tone="needs-you">{`${c.needs} need${c.needs === 1 ? "s" : ""} you`}</Badge>
      ) : (
        <Badge tone="ok">All clear</Badge>
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
              <span
                class="flex size-6 shrink-0 items-center justify-center rounded-full bg-[var(--ui-surface-2)] text-xs font-semibold text-[var(--ui-muted)]"
                title={`${more} more online`}
              >
                +{more}
              </span>
            ) : null}
          </span>
          {on.length} online
        </>
      ) : (
        <>
          <Icon name="users" class="size-4 text-[var(--ui-muted)]" />
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
          <Icon name="list" class="size-4 text-[var(--ui-muted)]" />
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
      <Icon name="activity" class="size-4 text-[var(--ui-muted)]" />
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
        class="h-full flex flex-col gap-4 rounded-[var(--ui-radius)] bg-[var(--ui-surface)] p-5 shadow-[var(--ui-shadow-1)] outline-none transition-colors focus-visible:ring-2 focus-visible:ring-[var(--ui-accent)] hover:bg-[var(--ui-surface-2)]"
        href={`#/channels/${c.name}`}
        data-ch={c.name}
      >
        <ChannelHead c={c} />
        <div
          class={`mt-auto flex flex-wrap items-center gap-x-5 gap-y-3 border-t border-[var(--ui-line)] pt-4 text-xs text-[var(--ui-muted)]${onRemove ? " pr-10" : ""}`}
        >
          <OnlineCell on={c.sessions ?? []} />
          <TasksCell st={st} />
          <LastCell st={st} />
        </div>
      </a>
      {onRemove ? (
        <Button
          variant="ghost"
          class="ui-btn-icon absolute right-4 bottom-4 text-[var(--ui-muted)] hover:text-[var(--ui-fail-ink)]"
          label={`Remove channel ${c.name}`}
          title="Remove channel"
          data-remove={c.name}
          onClick={() => onRemove(c)}
        >
          <Icon name="trash" class="size-4" />
        </Button>
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
        <p class="text-xs text-[var(--ui-fail-ink)]" role="alert">
          {err}
        </p>
        <div class="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="danger"
            data-confirm-remove=""
            disabled={busy}
            onClick={() => {
              void submit();
            }}
          >
            Remove channel
          </Button>
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
  const [name, setName] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [members, setMembers] = useState("");
  const submit = async (e: Event): Promise<void> => {
    e.preventDefault();
    setErr("");
    const clean = name.trim();
    const memberList = members.split(/[\s,]+/).filter(Boolean);
    if (!/^[a-z0-9][a-z0-9-]{0,39}$/.test(clean)) {
      setErr("Use 1 to 40 lowercase letters, digits or dashes, starting with a letter or digit.");
      (e.target as HTMLFormElement).querySelector<HTMLInputElement>("input")?.focus();
      return;
    }
    const cleanTitle = title.trim();
    const cleanDescription = description.trim();
    try {
      await api.api("/api/channels", {
        body: {
          name: clean,
          title: cleanTitle || undefined,
          description: cleanDescription || undefined,
          ...(memberList.length ? { members: memberList } : {}),
        },
      });
      onClose();
      toast(`Created ${clean}`);
      go(`#/channels/${clean}`);
    } catch (x) {
      setErr(errText(x));
    }
  };
  return (
    <Dialog open={open} onClose={onClose} title="New channel">
      <form class="flex flex-col gap-3 p-4" noValidate onSubmit={(e) => void submit(e)}>
        <Field
          label="Name"
          value={name}
          onInput={(v) => {
            setName(v);
            setErr("");
          }}
          placeholder="checkout-v2"
          error={err || undefined}
          hint={
            <>
              Lowercase letters, digits and dashes. Sessions use it in <code>.agents/huddle/huddle.json</code>
              .
            </>
          }
        />
        <Field label="Title (optional)" value={title} onInput={setTitle} placeholder="Checkout, version 2" />
        <Field
          label="What the team is doing (optional)"
          value={description}
          onInput={setDescription}
          multiline
          minRows={2}
        />
        <Field
          label="Allowed sessions (optional)"
          value={members}
          onInput={setMembers}
          placeholder="api, web, docs"
          hint="Leave empty to let any local session join."
        />
        <div class="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit">Create channel</Button>
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
        <Button onClick={() => setOpen(true)}>
          <Icon name="plus" class="size-4" /> New channel
        </Button>
      </IntroActions>
      {/* biome-ignore lint/a11y/useAriaPropsSupportedByRole: the list region's label */}
      <div id="chlist" aria-label="Channels">
        {list.length ? (
          <div class="grid grid-cols-1 gap-4 tablet:grid-cols-2">
            {list.map((c) => (
              <ChannelCard key={c.name} c={c} onRemove={who.kind === "owner" ? setRm : undefined} />
            ))}
          </div>
        ) : (
          <Empty icon={<Icon name="layers" class="size-6" />}>
            <span class="block font-medium text-[var(--ui-text)]">No channels yet</span>
            <span class="block text-sm">
              Create one with New channel, or let a session's first join create it.
            </span>
          </Empty>
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
