// ChatComposer.tsx — the chat's composer: who it goes to (everyone or one member), the words, and
// Send. It rests at the bottom of the chat; while the phone's keyboard is up it is pinned to the
// keyboard's top edge, so the box being typed in never hides under it. Enter sends, Shift+Enter
// breaks the line; a failed send keeps the words.

import { Button } from "@huddle/ui/button.tsx";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { Icon } from "../icons.tsx";
import { useKeyboardInset } from "./keyboard.ts";

/** What the composer offers and where a send goes: the answer says whether it landed. */
export type ChatComposerProps = {
  members: readonly string[];
  onSend: (to: string, msg: string) => Promise<boolean>;
};

/** The composer. */
export function ChatComposer({ members, onSend }: ChatComposerProps): JSX.Element {
  const [to, setTo] = useState("");
  const [msg, setMsg] = useState("");
  const [busy, setBusy] = useState(false);
  const inset = useKeyboardInset();

  const send = async (): Promise<void> => {
    const words = msg.trim();
    if (!words || busy) return;
    setBusy(true);
    const ok = await onSend(to, words);
    setBusy(false);
    if (ok) setMsg("");
  };

  // up with the keyboard: fixed to its top edge, over the tab bar the keyboard hides
  const pinned = inset > 0;
  return (
    <form
      data-chat-composer=""
      aria-label="Send a message"
      class={`z-40 flex flex-col gap-2 border-t border-[var(--ui-line)] bg-[var(--ui-surface)] p-3 ${
        pinned ? "fixed inset-x-0" : "sticky bottom-0 -mx-4 -mb-4 rounded-b-[var(--ui-radius-lg)]"
      }`}
      style={pinned ? { bottom: `${inset}px` } : undefined}
      onSubmit={(e) => {
        e.preventDefault();
        void send();
      }}
    >
      <label class="flex min-w-0 items-center gap-2 text-sm">
        <span class="text-[var(--ui-muted)]">To</span>
        <select
          class="ui-input min-w-0 flex-1 sm:max-w-60"
          aria-label="To"
          value={to}
          onChange={(e) => setTo(e.currentTarget.value)}
        >
          <option value="">Everyone</option>
          {members.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </select>
      </label>
      <div class="flex min-w-0 items-end gap-2">
        <textarea
          class="ui-input min-w-0 flex-1 resize-none"
          // the field's padding is a line's, so one row of words sits in the middle of the 44 px box
          style={{ paddingBlock: "var(--ui-space-2)" }}
          aria-label="Message"
          rows={1}
          placeholder={to ? `Message ${to}` : "Message everyone"}
          value={msg}
          onInput={(e) => setMsg(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <Button type="submit" disabled={busy}>
          <Icon name="send" class="size-4" />
          Send
        </Button>
      </div>
    </form>
  );
}
