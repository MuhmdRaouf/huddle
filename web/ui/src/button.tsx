// button.tsx — the design system's button: four variants (primary for the one main action, secondary,
// ghost for quiet ones, danger for a destructive one), an optional Lucide icon, and a pressed state for
// a switch's segments. Every button is at least one tap target (44 px) tall and wide.

import type { ComponentChildren, JSX } from "preact";
import { Icon, type IconName } from "./icons.tsx";

/** How loud the button is. */
export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

/** What a button shows and does. */
export type ButtonProps = {
  children?: ComponentChildren;
  variant?: ButtonVariant | undefined;
  icon?: IconName | undefined;
  /** The accessible name; required when the button shows an icon alone. */
  label?: string | undefined;
  /** A switch segment's state; leave it out on a plain button. */
  pressed?: boolean | undefined;
  type?: "button" | "submit" | undefined;
  disabled?: boolean | undefined;
  onClick?: (() => void) | undefined;
  class?: string | undefined;
};

/** One button on the tokens. An icon with no text makes it a square icon button. */
export function Button({
  children,
  variant = "primary",
  icon,
  label,
  pressed,
  type = "button",
  disabled = false,
  onClick,
  class: c,
}: ButtonProps): JSX.Element {
  const iconOnly = children === undefined ? " ui-btn-icon" : "";
  return (
    <button
      type={type}
      class={`ui-btn ui-btn-${variant}${iconOnly}${c ? ` ${c}` : ""}`}
      aria-label={label}
      aria-pressed={pressed}
      disabled={disabled}
      onClick={onClick}
    >
      {icon ? <Icon name={icon} class="ui-icon" /> : null}
      {children}
    </button>
  );
}
