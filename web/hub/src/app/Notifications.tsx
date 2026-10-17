// Notifications.tsx — this device's push notifications (SPEC 7.6): what it is woken for —
// needs-you items only, needs-you items and alerts, or nothing. The choice is the device's own:
// the routes answer a browser that holds no paired device with the refusal to read or write one.

import { Panel } from "@huddle/ui/page.tsx";
import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { Icon } from "../icons.tsx";
import { errText, useHuddle } from "./context.tsx";

/** The choices the server knows, and what each one wakes the device for. */
const CHOICES: readonly (readonly [value: NotifyChoice, label: string, help: string])[] = [
  ["needs", "Needs you only", "A permission, a question, a finished turn, a blocked task."],
  [
    "all",
    "Needs you and alerts",
    "The items, plus a paired device, a satellite, a rule change, a remote allow.",
  ],
  ["off", "Off", "This device is never woken."],
];

export type NotifyChoice = "needs" | "all" | "off";

/** Reads one answer's choice; anything the server does not name reads as the default. */
export function readNotifyChoice(v: unknown): NotifyChoice {
  const raw = (v as { notify?: unknown } | null)?.notify;
  return raw === "all" || raw === "off" ? raw : "needs";
}

/** The notifications card: the choice as three radios, saved the moment one is picked. */
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
    <Panel title="Notifications" label="Notifications" icon={<Icon name="bell" />}>
      <div role="radiogroup" aria-label="Notifications" class="flex flex-col gap-1">
        {CHOICES.map(([value, label, help]) => (
          <label
            key={value}
            class="flex cursor-pointer items-center gap-3 rounded-lg px-2 py-2 text-sm hover:bg-base-content/5"
          >
            <input
              type="radio"
              class="radio radio-sm"
              name="notify"
              value={value}
              checked={choice === value}
              disabled={choice === null}
              onChange={() => pick(value)}
            />
            <span class="flex-1">
              <span class="block">{label}</span>
              <span class="help block">{help}</span>
            </span>
          </label>
        ))}
      </div>
      {err !== "" ? (
        <p class="err text-xs" role="alert">
          {err}
        </p>
      ) : null}
    </Panel>
  );
}
