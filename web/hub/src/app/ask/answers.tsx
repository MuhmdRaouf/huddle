// answers.tsx — the AskUserQuestion answer sheet: one card per question, its options as radio or
// checkbox rows, a typed answer in place of the picks, and the pinned footer whose caption counts
// the answered questions and whose Send carries them to the decide call. Only the picked labels
// that are options the question offered count; the words typed stand above the picks.

import { Button } from "@huddle/ui/button.tsx";
import { Field } from "@huddle/ui/field.tsx";
import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { Icon } from "../../icons.tsx";
import type { NeedItem } from "../../registry.ts";
import { answersOf, questionsOf } from "./questions.ts";
import type { AskOption, AskPick, AskQuestion } from "./questions.ts";

const MUTED = "text-[var(--ui-muted)]";

/** The row's classes: the picked one sits on the accent wash, the rest take the card's chip fill
 *  on hover. */
const rowClass = (picked: boolean): string =>
  `flex min-h-14 cursor-pointer items-center gap-3 rounded-[var(--ui-radius)] px-3 py-2.5 ${
    picked ? "bg-[var(--ui-accent-soft)]" : "bg-transparent hover:bg-[var(--ui-surface-2)]"
  }`;

/** The mark a radio row draws: the ring, and the dot while picked. */
const RadioMark = ({ picked }: { picked: boolean }): JSX.Element => (
  <span
    aria-hidden="true"
    class={`flex size-5 shrink-0 items-center justify-center rounded-full border-2 ${
      picked ? "border-[var(--ui-accent)]" : "border-[var(--ui-line-strong)]"
    }`}
  >
    {picked ? <span class="size-2.5 rounded-full bg-[var(--ui-accent)]" /> : null}
  </span>
);

/** The mark a checkbox row draws: the square, and the check while picked. */
const CheckMark = ({ picked }: { picked: boolean }): JSX.Element => (
  <span
    aria-hidden="true"
    class={`flex size-5 shrink-0 items-center justify-center rounded-[var(--ui-radius-sm)] border-2 ${
      picked ? "border-[var(--ui-accent)] bg-[var(--ui-accent)]" : "border-[var(--ui-line-strong)]"
    }`}
  >
    {picked ? <Icon name="check" class="size-3.5 text-[var(--ui-on-accent)]" /> : null}
  </span>
);

/** One question's card: its header as the chip, the question, its rows, and the way in to a
 *  typed answer. */
const AskCard = ({
  q,
  pick,
  typing,
  onPick,
  onType,
  onTyped,
}: {
  q: AskQuestion;
  pick: AskPick | undefined;
  typing: boolean;
  onPick: (label: string) => void;
  onType: () => void;
  onTyped: (value: string) => void;
}): JSX.Element => {
  const picked = (label: string): boolean => pick?.picked.includes(label) ?? false;
  return (
    <div
      data-ask-card
      class="flex flex-col rounded-[var(--ui-radius)] border border-[var(--ui-line)] bg-[var(--ui-surface)] p-3"
    >
      {q.header === "" ? null : (
        <span
          class={`self-start rounded-[var(--ui-radius-sm)] bg-[var(--ui-surface-2)] px-2.5 py-1 text-xs font-medium text-[var(--ui-muted)]`}
        >
          {q.header}
        </span>
      )}
      <p class="m-0 mt-2 text-sm font-semibold">{q.question}</p>
      {typing ? (
        <div class="mt-3">
          <Field label="Your answer" value={pick?.typed ?? ""} onInput={onTyped} />
        </div>
      ) : (
        <>
          <div
            role={q.multiSelect ? "group" : "radiogroup"}
            aria-label={q.header === "" ? q.question : q.header}
            class="mt-3 flex flex-col gap-2"
          >
            {q.options.map((o) => (
              <PickRow
                key={o.label}
                o={o}
                role={q.multiSelect ? "checkbox" : "radio"}
                checked={picked(o.label)}
                onPick={() => onPick(o.label)}
              />
            ))}
          </div>
          <Button variant="ghost" size="lg" class="mt-2 w-full" onClick={onType}>
            <Icon name="pencil" class={`size-4 ${MUTED}`} />
            Type your own answer
          </Button>
        </>
      )}
    </div>
  );
};

/** One option's row: the whole row answers, with the keyboard as on a click. */
const PickRow = ({
  o,
  role,
  checked,
  onPick,
}: {
  o: AskOption;
  role: "radio" | "checkbox";
  checked: boolean;
  onPick: () => void;
}): JSX.Element => (
  <div
    role={role}
    aria-checked={checked ? "true" : "false"}
    tabIndex={0}
    onClick={onPick}
    onKeyDown={(e) => {
      if (e.key === " " || e.key === "Enter") {
        e.preventDefault();
        onPick();
      }
    }}
    class={rowClass(checked)}
  >
    {role === "radio" ? <RadioMark picked={checked} /> : <CheckMark picked={checked} />}
    <span class="flex min-w-0 flex-col gap-1">
      <span class="text-sm font-medium">{o.label}</span>
      {o.description === "" ? null : <span class={`text-xs ${MUTED}`}>{o.description}</span>}
    </span>
  </div>
);

/** The answer sheet for one AskUserQuestion ask: the caller has already checked that the input
 *  decodes; an input that does not draws nothing. Send hands the answers over once every
 *  question has one; Answer in terminal hands the ask back to the session undecided. */
export function AskAnswers({
  item,
  onSend,
  onTerminal,
}: {
  item: NeedItem;
  onSend: (answers: Record<string, string>) => void;
  onTerminal: () => void;
}): JSX.Element | null {
  const questions = questionsOf(item.input ?? "") ?? [];
  const [picks, setPicks] = useState<AskPick[]>(() => questions.map(() => ({ picked: [] })));
  const [typing, setTyping] = useState<boolean[]>(() => questions.map(() => false));
  if (questions.length === 0) return null;

  const setPicked = (i: number, label: string, multi: boolean): void =>
    setPicks((old) =>
      old.map((p, j) => {
        if (j !== i) return p;
        if (!multi) return { picked: [label] };
        return p.picked.includes(label)
          ? { picked: p.picked.filter((l) => l !== label) }
          : { picked: [...p.picked, label] };
      }),
    );

  const setTyped = (i: number, value: string): void => {
    const words = value.trim() !== "";
    setTyping((old) => old.map((t, j) => (j === i ? words : t)));
    setPicks((old) =>
      old.map((p, j) => (j === i ? { picked: p.picked, ...(words ? { typed: value } : {}) } : p)),
    );
  };

  const answers = answersOf(questions, picks);
  return (
    <div class="flex flex-col gap-3">
      {questions.map((q, i) => (
        <AskCard
          key={q.question}
          q={q}
          pick={picks[i]}
          typing={typing[i] ?? false}
          onPick={(label) => setPicked(i, label, q.multiSelect)}
          onType={() => setTyping((old) => old.map((t, j) => (j === i ? true : t)))}
          onTyped={(value) => setTyped(i, value)}
        />
      ))}
      <footer
        data-needs-foot
        class="sticky bottom-0 flex flex-col gap-2.5 border-t border-[var(--ui-line)] bg-[var(--ui-surface)] pt-3"
      >
        <p class={`m-0 text-center text-xs ${MUTED}`}>
          {questions.filter((q, i) => answersOf([q], [picks[i] ?? { picked: [] }]) !== null).length} of{" "}
          {questions.length} answered
        </p>
        <div class="flex gap-2.5">
          <Button variant="secondary" size="lg" class="flex-1" onClick={onTerminal}>
            Answer in terminal
          </Button>
          <Button
            variant="primary"
            size="lg"
            class="flex-1"
            disabled={answers === null}
            onClick={() => {
              if (answers !== null) onSend(answers);
            }}
          >
            <Icon name="up" class="size-4" />
            Send answers
          </Button>
        </div>
      </footer>
    </div>
  );
}
