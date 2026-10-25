// radio-group.tsx — a named group of native radios, one row per option: the label, an optional help
// line under it, and a ring and dot drawn from the tokens (marks, 3:1 on every surface). Each row is
// a full tap target tall; the arrows move between options the way the browser does for any radio.

import type { JSX } from "preact";

/** One option: the value it reports, what it is called and what it does. */
export type RadioOption<T extends string> = {
  value: T;
  label: string;
  help?: string | undefined;
};

/** What a radio group shows and reports. */
export type RadioGroupProps<T extends string> = {
  label: string;
  /** The radios' shared form name. */
  name: string;
  /** The checked option; null checks none (still loading). */
  value: T | null;
  options: readonly RadioOption<T>[];
  onChange: (value: T) => void;
  disabled?: boolean | undefined;
};

/** The group. */
export function RadioGroup<T extends string>({
  label,
  name,
  value,
  options,
  onChange,
  disabled = false,
}: RadioGroupProps<T>): JSX.Element {
  return (
    <div role="radiogroup" aria-label={label} class="ui-radios">
      {options.map((o) => (
        <label key={o.value} class="ui-radio">
          <input
            type="radio"
            class="ui-radio-input"
            name={name}
            value={o.value}
            checked={value === o.value}
            disabled={disabled}
            onChange={() => onChange(o.value)}
          />
          <span class="ui-radio-text">
            <span class="ui-radio-label">{o.label}</span>
            {o.help !== undefined ? <span class="ui-radio-help">{o.help}</span> : null}
          </span>
        </label>
      ))}
    </div>
  );
}
