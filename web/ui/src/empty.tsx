// empty.tsx — what an empty list or page shows: an icon in a soft tile, one muted line, and an
// optional action button under the line.

import type { ComponentChildren, JSX } from "preact";
import { Button } from "./button.tsx";

/** One empty state. */
export function Empty({
  icon,
  children,
  action,
}: {
  icon?: ComponentChildren;
  children: ComponentChildren;
  action?: { label: string; onClick: () => void };
}): JSX.Element {
  return (
    <div class="ui-empty">
      {icon ? <div class="ui-empty-icon">{icon}</div> : null}
      <div class="ui-empty-line">{children}</div>
      {action ? <Button onClick={action.onClick}>{action.label}</Button> : null}
    </div>
  );
}
