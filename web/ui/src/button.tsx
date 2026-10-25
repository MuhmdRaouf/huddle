// button.tsx — the design system's button: four variants (primary for the one main action, secondary,
// ghost for quiet ones, danger for a destructive one), an optional Lucide icon, and a pressed state for
// a switch's segments. Every button is at least one tap target (44 px) tall and wide; the lg size is the
// 48 px a thumb reaches first. With an href it is a link that looks like a button.

import type { ComponentChildren, JSX } from "preact";
import { Icon, type IconName } from "./icons.tsx";

/** How loud the button is. */
export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

/** What a button shows and does. */
export type ButtonProps = {
  children?: ComponentChildren;
  variant?: ButtonVariant | undefined;
  /** lg is the 48 px target of the action a thumb reaches first, such as an approve at a card's foot. */
  size?: "md" | "lg" | undefined;
  icon?: IconName | undefined;
  /** The accessible name; required when the button shows an icon alone. */
  label?: string | undefined;
  /** A switch segment's state; leave it out on a plain button. */
  pressed?: boolean | undefined;
  type?: "button" | "submit" | undefined;
  disabled?: boolean | undefined;
  onClick?: (() => void) | undefined;
  /** Where the link goes: the button renders as an <a> that looks the same. */
  href?: string | undefined;
  class?: string | undefined;
  /** The data attributes a page or a test finds the button by. */
  [data: `data-${string}`]: string | undefined;
};

/** One button on the tokens. An icon with no text makes it a square icon button. */
export function Button({
  children,
  variant = "primary",
  size = "md",
  icon,
  label,
  pressed,
  type = "button",
  disabled = false,
  onClick,
  href,
  class: c,
  ...data
}: ButtonProps): JSX.Element {
  const iconOnly = children === undefined ? " ui-btn-icon" : "";
  const lg = size === "lg" ? " ui-btn-lg" : "";
  const cls = `ui-btn ui-btn-${variant}${iconOnly}${lg}${c ? ` ${c}` : ""}`;
  const parts = (
    <>
      {icon ? <Icon name={icon} class="ui-icon" /> : null}
      {children}
    </>
  );
  if (href !== undefined) {
    return (
      <a {...data} href={href} class={cls} aria-label={label} onClick={onClick}>
        {parts}
      </a>
    );
  }
  return (
    <button
      {...data}
      type={type}
      class={cls}
      aria-label={label}
      aria-pressed={pressed}
      disabled={disabled}
      onClick={onClick}
    >
      {parts}
    </button>
  );
}
