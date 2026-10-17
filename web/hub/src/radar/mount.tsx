// mount.tsx — radar inside the huddle shell: one controller for the whole app (the sub-tab row's
// range and export, the views and the Settings › Radar section all read it), built on the
// browser's IO the first time an address needs it and started once — its stream and its polls
// then run beside huddle's own for the rest of the session. Every view reads it through
// <RadarProvider>, which repaints on each state change; main.tsx subscribes the shell's paint.

import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { type Act, AppContext, type AppValue } from "./app/context.ts";
import { type Io, RadarController } from "./app/controller.ts";
import { browserIo } from "./browser.ts";

let controller: RadarController | null = null;

/** The one controller, built and started on the first radar address (a Radar tab view, or the
 *  Settings › Radar section). Later calls answer the same one. The IO is the browser's own
 *  outside tests, which hand theirs in. */
export function ensureRadar(io: Io = browserIo()): RadarController {
  if (controller !== null) return controller;
  controller = new RadarController(io);
  const act: Act = (action, value = "") => controller?.act(action, value);
  window.addEventListener("hashchange", () => act("hash", location.hash));
  controller.start();
  return controller;
}

/** The controller when one exists, without starting it; the chrome reads it for badges and ranges. */
export function radarController(): RadarController | null {
  return controller;
}

/** Stops the controller and forgets it (a sign-out): the stream and the timers go, and the next
 *  radar address after a fresh sign-in builds a new one. */
export function stopRadar(): void {
  controller?.stop();
  controller = null;
}

/** Reads the controller's state, repainting on every change; null before the first radar address. */
export function useRadarState() {
  const [state, setState] = useState(() => controller?.getState() ?? null);
  useEffect(() => {
    if (controller === null) return;
    setState(controller.getState());
    return controller.subscribe(setState);
  }, []);
  return state;
}

/** One reader action on the controller, when one runs; a no-op before the first radar address. */
export const radarAct: Act = (action, value = "") => {
  controller?.act(action, value);
};

/** Wraps the children in the app context every radar view reads, repainting on each state change
 *  and once a second (the relative times every view reads move with the clock). Renders nothing
 *  before the first radar address, so the caller's own empty pane stands then. */
export function RadarProvider({ children }: { children: ComponentChildren }): JSX.Element | null {
  const state = useRadarState();
  const [, setTick] = useState(0);
  useEffect(() => {
    const timer = setInterval(() => setTick((tick) => tick + 1), 1000);
    return () => clearInterval(timer);
  }, []);
  if (state === null) return null;
  const value: AppValue = {
    state,
    now: Date.now(),
    url: location.host === "" ? "127.0.0.1" : location.host,
    act: radarAct,
  };
  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}
