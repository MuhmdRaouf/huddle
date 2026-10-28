import { Icon } from "@huddle/ui/icons.tsx";
import { Sheet } from "@huddle/ui/sheet.tsx";
import { timeAgo } from "@huddle/ui/time.ts";
import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
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
};

/** One pickable row: the word it hands up, the label it shows, its card count, whether it is picked. */
type Row = { value: string | null; label: string; count: number; picked: boolean };

const POPOVER_ID = "kanban-feature-picker-pop";
const LIST_ID = "kanban-feature-picker";
const CLOSED_LABEL_ID = "kanban-feature-closed-label";

/**
 * The feature picker: a button naming the picked feature, or All features, opening the list of the
 * board's features with their counts and the closed ones read-only under them. The arrows walk,
 * Enter picks, Escape closes; a phone opens the same list as a sheet titled Feature.
 */
export function FeaturePicker({ cards, closed, feature, onFeature }: FeaturePickerProps): JSX.Element {
  const phone = useMedia("(max-width: 639px)");
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

  // opening hands the focus to the picked row, so the next arrow walks from it
  useEffect(() => {
    if (!open || phone) return;
    const checked = [...(list.current?.querySelectorAll("button") ?? [])].find(
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
    // the walk is over the real focus; bs[i] is rows[i]
    const bs = [...(list.current?.querySelectorAll("button") ?? [])];
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
        <strong>{row.label}</strong>
        <span class="count">{row.count}</span>
        {row.picked ? <Icon name="check" class="picker-check" /> : null}
      </button>
    ));

  const closedGroup =
    closed.length > 0 ? (
      <span class="picker-closed">
        <span class="picker-closed-label" id={CLOSED_LABEL_ID}>
          Closed
        </span>
        <span class="picker-closed-rows" role="list" aria-labelledby={CLOSED_LABEL_ID}>
          {closed.map((f) => (
            <span class="picker-closed-row" role="listitem" key={f.name}>
              <span class="picker-closed-name">{f.name}</span>
              <span class="picker-closed-when">{`Closed ${timeAgo(f.closedAt, now)} · ${f.cards} cards`}</span>
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
            <span class="picker-list" role="listbox" aria-label="Features">
              {options(true)}
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
          <span class="picker-list" id={LIST_ID} ref={list} role="listbox" aria-label="Features">
            {options(false)}
          </span>
          {closedGroup}
        </span>
      )}
    </span>
  );
}
