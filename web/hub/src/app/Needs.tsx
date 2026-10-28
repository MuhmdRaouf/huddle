// Needs.tsx — the owner's actions on a needs-you item (SPEC 6.2, 6.3): allow once and deny on a
// permission ask, the passkey sheet a 401 "step-up required" answer opens, the answer composer
// with its 4000-character counter and its delivery-route line, send to many, and snooze and
// dismiss with an optimistic update that rolls back. Every call goes through the API client; the
// item list lives in the registry store, whose needs.item rows keep it in step with the server.
import { Button } from "@huddle/ui/button.tsx";
import { Dialog } from "@huddle/ui/dialog.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import type { ToastFn } from "@huddle/ui/toast.tsx";
import type { JSX } from "preact";
import { Fragment } from "preact";
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
import { AskAnswers } from "./ask/answers.tsx";
import { AskPreview, SECTION_LABEL } from "./ask/preview.tsx";
import { type AskQuestion, questionsOf } from "./ask/questions.ts";
import { errText, useHuddle } from "./context.tsx";
import type { Layout } from "./layout.ts";
import { homeShort } from "../radar/shared/repo.ts";

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
      <dd class={`m-0 min-w-0 ${term === "Command" ? "ui-mono truncate text-[13px]" : ""}`}>{value}</dd>
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
      <div class="flex flex-col gap-4" data-stepup="">
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
        class="min-h-[88px] w-full rounded-[var(--ui-radius)] border border-[var(--ui-line-strong)] bg-[var(--ui-surface)] px-3 py-2 text-[length:var(--ui-text-md)] text-[var(--ui-text)] placeholder:text-[var(--ui-muted)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--ui-accent)]"
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

/** Where a permission ask runs and how long it has waited, for a session that reports a branch,
 *  a machine or both; "Runs in" stays out when it reports no branch. */
function whereLine({ branch, machine }: RegistrySession, waited: string): string {
  const onMachine = machine ? ` on ${machine}` : "";
  const where = branch ? `Runs in ${branch}${onMachine}` : `On ${machine}`;
  return `${where}. Waiting ${waited}.`;
}

/** The where-and-waited note, hidden when the session reports neither branch nor machine. */
function WhereNote({ session, waited }: { session: RegistrySession; waited: string }): JSX.Element | null {
  if (!session.branch && !session.machine) return null;
  return <p class={`m-0 text-xs ${MUTED}`}>{whereLine(session, waited)}</p>;
}

/** The screen reader's label for one item's ask. */
function askLabel(item: NeedItem, who: string): string {
  if (item.kind === "permission") return `Permission ask on ${who}`;
  if (item.kind === "turn") return `Finished turn on ${who}`;
  return `Ask from ${who}`;
}

/** The verb the ask's title names for its tool. */
function askWhat(tool: string): string {
  if (tool === "Bash") return "run a command";
  if (tool === "Edit" || tool === "MultiEdit") return "edit a file";
  if (tool === "Write") return "write a file";
  if (tool === "WebFetch") return "fetch a page";
  return `use ${tool}`;
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

/** A non-permission item's facts: its summary and the input it carries, wrapped now that no box
 *  scrolls sideways. */
function ItemFacts({ item }: { item: NeedItem }): JSX.Element {
  const input = askInput(item);
  return (
    <div class="flex min-w-0 flex-col gap-2">
      <h2 class="m-0 text-base font-semibold">{item.summary}</h2>
      {input ? (
        <code class="ui-mono block whitespace-pre-wrap break-words rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface-2)] px-3 py-2.5 text-sm text-[var(--ui-text)]">
          {input}
        </code>
      ) : null}
    </div>
  );
}

/** The key a wide bar's answer names, in the ink of the button it sits on. */
function Key({ k }: { k: string }): JSX.Element {
  return (
    <kbd class="ui-mono rounded-[var(--ui-radius-sm)] border border-current px-1 text-[11px] leading-4">
      {k}
    </kbd>
  );
}

/** A permission ask's two answers: Deny, and Allow once with the key its name carries. */
function BarActions({
  act,
  item,
  what,
  wide,
}: {
  act: NeedsAct;
  item: NeedItem;
  what?: StepUpWhat | undefined;
  wide: boolean;
}): JSX.Element {
  // the phone's sheet raises its targets to 48 px, the inline bar keeps its 44 px
  const size = wide ? "md" : "lg";
  const fill = wide ? undefined : "flex-1";
  return (
    <div class={`flex gap-2.5 ${wide ? "shrink-0" : ""}`}>
      <Button variant="danger" size={size} data-act="deny" class={fill} onClick={() => act.deny(item)}>
        Deny {wide ? <Key k="d" /> : null}
      </Button>
      <Button size={size} data-act="allow" class={fill} onClick={() => act.allow(item, what)}>
        <Icon name="key" class="size-4" />
        Allow once {wide ? <Key k="a" /> : null}
      </Button>
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

/** Every other item's actions: the snooze menu, Dismiss, and the Answer that opens the composer
 *  for an item that asks something. A finished turn asks nothing, so it carries no Answer. */
function ItemActions({
  act,
  item,
  wide,
  onAnswer,
}: {
  act: NeedsAct;
  item: NeedItem;
  wide: boolean;
  onAnswer: () => void;
}): JSX.Element {
  return (
    <div class="flex flex-wrap items-center gap-2">
      <SnoozeMenu item={item} />
      <Button
        variant="secondary"
        size={wide ? "md" : "lg"}
        data-act="dismiss"
        onClick={() => act.dismiss(item)}
      >
        Dismiss
      </Button>
      {item.kind === "turn" ? null : (
        <Button variant="ghost" data-act="reply" onClick={onAnswer}>
          Answer
        </Button>
      )}
    </div>
  );
}

/** The phone sheet's title row: the tool's tile, on the accent wash while the questions decode,
 *  the session's wants-to line at the mock's 17 px / 600, and the ask's meta under it. */
function SheetTitle({
  asking,
  tool,
  title,
  meta,
}: {
  asking: boolean;
  tool: string;
  title: string;
  meta: string;
}): JSX.Element {
  return (
    <span class="flex min-w-0 items-center gap-3">
      <span
        class={`flex size-10 shrink-0 items-center justify-center rounded-[var(--ui-radius)] ${
          asking
            ? "bg-[var(--ui-accent-soft)] text-[var(--ui-accent)]"
            : "bg-[var(--ui-warn-soft)] text-[var(--ui-warn-ink)]"
        }`}
      >
        <Icon name={asking ? "sparkle" : toolIcon(tool)} class="size-5" />
      </span>
      <span class="flex min-w-0 flex-col">
        <span class="text-lg font-semibold leading-[1.2]">{title}</span>
        {meta === "" ? null : <span class={`text-sm ${MUTED}`}>{meta}</span>}
      </span>
    </span>
  );
}

/** The sheet head's meta line: the tool, where the ask runs, and how long it has waited. */
function sheetMeta(item: NeedItem, session: RegistrySession, waited: string): string {
  return [
    item.tool,
    projectName(session.project),
    session.branch ?? "",
    session.machine,
    waited === "" ? "" : `waiting ${waited}`,
  ]
    .filter((part) => part !== "")
    .join(" · ");
}

/** The Where section: the folder and branch the ask runs in, as an inset grouped list. A row
 *  without a value stays out, and a section with no rows never draws. */
function WhereSection({ session }: { session: RegistrySession }): JSX.Element | null {
  const folder = session.cwd ?? session.project;
  const rows = [
    ...(folder ? [{ term: "Folder", value: homeShort(folder) }] : []),
    ...(session.branch ? [{ term: "Branch", value: session.branch }] : []),
  ];
  if (rows.length === 0) return null;
  return (
    <section class="flex flex-col" data-ask-where="">
      <h3 class={`${SECTION_LABEL} mb-2`}>Where</h3>
      <div class="overflow-hidden rounded-[var(--ui-radius)] bg-[var(--ui-surface)] shadow-[var(--ui-shadow-1)]">
        {rows.map(({ term, value }, i) => (
          <Fragment key={term}>
            {i > 0 ? <div class="ml-4 h-px bg-[var(--ui-line)]" aria-hidden="true" /> : null}
            <div class="flex min-h-[var(--ui-tap)] items-center justify-between gap-3 px-4">
              <span class={`shrink-0 text-sm ${MUTED}`}>{term}</span>
              <span class="ui-mono min-w-0 break-words text-right text-[length:var(--ui-text-md)]">
                {value}
              </span>
            </div>
          </Fragment>
        ))}
      </div>
    </section>
  );
}

/** The sheet's body once the meta line is said: the answer sheet when the input decodes, the
 *  per-tool preview with Where and the Deny/Allow footer otherwise. */
function SheetBody({
  act,
  item,
  tool,
  confirm,
  asking,
  session,
  onSend,
  onDock,
  onReply,
}: {
  act: NeedsAct;
  item: NeedItem;
  tool: string;
  confirm: StepUpWhat | undefined;
  asking: boolean;
  session: RegistrySession;
  onSend: (answers: Record<string, string>) => void;
  onDock: () => void;
  onReply: () => void;
}): JSX.Element {
  if (asking) return <AskAnswers item={item} onSend={onSend} onTerminal={onDock} />;
  return (
    <>
      <AskPreview tool={tool} input={item.input ?? ""} truncated={item.truncated} />
      <WhereSection session={session} />
      <footer
        data-needs-foot=""
        class="sticky bottom-0 flex flex-col gap-2.5 border-t border-[var(--ui-line)] bg-[var(--ui-surface)] pt-3"
      >
        <BarActions act={act} item={item} what={confirm} wide={false} />
        <Button variant="ghost" data-act="reply" class="self-center" onClick={onReply}>
          Reply instead
        </Button>
      </footer>
    </>
  );
}

/** The session screen's action bar (SPEC 9): a permission ask as the phone's bottom sheet — the
 *  per-tool preview inside, Deny and Allow once on a footer that stays in reach, Reply instead
 *  under them — and the wide layouts' inline box with the same preview. An AskUserQuestion ask
 *  whose input decodes draws the answer sheet instead of the preview and its buttons: Send posts
 *  the allow with the owner's answers, Answer in terminal docks the ask undecided. Closing the
 *  sheet without deciding docks a one-line row that reopens it. Every other item keeps its
 *  summary with Snooze and Dismiss (and Answer when it asks something); the composer opens in
 *  place. */
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
  const { now } = useHuddle();
  const permission = item.kind === "permission" && item.tool !== "";
  const tool = item.tool ?? "";
  const wide = layout !== "phone";
  const who = sessionName(session);
  const confirm = confirmWhat(item, session, permission);
  const ask = askLabel(item, who);
  const replyWord = permission ? `Reply to ${who}…` : `Answer ${who}…`;
  const title = `${who} wants to ${askWhat(tool)}`;
  const askQuestions = askOf(item);
  const asking = askQuestions !== null;
  const askTitle = askTitleFor(who, askQuestions, title);
  const meta = sheetMeta(item, session, ageText(item.created_at, now));
  const sendAnswers = (answers: Record<string, string>): void => act.allow(item, confirm, answers);
  const panelClass = wide
    ? "mx-4 mb-4 flex shrink-0 flex-col gap-3 rounded-[var(--ui-radius-lg)] border border-[var(--ui-needs-you)] bg-[var(--ui-surface)] p-4 text-[var(--ui-text)] sm:mx-6 sm:mb-5"
    : "flex shrink-0 flex-col gap-3 rounded-t-[var(--ui-radius-lg)] border-t-2 border-[var(--ui-needs-you)] bg-[var(--ui-surface)] px-4 pt-4 pb-7 text-[var(--ui-text)]";
  const composer = (
    <ActComposer
      kind="answer"
      targets={[session.session_id]}
      routes={[session.delivery_route]}
      placeholder={replyWord}
      onClose={() => setReplying(false)}
    />
  );
  if (permission && wide && !(asking && docked)) {
    return (
      <>
        {act.sheet}
        <section data-needs-bar="" aria-label={ask} class={panelClass}>
          {replying ? (
            composer
          ) : asking ? (
            <>
              <h2 class="m-0 text-base font-semibold">{askTitle}</h2>
              <AskAnswers item={item} onSend={sendAnswers} onTerminal={() => setDocked(true)} />
              <Button variant="ghost" data-act="reply" class="self-start" onClick={() => setReplying(true)}>
                Reply instead
              </Button>
            </>
          ) : (
            <>
              {/* the answers sit beside the facts, and under them once the pane leaves the facts
                  less than 240 px */}
              <div class="flex flex-wrap items-start gap-4">
                <div class="flex min-w-0 flex-[1_1_240px] flex-col gap-2">
                  <h2 class="m-0 text-base font-semibold">
                    Allow <code class="ui-mono">{item.tool}</code>?
                  </h2>
                  <AskPreview tool={tool} input={item.input ?? ""} truncated={item.truncated} />
                  <WhereNote session={session} waited={ageText(item.created_at, now)} />
                </div>
                <BarActions act={act} item={item} what={confirm} wide />
              </div>
              <Button variant="ghost" data-act="reply" class="self-start" onClick={() => setReplying(true)}>
                Reply instead
              </Button>
            </>
          )}
        </section>
      </>
    );
  }
  if (permission && !docked) {
    return (
      <>
        {act.sheet}
        <Sheet
          open
          onClose={() => setDocked(true)}
          title={<SheetTitle asking={asking} tool={tool} title={askTitle} meta={meta} />}
        >
          {/* autofocus keeps the sheet's opening focus here instead of the close button, whose
              ring would paint on every open that did not come from a pointer; the outline goes
              with it, since this target only ever takes programmatic focus */}
          <section
            data-needs-sheet=""
            aria-label={ask}
            class="flex flex-col gap-4 focus-visible:outline-none"
            autofocus
            tabindex={-1}
          >
            {replying ? (
              composer
            ) : (
              <SheetBody
                act={act}
                item={item}
                tool={tool}
                confirm={confirm}
                asking={asking}
                session={session}
                onSend={sendAnswers}
                onDock={() => setDocked(true)}
                onReply={() => setReplying(true)}
              />
            )}
          </section>
        </Sheet>
      </>
    );
  }
  if (permission) {
    return (
      <>
        {act.sheet}
        <section
          data-needs-sheet=""
          aria-label={ask}
          class="flex shrink-0 items-center gap-3 rounded-t-[var(--ui-radius-lg)] border-t-2 border-[var(--ui-needs-you)] bg-[var(--ui-surface)] px-4 py-3 text-[var(--ui-text)]"
        >
          <p class="m-0 flex-1 text-sm">{`Needs you · ${askTitle}`}</p>
          <Button variant="ghost" onClick={() => setDocked(false)}>
            Review
          </Button>
        </section>
      </>
    );
  }
  return (
    <>
      {act.sheet}
      <section
        data-needs-sheet={wide ? undefined : ""}
        data-needs-bar={wide ? "" : undefined}
        aria-label={ask}
        class={panelClass}
      >
        {replying ? (
          composer
        ) : (
          <>
            <ItemFacts item={item} />
            <ItemActions act={act} item={item} wide={wide} onAnswer={() => setReplying(true)} />
          </>
        )}
      </section>
    </>
  );
}
