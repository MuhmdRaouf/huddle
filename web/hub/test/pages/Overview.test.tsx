// Overview.test.tsx — the channel page's Messages card: the channel's message events newest
// last, each with its sender, its recipient, the body and the age; the other families stay off
// the list, and a channel with no messages carries no card.

import { screen } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { Overview } from "../../src/pages/Overview.tsx";
import type { FeedEvent } from "../../src/store.ts";
import { makeCtx, makeState, renderIn } from "../helpers";

const ev = (seq: number, topic: string, extra: Record<string, unknown> = {}): FeedEvent =>
  ({ seq, topic, ts: "2026-10-08T11:59:00Z", ...extra }) as FeedEvent;

describe("the channel's Messages card", () => {
  it("lists the message events newest last: sender, recipient, body, age", () => {
    renderIn(
      <Overview />,
      makeCtx(
        makeState({
          timeline: [
            ev(1, "session.joined", { from: "alpha" }),
            ev(2, "msg", { from: "alpha", to: "beta", msg: "ping" }),
            ev(3, "msg", { from: "beta", to: "alpha", msg: "pong" }),
          ],
        }),
      ),
    );
    const rows = [...document.querySelectorAll("[data-messages] [data-seq]")];
    expect(rows.map((r) => r.getAttribute("data-seq"))).toEqual(["2", "3"]);
    expect(rows[0]?.textContent).toContain("alpha");
    expect(rows[0]?.textContent).toContain("beta");
    expect(rows[0]?.textContent).toContain("ping");
    expect(rows[0]?.querySelector("time")).not.toBeNull();
    expect(screen.getByRole("heading", { name: "Messages" })).not.toBeNull();
  });

  it("names the owner You and an unaddressed message everyone", () => {
    renderIn(
      <Overview />,
      makeCtx(
        makeState({
          timeline: [
            ev(1, "msg", { from: "owner", msg: "stand by" }),
            ev(2, "msg", { from: "alpha", msg: "hi all" }),
          ],
        }),
      ),
    );
    const rows = [...document.querySelectorAll("[data-messages] [data-seq]")];
    expect(rows[0]?.textContent).toContain("You");
    expect(rows[0]?.textContent).toContain("everyone");
    expect(rows[1]?.textContent).toContain("everyone");
  });

  it("keeps the other families off the list, and shows no card without messages", () => {
    renderIn(
      <Overview />,
      makeCtx(
        makeState({
          timeline: [
            ev(1, "task.status", { ref: "t1", data: { status: "doing" } }),
            ev(2, "session.joined", { from: "alpha" }),
          ],
        }),
      ),
    );
    expect(screen.queryByRole("heading", { name: "Messages" })).toBeNull();
    expect(document.querySelector("[data-messages]")).toBeNull();
  });
});
