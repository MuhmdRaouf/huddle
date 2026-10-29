// field.tsx — one labelled field: the label above, a hint or an error under it, both read out with
// the control. The input is a full tap target tall; ids, commands and numbers can ask for the mono
// face. A multiline field is a textarea that grows with its text up to maxRows lines, then scrolls.

import type { CSSProperties, JSX } from "preact";
import { useUiId } from "./id.ts";

/** What a field shows and reports. */
export type FieldProps = {
  label: string;
  value: string;
  onInput: (value: string) => void;
  type?: "text" | "search" | "number" | undefined;
  /** The keyboard a touch screen raises for the control; none when left out. */
  inputMode?: "decimal" | "numeric" | "email" | "tel" | "url" | "search" | "text" | undefined;
  placeholder?: string | undefined;
  hint?: string | undefined;
  /** Marks the input invalid and replaces the hint. */
  error?: string | undefined;
  mono?: boolean | undefined;
  /** A growing textarea instead of the one-line input; type is ignored. */
  multiline?: boolean | undefined;
  /** The lines the textarea stands open at before it grows; multiline only, 1 when left out. */
  minRows?: number | undefined;
  /** The lines the textarea grows to before it scrolls; multiline only, 6 when left out. */
  maxRows?: number | undefined;
};

/** The wiring every control carries: the invalid mark and the note it reads out. */
function described(
  error: string | undefined,
  note: string | undefined,
  id: string,
): {
  "aria-invalid": true | undefined;
  "aria-describedby": string | undefined;
} {
  return {
    "aria-invalid": error !== undefined ? true : undefined,
    "aria-describedby": note !== undefined ? `${id}-note` : undefined,
  };
}

/** The one-line input. */
function Line(props: {
  id: string;
  value: string;
  onInput: (value: string) => void;
  type: "text" | "search" | "number";
  inputMode: FieldProps["inputMode"];
  placeholder: string | undefined;
  error: string | undefined;
  note: string | undefined;
  mono: boolean;
}): JSX.Element {
  return (
    <input
      id={props.id}
      class={props.mono ? "ui-input ui-mono" : "ui-input"}
      // preact types each input type with the roles it allows; a type held in a variable matches no
      // single pair, so it is read as the text pair, whose role (none set here) all three share
      type={props.type as "text"}
      inputMode={props.inputMode}
      value={props.value}
      placeholder={props.placeholder}
      {...described(props.error, props.note, props.id)}
      onInput={(e) => props.onInput(e.currentTarget.value)}
    />
  );
}

/** The growing field: the box stands minRows lines open and follows its text up to maxRows lines,
 *  then scrolls. */
function Growing(props: {
  id: string;
  value: string;
  onInput: (value: string) => void;
  inputMode: FieldProps["inputMode"];
  placeholder: string | undefined;
  error: string | undefined;
  note: string | undefined;
  minRows: number | undefined;
  maxRows: number;
}): JSX.Element {
  return (
    <textarea
      id={props.id}
      class="ui-input ui-input-multi"
      rows={Math.min(props.minRows ?? 1, props.maxRows)}
      inputMode={props.inputMode}
      // the cap and the opening height travel as custom properties the stylesheet reads, set
      // through the CSSOM, which the page's CSP allows where a style attribute would be stripped
      style={
        {
          "--ui-field-rows": props.maxRows,
          // where the browser sizes the box to its content the rows attribute sets nothing, so
          // the height it opens at travels to the stylesheet beside the cap
          ...(props.minRows === undefined
            ? undefined
            : { "--ui-field-min-rows": Math.min(props.minRows, props.maxRows) }),
        } as CSSProperties
      }
      value={props.value}
      placeholder={props.placeholder}
      {...described(props.error, props.note, props.id)}
      onInput={(e) => {
        // field-sizing grows the box where the browser has it; elsewhere the input sets its own
        // height as the text changes
        if (!CSS.supports("field-sizing", "content")) grow(e.currentTarget);
        props.onInput(e.currentTarget.value);
      }}
    />
  );
}

/** The height a growing field needs right now, where the browser has no field-sizing to grow it. */
function grow(box: HTMLTextAreaElement): void {
  box.style.height = "auto";
  box.style.height = `${box.scrollHeight}px`;
}

/** The field. */
export function Field({
  label,
  value,
  onInput,
  type = "text",
  inputMode,
  placeholder,
  hint,
  error,
  mono = false,
  multiline = false,
  minRows,
  maxRows = 6,
}: FieldProps): JSX.Element {
  const id = useUiId("ui-field");
  const note = error ?? hint;
  return (
    <div class="ui-field">
      <label class="ui-field-label" for={id}>
        {label}
      </label>
      {multiline ? (
        <Growing
          id={id}
          value={value}
          onInput={onInput}
          inputMode={inputMode}
          placeholder={placeholder}
          error={error}
          note={note}
          minRows={minRows}
          maxRows={maxRows}
        />
      ) : (
        <Line
          id={id}
          value={value}
          onInput={onInput}
          type={type}
          inputMode={inputMode}
          placeholder={placeholder}
          error={error}
          note={note}
          mono={mono}
        />
      )}
      {note !== undefined ? (
        <p id={`${id}-note`} class={error !== undefined ? "ui-field-error" : "ui-field-hint"}>
          {note}
        </p>
      ) : null}
    </div>
  );
}
