import { Button } from "@huddle/ui/button.tsx";
import { Icon } from "@huddle/ui/icons.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import { timeAgo } from "@huddle/ui/time.ts";
import type { ToastFn } from "@huddle/ui/toast.tsx";
import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { featureAction } from "../io.ts";
import { NO_FEATURE, featureLabel, useMedia } from "../store.ts";
import type { Card, ClosedFeature } from "../types.ts";

export type FeaturePickerProps = {
  /** every card the board carries; the open features and their counts are read from them */
  cards: Card[];
  /** the features the board has closed, shown read-only under the open ones */
  closed: ClosedFeature[];
  /** the picked feature: null is every feature, NO_FEATURE is the cards with none */
  feature: string | null;
  /** hands the picked feature word up, as the address change that reopens the tab */
  onFeature: (feature: string | null) => void;
  /** whether the viewer may close and reopen: only the board's owner and its device may */
  canClose?: boolean;
  /** the board's key, the address the close and reopen posts ride */
  boardKey?: string;
  /** the app's toast: the answer's first line on ok, its error as bad news */
  toast?: ToastFn | undefined;
};

/** One pickable row: the word it hands up, the label it shows, its card count, whether it is picked. */
type Row = { value: string | null; label: string; count: number; picked: boolean };

const POPOVER_ID = "kanban-feature-picker-pop";
const LIST_ID = "kanban-feature-picker";
const CLOSED_LABEL_ID = "kanban-feature-closed-label";

/**
 * The feature picker: a button naming the picked feature, or All features, opening the list of the
 * board's features with their counts and the closed ones under them. The arrows walk, Enter picks,
 * Escape closes; a phone opens the same list as a sheet titled Feature. For the owner every open
 * feature row ends in a Close button and every closed one in a Reopen, each posting the action and
 * toasting the answer; the rows themselves move only when the next delta says so.
 */
export function FeaturePicker({
  cards,
  closed,
  feature,
  onFeature,
  canClose = false,
  boardKey,
  toast,
}: FeaturePickerProps): JSX.Element {
  const phone = useMedia("(max-width: 39.9375rem)");
  const [open, setOpen] = useState(false);
  const button = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLSpanElement>(null);

  const counts = new Map<string, number>();
  let none = 0;
  for (const card of cards) {
    if (card.cluster === "") none += 1;
    else counts.set(card.cluster, (counts.get(card.cluster) ?? 0) + 1);
  }
  const rows: Row[] = [
    { value: null, label: "All features", count: cards.length, picked: feature === null },
    ...[...counts.keys()]
      .sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))
      .map((name) => ({ value: name, label: name, count: counts.get(name) ?? 0, picked: feature === name })),
    ...(none > 0
      ? [{ value: NO_FEATURE, label: "No feature", count: none, picked: feature === NO_FEATURE }]
      : []),
  ];

  const close = (toButton: boolean): void => {
    setOpen(false);
    if (toButton) button.current?.focus();
  };

  // a mouse pick leaves the list open to compare features; a keyboard pick, like a phone pick,
  // ends in the board behind
  const pick = (row: Row, closeAfter: boolean): void => {
    if (closeAfter) setOpen(false);
    onFeature(row.value);
  };

  // the post behind Close and Reopen: the answer's first line toasts on ok, its error as bad news,
  // and closing the feature the address holds hands All features up
  const act = async (action: "close" | "reopen", name: string, label: string): Promise<void> => {
    if (boardKey === undefined || toast === undefined) return;
    const answer = await featureAction(boardKey, action, name);
    if (!answer.ok) {
      toast(answer.error, { bad: true });
      return;
    }
    toast(answer.lines[0] ?? `${action === "close" ? "Closed" : "Reopened"} ${label}`);
    if (action === "close" && feature === name) onFeature(null);
  };

  /** a real feature's row carries Close for the owner; All features and No feature carry none */
  const closable = (row: Row): boolean => canClose && row.value !== null && row.value !== NO_FEATURE;

  // a click outside closes without a pick; the timeout keeps the opening click out. A phone needs
  // none of it: the sheet owns its own outside taps, and the popover id is not on the page there.
  useEffect(() => {
    if (!open || phone) return;
    const outside = (event: Event): void => {
      if (!document.getElementById(POPOVER_ID)?.contains(event.target as Node)) close(false);
    };
    const timer = setTimeout(() => document.addEventListener("click", outside, true), 0);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("click", outside, true);
    };
  }, [open, phone]);

  // opening hands the focus to the picked option, so the next arrow walks from it
  useEffect(() => {
    if (!open || phone) return;
    const checked = [...(list.current?.querySelectorAll<HTMLElement>("button[role='option']") ?? [])].find(
      (b) => b.getAttribute("aria-selected") === "true",
    );
    checked?.focus();
  }, [open, phone]);

  const onKeyDown = (event: KeyboardEvent): void => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      close(true);
      return;
    }
    // the walk is over the options only — the Close and Reopen buttons beside them are plain
    // buttons, never stops; among the options bs[i] is rows[i]
    const bs = [...(list.current?.querySelectorAll<HTMLElement>("button[role='option']") ?? [])];
    const n = bs.length;
    if (n === 0) return;
    const from =
      document.activeElement instanceof HTMLButtonElement ? bs.indexOf(document.activeElement) : -1;
    const picked = from >= 0 ? rows[from] : undefined;
    if (event.key === "Enter" && picked !== undefined) {
      event.preventDefault();
      pick(picked, true);
      return;
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      bs[(((from + step) % n) + n) % n]?.focus();
    } else if (event.key === "Home") {
      event.preventDefault();
      bs[0]?.focus();
    } else if (event.key === "End") {
      event.preventDefault();
      bs[n - 1]?.focus();
    }
  };

  const now = Date.now();

  // an option: its label and count, the picked row's check at its right
  const options = (onPhone: boolean): JSX.Element[] =>
    rows.map((row) => (
      <button
        key={row.label}
        type="button"
        role="option"
        class="picker-opt"
        aria-selected={row.picked}
        aria-label={`${row.label} ${row.count}`}
        onClick={(event) => pick(row, onPhone || event.detail === 0)}
        onMouseDown={onPhone ? undefined : (event) => event.preventDefault()}
      >
        {/* the real space keeps the visible text "name count" inside the aria-label, which the
            accessibility check requires of a labelled button */}
        <strong>{row.label}</strong> <span class="count">{row.count}</span>
        {row.picked ? <Icon name="check" class="picker-check" /> : null}
      </button>
    ));

  // the owner's Close buttons in a column beside the list, one slot per row in the rows' order.
  // The buttons cannot sit in the rows: a listbox owns only options, and axe's
  // aria-required-children fails any other element inside one, however wrapped — so each slot
  // takes its row's height and the two columns read as one row for the eyes. A row without an
  // action leaves an empty slot to keep the pairs aligned.
  const hasActs = canClose && rows.some(closable);
  const acts = (
    <span class="picker-acts">
      {rows.map((row) => (
        <span class="picker-act-slot" key={row.label}>
          {closable(row) ? (
            <Button
              variant="ghost"
              class="picker-act"
              label={`Close ${row.label}`}
              onClick={() => void act("close", row.value ?? "", row.label)}
            >
              Close
            </Button>
          ) : null}
        </span>
      ))}
    </span>
  );

  const closedGroup =
    closed.length > 0 ? (
      <span class="picker-closed">
        <span class="picker-closed-label" id={CLOSED_LABEL_ID}>
          Closed
        </span>
        <span class="picker-closed-rows" role="list" aria-labelledby={CLOSED_LABEL_ID}>
          {closed.map((f) => (
            <span class="picker-closed-row" role="listitem" key={f.name}>
              <span class="picker-closed-text">
                <span class="picker-closed-name">{f.name}</span>
                <span class="picker-closed-when">{`Closed ${timeAgo(f.closedAt, now)} · ${f.cards} cards`}</span>
              </span>
              {canClose ? (
                <Button
                  variant="ghost"
                  class="picker-act"
                  label={`Reopen ${f.name}`}
                  onClick={() => void act("reopen", f.name, f.name)}
                >
                  Reopen
                </Button>
              ) : null}
            </span>
          ))}
        </span>
      </span>
    ) : null;

  const trigger = (onPhone: boolean): JSX.Element => (
    <button
      {...(onPhone ? {} : { ref: button })}
      type="button"
      class="picker-button"
      aria-label="Feature"
      aria-haspopup="listbox"
      aria-expanded={open}
      aria-controls={open && !onPhone ? LIST_ID : undefined}
      onClick={() => (open && !onPhone ? close(true) : setOpen(true))}
    >
      {featureLabel(feature)}
      <span class="chev" aria-hidden="true">
        ▾
      </span>
    </button>
  );

  if (phone) {
    return (
      <span class="picker">
        {trigger(true)}
        {open && (
          <Sheet open onClose={() => close(false)} title="Feature">
            <span class="picker-body">
              <span class="picker-list" role="listbox" aria-label="Features">
                {options(true)}
              </span>
              {hasActs ? acts : null}
            </span>
            {closedGroup}
          </Sheet>
        )}
      </span>
    );
  }
  return (
    <span class="picker">
      {trigger(false)}
      {open && (
        <span class="picker-pop" id={POPOVER_ID} onKeyDown={onKeyDown}>
          <span class="picker-body">
            <span class="picker-list" id={LIST_ID} ref={list} role="listbox" aria-label="Features">
              {options(false)}
            </span>
            {hasActs ? acts : null}
          </span>
          {closedGroup}
        </span>
      )}
    </span>
  );
}
