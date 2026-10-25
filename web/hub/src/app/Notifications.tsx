// Notifications.tsx — Settings › Notifications: this browser's two switches (notifications while
// the page is open, and the Web Push subscription that wakes the installed app, with iOS's
// install rule), and what this device is woken for (SPEC 7.6) — needs-you items only, needs-you
// items and alerts, or nothing — with a finished turn as its own switch, off until turned on.
// Those choices are the device's own: the routes answer a browser that
// holds no paired device with the refusal to read or write one.

import { Card } from "@huddle/ui/card.tsx";
import { List, ListItem } from "@huddle/ui/list.tsx";
import { RadioGroup, type RadioOption } from "@huddle/ui/radio-group.tsx";
import { Switch } from "@huddle/ui/switch.tsx";
import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { Icon } from "../icons.tsx";
import type { PushStatus } from "../pwa.ts";
import { errText, useHuddle } from "./context.tsx";

/** The choices the server knows, and what each one wakes the device for. */
const CHOICES: readonly RadioOption<NotifyChoice>[] = [
  {
    value: "needs",
    label: "Needs you only",
    help: "A permission, a question, a blocked task.",
  },
  {
    value: "all",
    label: "Needs you and alerts",
    help: "The items, plus a paired device, a satellite, a rule change, a remote allow.",
  },
  { value: "off", label: "Off", help: "This device is never woken." },
];

export type NotifyChoice = "needs" | "all" | "off";

/** Reads one answer's choice; anything the server does not name reads as the default. */
export function readNotifyChoice(v: unknown): NotifyChoice {
  const raw = (v as { notify?: unknown } | null)?.notify;
  return raw === "all" || raw === "off" ? raw : "needs";
}

/** Reads one answer's finished-turn choice; anything but true reads as off. */
export function readTurnChoice(v: unknown): boolean {
  return (v as { turn?: unknown } | null)?.turn === true;
}

/** One switch row: its title and help line name and describe the switch, a full tap target.
 *  A click asks onFlip; the row's state comes back through `on`. */
function SwitchRow({
  id,
  title,
  help,
  on,
  disabled = false,
  onFlip,
}: {
  id: string;
  title: string;
  help: ComponentChildren;
  on: boolean;
  disabled?: boolean;
  onFlip: () => void;
}): JSX.Element {
  return (
    <ListItem
      title={
        <span id={`${id}-l`} class="whitespace-normal">
          {title}
        </span>
      }
      meta={<span id={`${id}-h`}>{help}</span>}
      trailing={
        <Switch
          id={`set-${id}`}
          labelledBy={`${id}-l`}
          describedBy={`${id}-h`}
          checked={on}
          disabled={disabled}
          onChange={onFlip}
        />
      }
    />
  );
}

/** The notify switch: the page's own notifications while it is open (live.ts). */
function NotifyRow(): JSX.Element {
  const { notify } = useHuddle();
  const [on, setOn] = useState(notify.on());
  return (
    <SwitchRow
      id="notif"
      title="Notify me when a session asks me something"
      help={
        "Notification" in window
          ? `Browser permission: ${Notification.permission}`
          : "This browser cannot show notifications."
      }
      on={on}
      onFlip={() => void notify.toggle().then(() => setOn(notify.on()))}
    />
  );
}

/** The help line under the push switch: what the browser allows, and iOS's install rule. */
function pushHelp(status: PushStatus | null): string {
  if (!status) return "Checking what this browser allows…";
  if (!status.supported) return "This browser cannot receive push.";
  const permission = `Push permission: ${status.permission}.`;
  return status.standalone
    ? permission
    : `${permission} On iPhone and iPad, add Huddle to the Home Screen and turn this on there.`;
}

/** True on an iPhone or iPad, iPadOS's desktop user agent included (a Mac with a touch screen). */
function isIOS(nav: Pick<Navigator, "userAgent" | "maxTouchPoints">): boolean {
  return (
    /iPhone|iPad|iPod/.test(nav.userAgent) || (/Macintosh/.test(nav.userAgent) && nav.maxTouchPoints > 1)
  );
}

/** The push switch (SPEC 7.6): this device's subscription, woken for what the choice below says.
 *  A refusal (no permission, an unpaired browser) is a toast. */
function PushRow({
  status,
  onStatus,
}: {
  status: PushStatus | null;
  onStatus: (s: PushStatus) => void;
}): JSX.Element {
  const { push, toast } = useHuddle();
  const [busy, setBusy] = useState(false);
  const flip = async (): Promise<void> => {
    setBusy(true);
    try {
      await (status?.on ? push.disable() : push.enable());
    } catch (x) {
      toast(errText(x), { bad: true });
    }
    onStatus(await push.status());
    setBusy(false);
  };
  return (
    <SwitchRow
      id="push"
      title="Push to this device when a session needs me"
      help={pushHelp(status)}
      on={status?.on ?? false}
      disabled={busy || !status?.supported}
      onFlip={() => void flip()}
    />
  );
}

/** This browser's two switches; an iPhone or iPad in a Safari tab, which can neither show
 *  notifications nor take push, gets the Home Screen step instead. */
function BrowserRows(): JSX.Element {
  const { push } = useHuddle();
  const [status, setStatus] = useState<PushStatus | null>(null);
  useEffect(() => {
    let live = true;
    void push.status().then((s) => {
      if (live) setStatus(s);
    });
    return () => {
      live = false;
    };
  }, [push]);
  if (status && !status.standalone && !("Notification" in window) && isIOS(navigator)) {
    return (
      <p class="text-sm text-[var(--ui-muted)]">
        Web push on iPhone and iPad works only from the Home Screen app. In Safari, tap Share, then Add to
        Home Screen, open Huddle from there and turn notifications on.
      </p>
    );
  }
  return (
    <List label="This browser">
      <NotifyRow />
      <PushRow status={status} onStatus={setStatus} />
    </List>
  );
}

/** The notifications card: this browser's two switches (or the Home Screen step), then the
 *  choice as three radios and the finished-turn switch, each saved the moment it changes. */
export function NotificationsCard(): JSX.Element {
  const { api, toast } = useHuddle();
  const [choice, setChoice] = useState<NotifyChoice | null>(null);
  const [turn, setTurn] = useState(false);
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    api
      .api("/api/push/notify")
      .then((v) => {
        if (!live) return;
        setChoice(readNotifyChoice(v));
        setTurn(readTurnChoice(v));
      })
      .catch((x) => {
        if (live) setErr(errText(x));
      });
    return () => {
      live = false;
    };
  }, [api]);
  const pick = (next: NotifyChoice): void => {
    if (next === choice) return;
    const before = choice;
    setChoice(next);
    setErr("");
    api
      .api("/api/push/notify", { body: { notify: next } })
      .then(() => toast("Notifications saved"))
      .catch((x) => {
        setChoice(before);
        setErr(errText(x));
      });
  };
  const flipTurn = (): void => {
    const next = !turn;
    setTurn(next);
    setErr("");
    api
      .api("/api/push/notify", { body: { turn: next } })
      .then(() => toast("Notifications saved"))
      .catch((x) => {
        setTurn(!next);
        setErr(errText(x));
      });
  };
  return (
    <Card
      title={
        <span class="flex items-center gap-2.5">
          <span class="text-[var(--ui-accent)]" aria-hidden="true">
            <Icon name="bell" />
          </span>
          Notifications
        </span>
      }
    >
      <BrowserRows />
      <p class="mt-4 mb-1 text-sm font-semibold">What wakes this device</p>
      <RadioGroup
        label="Notifications"
        name="notify"
        value={choice}
        options={CHOICES}
        disabled={choice === null}
        onChange={pick}
      />
      <div class="mt-3">
        <List label="Finished turns">
          <SwitchRow
            id="turn"
            title="Push when a turn finishes"
            help="A session finished its turn and waits for your next prompt."
            on={turn}
            disabled={choice === null}
            onFlip={flipTurn}
          />
        </List>
      </div>
      {err !== "" ? (
        <p class="ui-field-error mt-2" role="alert">
          {err}
        </p>
      ) : null}
    </Card>
  );
}
