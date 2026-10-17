import { render } from "@testing-library/preact";
import type { ComponentChildren } from "preact";
import { vi } from "vite-plus/test";
import { AppContext, type AppValue } from "../../../src/radar/app/context.ts";
import { type ClientState, initialClientState } from "../../../src/radar/state.ts";

/** Render `ui` inside the app context: `state` overrides the initial client state, `act` is a spy. */
export function renderApp(ui: ComponentChildren, state: Partial<ClientState> = {}, now = 1_790_000_000_000) {
  const act = vi.fn<AppValue["act"]>();
  const value: AppValue = { state: { ...initialClientState(), ...state }, now, url: "127.0.0.1:4000", act };
  const result = render(<AppContext.Provider value={value}>{ui}</AppContext.Provider>);
  return { ...result, act, value };
}
