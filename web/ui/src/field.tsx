// field.tsx — one labelled input: the label above, a hint or an error under it, both read out with the
// input. The input is a full tap target tall; ids, commands and numbers can ask for the mono face.

import type { JSX } from "preact";
import { useId } from "preact/hooks";

/** What a field shows and reports. */
export type FieldProps = {
  label: string;
  value: string;
  onInput: (value: string) => void;
  type?: "text" | "search" | "number" | undefined;
  placeholder?: string | undefined;
  hint?: string | undefined;
  /** Marks the input invalid and replaces the hint. */
  error?: string | undefined;
  mono?: boolean | undefined;
};

/** The field. */
export function Field({
  label,
  value,
  onInput,
  type = "text",
  placeholder,
  hint,
  error,
  mono = false,
}: FieldProps): JSX.Element {
  const id = useId();
  const note = error ?? hint;
  return (
    <div class="ui-field">
      <label class="ui-field-label" for={id}>
        {label}
      </label>
      <input
        id={id}
        class={mono ? "ui-input ui-mono" : "ui-input"}
        // preact types each input type with the roles it allows; a type held in a variable matches no
        // single pair, so it is read as the text pair, whose role (none set here) all three share
        type={type as "text"}
        value={value}
        placeholder={placeholder}
        aria-invalid={error !== undefined ? true : undefined}
        aria-describedby={note !== undefined ? `${id}-note` : undefined}
        onInput={(e) => onInput(e.currentTarget.value)}
      />
      {note !== undefined ? (
        <p id={`${id}-note`} class={error !== undefined ? "ui-field-error" : "ui-field-hint"}>
          {note}
        </p>
      ) : null}
    </div>
  );
}
