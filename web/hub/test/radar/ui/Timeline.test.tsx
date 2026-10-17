import { fireEvent, screen } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { TimelineView } from "../../../src/radar/app/views/Timeline.tsx";
import type { AuditRow } from "../../../src/radar/audit.ts";
import type { SessionListItem } from "../../../src/radar/shared/api.ts";
import type { EventRecord } from "../../../src/radar/shared/model.ts";
import type { ClientState } from "../../../src/radar/state.ts";
import { uiFixtures } from "../helpers.ts";
import { renderApp } from "./render.tsx";

const NOW = 1_790_000_000_000;

function makeEvent(over: Partial<EventRecord> = {}): EventRecord {
  return {
    seq: 1,
    ts: NOW,
    kind: "Stop",
    sessionId: null,
    agentId: null,
    label: null,
    payload: null,
    ...over,
  };
}

const fixtures = uiFixtures();

function item(id: string, over: Partial<SessionListItem> = {}): SessionListItem {
  return { ...fixtures.sessionListItem, id, ...over };
}

/** The audit read as the view's tests answer it: rows when given, a refusal when asked for one. */
const auditOf =
  (rows: AuditRow[] = []) =>
  async () => ({ rows, refused: null });
const auditRefused = async () => ({
  rows: [] as AuditRow[],
  refused: "Only the owner reads the audit log.",
});

function timeline(events: EventRecord[], over: Partial<ClientState> = {}) {
  return renderApp(<TimelineView auditFetch={auditOf()} auditEveryMs={0} />, { events, ...over }, NOW);
}

/** One audit row, newest last the way the log answers it. */
function decision(id: number, atMs: number, over: Partial<AuditRow> = {}): AuditRow {
  return {
    id,
    at: new Date(atMs).toISOString(),
    actor: "owner",
    action: "signin",
    target: "dashboard",
    detail: null,
    ...over,
  };
}

describe("Timeline view", () => {
  it("lays events on one spine with an icon, a plain label, the session's name and a relative time", () => {
    const { container } = timeline(
      [
        makeEvent({ seq: 1, ts: NOW - 60_000, kind: "Stop", sessionId: "sess-12345678", label: "end_turn" }),
        makeEvent({ seq: 2, ts: NOW, kind: "PostToolUseFailure", agentId: "agent-1" }),
        makeEvent({ seq: 3, ts: NOW - 5_000, kind: "Odd", sessionId: "other-session", label: "" }),
      ],
      { sessions: [item("sess-12345678", { project: "app" })] },
    );
    const items = [...container.querySelectorAll("ol.timeline > li")];
    expect(items).toHaveLength(3);
    const kinds = [...container.querySelectorAll("[data-kind-label]")].map((el) => el.textContent);
    expect(kinds).toEqual(["Tool failed", "Odd", "Turn ended"]); // newest first
    expect(container.querySelectorAll("[data-kind-label]")[2]?.getAttribute("title")).toBe("Stop");
    const labels = [...container.querySelectorAll("[data-event-label]")].map((el) => el.textContent);
    expect(labels).toEqual(["end_turn"]);
    const subs = [...container.querySelectorAll("[data-event-where]")].map((el) => el.textContent);
    expect(subs).toEqual(["No sessionagent-1", "other-seother-semain", "appsess-123main"]);
    const tss = [...container.querySelectorAll("time")].map((el) => el.textContent);
    expect(tss).toEqual(["now", "5s ago", "1m ago"]);
    expect(container.querySelector("time")?.getAttribute("datetime")).toBe(new Date(NOW).toISOString());
    const icons = [...container.querySelectorAll("ol [data-icon]")].map((el) => el.getAttribute("data-icon"));
    expect(icons).toEqual(["close", "dot", "pause"]);
    expect(panelMetaOf(container)).toBe("3 events, newest first");
  });

  it("shows the empty state when no events are in view", () => {
    const { container } = timeline([]);
    expect(container.querySelector(".empty-title")?.textContent).toBe("Nothing has happened yet");
    expect(container.querySelector(".empty-hint")?.textContent).toBe(
      "Sessions starting, agents spawning, compactions and errors, beside every allow, answer, send and pairing, land here in order.",
    );
  });

  it("merges the audit log's decisions between the events, newest first, and chips them apart", async () => {
    const { container } = renderApp(
      <TimelineView
        auditFetch={auditOf([
          decision(2, NOW - 30_000, { action: "allow", target: "Bash(git status)" }),
          decision(1, NOW - 120_000, { actor: "laptop", action: "signin", target: "dashboard" }),
        ])}
        auditEveryMs={0}
      />,
      { events: [makeEvent({ seq: 1, ts: NOW - 60_000, kind: "Stop" })] },
      NOW,
    );
    await screen.findByText("Allowed a tool");
    const kinds = [...container.querySelectorAll("[data-kind-label]")].map((el) => el.textContent);
    expect(kinds).toEqual(["Allowed a tool", "Turn ended", "Signed in"]); // by time, newest first
    expect(container.querySelector("[data-audit-refused]")).toBeNull();
    // the Decisions chip narrows to the audit rows alone
    fireEvent.click(container.querySelector('[data-chip="decisions"]') as HTMLElement);
    expect([...container.querySelectorAll("[data-kind-label]")].map((el) => el.textContent)).toEqual([
      "Allowed a tool",
      "Signed in",
    ]);
    fireEvent.click(container.querySelector('[data-chip="events"]') as HTMLElement);
    expect([...container.querySelectorAll("[data-kind-label]")].map((el) => el.textContent)).toEqual([
      "Turn ended",
    ]);
  });

  it("says why when the audit log is not this browser's to read", async () => {
    renderApp(<TimelineView auditFetch={auditRefused} auditEveryMs={0} />, { events: [] }, NOW);
    expect(await screen.findByText("Only the owner reads the audit log.")).toBeTruthy();
  });

  it("switches the feed between relative and clock times", () => {
    const events = [makeEvent({ seq: 1, ts: NOW - 60_000, sessionId: "a" })];
    const relative = timeline(events, { sessions: [item("a")] }).container.querySelector("time");
    expect(relative?.textContent).toBe("1m ago");
    const clock = timeline(events, { sessions: [item("a")], timeMode: "absolute" }).container.querySelector(
      "time",
    );
    expect(clock?.textContent).toMatch(/^\d{1,2}:\d{2}/);
    expect(clock?.getAttribute("title")).toBe("1m ago");
  });

  it("caps the feed at the newest 200 events", () => {
    const events = Array.from({ length: 205 }, (_, i) => makeEvent({ seq: i + 1, ts: NOW - i * 1000 }));
    const { container } = timeline(events);
    expect(container.querySelectorAll("ol.timeline > li")).toHaveLength(200);
    expect(panelMetaOf(container)).toBe("205 events, newest first");
  });

  it("reads kind, session and agent per event", () => {
    const { container } = timeline(
      [
        makeEvent({ seq: 1, kind: "UserPromptSubmit", sessionId: "s9", agentId: "agent-2", label: "hello" }),
        makeEvent({ seq: 2, kind: "CompletelyNew" }),
      ],
      { sessions: [item("s9", { project: "web" })] },
    );
    const kinds = [...container.querySelectorAll("[data-kind-label]")].map((el) => el.textContent);
    expect(kinds).toEqual(["Prompt", "CompletelyNew"]);
    const icons = [...container.querySelectorAll("ol [data-icon]")].map((el) => el.getAttribute("data-icon"));
    expect(icons).toEqual(["message", "dot"]);
    expect(screen.getByText("hello")).toBeTruthy();
    expect(screen.getByText("web")).toBeTruthy();
    expect(screen.getByText("agent-2")).toBeTruthy();
  });
});

/** The panel head's muted count beside the title (the shared Panel's meta slot). */
function panelMetaOf(container: Element): string {
  return container.querySelector("section h2")?.nextElementSibling?.textContent ?? "";
}
