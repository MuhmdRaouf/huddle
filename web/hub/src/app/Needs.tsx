// Needs.tsx — the owner's actions on a needs-you item (SPEC 6.2, 6.3): allow once and deny on a
// permission ask, the passkey sheet a 401 "step-up required" answer opens, the answer composer
// with its 4000-character counter and its delivery-route line, send to many, and snooze and
// dismiss with an optimistic update that rolls back. Every call goes through the API client; the
// item list lives in the registry store, whose needs.item rows keep it in step with the server.
import { Button } from "@huddle/ui/button.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import type { ToastFn } from "@huddle/ui/toast.tsx";
import type { ComponentChildren, JSX } from "preact";
import { useRef, useState } from "preact/hooks";
import type { Api, ApiError } from "../api.ts";
import { Icon } from "../icons.tsx";
import { assertionFromJSON, credentialToJSON } from "../passkey.ts";
import {
  ageText,
  type NeedItem,
  projectName,
  type RegistrySession,
  type RegistryStore,
  sessionName,
} from "../registry.ts";
import {
  type AskActionId,
  type AskActions,
  type AskButton,
  type AskLink,
  askActionsOf,
  askTitleOf,
} from "./ask/actions.ts";
import { AskAnswers } from "./ask/answers.tsx";
import { AskPreview } from "./ask/preview.tsx";
import { type AskQuestion, questionsOf } from "./ask/questions.ts";
import { errText, useHuddle } from "./context.tsx";
import type { Layout } from "./layout.ts";

/** The spans a snooze offers (SPEC 6.3): a quarter, an hour, tomorrow at nine. */
export type SnoozeSpan = "15m" | "1h" | "tomorrow";

/** What the passkey sheet confirms: the act's name, and the facts a row names when the caller
 *  knows them — a row without a value stays out of the sheet. */
export type StepUpWhat = {
  action: string;
  command?: string | undefined;
  session?: string | undefined;
  machine?: string | undefined;
};

/** The line under a composer for one session's route: a push bridge reads "Delivers now",
 *  anything else (and an unknown route) reads "Delivers on the next prompt". */
export function routeLine(route: string | undefined): string {
  return route === "now" ? "Delivers now" : "Delivers on the next prompt";
}

/** The same line for several sessions: the send delivers now only when every target would. */
export function routesLine(routes: readonly (string | undefined)[]): string {
  return routes.length > 0 && routes.every((r) => r === "now")
    ? "Delivers now"
    : "Delivers on the next prompt";
}

/** What one decision needs: the ask, the answer, and the passkey check a 401 opens (with the
 *  facts the sheet shows, when the caller knows them). */
type DecideDeps = {
  api: Api;
  registry: RegistryStore;
  toast: ToastFn;
  id: number;
  decision: "allow" | "deny";
  what?: StepUpWhat | undefined;
  answers?: Record<string, string> | undefined;
  stepUp: (what?: StepUpWhat) => Promise<boolean>;
};

const postDecision = (
  api: Api,
  id: number,
  decision: string,
  answers?: Record<string, string>,
): Promise<unknown> =>
  api.api(`/api/needs/${id}/decide`, {
    body: answers === undefined ? { decision } : { decision, answers },
  });

/** The ask that left the list once its decision stood; the stream restates the session's row. */
function decided(registry: RegistryStore, id: number): void {
  registry.removeNeed(id);
}

/** One decision on a permission ask (SPEC 6.2): posted at once; on a 401 the passkey sheet runs
 *  first and the decision retries once. An ask the terminal answered first leaves the list with
 *  a plain word; every other refusal is toasted as the server spelled it. */
export async function decideItem(d: DecideDeps): Promise<void> {
  try {
    await postDecision(d.api, d.id, d.decision, d.answers);
  } catch (e) {
    await decideRetry(e as ApiError, d);
    return;
  }
  decided(d.registry, d.id);
}

async function decideRetry(e: ApiError, d: DecideDeps): Promise<void> {
  if (e.status !== 401 || e.message !== "step-up required") {
    decideRefused(e, d);
    return;
  }
  if (!(await d.stepUp(d.what))) return;
  try {
    await postDecision(d.api, d.id, d.decision, d.answers);
  } catch (e2) {
    decideRefused(e2 as ApiError, d);
    return;
  }
  decided(d.registry, d.id);
}

function decideRefused(e: ApiError, d: DecideDeps): void {
  if (e.status === 409 && e.message.startsWith("already answered")) {
    // the terminal or the app got there first: the ask is gone, not failed
    d.registry.removeNeed(d.id);
    d.toast(e.message);
    return;
  }
  d.toast(e.message, { bad: true });
}

/** The snooze's wake time as the server would store it: a quarter, an hour, or tomorrow at nine
 *  in this browser's own morning. */
function snoozeUntil(span: SnoozeSpan, now: number): string {
  const d = new Date(now);
  if (span === "15m") d.setMinutes(d.getMinutes() + 15);
  else if (span === "1h") d.setHours(d.getHours() + 1);
  else {
    d.setDate(d.getDate() + 1);
    d.setHours(9, 0, 0, 0);
  }
  return d.toISOString();
}

/** The quiet ink of a hint or a label. */
const MUTED = "text-[var(--ui-muted)]";

/** One fact row of the confirmation box: its name, and the value the caller knew. */
function Fact({ term, value }: { term: string; value: string | undefined }): JSX.Element | null {
  if (!value) return null;
  return (
    <>
      <dt class={MUTED}>{term}</dt>
      <dd
        class={`m-0 min-w-0 ${term === "Command" ? "ui-mono truncate text-[length:var(--ui-text-sm)]" : ""}`}
      >
        {value}
      </dd>
    </>
  );
}

/** The passkey sheet the dangerous actions open (SPEC 7.4): it runs the ceremony's two calls —
 *  the options this browser would sign, then the assertion it hands back — and resolves the
 *  waiting action once the server stamps the session. A prompt the owner dismissed keeps the
 *  sheet open for another try; a refused assertion is said as the server spelled it. */
export function StepUpSheet({
  open,
  onClose,
  onDone,
  what,
}: {
  open: boolean;
  onClose: () => void;
  onDone: () => void;
  what?: StepUpWhat | undefined;
}): JSX.Element {
  const { api, toast } = useHuddle();
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");
  const run = async (): Promise<void> => {
    setBusy(true);
    setNote("");
    try {
      const r = (await api.api("/api/stepup/begin", { body: {} })) as { options?: unknown };
      let cred: PublicKeyCredential;
      try {
        cred = (await navigator.credentials.get({
          publicKey: assertionFromJSON(r.options).publicKey,
        })) as PublicKeyCredential;
      } catch (e) {
        // the owner dismissed the browser's prompt: nothing reached the server, so the same
        // ceremony can run again as it stands
        if ((e as { name?: string }).name === "NotAllowedError") {
          setNote("The passkey was not used. Try again.");
          setBusy(false);
          return;
        }
        throw e;
      }
      await api.api("/api/stepup/finish", { body: { response: credentialToJSON(cred) } });
      onDone();
    } catch (e) {
      toast(errText(e), { bad: true });
      setBusy(false);
    }
  };
  return (
    <Dialog open={open} onClose={onClose} title="Confirm with your passkey">
      <div class="flex flex-col gap-4 px-4" data-stepup="">
        <p class={`m-0 text-center text-sm ${MUTED}`}>Huddle checks it is you before it allows this.</p>
        {what ? (
          <dl
            class="m-0 grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-3.5 gap-y-2 rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface-2)] px-3.5 py-3 text-sm"
            data-stepup-what=""
          >
            <Fact term="Action" value={what.action} />
            <Fact term="Command" value={what.command} />
            <Fact term="Session" value={what.session} />
            <Fact term="Machine" value={what.machine} />
          </dl>
        ) : null}
        {note ? (
          <p role="alert" class="m-0 text-sm text-[var(--ui-fail-ink)]">
            {note}
          </p>
        ) : null}
        <Button size="lg" disabled={busy} onClick={() => void run()}>
          <Icon name="key" class="size-4" />
          Use passkey
        </Button>
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <p class={`m-0 text-center text-xs ${MUTED}`}>
          Confirming keeps you signed in for actions like this for 5 minutes.
        </p>
      </div>
    </Dialog>
  );
}

/** Asks for the passkey check and renders the sheet beside the caller: `ask` opens it (with the
 *  facts the confirmation names, when the caller knows them) and resolves true only once the
 *  server stamped the session. Every caller renders `sheet` beside its own controls. */
export function useStepUp(): {
  ask: (what?: StepUpWhat) => Promise<boolean>;
  sheet: JSX.Element;
} {
  const waiting = useRef<((passed: boolean) => void) | null>(null);
  const [asking, setAsking] = useState(false);
  const [what, setWhat] = useState<StepUpWhat | undefined>(undefined);
  const ask = (w?: StepUpWhat): Promise<boolean> =>
    new Promise<boolean>((resolve) => {
      waiting.current = resolve;
      setWhat(w);
      setAsking(true);
    });
  const settle = (passed: boolean): void => {
    const resolve = waiting.current;
    waiting.current = null;
    setAsking(false);
    setWhat(undefined);
    resolve?.(passed);
  };
  return {
    ask,
    sheet: (
      <StepUpSheet open={asking} what={what} onClose={() => settle(false)} onDone={() => settle(true)} />
    ),
  };
}

/** Everything an item's card or bar can do with it: the decisions, the spans, and the sheet the
 *  decisions may open — render `sheet` beside the actions that use them. */
export type NeedsAct = {
  allow: (item: NeedItem, what?: StepUpWhat, answers?: Record<string, string>) => void;
  deny: (item: NeedItem) => void;
  snooze: (item: NeedItem, span: SnoozeSpan) => void;
  dismiss: (item: NeedItem) => void;
  sheet: JSX.Element;
};

/** Wires the actions to the API client and the registry store. A snooze and a dismiss push no
 *  event of their own, so they move the item first and reread both lists when the server took
 *  the move — or put it back and say so when it did not, unless the item no longer waits. */
export function useNeedsAct(): NeedsAct {
  const { api, registry, toast, now } = useHuddle();
  const { ask, sheet } = useStepUp();

  const decide = (
    item: NeedItem,
    decision: "allow" | "deny",
    what?: StepUpWhat,
    answers?: Record<string, string>,
  ): void => {
    void decideItem({ api, registry, toast, id: item.id, decision, what, answers, stepUp: ask });
  };

  // a 404 says the item waits no longer (opening its session answers a finished turn): the
  // server's own list is the truth, so it is read again instead of putting the item back
  const rollBack =
    (before: NeedItem[] | null) =>
    (e: unknown): void => {
      if ((e as ApiError).status === 404) {
        void registry.load();
        return;
      }
      registry.setNeeds(before);
      toast(errText(e), { bad: true });
    };
  const restate = (item: NeedItem, over: Partial<NeedItem>): void => {
    registry.applyNeed({ ...item, ...over });
  };

  const snooze = (item: NeedItem, span: SnoozeSpan): void => {
    const before = registry.getState().needs;
    restate(item, { state: "snoozed", snoozed_until: snoozeUntil(span, now) });
    api
      .api(`/api/needs/${item.id}/snooze`, { body: { until: span } })
      .then(() => registry.load())
      .catch(rollBack(before));
  };
  const dismiss = (item: NeedItem): void => {
    const before = registry.getState().needs;
    registry.removeNeed(item.id);
    api
      .api(`/api/needs/${item.id}/dismiss`, { body: {} })
      .then(() => registry.load())
      .catch(rollBack(before));
  };

  return {
    allow: (item, what, answers) => decide(item, "allow", what, answers),
    deny: (item) => decide(item, "deny"),
    snooze,
    dismiss,
    sheet,
  };
}

/** The composer the owner's words go through: one session's answer, or new work to several.
 *  The counter counts characters the way the server does (UTF-16 units, 4000 the most), the
 *  send waits for words, and the route line says how the message would travel. */
export function ActComposer({
  kind,
  targets,
  routes,
  placeholder,
  onClose,
}: {
  kind: "answer" | "send";
  targets: readonly string[];
  routes: readonly (string | undefined)[];
  placeholder: string;
  onClose: () => void;
}): JSX.Element {
  const { api, toast } = useHuddle();
  const { ask, sheet } = useStepUp();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const over = text.length > 4000;
  const dead = busy || text.trim().length === 0 || over;

  const post = (): Promise<unknown> => {
    if (kind === "answer") {
      return api.api(`/api/sessions/${encodeURIComponent(targets[0] ?? "")}/answer`, {
        body: { text },
      });
    }
    return api.api("/api/send", { body: { sessions: [...targets], text } });
  };

  const celebrate = (): void => {
    toast(kind === "answer" ? "Sent" : `Sent to ${targets.length} sessions`);
    onClose();
  };

  // the owner's words are a dangerous act for a paired device (SPEC 7.4): a 401 runs the passkey
  // sheet once and posts again
  const send = async (): Promise<void> => {
    setBusy(true);
    try {
      await post();
    } catch (e) {
      await sendRetry(e as ApiError);
      return;
    }
    celebrate();
  };

  const sendRetry = async (e: ApiError): Promise<void> => {
    if (e.status !== 401 || e.message !== "step-up required") {
      toast(errText(e), { bad: true });
      setBusy(false);
      return;
    }
    const what: StepUpWhat =
      kind === "answer" ? { action: "Reply", session: targets[0] ?? "" } : { action: "Send" };
    if (!(await ask(what))) {
      setBusy(false);
      return;
    }
    try {
      await post();
    } catch (e2) {
      toast(errText(e2), { bad: true });
      setBusy(false);
      return;
    }
    celebrate();
  };

  return (
    <div class="flex flex-col gap-3" data-act-composer="">
      {sheet}
      <textarea
        class="min-h-[5.8667rem] w-full rounded-[var(--ui-radius)] border border-[var(--ui-line-strong)] bg-[var(--ui-surface)] px-3 py-2 text-[length:var(--ui-text-md)] text-[var(--ui-text)] placeholder:text-[var(--ui-muted)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--ui-accent)]"
        rows={3}
        aria-label="Message"
        placeholder={placeholder}
        value={text}
        onInput={(e) => setText(e.currentTarget.value)}
      />
      <div class="flex items-center gap-3">
        <span class={`text-xs tabular-nums ${over ? "text-[var(--ui-fail-ink)]" : MUTED}`} data-counter="">
          {text.length}/4000
        </span>
        <span class="flex-1" />
        <span class={`text-xs ${MUTED}`} data-route-line="">
          {routesLine(routes)}
        </span>
      </div>
      <div class="flex justify-end gap-2">
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button disabled={dead} onClick={() => void send()}>
          <Icon name="send" class="size-4" />
          Send
        </Button>
      </div>
    </div>
  );
}

/** One entry of the snooze menu: a full tap target that fills on hover and focus. */
const MENU_ITEM =
  "flex min-h-[var(--ui-tap)] items-center rounded-[var(--ui-radius-sm)] px-2.5 text-left text-sm outline-none hover:bg-[var(--ui-surface-2)] focus-visible:bg-[var(--ui-surface-2)]";

/** The snooze and dismiss menu of one item's card: the three spans, then the dismissal. The
 *  trigger is a 44 px square in the card's footer, so the menu opens upward from it. */
export function SnoozeMenu({ item }: { item: NeedItem }): JSX.Element {
  const act = useNeedsAct();
  const [open, setOpen] = useState(false);
  const spans: readonly [SnoozeSpan, string][] = [
    ["15m", "Snooze 15 minutes"],
    ["1h", "Snooze 1 hour"],
    ["tomorrow", "Snooze until tomorrow 09:00"],
  ];
  return (
    <span class="relative inline-flex">
      <button
        type="button"
        aria-label="More actions"
        aria-expanded={open ? "true" : "false"}
        class="ui-btn ui-btn-secondary ui-btn-icon size-11! min-h-11!"
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="more" class="size-4" />
      </button>
      {open ? (
        <div
          role="menu"
          aria-label="Snooze or dismiss"
          class="absolute bottom-full left-0 z-20 mb-1 flex min-w-52 flex-col rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface)] p-1 shadow-lg"
        >
          {spans.map(([span, label]) => (
            <button
              key={span}
              type="button"
              role="menuitem"
              class={`${MENU_ITEM} text-[var(--ui-text)]`}
              onClick={() => {
                setOpen(false);
                act.snooze(item, span);
              }}
            >
              {label}
            </button>
          ))}
          <button
            type="button"
            role="menuitem"
            class={`${MENU_ITEM} text-[var(--ui-fail-ink)]`}
            onClick={() => {
              setOpen(false);
              act.dismiss(item);
            }}
          >
            Dismiss
          </button>
        </div>
      ) : null}
    </span>
  );
}

/** What an ask's code box shows: a Bash ask's command as the terminal runs it, and any other
 *  input, or a Bash input cut short of whole JSON, as the hook sent it. */
export function askInput(item: NeedItem): string {
  const raw = item.input ?? "";
  if (item.tool !== "Bash") return raw;
  try {
    const parsed: unknown = JSON.parse(raw);
    const command = (parsed as { command?: unknown } | null)?.command;
    if (typeof command === "string") return command;
  } catch {
    // a cut or plain input is not JSON: it shows as it came
  }
  return raw;
}

/** Where a permission ask runs, for a session that reports a branch, a machine or both; empty
 *  when it reports neither. */
function whereMeta({ branch, machine }: RegistrySession): string {
  const onMachine = machine ? ` on ${machine}` : "";
  if (branch) return `runs in ${branch}${onMachine}`;
  return machine ? `on ${machine}` : "";
}

/** The parts of a meta line, in order, with the empty ones left out. */
const metaLine = (parts: readonly string[]): string => parts.filter((part) => part !== "").join(" · ");

/** The screen reader's label for one item's ask. */
function askLabel(item: NeedItem, who: string): string {
  if (item.kind === "permission") return `Permission ask on ${who}`;
  if (item.kind === "turn") return `Finished turn on ${who}`;
  return `Ask from ${who}`;
}

/** The icon on a permission ask's title tile. */
function toolIcon(tool: string): "terminal" | "pencil" | "file" | "ext" | "search" | "sparkle" {
  if (tool === "Bash") return "terminal";
  if (tool === "Edit" || tool === "MultiEdit") return "pencil";
  if (tool === "Write") return "file";
  if (tool === "WebFetch") return "ext";
  if (tool === "WebSearch") return "search";
  return "sparkle";
}

/** An AskUserQuestion ask's questions when its input decodes; null keeps the per-tool preview. */
function askOf(item: NeedItem): AskQuestion[] | null {
  if (item.kind !== "permission" || item.tool !== "AskUserQuestion") return null;
  return questionsOf(item.input ?? "");
}

/** A permission ask's heading: a decodable AskUserQuestion counts its questions, anything else
 *  keeps the title. */
function askTitleFor(who: string, questions: AskQuestion[] | null, title: string): string {
  if (questions === null) return title;
  return `${who} asks ${questions.length} question${questions.length === 1 ? "" : "s"}`;
}

/** One button of an item's row, in the tone its kind gives it. */
function ActionButton({
  button,
  size,
  fill,
  run,
}: {
  button: AskButton;
  size: "md" | "lg";
  fill: string | undefined;
  run: (id: AskActionId) => void;
}): JSX.Element {
  return (
    <Button
      variant={button.tone}
      size={size}
      data-act={button.id}
      class={fill}
      onClick={() => run(button.id)}
    >
      {button.id === "allow" ? <Icon name="key" class="size-4" /> : null}
      {button.label}
    </Button>
  );
}

/** The quiet link under an item's buttons. */
function GhostLink({
  link,
  run,
  class: c,
}: {
  link: AskLink;
  run: (id: AskActionId) => void;
  class?: string;
}): JSX.Element {
  return (
    <Button variant="ghost" data-act={link.id} class={c} onClick={() => run(link.id)}>
      {link.label}
    </Button>
  );
}

/** A permission ask's two answers: 48 px and sharing the row on the phone, the wide card's
 *  40 px beside the title. */
function BarActions({
  buttons,
  run,
  wide,
}: {
  buttons: readonly AskButton[];
  run: (id: AskActionId) => void;
  wide: boolean;
}): JSX.Element {
  return (
    <div class={`flex gap-3 ${wide ? "shrink-0" : ""}`}>
      {buttons.map((button) => (
        <ActionButton
          key={button.id}
          button={button}
          size={wide ? "md" : "lg"}
          fill={wide ? undefined : "flex-1"}
          run={run}
        />
      ))}
    </div>
  );
}

/** The facts the passkey sheet names when an allow on this item needs one: the act, the command
 *  it would run, and where it runs. A non-permission ask confirms nothing. */
export function confirmWhat(
  item: NeedItem,
  session: RegistrySession,
  permission: boolean,
): StepUpWhat | undefined {
  if (!permission) return undefined;
  return {
    action: `Allow ${item.tool} once`,
    command: askInput(item) || undefined,
    session: sessionName(session),
    machine: session.machine || undefined,
  };
}

/** The tile a non-permission item wears: its icon on the wash of its tone. */
const ITEM_TILE: Record<string, { icon: "ask" | "check"; wash: string }> = {
  turn: { icon: "check", wash: "bg-[var(--ui-ok-soft)] text-[var(--ui-ok-ink)]" },
  question: { icon: "ask", wash: "bg-[var(--ui-accent-soft)] text-[var(--ui-accent)]" },
};

/** The meta line of a non-permission item: the project, and what a reader of this kind wants to
 *  know next: how long a finished turn has waited, or the branch and wait of a question. */
function itemMeta(item: NeedItem, session: RegistrySession, waited: string): string {
  const project = projectName(session.project);
  if (item.kind === "turn") return metaLine([project, waited === "" ? "" : `${waited} ago`]);
  return metaLine([project, session.branch ?? "", waited === "" ? "" : `waiting ${waited}`]);
}

/** The ask's block in the session page's flow: a card on the surface, 8 px under the header,
 *  with the page's side margins. A tall body scrolls inside it, so the transcript keeps room. */
function AskCard({
  wide,
  label,
  children,
}: {
  wide: boolean;
  label: string;
  children: ComponentChildren;
}): JSX.Element {
  return (
    <section
      data-block="ask"
      data-needs-bar=""
      aria-label={label}
      class={`flex max-h-[60dvh] shrink-0 flex-col overflow-y-auto rounded-[var(--ui-radius)] bg-[var(--ui-surface)] text-[var(--ui-text)] shadow-[var(--ui-shadow-1)] ${
        wide ? "mx-6 mt-2.5 gap-3 px-5 pt-3 pb-2" : "mx-4 mt-2 gap-2 px-4 py-3"
      }`}
    >
      {children}
    </section>
  );
}

/** The card's title row: the tile (the tool's icon on the warn wash, the sparkle on the accent
 *  wash while questions decode), the plain title, and the meta line under it. */
function AskHead({
  tile,
  wash,
  title,
  meta,
}: {
  tile: "ask" | "check" | "sparkle" | ReturnType<typeof toolIcon>;
  wash: string;
  title: string;
  meta: string;
}): JSX.Element {
  return (
    <div class="flex min-w-0 flex-1 items-center gap-3">
      <span class={`flex size-10 shrink-0 items-center justify-center rounded-[var(--ui-radius)] ${wash}`}>
        <Icon name={tile} class="size-5" />
      </span>
      <div class="flex min-w-0 flex-col">
        <h2 class="m-0 text-lg font-semibold leading-[1.3]">{title}</h2>
        {meta === "" ? null : <span class={`text-sm ${MUTED}`}>{meta}</span>}
      </div>
    </div>
  );
}

/** Every item that is not a permission ask: the tile and plain title with the project line, the
 *  item's own words quoted, the input it carries, the kind's two buttons beside the snooze menu,
 *  its note, and its quiet link. A kind with no plain title (a blocked task) heads the box with
 *  its summary. */
function ItemBox({
  item,
  session,
  waited,
  wide,
  actions,
  run,
}: {
  item: NeedItem;
  session: RegistrySession;
  waited: string;
  wide: boolean;
  actions: AskActions;
  run: (id: AskActionId) => void;
}): JSX.Element {
  const title = askTitleOf(item.kind, "");
  const tile = ITEM_TILE[item.kind] ?? ITEM_TILE["question"];
  const input = askInput(item);
  return (
    <>
      <AskHead
        tile={tile?.icon ?? "ask"}
        wash={tile?.wash ?? ""}
        title={title ?? item.summary}
        meta={itemMeta(item, session, waited)}
      />
      {title === null ? null : <p class="m-0 text-[length:var(--ui-text-md)]">{item.summary}</p>}
      {input ? (
        <code class="ui-mono block whitespace-pre-wrap break-words rounded-[var(--ui-radius)] bg-[var(--ui-surface-2)] px-3.5 py-2.5 text-sm text-[var(--ui-text)]">
          {input}
        </code>
      ) : null}
      <div class="flex items-center gap-2.5">
        <SnoozeMenu item={item} />
        {actions.buttons.map((button) => (
          <ActionButton key={button.id} button={button} size={wide ? "md" : "lg"} fill="flex-1" run={run} />
        ))}
      </div>
      {actions.note === null ? null : <p class={`m-0 text-center text-sm ${MUTED}`}>{actions.note}</p>}
      {actions.ghost === null ? null : <GhostLink link={actions.ghost} run={run} class="self-center" />}
    </>
  );
}

/** A permission ask's body. The answer form when the input decodes as questions; otherwise the
 *  per-tool preview with the two answers: under it on the phone, beside the title on the wide
 *  layouts, where Reply instead sits under the preview. */
function PermissionBody({
  item,
  asking,
  head,
  actions,
  wide,
  onSend,
  onDock,
  run,
}: {
  item: NeedItem;
  asking: boolean;
  head: JSX.Element;
  actions: AskActions;
  wide: boolean;
  onSend: (answers: Record<string, string>) => void;
  onDock: () => void;
  run: (id: AskActionId) => void;
}): JSX.Element {
  const ghost =
    actions.ghost === null ? null : <GhostLink link={actions.ghost} run={run} class="self-start" />;
  if (asking) {
    return (
      <>
        {head}
        <div class="min-h-0 overflow-y-auto">
          <AskAnswers item={item} onSend={onSend} onTerminal={onDock} />
        </div>
        {ghost}
      </>
    );
  }
  const preview = (
    <div class="min-h-0 overflow-y-auto">
      <AskPreview tool={item.tool ?? ""} input={item.input ?? ""} truncated={item.truncated} />
    </div>
  );
  const buttons = <BarActions buttons={actions.buttons} run={run} wide={wide} />;
  if (!wide) {
    return (
      <>
        {head}
        {preview}
        {buttons}
      </>
    );
  }
  return (
    <>
      <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
        {head}
        {buttons}
      </div>
      {preview}
      {ghost}
    </>
  );
}

/** The composer's placeholder: a permission ask and a finished turn are replied to, anything
 *  else is answered. */
function replyPlaceholder(item: NeedItem, who: string): string {
  return item.kind === "permission" || item.kind === "turn" ? `Reply to ${who}…` : `Answer ${who}…`;
}

/** What each action id of an item's box does: the decisions, the dismissal, the copy of the
 *  command that resumes the session in a terminal, and the composer for both kinds of reply. */
function askRunner(d: {
  act: NeedsAct;
  item: NeedItem;
  confirm: StepUpWhat | undefined;
  copy: (text: string) => void;
  sessionId: string;
  reply: () => void;
}): (id: AskActionId) => void {
  return (id) => {
    if (id === "deny") d.act.deny(d.item);
    else if (id === "allow") d.act.allow(d.item, d.confirm);
    else if (id === "dismiss") d.act.dismiss(d.item);
    else if (id === "copy-terminal") d.copy(`claude --resume ${d.sessionId}`);
    else d.reply();
  };
}

/** The session page's ask, the page's second block under the header on every layout: a permission
 *  ask with the per-tool preview, Deny and Allow once (and Reply instead on the wide layouts); an
 *  AskUserQuestion ask whose input decodes draws the answer form instead, where Send posts the
 *  allow with the owner's answers and Answer in terminal folds the ask to a one-line row that
 *  reopens it. Every other item keeps its kind's box (a question: Dismiss and Answer; a finished
 *  turn: Mark read and Reply) beside the snooze menu; the composer opens in place. */
export function NeedsBar({
  item,
  session,
  layout,
}: {
  item: NeedItem;
  session: RegistrySession;
  layout: Layout;
}): JSX.Element {
  const act = useNeedsAct();
  const [replying, setReplying] = useState(false);
  const [docked, setDocked] = useState(false);
  const { now, copy } = useHuddle();
  const permission = item.kind === "permission" && item.tool !== "";
  const tool = item.tool ?? "";
  const wide = layout !== "phone";
  const who = sessionName(session);
  const confirm = confirmWhat(item, session, permission);
  const label = askLabel(item, who);
  const actions = askActionsOf(item.kind, layout);
  const askQuestions = askOf(item);
  const asking = askQuestions !== null;
  const title = askTitleFor(who, askQuestions, askTitleOf(item.kind, tool) ?? item.summary);
  const waited = ageText(item.created_at, now);
  const run = askRunner({
    act,
    item,
    confirm,
    copy,
    sessionId: session.session_id,
    reply: () => setReplying(true),
  });
  const composer = (
    <ActComposer
      kind="answer"
      targets={[session.session_id]}
      routes={[session.delivery_route]}
      placeholder={replyPlaceholder(item, who)}
      onClose={() => setReplying(false)}
    />
  );
  if (permission && asking && docked) {
    return (
      <>
        <AskCard wide={wide} label={label}>
          <div class="flex items-center gap-3">
            <p class="m-0 flex-1 text-sm">{`Needs you · ${title}`}</p>
            <Button variant="ghost" onClick={() => setDocked(false)}>
              Review
            </Button>
          </div>
        </AskCard>
        {act.sheet}
      </>
    );
  }
  const meta = permission
    ? metaLine([tool, whereMeta(session), waited === "" ? "" : `waiting ${waited}`])
    : "";
  const head = (
    <AskHead
      tile={asking ? "sparkle" : toolIcon(tool)}
      wash={
        asking
          ? "bg-[var(--ui-accent-soft)] text-[var(--ui-accent)]"
          : "bg-[var(--ui-warn-soft)] text-[var(--ui-warn-ink)]"
      }
      title={title}
      meta={meta}
    />
  );
  return (
    <>
      <AskCard wide={wide} label={label}>
        {replying ? (
          composer
        ) : permission ? (
          <PermissionBody
            item={item}
            asking={asking}
            head={head}
            actions={actions}
            wide={wide}
            onSend={(answers) => act.allow(item, confirm, answers)}
            onDock={() => setDocked(true)}
            run={run}
          />
        ) : (
          <ItemBox item={item} session={session} waited={waited} wide={wide} actions={actions} run={run} />
        )}
      </AskCard>
      {act.sheet}
    </>
  );
}
