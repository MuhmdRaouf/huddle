// Settings.tsx — the channel's settings as the mock's grouped page: one section per group, a
// heading over a card of rows, and a row opens that group's form in a sheet. A sheet's save posts
// only its own group's fields through op/configure, toasts, reads the channel row again and
// closes; a refusal is said inside the sheet. Linked sessions, this browser's filters, the
// exports and the connect card follow (app.js settingsView + startOptions).

import { Button } from "@huddle/ui/button.tsx";
import { Field } from "@huddle/ui/field.tsx";
import { List, ListItem } from "@huddle/ui/list.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import type { Stamp } from "@huddle/ui/time.ts";
import type { ComponentChildren, JSX, RefObject } from "preact";
import { useCallback, useRef, useState } from "preact/hooks";
import { errText, useHuddle } from "../app/context.tsx";
import { Icon } from "../icons.tsx";
import { type ChannelInfo, type HuddleStore, hasOrch } from "../store.ts";
import { ConnectCard } from "../app/ConnectCard.tsx";
import { LinkedSessions } from "./LinkedSessions.tsx";

/** The config fields the settings read (channel row config; everything else passes through). */
export type SettingsConfig = {
  title?: string;
  description?: string;
  repo?: string;
  profile?: string;
  orchestrator?: unknown;
  members?: string[];
  start?: string;
  handover?: Record<string, unknown>;
  created_at?: Stamp;
} & Record<string, unknown>;

/** The stats line's numbers (channel row stats). */
export type SettingsStats = { events?: number; tasks?: number; knowledge?: number };

/** A section's heading (SettingsTab's HEAD). */
const HEAD = "mb-1.5 text-xs font-semibold tracking-[0.06em] uppercase text-[var(--ui-muted)]";

/** A note the page says under a card (SettingsTab's NOTE). */
const NOTE = "m-0 text-sm text-[var(--ui-muted)]";

/** A field's hint line. */
const HINT = "m-0 text-xs font-normal text-[var(--ui-muted)]";

/** A sheet form's refusal line. */
const ALERT = "m-0 text-sm text-[var(--ui-fail-ink)]";

/** A small quiet icon: a row's chevron, an export row's mark. */
const QUIET_ICON = "size-3.5 shrink-0 text-[var(--ui-muted)]";

/** A row's chevron: the mark that says the row opens something. */
const CHEVRON = <Icon name="right" class={QUIET_ICON} />;

/** The turn's starting options: nobody (work in parallel) or one top-level session; a kept value
 *  that no longer names a session stays listed (app.js startOptions). */
export function startOptions(
  names: readonly string[],
  v: string,
): { value: string; label: string; selected: boolean }[] {
  const n = names.filter((x) => !x.includes("."));
  if (v && !n.includes(v)) n.push(v);
  return [
    { value: "", label: "Nobody: work in parallel", selected: v === "" },
    ...n.map((x) => ({ value: x, label: x, selected: x === v })),
  ];
}

/** The exports a channel serves, as rows (app.js settingsView's Export card). */
const EXPORTS: readonly (readonly [path: string, title: string, what: string])[] = [
  ["/export.md", "Plan", "Markdown"],
  ["/plan.json", "Tasks", "JSON"],
  ["/timeline?limit=2000", "Timeline", "last 2,000 events"],
];

/** The keys "Reset filters" clears, per-viewer settings only (app.js's reset). */
const RESET = /^huddle:(wfilter|wowner|wphase|wq|wview|kbq|kbk|tlf|tls|kmore)/;

/** What one form's save does: build the configure args, say a refusal in the form, save, reload.
 *  Resolves true when the save went through. */
type Save = (
  err: RefObject<HTMLSpanElement | null>,
  build: () => Record<string, unknown>,
) => Promise<boolean>;

/** One group of the mock's page: a heading over one card of rows. */
function Section({
  id,
  heading,
  children,
}: {
  id: string;
  heading: string;
  children: ComponentChildren;
}): JSX.Element {
  return (
    <section data-section={id} aria-labelledby={`set-${id}-h`} class="flex flex-col gap-3">
      <h2 id={`set-${id}-h`} class={HEAD}>
        {heading}
      </h2>
      {children}
    </section>
  );
}

/** The orchestrator field: only there when the channel's config carries the key (hasOrch). */
function OrchField({
  info,
  options,
  current,
}: {
  info: ChannelInfo | null;
  options: string[];
  current: unknown;
}): JSX.Element | null {
  if (!hasOrch(info)) return null;
  return (
    <div class="flex flex-col gap-1.5">
      <label for="st-orch" class="text-sm font-semibold">
        Orchestrator
      </label>
      <select id="st-orch" class="ui-input" name="st-orch">
        <option value="">None</option>
        {options.map((n) => (
          <option key={n} selected={n === current}>
            {n}
          </option>
        ))}
      </select>
      <p class={HINT}>
        The orchestrator plans the work for the others: it imports plans, assigns tasks, and briefs or
        restarts sessions.
      </p>
    </div>
  );
}

/** The about sheet's form: the channel's title and description. */
function AboutForm({
  info,
  err,
  save,
  done,
}: {
  info: ChannelInfo | null;
  err: RefObject<HTMLSpanElement | null>;
  save: Save;
  done: () => void;
}): JSX.Element {
  const c = (info?.config ?? {}) as SettingsConfig;
  const [title, setTitle] = useState(c.title ?? "");
  const [description, setDescription] = useState(c.description ?? "");
  return (
    <form
      class="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        void save(err, () => ({ title: title.trim(), description })).then((ok) => {
          if (ok) done();
        });
      }}
    >
      <Field label="Title" value={title} onInput={setTitle} />
      <Field
        label="What the team is doing"
        value={description}
        onInput={setDescription}
        multiline
        minRows={3}
        maxRows={3}
      />
      <Button type="submit">Save</Button>
      <span role="alert" ref={err} class={ALERT} />
    </form>
  );
}

/** The members sheet's form: the session names allowed to join the channel. */
function MembersForm({
  info,
  err,
  save,
  done,
}: {
  info: ChannelInfo | null;
  err: RefObject<HTMLSpanElement | null>;
  save: Save;
  done: () => void;
}): JSX.Element {
  const c = (info?.config ?? {}) as SettingsConfig;
  const [names, setNames] = useState((c.members ?? []).join(", "));
  return (
    <form
      class="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        void save(err, () => ({ members: names.split(/[\s,]+/).filter(Boolean) })).then((ok) => {
          if (ok) done();
        });
      }}
    >
      <Field
        label="Allowed sessions"
        value={names}
        onInput={setNames}
        placeholder="Any local session"
        hint="Session names, comma separated. A member's subagents may always join. Leave empty to let any local session join."
      />
      <Button type="submit">Save members</Button>
      <span role="alert" ref={err} class={ALERT} />
    </form>
  );
}

/** The turn sheet's form: who starts with the turn and the hand-over rules as JSON
 *  (app.js settingsView's set-turn card). */
function TurnForm({
  info,
  sessionNames,
  err,
  save,
  done,
}: {
  info: ChannelInfo | null;
  sessionNames: string[];
  err: RefObject<HTMLSpanElement | null>;
  save: Save;
  done: () => void;
}): JSX.Element {
  const c = (info?.config ?? {}) as SettingsConfig;
  const orchOptions = [...new Set([...(c.members ?? []), ...sessionNames])].filter((x) => !x.includes("."));
  return (
    <form
      class="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        const f = e.currentTarget as HTMLFormElement;
        void save(err, () => {
          let handover: Record<string, unknown>;
          const raw = (f.elements.namedItem("st-hand") as HTMLTextAreaElement).value;
          try {
            handover = JSON.parse(raw || "{}") as Record<string, unknown>;
          } catch (x) {
            throw new Error(`The hand-over rules are not valid JSON: ${errText(x)}`);
          }
          const a: Record<string, unknown> = {
            start: (f.elements.namedItem("st-start") as HTMLSelectElement).value,
            handover,
          };
          const orch = f.elements.namedItem("st-orch");
          if (orch) a.orchestrator = (orch as HTMLSelectElement).value || null;
          return a;
        }).then((ok) => {
          if (ok) done();
        });
      }}
    >
      <p class={NOTE}>
        For ping-pong work, where only one session acts at a time. Without a turn, sessions work in parallel,
        ordered by task dependencies.
      </p>
      <div class="flex flex-col gap-1.5">
        <label for="st-start" class="text-sm font-semibold">
          Starts with the turn
        </label>
        <select id="st-start" class="ui-input" name="st-start">
          {startOptions(sessionNames, c.start ?? "").map((o) => (
            <option key={o.value} value={o.value} selected={o.selected}>
              {o.label}
            </option>
          ))}
        </select>
      </div>
      <div class="flex flex-col gap-1.5">
        <label for="st-hand" class="text-sm font-semibold">
          Hand-over rules
        </label>
        <textarea id="st-hand" class="ui-input ui-mono" name="st-hand" rows={4} spellcheck={false}>
          {JSON.stringify(c.handover ?? {}, null, 2)}
        </textarea>
        <p class={HINT}>
          JSON: <code>{`{"api": {"to": "web", "topics": ["build.ready"]}}`}</code> passes the turn from api to
          web when api publishes build.ready.
        </p>
      </div>
      <OrchField info={info} options={orchOptions} current={c.orchestrator} />
      <Button type="submit">Save turn</Button>
      <span role="alert" ref={err} class={ALERT} />
    </form>
  );
}

/** The repo sheet's form: the folder Huddle reads for the Code, Drift and Diagrams views
 *  (app.js settingsView's set-repo card). */
function RepoForm({
  info,
  err,
  save,
  done,
}: {
  info: ChannelInfo | null;
  err: RefObject<HTMLSpanElement | null>;
  save: Save;
  done: () => void;
}): JSX.Element {
  const c = (info?.config ?? {}) as SettingsConfig;
  const views = info?.views ?? [];
  const [repo, setRepo] = useState(c.repo ?? "");
  const [profile, setProfile] = useState(c.profile ?? "");
  const hint =
    "A folder Huddle can read. It adds Code, Drift and Diagrams under Work. " +
    (views.length ? `Available now: ${views.join(", ")}.` : c.repo ? "Huddle cannot read this path." : "");
  return (
    <form
      class="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        void save(err, () => ({ repo: repo.trim(), profile: profile.trim() })).then((ok) => {
          if (ok) done();
        });
      }}
    >
      <Field
        label="Repo path"
        value={repo}
        onInput={setRepo}
        mono
        placeholder="/absolute/path/to/repo"
        hint={hint}
      />
      <Field label="Profile" value={profile} onInput={setProfile} />
      <Button type="submit">Save repo</Button>
      <span role="alert" ref={err} class={ALERT} />
    </form>
  );
}

/** The four group sheets. Each form mounts only while its sheet is open, so its fields re-seed
 *  from the channel row every time the sheet opens; a refusal lands in the sheet that raised it. */
function Sheets({
  open,
  close,
  info,
  sessionNames,
  save,
}: {
  open: null | "about" | "members" | "turn" | "repo";
  close: () => void;
  info: ChannelInfo | null;
  sessionNames: string[];
  save: Save;
}): JSX.Element {
  const errAbout = useRef<HTMLSpanElement>(null);
  const errMembers = useRef<HTMLSpanElement>(null);
  const errTurn = useRef<HTMLSpanElement>(null);
  const errRepo = useRef<HTMLSpanElement>(null);
  return (
    <>
      <Sheet open={open === "about"} onClose={close} title="About">
        {open === "about" ? <AboutForm info={info} err={errAbout} save={save} done={close} /> : null}
      </Sheet>
      <Sheet open={open === "members"} onClose={close} title="Members">
        {open === "members" ? <MembersForm info={info} err={errMembers} save={save} done={close} /> : null}
      </Sheet>
      <Sheet open={open === "turn"} onClose={close} title="Turn">
        {open === "turn" ? (
          <TurnForm info={info} sessionNames={sessionNames} err={errTurn} save={save} done={close} />
        ) : null}
      </Sheet>
      <Sheet open={open === "repo"} onClose={close} title="Repository">
        {open === "repo" ? <RepoForm info={info} err={errRepo} save={save} done={close} /> : null}
      </Sheet>
    </>
  );
}

/** The channel's settings page. Renders the channel row the store holds — the live stream
 *  refreshes it, and the page re-renders with it; only a save reads the row again, so the forms
 *  never overwrite other people's changes with a stale copy. Saves go through op/configure;
 *  refusals are said in the sheet they came from. */
export function Settings({
  store,
}: {
  /** The store, when the shell hands it over: a save reads the channel row through it. */
  store?: HuddleStore | undefined;
}): JSX.Element | null {
  const { state, api, toast, registry } = useHuddle();
  const ch = state.ch;
  const info = state.info;
  const [open, setOpen] = useState<null | "about" | "members" | "turn" | "repo">(null);

  const reload = useCallback((): Promise<void> => store?.loadInfo() ?? Promise.resolve(), [store]);

  if (!ch) return null;

  const save: Save = async (err, build) => {
    if (err.current) err.current.textContent = "";
    let a: Record<string, unknown>;
    try {
      a = build();
    } catch (x) {
      if (err.current) err.current.textContent = errText(x);
      return false;
    }
    try {
      await api.op(ch, "configure", a);
      toast("Saved");
      await reload();
      return true;
    } catch (x) {
      if (err.current) err.current.textContent = errText(x);
      return false;
    }
  };
  const sessionNames = (state.sessions?.sessions ?? []).map((s) => s.name);
  const c = (info?.config ?? {}) as SettingsConfig;
  const members = c.members ?? [];
  // a member's second line comes from its live session's registry row, but only while the
  // session is actually on the channel's roster
  const roster = state.sessions?.sessions ?? [];
  const memberLine = (m: string): string | undefined => {
    if (!roster.some((s) => s.name === m)) return undefined;
    const row = registry.getState().sessions?.find((s) => s.channel === ch && s.member === m);
    const line = [row?.branch, row?.machine].filter(Boolean).join(" · ");
    return line || undefined;
  };
  const show =
    (which: "about" | "members" | "turn" | "repo"): (() => void) =>
    () =>
      setOpen(which);
  const close = (): void => setOpen(null);
  const reset = (): void => {
    try {
      for (const k of Object.keys(localStorage)) if (RESET.test(k)) localStorage.removeItem(k);
    } catch {
      // site data is blocked: nothing to reset
    }
    toast("Filters reset");
  };

  return (
    <div class="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-4">
      <Section id="about" heading="About">
        <List label="About">
          <ListItem
            title="Name"
            meta={c.title || info?.name || ch}
            onClick={show("about")}
            trailing={CHEVRON}
          />
          <ListItem
            title="Description"
            meta={c.description || "None"}
            onClick={show("about")}
            trailing={CHEVRON}
          />
        </List>
      </Section>
      <Section id="members" heading="Members">
        <List label="Members">
          {members.length ? (
            members.map((m) => (
              <ListItem key={m} title={m} meta={memberLine(m)} onClick={show("members")} trailing={CHEVRON} />
            ))
          ) : (
            <ListItem title="Any local session" onClick={show("members")} trailing={CHEVRON} />
          )}
        </List>
      </Section>
      <Section id="turn" heading="Turn">
        <List label="Turn">
          <ListItem
            title="Turn order"
            meta={c.start || "Free (parallel)"}
            onClick={show("turn")}
            trailing={CHEVRON}
          />
          <ListItem
            title="Orchestrator"
            meta={typeof c.orchestrator === "string" && c.orchestrator ? c.orchestrator : "None"}
            onClick={show("turn")}
            trailing={CHEVRON}
          />
        </List>
      </Section>
      <Section id="repo" heading="Repository">
        <List label="Repository">
          <ListItem title="Path" meta={c.repo || "Not set"} onClick={show("repo")} trailing={CHEVRON} />
          <ListItem title="Profile" meta={c.profile || "Not set"} onClick={show("repo")} trailing={CHEVRON} />
        </List>
      </Section>
      <LinkedSessions channel={ch} />
      <Section id="browser" heading="This browser">
        <List label="This browser">
          <ListItem title="Reset filters" onClick={reset} />
        </List>
        <p class={NOTE}>Clears the filters and views this browser keeps for this channel.</p>
      </Section>
      <Section id="export" heading="Export">
        <List label="Export">
          {EXPORTS.map(([p, t, d]) => (
            <li key={p} class="ui-list-item">
              <a
                class="ui-list-row cursor-pointer hover:bg-[var(--ui-surface-2)]"
                href={api.channelPath(ch, p)}
                target="_blank"
                rel="noopener"
              >
                <span class="ui-list-text">
                  <span class="ui-list-title">{t}</span>
                  <span class="ui-list-meta">{d}</span>
                </span>
                <span class="ui-list-trailing">
                  <Icon name="ext" class={QUIET_ICON} />
                </span>
              </a>
            </li>
          ))}
        </List>
      </Section>
      <div id="set-ext" class="flex flex-col gap-4 empty:hidden" />
      <ConnectCard ch={ch} />
      <Sheets open={open} close={close} info={info} sessionNames={sessionNames} save={save} />
    </div>
  );
}
