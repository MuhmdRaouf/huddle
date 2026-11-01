// conversation.ts — the conversation the session page holds: the items read so far and the byte
// cursor to read from next, the pure steps that join a page into it, and the hook that reads it
// once on open and again each time the hub says the transcript grew. A read follows the cursor
// until it stops moving, so a long transcript arrives whole before the page first paints it.
import { useEffect, useRef, useState } from "preact/hooks";
import { useHuddle } from "../app/context.tsx";
import { sessionApi } from "./api.ts";
import type { ConvoItem, ConvoPage, TurnFacts } from "./types.ts";

/** The items held, oldest first, and the byte cursor the next read starts from. */
export type ConversationState = { items: ConvoItem[]; next: number };

/** Nothing read yet. */
export const emptyConversation: ConversationState = { items: [], next: 0 };

/** The state with one more page: an item it already holds is dropped, and the cursor only
 *  moves forward. */
export function appendPage(state: ConversationState, page: ConvoPage): ConversationState {
  const held = new Set(state.items.map((i) => i.id));
  const fresh = page.items.filter((i) => !held.has(i.id));
  return { items: [...state.items, ...fresh], next: Math.max(state.next, page.next) };
}

/** The items that came after the one the reader last saw: none when nothing was seen or the
 *  item is not held. */
export function newSince(state: ConversationState, seenId: string | null): ConvoItem[] {
  if (seenId === null) return [];
  const at = state.items.findIndex((i) => i.id === seenId);
  return at < 0 ? [] : state.items.slice(at + 1);
}

/** What the page holds of a session's conversation, and whether the first read has answered. A
 *  failed read settles the page with what it holds. */
export type Conversation = { state: ConversationState; settled: boolean };

/** The session's main conversation: read from the start on open, then from the cursor each time
 *  the registry counts the transcript growing. No timer: a read follows an event or an open. */
export function useConversation(id: string): Conversation {
  const { api, registry } = useHuddle();
  const grew = registry.getState().convoGrew[id] ?? 0;
  const held = useRef<ConversationState>(emptyConversation);
  const [view, setView] = useState<Conversation>({ state: emptyConversation, settled: false });

  useEffect(() => {
    let alive = true;
    const client = sessionApi(api);
    void (async () => {
      try {
        for (;;) {
          const from = held.current.next;
          held.current = appendPage(held.current, await client.conversation(id, "main", from));
          if (held.current.next === from) break;
        }
      } catch {
        // a failed read leaves the page with what it holds
      }
      if (alive) setView({ state: held.current, settled: true });
    })();
    return () => {
      alive = false;
    };
  }, [api, id, grew]);

  return view;
}

/** The facts of the session's current turn, read again whenever the session's state changes;
 *  none until the first read answers, and none for a session whose turn the hub cannot read. */
export function useTurn(id: string, state: string): TurnFacts | undefined {
  const { api } = useHuddle();
  const [turn, setTurn] = useState<TurnFacts>();
  useEffect(() => {
    let alive = true;
    sessionApi(api)
      .turn(id)
      .then((t) => {
        if (alive) setTurn(t);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [api, id, state]);
  return turn;
}
