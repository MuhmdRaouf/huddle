import { screen, within } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { OTEL_PATH, type OtelAnswer } from "../../../src/radar/app/views/OtelUsage.tsx";
import { UsageView } from "../../../src/radar/app/views/Usage.tsx";
import { fmtTokens, fmtUsd } from "../../../src/radar/fmt.ts";
import { renderApp } from "./render.tsx";
import { expectTokensOnly } from "./tokens.ts";

// every render in this file paints only token classes and labels its table cells for the phone
afterEach(() => expectTokensOnly());

const ANSWER: OtelAnswer = {
  source: "otel",
  points: 6,
  byModel: [
    {
      model: "claude-sonnet-4-5",
      input: 1200,
      output: 340,
      cacheRead: 56000,
      cacheCreation: 2048,
      costUsd: 0.0315,
      sessions: 1,
    },
  ],
  rows: [],
};

function answering(body: unknown, status = 200) {
  const fetcher = vi.fn(async () => new Response(JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

async function pickOtel() {
  const source = screen.getByRole("group", { name: "Source" });
  await userEvent.click(within(source).getByRole("button", { name: "OpenTelemetry" }));
}

describe("the usage view's source toggle", () => {
  it("starts on the transcripts and names both sources", () => {
    answering(ANSWER);
    renderApp(<UsageView />);
    const source = screen.getByRole("group", { name: "Source" });
    expect(within(source).getByRole("button", { name: "Transcripts" }).getAttribute("aria-pressed")).toBe(
      "true",
    );
    expect(within(source).getByRole("button", { name: "OpenTelemetry" }).getAttribute("aria-pressed")).toBe(
      "false",
    );
    expect(screen.getByRole("status", { name: "Loading usage" })).toBeTruthy();
  });

  it("shows Claude Code's export per model, apart from the transcripts", async () => {
    const fetcher = answering(ANSWER);
    renderApp(<UsageView />);
    await pickOtel();
    const table = await screen.findByRole("table");
    expect(fetcher).toHaveBeenCalledWith(OTEL_PATH);
    const row = within(table).getAllByRole("row")[1];
    expect(row?.textContent).toContain("Sonnet 4.5");
    expect(row?.textContent).toContain(fmtTokens(1200));
    expect(row?.textContent).toContain(fmtTokens(56000));
    expect(row?.textContent).toContain(fmtUsd(0.0315));
    expect(screen.getByText(/never added to the other views/)).toBeTruthy();
    expect(screen.queryByRole("status", { name: "Loading usage" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Transcripts" }));
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("says how to point Claude Code at the hub while no export has landed", async () => {
    answering({ ...ANSWER, points: 0, byModel: [] });
    renderApp(<UsageView />);
    await pickOtel();
    expect(await screen.findByText(/OTEL_EXPORTER_OTLP_ENDPOINT/)).toBeTruthy();
  });

  it("names a read that failed with no Error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Promise.reject("offline")),
    );
    renderApp(<UsageView />);
    await pickOtel();
    expect((await screen.findByRole("alert")).textContent).toContain("offline");
  });

  it("names a failed read", async () => {
    answering({ error: "the OpenTelemetry intake is off" }, 503);
    renderApp(<UsageView />);
    await pickOtel();
    expect((await screen.findByRole("alert")).textContent).toContain("503");
  });

  // an unmounted effect's cleanup runs after the next paint (a frame, or preact's 100 ms
  // fallback), so each answer lands 150 ms after the switch back
  it("drops an answer that lands after the source switched back", async () => {
    let settle: (response: Response) => void = () => undefined;
    let fail: (reason: unknown) => void = () => undefined;
    const pending = vi.fn(
      () =>
        new Promise<Response>((resolve, reject) => {
          settle = resolve;
          fail = reject;
        }),
    );
    vi.stubGlobal("fetch", pending);
    renderApp(<UsageView />);
    await pickOtel();
    expect(screen.getByRole("status", { name: "Loading OpenTelemetry" })).toBeTruthy();
    await vi.waitFor(() => expect(pending).toHaveBeenCalledTimes(1));
    await userEvent.click(screen.getByRole("button", { name: "Transcripts" }));
    await screen.findByRole("status", { name: "Loading usage" });
    await new Promise((resolve) => setTimeout(resolve, 150));
    settle(new Response(JSON.stringify(ANSWER)));
    await new Promise((resolve) => setTimeout(resolve, 20));
    await pickOtel();
    await vi.waitFor(() => expect(pending).toHaveBeenCalledTimes(2));
    await userEvent.click(screen.getByRole("button", { name: "Transcripts" }));
    await screen.findByRole("status", { name: "Loading usage" });
    await new Promise((resolve) => setTimeout(resolve, 150));
    fail(new Error("gone"));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(pending).toHaveBeenCalledTimes(2);
  });
});
