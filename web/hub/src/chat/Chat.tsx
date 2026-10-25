// Chat.tsx — the channel as a chat: the message events newest last as bubbles (the owner's on the
// right, each session's on the left) with who told whom and the age, the composer under them,
// and the Members button that opens the members sheet. A message to nobody in particular reads
// as to everyone, and the owner reads as You. The composer reads each member's route from the
// registry row of its newest session in this channel.

import { Button } from "@huddle/ui/button.tsx";
import { Card } from "@huddle/ui/card.tsx";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { useHuddle } from "../app/context.tsx";
import { Icon } from "../icons.tsx";
import { Time } from "../kit.tsx";
import type { ReservationItem } from "../pages/Overview.tsx";
import { who } from "../status.ts";
import type { FeedEvent } from "../store.ts";
import { familyOf } from "../timeline.tsx";
import { ChatComposer } from "./ChatComposer.tsx";
import { MembersSheet } from "./MembersSheet.tsx";

/** What the chat reads: the channel's timeline, the paint clock and the live claims. */
export type ChatProps = {
  events: readonly FeedEvent[];
  now: number;
  reservations: readonly ReservationItem[];
};

/** One message as a bubble: the owner's on the right in the accent, the others on the left. */
function Bubble({ e, now }: { e: FeedEvent; now: number }): JSX.Element {
  const mine = e.from === "owner";
  return (
    <li data-seq={e.seq} class={`flex min-w-0 flex-col gap-1 ${mine ? "items-end" : "items-start"}`}>
      <span class="max-w-full truncate text-xs text-[var(--ui-muted)]">
        <b class="font-semibold text-[var(--ui-text)]">{who(e.from ?? "")}</b> → {who(e.to ?? "everyone")} ·{" "}
        <Time ts={e.ts} now={now} />
      </span>
      <p
        class={`m-0 max-w-[85%] rounded-2xl px-3.5 py-2 text-[15px] break-words whitespace-pre-wrap ${
          mine
            ? "rounded-br-md bg-[var(--ui-accent)] text-[var(--ui-on-accent)]"
            : "rounded-bl-md bg-[var(--ui-surface-2)] text-[var(--ui-text)]"
        }`}
      >
        {e.msg}
      </p>
    </li>
  );
}

/** The chat. */
export function Chat({ events, now, reservations }: ChatProps): JSX.Element {
  const { state, act, toast, registry } = useHuddle();
  const [membersOpen, setMembersOpen] = useState(false);
  // removed here and not yet gone from the roster the stream sends
  const [removed, setRemoved] = useState<readonly string[]>([]);
  const msgs = events.filter((e) => familyOf(e.topic) === "msg");
  const members = (state.sessions?.sessions ?? []).filter(
    (s) => s.state !== "left" && !removed.includes(s.name),
  );

  // the registry lists the newest activity first: the first row is the member's newest session
  const routeOf = (member: string): string | undefined =>
    registry.getState().sessions?.find((s) => s.channel === state.ch && s.member === member)?.delivery_route;

  const send = async (to: string, msg: string): Promise<boolean> =>
    (await act("send", to ? { to, msg } : { msg })) !== null;

  const remove = async (name: string): Promise<boolean> => {
    if ((await act("remove", { target: name })) === null) return false;
    setRemoved((r) => [...r, name]);
    toast(`${name} removed`);
    return true;
  };

  return (
    <div data-chat="" class="min-w-0">
      <div data-messages="">
        <Card
          title="Messages"
          meta={msgs.length > 0 ? String(msgs.length) : undefined}
          actions={
            <Button variant="ghost" onClick={() => setMembersOpen(true)}>
              <Icon name="users" class="size-4" />
              Members · {members.length}
            </Button>
          }
        >
          {msgs.length > 0 ? (
            <ul class="m-0 mb-4 flex list-none flex-col gap-3 p-0" aria-label="Messages">
              {msgs.map((e) => (
                <Bubble key={e.seq} e={e} now={now} />
              ))}
            </ul>
          ) : (
            <p class="m-0 mb-4 text-sm text-[var(--ui-muted)]">
              No messages yet. Say something to the channel below.
            </p>
          )}
          <ChatComposer members={members.map((m) => m.name)} onSend={send} routeOf={routeOf} />
        </Card>
      </div>
      <MembersSheet
        open={membersOpen}
        onClose={() => setMembersOpen(false)}
        members={members}
        claims={reservations}
        onRemove={remove}
      />
    </div>
  );
}
