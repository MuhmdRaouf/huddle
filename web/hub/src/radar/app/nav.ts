// nav.ts — which Radar views the phone's nav holds and which open from the Overview's More list.
// The tablet and the desktop list every view in a sidebar.

import { RADAR_VIEWS, type RadarView } from "../../app/router.ts";

/** The phone's four views, in nav order. */
export const PHONE_VIEWS: readonly RadarView[] = ["overview", "requests", "costs", "alerts"];

/** Every other view, in sub-tab order: the rows of the Overview's More list on the phone. */
export const MORE_VIEWS: readonly RadarView[] = RADAR_VIEWS.filter((view) => !PHONE_VIEWS.includes(view));
