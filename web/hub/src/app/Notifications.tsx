// Notifications.tsx — Settings › Notifications: this browser's two switches (notifications while
// the page is open, and the Web Push subscription that wakes the installed app, with iOS's
// install rule), and what this device is woken for (SPEC 7.6) — needs-you items only, needs-you
// items and alerts, or nothing. That choice is the device's own: the routes answer a browser that
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
    help: "A permission, a question, a finished turn, a blocked task.",
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

/** The push switch (SPEC 7.6): this device's subscription, woken for what the choice below says.
 *  A refusal (no permission, an unpaired browser) is a toast. */
function PushRow(): JSX.Element {
  const { push, toast } = useHuddle();
  const [status, setStatus] = useState<PushStatus | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    void push.status().then((s) => {
      if (live) setStatus(s);
    });
    return () => {
      live = false;
    };
  }, [push]);
  const flip = async (): Promise<void> => {
    setBusy(true);
    try {
      await (status?.on ? push.disable() : push.enable());
    } catch (x) {
      toast(errText(x), { bad: true });
    }
    setStatus(await push.status());
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

/** The notifications card: the two switches, then the choice as three radios, saved the moment
 *  one is picked. */
export function NotificationsCard(): JSX.Element {
  const { api, toast } = useHuddle();
  const [choice, setChoice] = useState<NotifyChoice | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    api
      .api("/api/push/notify")
      .then((v) => {
        if (live) setChoice(readNotifyChoice(v));
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
      <List label="This browser">
        <NotifyRow />
        <PushRow />
      </List>
      <p class="mt-4 mb-1 text-sm font-semibold">What wakes this device</p>
      <RadioGroup
        label="Notifications"
        name="notify"
        value={choice}
        options={CHOICES}
        disabled={choice === null}
        onChange={pick}
      />
      {err !== "" ? (
        <p class="ui-field-error mt-2" role="alert">
          {err}
        </p>
      ) : null}
    </Card>
  );
}
