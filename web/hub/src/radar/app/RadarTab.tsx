// RadarTab.tsx — the Radar tab's pane inside huddle's shell: a header with the self-check badge
// over the active view. The shell draws the chrome around it (the sub-tab row, the range, the
// export); a session row in a view opens the Session screen. The drawer and the agent transcript
// ride along, and the keys they answer (J/K step, Escape closes) live only while this pane is on
// screen.

import type { ComponentType, JSX } from "preact";
import { useEffect } from "preact/hooks";
import type { RadarView } from "../../app/router.ts";
import { RadarProvider, radarAct, useRadarState } from "../mount.tsx";
import "../radar.css";
import type { Tab } from "../state.ts";
import { AgentTranscript } from "./AgentTranscript.tsx";
import { Banner } from "./Banner.tsx";
import { useApp } from "./context.ts";
import { Drawer } from "./Drawer.tsx";
import { SelfCheckBadge } from "./SelfCheckBadge.tsx";
import { AgentsView } from "./views/Agents.tsx";
import { AlertsView } from "./views/Alerts.tsx";
import { BlocksView } from "./views/Blocks.tsx";
import { CostsView } from "./views/Costs.tsx";
import { ModelsView } from "./views/Models.tsx";
import { OverviewView } from "./views/Overview.tsx";
import { RequestsView } from "./views/Requests.tsx";
import { RouterView } from "./views/Router.tsx";
import { SessionAgentsView } from "./views/SessionAgents.tsx";
import { TimelineView } from "./views/Timeline.tsx";
import { ToolsView } from "./views/Tools.tsx";
import { UsageView } from "./views/Usage.tsx";

/** The views by radar's own tab id; the settings view lives in Settings › Radar, not here. */
const VIEWS: Record<Exclude<Tab, "settings">, ComponentType> = {
  overview: OverviewView,
  agents: AgentsView,
  requests: RequestsView,
  tools: ToolsView,
  timeline: () => <TimelineView />,
  models: ModelsView,
  costs: CostsView,
  usage: UsageView,
  blocks: BlocksView,
  alerts: AlertsView,
  router: RouterView,
};

/** The label a view's pane carries for screen readers. */
const VIEW_LABEL: Record<RadarView, string> = {
  overview: "Overview",
  costs: "Costs",
  models: "Models",
  usage: "Usage",
  blocks: "Blocks",
  requests: "Requests",
  tools: "Tools",
  agents: "Agents",
  routers: "Routers",
  alerts: "Alerts",
  timeline: "Timeline",
};

/** Skeleton blocks shaped like the overview, shown until the first snapshot arrives. */
function Connecting(): JSX.Element {
  return (
    <div class="grid gap-5" aria-live="polite">
      <p class="muted text-row">Connecting to radar. The first snapshot arrives in a moment.</p>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-5">
        {[0, 1, 2, 3, 4].map((card) => (
          <div key={card} class="skeleton h-24" />
        ))}
      </div>
      <div class="skeleton h-64" />
    </div>
  );
}

/** The view a radar tab id draws: the picked session's own table on Agents, the mapped view
 *  elsewhere; the Overview answers any id this pane never names. */
function viewOf(tab: Tab, picked: boolean): ComponentType {
  if (tab === "agents" && picked) return SessionAgentsView;
  if (tab === "settings") return OverviewView;
  return VIEWS[tab as Exclude<Tab, "settings">] ?? OverviewView;
}

/** The pane's body: the view the controller's tab names, over the pieces every view shares. */
function RadarBody({ view }: { view: RadarView }): JSX.Element {
  const { state } = useApp();
  useEffect(() => {
    // the address leads; this holds the controller's tab on it even when the hashchange the shell
    // already handled was the controller's own rewrite
    const named: Tab = view === "routers" ? "router" : view;
    if (state.tab !== named) radarAct("tab", named);
  }, [view, state.tab]);
  const View = viewOf(state.tab, state.session !== null);
  return (
    <>
      {state.error !== null && <Banner message={state.error} />}
      {state.summary === null ? <Connecting /> : <View />}
    </>
  );
}

/** The drawer action one key asks for while this pane is on screen; null for every key the
 *  shell owns — its searches, its shortcuts, anything typed into a field. */
function drawerKeyOf(event: KeyboardEvent): "close-drawer" | "next-request" | "prev-request" | null {
  if (event.key === "Escape") return "close-drawer";
  if (event.target instanceof HTMLElement && event.target.matches("input, textarea, select")) return null;
  if (event.metaKey || event.ctrlKey || event.altKey) return null;
  if (event.key === "j" || event.key === "J") return "next-request";
  if (event.key === "k" || event.key === "K") return "prev-request";
  return null;
}

/** The Radar tab: the active view inside the shell's panes. Renders nothing until the controller
 *  runs (main.tsx starts it on the first radar address). */
export function RadarTab({ view }: { view: RadarView }): JSX.Element | null {
  const state = useRadarState();
  const live = state !== null;
  useEffect(() => {
    if (!live) return;
    const on = (event: KeyboardEvent): void => {
      const action = drawerKeyOf(event);
      if (action !== null) radarAct(action);
    };
    document.addEventListener("keydown", on);
    return () => document.removeEventListener("keydown", on);
  }, [live]);
  if (state === null) return null;
  return (
    <section
      class="mx-auto w-full max-w-[1600px] px-4 py-5 sm:px-6 lg:px-8"
      data-radar-view=""
      aria-label={VIEW_LABEL[view]}
    >
      <header class="mb-4 flex justify-end empty:hidden">
        <SelfCheckBadge />
      </header>
      <RadarProvider>
        <RadarBody view={view} />
        <Drawer />
        <AgentTranscript />
      </RadarProvider>
    </section>
  );
}
