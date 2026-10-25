// Needs.tsx — the owner's actions on a needs-you item (SPEC 6.2, 6.3): allow once and deny on a
// permission ask, the passkey sheet a 401 "step-up required" answer opens, the answer composer
// with its 4000-character counter and its delivery-route line, send to many, and snooze and
// dismiss with an optimistic update that rolls back. Every call goes through the API client; the
// item list lives in the registry store, whose needs.item rows keep it in step with the server.
import { Dialog } from "@huddle/ui/dialog.tsx";
import type { ToastFn } from "@huddle/ui/toast.tsx";
import type { JSX } from "preact";
import { useRef, useState } from "preact/hooks";
import type { Api, ApiError } from "../api.ts";
import { Icon } from "../icons.tsx";
import { assertionFromJSON, credentialToJSON } from "../passkey.ts";
import {
  ageText,
  type NeedItem,
  type RegistrySession,
  type RegistryStore,
  sessionName,
} from "../registry.ts";
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
  stepUp: (what?: StepUpWhat) => Promise<boolean>;
};

const postDecision = (api: Api, id: number, decision: string): Promise<unknown> =>
  api.api(`/api/needs/${id}/decide`, { body: { decision } });

/** The ask that left the list once its decision stood; the stream restates the session's row. */
function decided(registry: RegistryStore, id: number): void {
  registry.removeNeed(id);
}

/** One decision on a permission ask (SPEC 6.2): posted at once; on a 401 the passkey sheet runs
 *  first and the decision retries once. An ask the terminal answered first leaves the list with
 *  a plain word; every other refusal is toasted as the server spelled it. */
export async function decideItem(d: DecideDeps): Promise<void> {
  try {
    await postDecision(d.api, d.id, d.decision);
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
    await postDecision(d.api, d.id, d.decision);
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

/** One fact row of the confirmation box: its name, and the value the caller knew. */
function Fact({ term, value }: { term: string; value: string | undefined }): JSX.Element | null {
  if (!value) return null;
  return (
    <>
      <dt class="muted">{term}</dt>
      <dd class={`m-0 min-w-0 ${term === "Command" ? "truncate font-mono text-[13px]" : ""}`}>{value}</dd>
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
        <p class="m-0 text-center text-sm muted">Huddle checks it is you before it allows this.</p>
        {what ? (
          <dl
            class="m-0 grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-3.5 gap-y-2 rounded-xl border hairline bg-base-300 px-3.5 py-3 text-sm"
            data-stepup-what=""
          >
            <Fact term="Action" value={what.action} />
            <Fact term="Command" value={what.command} />
            <Fact term="Session" value={what.session} />
            <Fact term="Machine" value={what.machine} />
          </dl>
        ) : null}
        {note ? (
          <p role="alert" class="m-0 text-sm text-error">
            {note}
          </p>
        ) : null}
        <button type="button" class="btn btn-primary min-h-13" disabled={busy} onClick={() => void run()}>
          <Icon name="key" class="size-4" />
          Use passkey
        </button>
        <button type="button" class="btn btn-ghost" onClick={onClose}>
          Cancel
        </button>
        <p class="m-0 text-center text-xs muted">
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
  allow: (item: NeedItem, what?: StepUpWhat) => void;
  deny: (item: NeedItem) => void;
  snooze: (item: NeedItem, span: SnoozeSpan) => void;
  dismiss: (item: NeedItem) => void;
  sheet: JSX.Element;
};

/** Wires the actions to the API client and the registry store. A snooze and a dismiss push no
 *  event of their own, so they move the item first and reread both lists when the server took
 *  the move — or put it back and say so when it did not. */
export function useNeedsAct(): NeedsAct {
  const { api, registry, toast, now } = useHuddle();
  const { ask, sheet } = useStepUp();

  const decide = (item: NeedItem, decision: "allow" | "deny", what?: StepUpWhat): void => {
    void decideItem({ api, registry, toast, id: item.id, decision, what, stepUp: ask });
  };

  const rollBack =
    (before: NeedItem[] | null) =>
    (e: unknown): void => {
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
    allow: (item, what) => decide(item, "allow", what),
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
        class="textarea"
        rows={3}
        aria-label="Message"
        placeholder={placeholder}
        value={text}
        onInput={(e) => setText(e.currentTarget.value)}
      />
      <div class="flex items-center gap-3">
        <span class={`text-xs tnum ${over ? "text-error" : "muted"}`} data-counter="">
          {text.length}/4000
        </span>
        <span class="flex-1" />
        <span class="text-xs muted" data-route-line="">
          {routesLine(routes)}
        </span>
      </div>
      <div class="flex justify-end gap-2">
        <button type="button" class="btn btn-ghost btn-sm" onClick={onClose}>
          Cancel
        </button>
        <button type="button" class="btn btn-primary btn-sm" disabled={dead} onClick={() => void send()}>
          <Icon name="send" class="size-4" />
          Send
        </button>
      </div>
    </div>
  );
}

/** The snooze and dismiss menu of one item's card: the three spans, then the dismissal. The
 *  trigger is a 48 px target in the card's footer, so the menu opens upward from it. */
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
        class="ui-btn ui-btn-secondary ui-btn-icon ui-btn-lg"
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="more" class="size-4" />
      </button>
      {open ? (
        <div
          role="menu"
          aria-label="Snooze or dismiss"
          class="absolute bottom-full left-0 z-20 mb-1 flex min-w-52 flex-col rounded-[10px] border hairline bg-base-100 p-1 shadow-lg"
        >
          {spans.map(([span, label]) => (
            <button
              key={span}
              type="button"
              role="menuitem"
              class="flex min-h-[44px] items-center rounded-md px-2.5 text-left text-sm outline-none hover:bg-base-200 focus-visible:bg-base-200"
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
            class="flex min-h-[44px] items-center rounded-md px-2.5 text-left text-sm text-error outline-none hover:bg-base-200 focus-visible:bg-base-200"
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

/** The ask's facts: what it asks (the tool for a permission, the summary otherwise), the input
 *  it would run, and where it runs. */
function AskFacts({
  item,
  session,
  permission,
}: {
  item: NeedItem;
  session: RegistrySession;
  permission: boolean;
}): JSX.Element {
  const { now } = useHuddle();
  return (
    <div class="flex min-w-0 flex-1 flex-col gap-2">
      <h2 class="m-0 text-base font-semibold">
        {permission ? (
          <>
            Allow <code class="font-mono text-[15px]">{item.tool}</code>?
          </>
        ) : (
          item.summary
        )}
      </h2>
      {item.input ? (
        <code class="block overflow-x-auto rounded-[10px] border hairline bg-base-300 px-3 py-2.5 font-mono text-[13px] text-base-content/80">
          {item.input}
        </code>
      ) : null}
      {permission && (session.branch || session.machine) ? (
        <p class="m-0 text-xs muted">
          Runs in {session.branch || "—"} on {session.machine || "—"}. Waiting {ageText(item.created_at, now)}
          .
        </p>
      ) : null}
    </div>
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
  const height = wide ? "min-h-[44px]" : "min-h-[48px]";
  const fill = wide ? "" : " flex-1";
  return (
    <div class={`flex gap-2.5 ${wide ? "shrink-0" : ""}`}>
      <button
        type="button"
        data-act="deny"
        onClick={() => act.deny(item)}
        class={`${height} rounded-[10px] border border-error bg-transparent px-4 text-[15px] font-medium text-error${fill}`}
      >
        Deny {wide ? <kbd class="kbd kbd-xs">d</kbd> : null}
      </button>
      <button
        type="button"
        data-act="allow"
        onClick={() => act.allow(item, what)}
        class={`${height} rounded-[10px] bg-primary px-4 text-[15px] font-semibold text-base-300${fill}`}
      >
        <Icon name="key" class="mr-1 inline size-4" />
        Allow once {wide ? <kbd class="kbd kbd-xs">a</kbd> : null}
      </button>
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
    command: item.input || undefined,
    session: sessionName(session),
    machine: session.machine || undefined,
  };
}

/** The session screen's action bar (SPEC 9): a permission ask as the phone's bottom sheet and
 *  the wide layouts' inline box — the tool, its input, where it runs, Deny and Allow once with
 *  the keys named, and the composer a Reply instead (or an Answer) opens under it. */
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
  const permission = item.kind === "permission" && item.tool !== "";
  const wide = layout !== "phone";
  const who = sessionName(session);
  const confirm = confirmWhat(item, session, permission);
  const ask = permission ? `Permission ask on ${who}` : `Ask from ${who}`;
  const replyWord = permission ? `Reply to ${who}…` : `Answer ${who}…`;
  const sheet = wide
    ? "mx-4 mb-4 flex shrink-0 flex-col gap-3 rounded-[14px] border border-warning bg-base-100 p-4 sm:mx-6 sm:mb-5"
    : "flex shrink-0 flex-col gap-3 rounded-t-[18px] border-t-2 border-warning bg-base-100 px-4 pt-4 pb-7";
  const spread = wide ? "flex items-start gap-4" : "flex flex-col gap-3";
  return (
    <>
      {act.sheet}
      <section
        data-needs-sheet={wide ? undefined : ""}
        data-needs-bar={wide ? "" : undefined}
        aria-label={ask}
        class={sheet}
      >
        {replying ? (
          <ActComposer
            kind="answer"
            targets={[session.session_id]}
            routes={[session.delivery_route]}
            placeholder={replyWord}
            onClose={() => setReplying(false)}
          />
        ) : (
          <>
            <div class={spread}>
              <AskFacts item={item} session={session} permission={permission} />
              {permission ? <BarActions act={act} item={item} what={confirm} wide={wide} /> : null}
            </div>
            <button
              type="button"
              data-act="reply"
              onClick={() => setReplying(true)}
              class="min-h-[44px] bg-transparent text-left text-sm font-medium text-primary outline-none focus-visible:ring-2 focus-visible:ring-primary"
            >
              {permission ? "Reply instead" : "Answer"}
            </button>
          </>
        )}
      </section>
    </>
  );
}
