// RadarTab.test.tsx — the Radar tab's pane: nothing before the controller runs, the connecting
// skeleton before the first snapshot, the view the address names, and the drawer keys it answers
// while it is on screen.

import { fireEvent } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { RadarTab } from "../../../src/radar/app/RadarTab.tsx";
import { ensureRadar, radarController, stopRadar } from "../../../src/radar/mount.tsx";
import { makeCtx, makeState, renderIn } from "../../helpers.tsx";
import { radarIo, settleRadar, snapshot } from "./io.ts";

/** Boots a controller on `io`, renders the pane and waits for the stream to attach. */
async function booted(io: ReturnType<typeof radarIo>["io"], view: "overview" | "routers" = "overview") {
  const controller = ensureRadar(io);
  const page = renderIn(<RadarTab view={view} />);
  await settleRadar(); // the backfill settles and the stream attaches
  return { controller, page };
}

/** The snapshot through the pane's own stream, then let the repaint land. */
async function deliver(streams: ReturnType<typeof radarIo>["streams"]): Promise<void> {
  streams[0]?.open();
  streams[0]?.push(snapshot());
  await settleRadar();
}

afterEach(() => {
  vi.unstubAllGlobals();
  stopRadar();
  document.body.textContent = "";
});

describe("the Radar tab pane", () => {
  it("renders nothing until a controller runs — the shell's empty pane stands instead", () => {
    const { container } = renderIn(<RadarTab view="overview" />);
    expect(container.querySelector("[data-radar-view]")).toBeNull();
  });

  it("shows the connecting skeleton until the first snapshot lands, then the view", async () => {
    const world = radarIo();
    const { page } = await booted(world.io);
    expect(page.container.querySelector("[data-radar-view]")?.getAttribute("aria-label")).toBe("Overview");
    expect(page.container.textContent).toContain("Connecting to radar.");
    expect(page.container.querySelectorAll(".skeleton").length).toBeGreaterThan(0);
    await deliver(world.streams);
    expect(page.container.querySelector(".skeleton")).toBeNull();
    expect(page.container.textContent).toContain("Spend today");
  });

  it("draws the view the address names, mapping the shell's routers word to radar's router tab", async () => {
    const world = radarIo();
    const { controller } = await booted(world.io, "routers");
    await deliver(world.streams);
    expect(controller.getState().tab).toBe("router");
    const pane = document.querySelector("[data-radar-view]");
    expect(pane?.getAttribute("aria-label")).toBe("Routers");
    expect(pane?.textContent).toContain("Router health");
  });

  it("answers j, k and Escape with the drawer's actions, and no key while a field is typed in", async () => {
    const world = radarIo();
    const { controller } = await booted(world.io);
    await deliver(world.streams);
    const act = vi.spyOn(controller, "act");
    fireEvent.keyDown(document, { key: "j" });
    fireEvent.keyDown(document, { key: "K" });
    fireEvent.keyDown(document, { key: "Escape" });
    expect(act.mock.calls.map((call) => call[0])).toEqual(["next-request", "prev-request", "close-drawer"]);
    // the shell's own keys and anything typed into a field never reach the drawer
    fireEvent.keyDown(document, { key: "j", ctrlKey: true });
    const field = document.createElement("input");
    document.body.append(field);
    fireEvent.keyDown(field, { key: "j" });
    expect(act.mock.calls).toHaveLength(3);
    act.mockRestore();
  });

  it("names the error a failed load leaves, above the pane still waiting for its snapshot", async () => {
    const world = radarIo({
      "/api/tools?limit=1000": { ok: false, status: 503, json: async () => ({}) },
    });
    const { page } = await booted(world.io);
    expect(radarController()?.getState().error).not.toBeNull();
    expect(page.container.querySelector("[data-radar-view]")?.textContent).toContain("Connecting to radar.");
  });

  it("tears down with stopRadar: the stream closes and the next address builds a fresh controller", async () => {
    const first = radarIo();
    await booted(first.io);
    await deliver(first.streams);
    const oldController = radarController();
    stopRadar();
    expect(first.streams[0]?.closed).toBe(true);
    expect(radarController()).toBeNull();
    // the next radar address builds a new controller, on its own stream
    const second = radarIo();
    const again = ensureRadar(second.io);
    await settleRadar();
    expect(again).not.toBe(oldController);
    expect(second.streams).toHaveLength(1);
  });

  it("shares one controller across addresses", () => {
    const world = radarIo();
    const one = ensureRadar(world.io);
    expect(ensureRadar(world.io)).toBe(one);
    expect(radarController()).toBe(one);
  });

  it("draws no self-check badge over the Overview: the self-check lives in Settings", async () => {
    // the self-check route answers, so a mounted badge would paint
    const today = { requests: 3, input: 1, output: 1, cacheRead: 0, cacheWrite: 0 };
    const answer = { day: "2026-10-25", radar: today, transcripts: today, driftPct: 0 };
    vi.stubGlobal("fetch", (url: string) =>
      Promise.resolve(
        url === "/api/radar/api/selfcheck"
          ? new Response(JSON.stringify(answer))
          : new Response("{}", { status: 404 }),
      ),
    );
    const world = radarIo();
    const { page } = await booted(world.io);
    await deliver(world.streams);
    await settleRadar();
    expect(page.container.textContent).toContain("Spend today");
    expect(page.container.querySelector("[data-selfcheck]")).toBeNull();
  });

  it("paints under the shell's own context", () => {
    const world = radarIo();
    ensureRadar(world.io);
    const { container } = renderIn(<RadarTab view="overview" />, makeCtx(makeState()));
    expect(container.querySelector("[data-radar-view]")).not.toBeNull();
  });
});
