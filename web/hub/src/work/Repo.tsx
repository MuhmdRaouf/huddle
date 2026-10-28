// Repo.tsx — the repo views: the channel's code tree, the drift report (every snippet a task
// quotes, checked against the file now), the PNG diagrams, and any view a local extension
// serves (Markdown reports, tables of rows, plain JSON). The view chips come from the channel's
// declared views; a path under Code browses the tree, a name under Diagrams picks the picture.
// Port of work.js RNAMES/repoView/codeHref/box/repoCode/repoDrift/repoDiagrams/repoGeneric.

import { highlight, langOf, type TokenKind } from "@huddle/ui/code.tsx";
import { Markdown } from "@huddle/ui/markdown.tsx";
import { Panel } from "@huddle/ui/page.tsx";
import { Table, type Column } from "@huddle/ui/table.tsx";
import type { Stamp } from "@huddle/ui/time.ts";
import type { ComponentChild, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { Api } from "../api.ts";
import { errText } from "../app/context.tsx";
import { Icon, type IconName } from "../icons.tsx";
import { Empty, Pill, Skeleton, StatusIcon, Time } from "../kit.tsx";
import type { PlanStep } from "../store.ts";
import { stepState, taskHref } from "./model.ts";

// ── the wire shapes the repo routes answer with ──────────────────────────────

/** One row of a tree listing. */
export type RepoEntry = { path: string; name: string; dir?: boolean | undefined };

/** GET /repo/tree: a folder's entries, or a file's flag with its folder's README. */
export type RepoTree = { entries?: RepoEntry[]; file?: boolean; readme?: string; error?: string };

/** GET /repo/file: a file's text with the total line count, or its binary size. */
export type RepoFile = { text?: string; total?: number; size?: number; binary?: boolean; error?: string };

/** One task that touches a path (GET /repo/refs). */
export type RepoRef = { id: string; title?: string };

/** One drift row: how a task's quoted snippet compares to the file now (GET /repo/drift). */
export type DriftRow = { state: string; step: string; path?: string; score?: number | null };

/** GET /repo/drift: the rows and when they were last checked. */
export type DriftReply = { rows?: DriftRow[]; at?: Stamp };

// ── the pure half: names, picks, orders, cells ───────────────────────────────

/** The built-in views' words; an extension's view capitalises itself. */
const RNAMES: Record<string, string> = { code: "Code", drift: "Drift", diagrams: "Diagrams" };

/** The segmented track the view chips sit on: a quiet fill the picked chip is raised out of. */
const TRACK =
  "flex max-w-full items-center gap-0.5 overflow-x-auto rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] p-[0.2rem]";

/** One view chip: the picked one on the surface with the drop shadow, the others quiet. */
const chip = (on: boolean): string =>
  `inline-flex min-h-[2.4rem] shrink-0 items-center whitespace-nowrap rounded-[calc(var(--ui-radius)-0.2rem)] px-3 text-sm pointer-coarse:min-h-[var(--ui-tap)]${
    on
      ? " bg-[var(--ui-surface)] font-semibold text-[var(--ui-text)] shadow-[var(--ui-shadow-1)]"
      : " font-medium text-[var(--ui-muted)]"
  }`;

/** One row of the file listing: quiet until hovered, the current path held on surface. */
const listingRow = (on: boolean): string =>
  `flex min-h-[2.4rem] items-center gap-2 rounded-[var(--ui-radius-sm)] px-2 text-sm text-[var(--ui-text)] hover:bg-[var(--ui-surface-2)] pointer-coarse:min-h-[var(--ui-tap)]${
    on ? " bg-[var(--ui-surface-2)] font-medium" : ""
  }`;

/** A link in the design's accent, underlined as links read. */
const LINK = "text-[var(--ui-accent)] underline";

/** Around rendered Markdown: its links read in the accent over the page's surfaces. */
const MD = "[&_a]:text-[var(--ui-accent)]";

/** A view's chip word: the built-in's name, or the extension's own, capitalised. */
export function repoLabel(k: string): string {
  return RNAMES[k] ?? (k ? `${k.charAt(0).toUpperCase()}${k.slice(1)}` : k);
}

/** Which view the route names and the path under it: a declared view wins, else the first one
 *  serves, and only a route that named the serving view carries a path (work.js repoView). */
export function repoPick(
  views: readonly string[],
  parts: readonly string[],
): { view: string | null; rest: string } {
  const first = parts[0];
  if (first !== undefined && views.includes(first)) return { view: first, rest: parts.slice(1).join("/") };
  return { view: views[0] ?? null, rest: "" };
}

/** A repo path's link: Code carrying the path, one part per address segment. */
export const codeHref = (ch: string, p: string): string =>
  `#/channels/${ch}/work/repo/code/${p.split("/").filter(Boolean).map(encodeURIComponent).join("/")}`;

/** A repo view's link. */
export const viewHref = (ch: string, v: string, rest = ""): string =>
  `#/channels/${ch}/work/repo/${v}${rest ? `/${rest}` : ""}`;

/** The drift table's order: the rows that no longer match first, the matching ones after. */
export const driftOrdered = (rows: readonly DriftRow[]): DriftRow[] => [
  ...rows.filter((r) => r.state !== "ok"),
  ...rows.filter((r) => r.state === "ok"),
];

/** A drift state as a status pill: its tone and its word. */
export function driftPill(state: string): { status: string; label: string } {
  if (state === "ok") return { status: "done", label: "Matches" };
  if (state === "partial") return { status: "doing", label: "Partly changed" };
  return { status: "blocked", label: state === "missing" ? "File missing" : "Changed" };
}

/** Is this reply one Markdown document per row? An empty one still is — it says so. */
export const isMdDocs = (r: unknown): r is { md: string }[] =>
  Array.isArray(r) && r.every((x) => !!x && typeof x === "object" && typeof x.md === "string");

/** Is this reply a table of row objects? */
export const isRows = (r: unknown): r is Record<string, unknown>[] =>
  Array.isArray(r) && r.length > 0 && r.every((x) => !!x && typeof x === "object");

/** The columns a table shows: the rows' keys in first-seen order, eight at most. */
export function tableKeys(rows: readonly Record<string, unknown>[]): string[] {
  return [...new Set(rows.flatMap((r) => Object.keys(r)))].slice(0, 8);
}

/** One table cell: arrays join, objects clip, scalars go through inline Markdown. */
export function cellNode(x: unknown): ComponentChild {
  if (x === null || x === undefined) return "";
  if (Array.isArray(x)) {
    return x.every((y) => typeof y !== "object") ? x.map((y) => String(y)).join(", ") : `${x.length}`;
  }
  if (typeof x === "object") return JSON.stringify(x).slice(0, 120);
  const scalar = typeof x === "string" ? x : typeof x === "number" || typeof x === "boolean" ? String(x) : "";
  return <Markdown text={scalar} />;
}

// ── the shared frame ─────────────────────────────────────────────────────────

/** A token's colour in a code mockup: the hues the theme scopes per palette. */
const TOKEN_CLASS: Record<TokenKind, string> = {
  kw: "text-[var(--hue-mauve)]",
  str: "text-[var(--hue-green)]",
  num: "text-[var(--hue-peach)]",
  com: "text-[var(--ui-muted)] italic",
  key: "text-[var(--hue-blue)]",
  param: "text-[var(--hue-teal)]",
};

/** A file's text as a code box on the quiet surface: line numbers down the left, the offline
 *  highlighter's tokens over them. */
export function MockupCode({ text, lang }: { text: string; lang: string }): JSX.Element {
  return (
    <div
      data-code
      class="ui-mono overflow-x-auto rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] p-3 text-xs leading-relaxed"
    >
      {highlight(text, lang).map((toks, i) => (
        <pre key={i} data-prefix={String(i + 1)} class="m-0 flex gap-3">
          <span aria-hidden="true" class="w-6 shrink-0 select-none text-right text-[var(--ui-muted)]">
            {i + 1}
          </span>
          <code class="whitespace-pre">
            {toks.map((t, j) =>
              t.kind ? (
                <span key={j} class={TOKEN_CLASS[t.kind]}>
                  {t.text}
                </span>
              ) : (
                t.text
              ),
            )}
          </code>
        </pre>
      ))}
    </div>
  );
}

/** A titled card: an icon and the title in a head row, the body under it, flush bodies touching
 *  the edges (tables, code). The shared Panel is the surface; this only spells its icon. Port of
 *  work.js box. */
export function Box({
  title,
  icon,
  right,
  flush = false,
  children,
}: {
  title: ComponentChild;
  icon: IconName;
  right?: ComponentChild | undefined;
  flush?: boolean | undefined;
  children: ComponentChild;
}): JSX.Element {
  return (
    <Panel
      title={title}
      icon={<Icon name={icon} class="size-4.5" />}
      actions={right}
      flush={flush}
      {...(typeof title === "string" ? { label: title } : {})}
    >
      {children}
    </Panel>
  );
}

/** What every repo view reads: the channel, the services, and the tasks for their chips. */
export type RepoProps = {
  ch: string;
  api: Api;
  /** The current time, for the drift report's checked stamp. */
  now: number;
  /** The route's parts under "repo": the view, then its path. */
  parts: readonly string[];
  /** The views the server serves; empty when the channel has no repo. */
  views: readonly string[];
  /** The repo the channel is pointed at, shown beside the chips. */
  repoPath?: string | undefined;
  /** The board's steps by id, for the tasks that touch a path. */
  byId: Map<string, PlanStep>;
  /** Opens a task's drawer. */
  onOpenTask: (id: string) => void;
};

/** The repo shell: the view chips and the repo path, then the view itself. */
export function Repo(props: RepoProps): JSX.Element {
  const { ch, views, parts } = props;
  const pick = repoPick(views, parts);
  const shared = {
    ch: props.ch,
    api: props.api,
    now: props.now,
    byId: props.byId,
    onOpenTask: props.onOpenTask,
  };
  return (
    <div class="flex min-w-0 flex-col gap-5">
      <nav class="flex flex-wrap items-center gap-3" aria-label="Repo views">
        {/* biome-ignore lint/a11y/useSemanticElements: the view switch's group role */}
        <div class={TRACK} role="group" aria-label="Repo view">
          {views.map((k) => (
            <a
              key={k}
              class={chip(k === pick.view)}
              href={viewHref(ch, k)}
              aria-current={k === pick.view ? "page" : undefined}
            >
              {repoLabel(k)}
            </a>
          ))}
        </div>
        <span class="flex-1" />
        <code class="hidden truncate text-meta text-[var(--ui-muted)] lg:block">{props.repoPath ?? ""}</code>
      </nav>
      {!pick.view ? (
        <Empty
          text="No repo here"
          hint="This channel has no repo to browse. Point one at it in its settings."
          icon="folder"
        />
      ) : pick.view === "code" ? (
        <RepoCode {...shared} path={pick.rest} />
      ) : pick.view === "drift" ? (
        <RepoDrift {...shared} />
      ) : pick.view === "diagrams" ? (
        <RepoDiagrams {...shared} name={pick.rest} />
      ) : (
        <RepoGeneric {...shared} view={pick.view} />
      )}
    </div>
  );
}

/** What the code view assembles once its reads land. */
type CodeState = {
  tree: RepoTree;
  refs: RepoRef[];
  file: RepoFile | null;
  /** The folder the listing shows: the file's parent, or the path itself. */
  dirPath: string;
  path: string;
};

/** The code view's reads: the tree, then the folder's own listing, the tasks that touch the
 *  path and the file's text, all at once (work.js repoCode's Promise.all). */
async function loadCode(api: Api, ch: string, p: string): Promise<CodeState> {
  const t = (await api.api(api.channelPath(ch, `/repo/tree?path=${encodeURIComponent(p)}`))) as RepoTree;
  const dirPath = t.file ? p.split("/").slice(0, -1).join("/") : p;
  const [dir, refs, file] = await Promise.all([
    t.file ? api.api(api.channelPath(ch, `/repo/tree?path=${encodeURIComponent(dirPath)}`)) : t,
    p
      ? api.api(api.channelPath(ch, `/repo/refs?path=${encodeURIComponent(p)}`)).catch(() => [])
      : Promise.resolve([]),
    t.file ? api.api(api.channelPath(ch, `/repo/file?path=${encodeURIComponent(p)}`)) : Promise.resolve(null),
  ]);
  return {
    tree: dir as RepoTree,
    refs: Array.isArray(refs) ? (refs as RepoRef[]) : [],
    file: (file as RepoFile | null) ?? null,
    dirPath,
    path: p,
  };
}

/** The tree listing down the left: Up, then one row per entry, the current path marked. */
function RepoListing({
  ch,
  tree,
  dirPath,
  path,
}: {
  ch: string;
  tree: RepoTree;
  dirPath: string;
  path: string;
}): JSX.Element {
  return (
    <Panel label="Files" flush class="max-h-[70dvh] overflow-y-auto">
      <ul class="flex w-full flex-col gap-0.5 p-1.5">
        {dirPath ? (
          <li>
            <a class={listingRow(false)} href={codeHref(ch, dirPath.split("/").slice(0, -1).join("/"))}>
              <span class="shrink-0 text-[var(--ui-muted)]">
                <Icon name="left" class="size-4" />
              </span>
              <span>Up</span>
            </a>
          </li>
        ) : null}
        {(tree.entries ?? []).map((e) => (
          <li key={e.path}>
            <a
              class={listingRow(e.path === path)}
              href={codeHref(ch, e.path)}
              aria-current={e.path === path ? "page" : undefined}
            >
              <span class="shrink-0 text-[var(--ui-muted)]">
                <Icon name={e.dir ? "folder" : "file"} class="size-4" />
              </span>
              <span class="truncate">{e.name}</span>
            </a>
          </li>
        ))}
      </ul>
    </Panel>
  );
}

/** The tasks that touch the path, as drawer-opening chips; none, no card. */
function RepoRefs({
  api,
  ch,
  refs,
  byId,
  onOpenTask,
}: {
  api: Api;
  ch: string;
  refs: readonly RepoRef[];
  byId: Map<string, PlanStep>;
  onOpenTask: (id: string) => void;
}): JSX.Element | null {
  if (!refs.length) return null;
  return (
    <Box title={`Tasks that touch this (${refs.length})`} icon="link" flush>
      <div class="flex flex-wrap gap-1.5 p-4">
        {refs.slice(0, 80).map((s) => (
          <a
            key={s.id}
            class="ui-badge ui-badge-neutral gap-1.5 hover:underline"
            href={taskHref(api, ch, s.id)}
            onClick={(e) => {
              e.preventDefault();
              onOpenTask(s.id);
            }}
          >
            <StatusIcon status={stepState(byId.get(s.id))} class="size-3.5" />
            <span class="font-mono">{s.id}</span>
            <span class="max-w-48 truncate">{s.title}</span>
          </a>
        ))}
      </div>
    </Box>
  );
}

/** The file's card: highlighted in a code mockup, or its Markdown, or the note a binary or a
 *  failed read deserves. */
function fileMain(f: RepoFile, path: string): ComponentChild {
  if (f.error) return <Empty text={f.error} icon="alert" />;
  if (f.binary) return <Empty text={`A binary file (${f.size ?? 0} bytes).`} icon="file" />;
  if (/\.md$/i.test(path))
    return (
      <Panel label={path}>
        <div class={MD}>
          <Markdown text={f.text ?? ""} />
        </div>
      </Panel>
    );
  return (
    <Box
      title={path.split("/").at(-1) ?? path}
      icon="code"
      flush
      right={
        <span class="text-meta text-[var(--ui-muted)] tnum">{(f.text ?? "").split("\n").length} lines</span>
      }
    >
      <MockupCode text={f.text ?? ""} lang={langOf(path)} />
    </Box>
  );
}

/** The right side under the refs: the file, else the folder's README, else the note there is
 *  none. */
function codeMain(st: CodeState): ComponentChild {
  if (st.file) return fileMain(st.file, st.path);
  if (st.tree.readme)
    return (
      <Box title="README" icon="book">
        <div class={MD}>
          <Markdown text={st.tree.readme} />
        </div>
      </Box>
    );
  return <Empty text="No README in this folder." icon="book" />;
}

/** The code view: the tree down the left, the tasks that touch the path and the file (or the
 *  folder's README) on the right. Port of work.js repoCode. */
export function RepoCode({
  ch,
  api,
  path,
  byId,
  onOpenTask,
}: {
  ch: string;
  api: Api;
  path: string;
  byId: Map<string, PlanStep>;
  onOpenTask: (id: string) => void;
}): JSX.Element {
  const p = path.replace(/^\/+|\/+$/g, "");
  const [st, setSt] = useState<CodeState | undefined>(undefined);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setSt(undefined);
    setErr(null);
    loadCode(api, ch, p)
      .then((x) => {
        if (alive) setSt(x);
      })
      .catch((e: unknown) => {
        if (alive) setErr(errText(e));
      });
    return () => {
      alive = false;
    };
  }, [api, ch, p]);

  if (err)
    return (
      <div class="pt-2">
        <Empty text={err} icon="alert" />
      </div>
    );
  if (st === undefined) return <Skeleton rows={6} />;

  const parts = st.path ? st.path.split("/") : [];
  const crumbs = (
    <nav class="flex flex-wrap items-center gap-1 font-mono text-sm text-[var(--ui-muted)]" aria-label="Path">
      <a class={LINK} href={codeHref(ch, "")}>
        repo
      </a>
      {parts.map((x, i) => (
        <span key={`${x}/${i}`}>
          {" / "}
          <a class={LINK} href={codeHref(ch, parts.slice(0, i + 1).join("/"))}>
            {x}
          </a>
        </span>
      ))}
    </nav>
  );
  if (st.tree.error) {
    return (
      <>
        {crumbs}
        <Empty text={st.tree.error} icon="alert" />
      </>
    );
  }
  return (
    <>
      {crumbs}
      <div class="grid items-start gap-5 lg:grid-cols-[18.6667rem_minmax(0,1fr)]">
        <RepoListing ch={ch} tree={st.tree} dirPath={st.dirPath} path={st.path} />
        <div class="flex min-w-0 flex-col gap-5">
          <RepoRefs api={api} ch={ch} refs={st.refs} byId={byId} onOpenTask={onOpenTask} />
          {codeMain(st)}
        </div>
      </div>
    </>
  );
}

/** The drift report: every snippet a task quotes, checked against the file now; the ones that no
 *  longer match come first. Port of work.js repoDrift. */
export function RepoDrift({
  ch,
  api,
  now,
  onOpenTask,
}: {
  ch: string;
  api: Api;
  now: number;
  byId: Map<string, PlanStep>;
  onOpenTask: (id: string) => void;
}): JSX.Element {
  const [d, setD] = useState<DriftReply | undefined>(undefined);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    setD(undefined);
    setErr(null);
    api
      .api(api.channelPath(ch, "/repo/drift"))
      .then((r) => {
        if (alive) setD((r as DriftReply) ?? {});
      })
      .catch((e: unknown) => {
        if (alive) setErr(errText(e));
      });
    return () => {
      alive = false;
    };
  }, [api, ch]);
  if (err)
    return (
      <div class="pt-2">
        <Empty text={err} icon="alert" />
      </div>
    );
  if (d === undefined) return <Skeleton rows={6} />;
  const rows = d.rows ?? [];
  const bad = rows.filter((r) => r.state !== "ok").length;
  return (
    <div class="flex min-w-0 flex-col gap-5">
      <p class="max-w-[72ch] text-[0.9375rem] text-[var(--ui-muted)]">
        Every code snippet a task quotes from a file, checked against the file now.{" "}
        {bad ? `${bad} of ${rows.length} no longer match.` : rows.length ? `All ${rows.length} match.` : ""}{" "}
        Checked <Time ts={d.at} now={now} />.
      </p>
      <Box title="Snippet drift" icon="code" flush>
        <Table
          label="Drift rows"
          columns={[
            {
              key: "state",
              label: "State",
              cell: (r) => {
                const pill = driftPill(r.state);
                return <Pill status={pill.status} label={pill.label} />;
              },
            },
            {
              key: "step",
              label: "Task",
              mono: true,
              cell: (r) => (
                <a
                  class={LINK}
                  href={taskHref(api, ch, r.step)}
                  onClick={(e) => {
                    e.preventDefault();
                    onOpenTask(r.step);
                  }}
                >
                  {r.step}
                </a>
              ),
            },
            {
              key: "path",
              label: "File",
              mono: true,
              cell: (r) => (
                <a class={LINK} href={codeHref(ch, String(r.path ?? "").replace(/:\d.*$/, ""))}>
                  {r.path}
                </a>
              ),
            },
            {
              key: "score",
              label: "Lines kept",
              align: "end",
              cell: (r) => (r.score === null || r.score === undefined ? "" : `${r.score}%`),
            },
          ]}
          rows={driftOrdered(rows)}
          rowKey={(r) => `${r.step}/${r.path}`}
          empty="No snippet quotes a file."
        />
      </Box>
    </div>
  );
}

/** The diagrams: one chip per PNG in docs/architecture, the picked one below it. Port of
 *  work.js repoDiagrams. */
export function RepoDiagrams({
  ch,
  api,
  name,
}: {
  ch: string;
  api: Api;
  name: string;
  byId: Map<string, PlanStep>;
  onOpenTask: (id: string) => void;
  now: number;
}): JSX.Element {
  const [ds, setDs] = useState<string[] | undefined>(undefined);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    setDs(undefined);
    setErr(null);
    api
      .api(api.channelPath(ch, "/repo/diagrams"))
      .then((r) => {
        if (alive) setDs(Array.isArray(r) ? (r as string[]) : []);
      })
      .catch((e: unknown) => {
        if (alive) setErr(errText(e));
      });
    return () => {
      alive = false;
    };
  }, [api, ch]);
  if (err)
    return (
      <div class="pt-2">
        <Empty text={err} icon="alert" />
      </div>
    );
  if (ds === undefined) return <Skeleton rows={6} />;
  if (!ds.length)
    return (
      <Empty
        text="No PNG diagrams in docs/architecture."
        hint="Drop .png files there and they show up here."
        icon="ext"
      />
    );
  const cur = name && ds.includes(name) ? name : (ds[0] ?? "");
  const label = (x: string): string => x.replace(/\.png$/, "").replace(/-/g, " ");
  return (
    <div class="flex min-w-0 flex-col gap-5">
      {/* biome-ignore lint/a11y/useSemanticElements: the chip group's role */}
      <div class={TRACK} role="group" aria-label="Diagrams">
        {ds.map((x) => (
          <a
            key={x}
            class={chip(x === cur)}
            href={viewHref(ch, "diagrams", encodeURIComponent(x))}
            aria-current={x === cur ? "page" : undefined}
          >
            {label(x)}
          </a>
        ))}
      </div>
      <Panel label="Diagram" class="overflow-auto">
        <img
          class="mx-auto min-w-0 max-w-full"
          src={api.channelPath(ch, `/diagram?name=${encodeURIComponent(cur)}`)}
          alt={`Diagram: ${label(cur)}`}
        />
      </Panel>
      <p class="text-meta text-[var(--ui-muted)]">
        From <code>docs/architecture/{cur}</code>.
      </p>
    </div>
  );
}

/** A view from a local extension: one Markdown document per row, a table of rows, or plain JSON.
 *  Port of work.js repoGeneric. */
export function RepoGeneric({
  ch,
  api,
  view,
}: {
  ch: string;
  api: Api;
  view: string;
  byId: Map<string, PlanStep>;
  onOpenTask: (id: string) => void;
  now: number;
}): JSX.Element {
  const [r, setR] = useState<unknown>(undefined);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    setR(undefined);
    setErr(null);
    api
      .api(api.channelPath(ch, `/repo/${encodeURIComponent(view)}`))
      .then((x) => {
        if (alive) setR(x);
      })
      .catch((e: unknown) => {
        if (alive) setErr(errText(e));
      });
    return () => {
      alive = false;
    };
  }, [api, ch, view]);
  if (err)
    return (
      <div class="pt-2">
        <Empty text={err} icon="alert" />
      </div>
    );
  if (r === undefined) return <Skeleton rows={6} />;
  const refused =
    r !== null && typeof r === "object" && !Array.isArray(r) && "error" in r
      ? String((r as { error: unknown }).error)
      : null;
  if (refused)
    return (
      <div class="pt-2">
        <Empty text={refused} icon="alert" />
      </div>
    );
  if (isMdDocs(r))
    return (
      <div class="flex min-w-0 flex-col gap-5">
        {r.map((x, i) => (
          <Panel key={i} label={`${repoLabel(view)} document ${i + 1}`}>
            <div class={MD}>
              <Markdown text={x.md} />
            </div>
          </Panel>
        ))}
        {!r.length ? <Empty text="Nothing here." icon="file" /> : null}
      </div>
    );
  if (isRows(r)) {
    const keys = tableKeys(r);
    type Keyed = { x: Record<string, unknown>; i: number };
    const keyed = r.map((x, i) => ({ x, i }));
    const cols: Column<Keyed>[] = keys.map((k) => ({
      key: k,
      label: k,
      cell: (row) => cellNode(row.x[k]),
    }));
    return (
      <Panel label={repoLabel(view)} flush class="min-w-0">
        <Table label={repoLabel(view)} columns={cols} rows={keyed} rowKey={(row) => String(row.i)} />
      </Panel>
    );
  }
  return (
    <Box title={repoLabel(view)} icon="code" flush>
      <MockupCode text={JSON.stringify(r ?? null, null, 2)} lang="json" />
    </Box>
  );
}
