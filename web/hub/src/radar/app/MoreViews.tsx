// MoreViews.tsx — the phone's More list under the Overview: one row per view the phone's nav
// leaves out, each opening its view, drawn with the same list classes as Settings.

import type { JSX } from "preact";
import { radarLabel } from "../../app/router.ts";
import { Icon } from "../../icons.tsx";
import { MORE_VIEWS } from "./nav.ts";

/** The More list: a heading over one row per remaining view. */
export function MoreViews(): JSX.Element {
  return (
    <nav aria-label="More views" class="grid gap-1.5" data-radar-more="">
      <h2 class="text-xs font-semibold tracking-[0.06em] text-[var(--ui-muted)] uppercase">More</h2>
      <ul class="ui-list">
        {MORE_VIEWS.map((view) => (
          <li key={view} class="ui-list-item">
            <a href={`#/radar/${view}`} data-view={view} class="ui-list-row hover:bg-[var(--ui-surface-2)]">
              <span class="ui-list-text">
                <span class="ui-list-title">{radarLabel(view)}</span>
              </span>
              <span class="ui-list-trailing text-[var(--ui-muted)]">
                <Icon name="right" />
              </span>
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
}
