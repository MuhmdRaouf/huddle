// banner.tsx — one line of news across the top of a page or card: info, a warning that needs a look
// soon, or a failure, each on its tone's soft tint with the tone's ink in the leading icon, and an
// optional action button at the end.

import type { ComponentChildren, JSX } from "preact";
import { Button } from "./button.tsx";
import { Icon, type IconName } from "./icons.tsx";

/** The banner's tone: info news, a warning, or a failure. */
export type BannerTone = "info" | "warn" | "fail";

/** The icon each tone leads with, the closest the one icon set has to the tone's meaning. */
const TONE_ICON: Record<BannerTone, IconName> = { info: "now", warn: "alert", fail: "x" };

/** One banner. Info speaks as a status; warn and fail speak as an alert. */
export function Banner({
  tone = "info",
  title,
  children,
  action,
}: {
  tone?: BannerTone;
  /** The bold lead words. */
  title?: string;
  children: ComponentChildren;
  action?: { label: string; onClick: () => void };
}): JSX.Element {
  return (
    <div class={`ui-banner ui-banner-${tone}`} role={tone === "info" ? "status" : "alert"}>
      <Icon name={TONE_ICON[tone]} class="ui-icon" />
      <div class="ui-banner-text">
        {title ? <strong class="ui-banner-title">{title}</strong> : null}
        {children}
      </div>
      {action ? (
        <Button variant="secondary" class="ui-banner-action" onClick={action.onClick}>
          {action.label}
        </Button>
      ) : null}
    </div>
  );
}
