// switch.tsx — an on/off switch: a button with role="switch" whose owner holds the state. A tap asks
// onChange for the other state; the switch shows on only when the owner says so. The button is a full
// tap target (44 px) around a track whose ring and thumb are marks, 3:1 on every surface.

import type { JSX } from "preact";

/** What a switch shows and asks for. */
export type SwitchProps = {
  checked: boolean;
  onChange: (next: boolean) => void;
  /** The accessible name when no visible text names the switch. */
  label?: string | undefined;
  /** The id of the visible text that names the switch. */
  labelledBy?: string | undefined;
  /** The id of the help line that describes the switch. */
  describedBy?: string | undefined;
  id?: string | undefined;
  disabled?: boolean | undefined;
};

/** One switch on the tokens. */
export function Switch({
  checked,
  onChange,
  label,
  labelledBy,
  describedBy,
  id,
  disabled = false,
}: SwitchProps): JSX.Element {
  return (
    <button
      type="button"
      role="switch"
      id={id}
      class="ui-switch"
      aria-checked={checked}
      aria-label={label}
      aria-labelledby={labelledBy}
      aria-describedby={describedBy}
      disabled={disabled}
      onClick={() => onChange(!checked)}
    >
      <span class="ui-switch-track" aria-hidden="true">
        <span class="ui-switch-thumb" />
      </span>
    </button>
  );
}
