import { useEffect, useRef, useState } from "preact/hooks";
import { renderMarkdown } from "../markdown.ts";
import type { Card, FieldValue, Ticket } from "../types.ts";
import { Badge } from "./Badge.tsx";

export type DrawerProps = {
  card: Card;
  getTicket: (id: string) => Promise<Ticket>;
  onClose: () => void;
  onOpenDep: (id: string) => void;
};

/** The ticket ids a deep link may name: the same shape the board's own ids take. */
const ID = /^#\/t\/([A-Z][A-Z0-9]*(?:-[A-Z0-9]+)*)$/;

export function idFromHash(hash: string): string | null {
  const match = ID.exec(hash);
  return match === null ? null : (match[1] ?? null);
}

export type Selection = {
  openId: string | null;
  open: (id: string, opener: HTMLElement) => void;
  close: () => void;
  /** swap the drawer to another ticket without closing it or losing the opener to return to */
  openDep: (id: string) => void;
};

export function useDrawerSelection(): Selection {
  const [openId, setOpenId] = useState<string | null>(null);
  const opener = useRef<HTMLElement | null>(null);
  return {
    openId,
    open: (id, element) => {
      opener.current = element;
      setOpenId(id);
    },
    close: () => {
      setOpenId(null);
      const element = opener.current;
      opener.current = null;
      if (element !== null && document.contains(element)) element.focus();
    },
    openDep: (id) => setOpenId(id),
  };
}

type TicketState = { id: string; ticket: Ticket | null };

/** Fetch the ticket for the open card, dropping answers that arrive after the card moved on. */
function useTicket(
  getTicket: (id: string) => Promise<Ticket>,
  id: string,
): { ticket: Ticket | null; loading: boolean } {
  const fetcher = useRef(getTicket);
  fetcher.current = getTicket;
  const [state, setState] = useState<TicketState>({ id, ticket: null });
  useEffect(() => {
    let live = true;
    setState({ id, ticket: null });
    fetcher
      .current(id)
      .then((ticket) => {
        if (live) setState({ id, ticket });
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [id]);
  return { ticket: state.id === id ? state.ticket : null, loading: state.ticket === null };
}

function fieldText(value: FieldValue): string {
  if (value === null) return "";
  if (Array.isArray(value)) return value.join(", ");
  if (typeof value === "boolean") return value ? "true" : "false";
  return value;
}

/** What the drawer shows before the ticket arrives: the fields the card itself carries. */
function fallbackFields(card: Card): Record<string, FieldValue> {
  return {
    id: card.id,
    title: card.title,
    type: card.type,
    priority: card.priority,
    est: card.est,
    cluster: card.cluster,
    tags: card.tags,
    holder: card.holder,
    author: card.author,
    branch: card.branch,
    worktree: card.worktree,
  };
}

const focusableSelector = "button, [href], input, select, textarea";

function focusablesIn(panel: HTMLElement): HTMLElement[] {
  return [...panel.querySelectorAll<HTMLElement>(focusableSelector)].filter(
    (el) => !el.hasAttribute("disabled"),
  );
}

/** Where Tab (or Shift+Tab) moves focus inside the drawer, wrapping at both ends. */
function nextOnTab(list: HTMLElement[], shift: boolean): HTMLElement | null {
  if (list.length === 0) return null;
  const index = list.indexOf(document.activeElement as HTMLElement);
  if (shift) return list[index <= 0 ? list.length - 1 : index - 1] ?? null;
  return list[index === -1 || index === list.length - 1 ? 0 : index + 1] ?? null;
}

/** The ticket drawer: every field, the badges, dependencies as links, and the body as markdown. */
export function Drawer({ card, getTicket, onClose, onOpenDep }: DrawerProps) {
  const { ticket, loading } = useTicket(getTicket, card.id);
  const panel = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    const panelEl = panel.current;
    if (panelEl === null) return;
    focusablesIn(panelEl)[0]?.focus();
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === "Escape") {
        event.preventDefault();
        closeRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const next = nextOnTab(focusablesIn(panelEl), event.shiftKey);
      if (next === null) return;
      event.preventDefault();
      next.focus();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [card.id]);

  const fields = ticket?.fields ?? fallbackFields(card);
  return (
    <div
      class="drawer"
      role="dialog"
      aria-modal="true"
      aria-label={`${card.id}: ${card.title}`}
      data-drawer
      ref={panel}
    >
      <header class="drawer-head">
        <p class="cid">{card.id}</p>
        <button type="button" class="close" onClick={onClose}>
          Close
        </button>
      </header>
      <ul class="badges">
        {card.badges.map((badge) => (
          <Badge key={`${badge.kind}:${badge.label}`} badge={badge} />
        ))}
      </ul>
      <dl class="fields">
        {Object.entries(fields).map(([key, value]) => (
          <div class="field" key={key}>
            <dt>{key}</dt>
            <dd>{fieldText(value)}</dd>
          </div>
        ))}
      </dl>
      {card.deps.length > 0 && (
        <section class="deps-block">
          <h3>Depends on</h3>
          <ul class="deps">
            {card.deps.map((dep) => (
              <li key={dep.id}>
                {dep.state === "text" || dep.state === "missing" ? (
                  <span class="dep">
                    <span class="dep-id">{dep.id}</span> <span class="dep-state">{dep.state}</span>
                  </span>
                ) : (
                  <button type="button" class="dep" onClick={() => onOpenDep(dep.id)}>
                    <span class="dep-id">{dep.id}</span> <span class="dep-state">{dep.state}</span>
                  </button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
      {loading && <p role="status">Loading the ticket…</p>}
      <div class="body" dangerouslySetInnerHTML={{ __html: renderMarkdown(ticket?.body ?? "") }} />
    </div>
  );
}
