// Home.test.tsx — the Home tab: one link row per open need, each to the session that asks, the
// needs chip, and "Nothing needs you" when none waits.
import { screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { DESKTOP_QUERY, TABLET_QUERY } from "../../src/app/layout.ts";
import { Home } from "../../src/pages/Home.tsx";
import type { NeedItem, RegistrySession } from "../../src/registry.ts";
import { makeCtx, makeRegistry, makeState, renderIn, stubMedia } from "../helpers.tsx";

const row = (id: string, title: string): RegistrySession => ({
  session_id: id,
  title,
  state: "needs-you",
  last_event: "Notification",
  last_event_at: "2026-10-08T11:00:00Z",
});

const need = (id: number, session: string, state = "open"): NeedItem => ({
  id,
  session,
  kind: "permission",
  summary: "Wants to run a command",
  created_at: "2026-10-08T11:00:00Z",
  state,
});

/** Mounts Home over a registry holding these sessions and needs. */
function mount(sessions: RegistrySession[], needs: NeedItem[]): void {
  renderIn(<Home />, makeCtx(makeState(), { registry: makeRegistry({ sessions, needs }) }));
}

beforeEach(() => {
  stubMedia({ [TABLET_QUERY]: true, [DESKTOP_QUERY]: true });
});

afterEach(() => {
  document.body.innerHTML = "";
});

describe("Home", () => {
  it("links each open need to the session that asks, named by its title", () => {
    mount(
      [row("s-1", "Move the backup"), row("s-2", "Fix sizing")],
      [need(1, "s-1"), need(2, "s-2", "answered")],
    );
    const links = [...document.querySelectorAll("a[data-item]")] as HTMLAnchorElement[];
    expect(links.map((a) => a.getAttribute("href"))).toEqual(["#/sessions/s-1"]);
    expect(links[0]?.textContent).toContain("Move the backup");
    expect(links[0]?.textContent).toContain("Wants to run a command");
  });

  it("leads with the title and the count of what needs the owner", () => {
    mount([row("s-1", "Move the backup")], [need(1, "s-1")]);
    expect(screen.getByRole("heading", { name: "Home" })).toBeDefined();
    expect(document.querySelector("[data-needs-chip]")?.textContent).toBe("1 needs you");
  });

  it("says so when nothing needs the owner, with no chip", () => {
    mount([], []);
    expect(screen.getByText("Nothing needs you")).toBeDefined();
    expect(document.querySelector("[data-needs-chip]")).toBeNull();
    expect(document.querySelector("a[data-item]")).toBeNull();
  });

  it("leaves the title to the shell's header on a phone", () => {
    stubMedia({ [TABLET_QUERY]: false, [DESKTOP_QUERY]: false });
    mount([], []);
    expect(screen.queryByRole("heading", { name: "Home" })).toBeNull();
  });
});
