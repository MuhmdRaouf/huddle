// field.tsx — one labelled field: the label above (or visually hidden, still read out), a hint or
// an error under it, both read out with the control. The input is a full tap target tall; ids,
// commands and numbers can ask for the mono face. A multiline field is a textarea that grows with
// its text up to maxRows lines, then scrolls.

import type { ComponentChild, CSSProperties, JSX, TargetedKeyboardEvent } from "preact";
import { useUiId } from "./id.ts";

/** What a field shows and reports. */
export type FieldProps = {
  label: string;
  value: string;
  onInput: (value: string) => void;
  type?: "text" | "search" | "number" | undefined;
  /** The keyboard a touch screen raises for the control; none when left out. */
  inputMode?: "decimal" | "numeric" | "email" | "tel" | "url" | "search" | "text" | undefined;
  /** The control's autocomplete hint, for example "off", "one-time-code" or "username"; none when left out. */
  autoComplete?: string | undefined;
  /** Whether the browser checks spelling in the control; none when left out. */
  spellCheck?: boolean | undefined;
  /** The control's form name. */
  name?: string | undefined;
  placeholder?: string | undefined;
  /** The hint under the control; text or markup such as a code snippet. */
  hint?: ComponentChild | undefined;
  /** Marks the input invalid and replaces the hint. */
  error?: string | undefined;
  mono?: boolean | undefined;
  /** A growing textarea instead of the one-line input; type is ignored. */
  multiline?: boolean | undefined;
  /** The lines the textarea stands open at before it grows; multiline only, 1 when left out. */
  minRows?: number | undefined;
  /** The lines the textarea grows to before it scrolls; multiline only, 6 when left out. */
  maxRows?: number | undefined;
  /** Each key press on the control, for example Enter to send. */
  onKeyDown?: ((event: TargetedKeyboardEvent<HTMLInputElement | HTMLTextAreaElement>) => void) | undefined;
  /** The control's id, for a box a command focuses by name; a generated one when left out. */
  id?: string | undefined;
  /** The label is read out but not shown. */
  labelHidden?: boolean | undefined;
};

/** The wiring every control carries: the invalid mark and the note it reads out. */
function described(
  error: string | undefined,
  note: ComponentChild | undefined,
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

/** The spelling check a control carries. Preact drops an attribute whose value is false, and the
 *  browser then falls back to its default, which leaves spellchecking on, so the check travels as
 *  the string the DOM stores; preact types the attribute boolean-only, so the type steps aside. */
function spellCheckAttr(check: boolean | undefined): { spellCheck: boolean | undefined } {
  return { spellCheck: check === undefined ? undefined : (String(check) as unknown as boolean) };
}

/** The one-line input. */
function Line(props: {
  id: string;
  value: string;
  onInput: (value: string) => void;
  onKeyDown: FieldProps["onKeyDown"];
  type: "text" | "search" | "number";
  inputMode: FieldProps["inputMode"];
  autoComplete: FieldProps["autoComplete"];
  spellCheck: FieldProps["spellCheck"];
  name: FieldProps["name"];
  placeholder: string | undefined;
  error: string | undefined;
  note: ComponentChild | undefined;
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
      autoComplete={props.autoComplete}
      {...spellCheckAttr(props.spellCheck)}
      name={props.name}
      value={props.value}
      placeholder={props.placeholder}
      {...described(props.error, props.note, props.id)}
      onInput={(e) => props.onInput(e.currentTarget.value)}
      onKeyDown={props.onKeyDown}
    />
  );
}

/** The growing field: the box stands minRows lines open and follows its text up to maxRows lines,
 *  then scrolls. */
function Growing(props: {
  id: string;
  value: string;
  onInput: (value: string) => void;
  onKeyDown: FieldProps["onKeyDown"];
  inputMode: FieldProps["inputMode"];
  autoComplete: FieldProps["autoComplete"];
  spellCheck: FieldProps["spellCheck"];
  name: FieldProps["name"];
  placeholder: string | undefined;
  error: string | undefined;
  note: ComponentChild | undefined;
  minRows: number | undefined;
  maxRows: number;
}): JSX.Element {
  return (
    <textarea
      id={props.id}
      class="ui-input ui-input-multi"
      rows={Math.min(props.minRows ?? 1, props.maxRows)}
      inputMode={props.inputMode}
      autoComplete={props.autoComplete}
      {...spellCheckAttr(props.spellCheck)}
      name={props.name}
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
      onKeyDown={props.onKeyDown}
    />
  );
}

/** The height a growing field needs right now, where the browser has no field-sizing to grow it. */
function grow(box: HTMLTextAreaElement): void {
  box.style.height = "auto";
  box.style.height = `${box.scrollHeight}px`;
}

/** The field's label, out of sight but still read out when the field is hidden-labelled. */
function Label(props: { label: string; for: string; hidden: boolean }): JSX.Element {
  return (
    <label class={props.hidden ? "ui-field-label sr-only" : "ui-field-label"} for={props.for}>
      {props.label}
    </label>
  );
}

/** The field. */
export function Field({
  label,
  value,
  onInput,
  type = "text",
  inputMode,
  autoComplete,
  spellCheck,
  name,
  placeholder,
  hint,
  error,
  mono = false,
  multiline = false,
  minRows,
  maxRows = 6,
  onKeyDown,
  id: asked,
  labelHidden = false,
}: FieldProps): JSX.Element {
  // the hook runs on every render so the hook order holds; the asked id wins over the generated one
  const generated = useUiId("ui-field");
  const id = asked ?? generated;
  const note = error ?? hint;
  return (
    <div class="ui-field">
      <Label label={label} for={id} hidden={labelHidden} />
      {multiline ? (
        <Growing
          id={id}
          value={value}
          onInput={onInput}
          onKeyDown={onKeyDown}
          inputMode={inputMode}
          autoComplete={autoComplete}
          spellCheck={spellCheck}
          name={name}
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
          onKeyDown={onKeyDown}
          type={type}
          inputMode={inputMode}
          autoComplete={autoComplete}
          spellCheck={spellCheck}
          name={name}
          placeholder={placeholder}
          error={error}
          note={note}
          mono={mono}
        />
      )}
      {note !== undefined ? (
        <p
          id={`${id}-note`}
          class={error !== undefined ? "ui-field-error" : "ui-field-hint"}
          // the screen reader announces an error at once; a hint reads in the normal order
          role={error !== undefined ? "alert" : undefined}
        >
          {note}
        </p>
      ) : null}
    </div>
  );
}
