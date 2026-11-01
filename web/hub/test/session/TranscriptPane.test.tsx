// TranscriptPane.test.tsx — the session page's transcript: text open (a prompt, a reply), each
// stretch of tool calls one folded row that opens to its calls, every row keyed by its item id,
// the page opening at the end once, and the items that arrive later sitting under a New pill
// that holds the reading place: the page never scrolls by itself, a tap on the pill goes to the
// first new item, and reading past it clears the pill.
import { act, cleanup, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it } from "vite-plus/test";
import { TranscriptPane } from "../../src/session/TranscriptPane.tsx";
import type { ConvoItem } from "../../src/session/types.ts";
import { NOW } from "../helpers.tsx";

afterEach(cleanup);

const AT = "2026-10-08T11:57:00Z";

const prompt = (id: string, text: string): ConvoItem => ({ id, kind: "user", at: AT, text });
const reply = (id: string, text: string): ConvoItem => ({ id, kind: "assistant", at: AT, text });
const call = (id: string, tool: string, command: string): ConvoItem => ({
  id,
  kind: "tool_use",
  at: AT,
  tool,
  tool_use_id: `u-${id}`,
  input: JSON.stringify({ command }),
});
const answer = (id: string, of: string, isError = false): ConvoItem => ({
  id,
  kind: "tool_result",
  at: AT,
  tool_use_id: `u-${of}`,
  ...(isError ? { is_error: true } : {}),
});

/** A prompt, a reply, then a run of three calls (all answered) and a closing reply. */
const conversation = (): ConvoItem[] => [
  prompt("p1", "Move the backup job to pve2"),
  reply("r1", "Looking at how the job runs today."),
  call("c1", "Read", "cat /etc/cron.d/backup"),
  answer("a1", "c1"),
  call("c2", "Grep", "grep -r backup /etc/cron.d/"),
  answer("a2", "c2"),
  call("c3", "Bash", "ls /mnt/tank"),
  answer("a3", "c3"),
  reply("r2", "The job runs nightly from cron."),
];

const mount = (items: ConvoItem[]): ReturnType<typeof render> =>
  render(<TranscriptPane items={items} now={NOW} />);

describe("the transcript pane", () => {
  it("shows a prompt's text, a reply's text and one folded row for a run that opens to its calls", () => {
    mount(conversation());
    const pane = document.querySelector("[data-block='transcript']") as HTMLElement;
    expect(pane).not.toBeNull();
    expect(screen.getByText("Move the backup job to pve2")).toBeDefined();
    expect(screen.getByText("Looking at how the job runs today.")).toBeDefined();
    expect(screen.getByText("The job runs nightly from cron.")).toBeDefined();

    const runs = pane.querySelectorAll("[data-run]");
    expect(runs).toHaveLength(1);
    const toggle = screen.getByRole("button", { name: /Ran 2 reads and searches, 1 command/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    // the row names the last call's target and carries no per-call line while folded
    expect(toggle.textContent).toContain("last: ls /mnt/tank");
    expect(pane.querySelector("[data-run-calls]")).toBeNull();
    expect(pane.textContent).not.toContain("cat /etc/cron.d/backup");

    void act(() => {
      toggle.click();
    });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    const calls = [...pane.querySelectorAll<HTMLElement>("[data-run-calls] [data-call]")];
    expect(calls.map((c) => c.textContent)).toEqual([
      expect.stringContaining("cat /etc/cron.d/backup"),
      expect.stringContaining("grep -r backup /etc/cron.d/"),
      expect.stringContaining("ls /mnt/tank"),
    ]);
    void act(() => {
      toggle.click();
    });
    expect(pane.querySelector("[data-run-calls]")).toBeNull();
  });

  it("keeps a run row's words whole and cuts only the target when the row is narrow", () => {
    mount(conversation());
    const row = document.querySelector("[data-run] button") as HTMLElement;
    const label = screen.getByText(/^Ran 2 reads and searches, 1 command$/);
    // the words never truncate: they wrap before they clip
    expect(label.className).not.toContain("truncate");
    expect(label.className).toContain("min-w-0");
    // the target is the part that gives way
    const target = row.querySelector("code") as HTMLElement;
    expect(target.className).toContain("truncate");
    expect(target.className).toContain("min-w-0");
    expect(row.className).toContain("!h-auto");
  });

  it("keys every row by its conversation item id", () => {
    mount(conversation());
    const ids = [...document.querySelectorAll<HTMLElement>("[data-block='transcript'] [data-item]")].map(
      (el) => el.dataset.item,
    );
    // the run keys by its first call's item id
    expect(ids).toEqual(["p1", "r1", "c1", "r2"]);
  });

  it("marks a run that still runs and a call that failed", () => {
    mount([
      prompt("p1", "Run the tests"),
      call("c1", "Bash", "bun test"),
      answer("a1", "c1", true),
      call("c2", "Bash", "bun run build"),
    ]);
    const run = document.querySelector("[data-run]") as HTMLElement;
    expect(run.hasAttribute("data-running")).toBe(true);
    expect(run.textContent).toContain("Running 2 commands");
    void act(() => {
      (run.querySelector("button") as HTMLElement).click();
    });
    const calls = run.querySelectorAll<HTMLElement>("[data-call]");
    expect(calls[0]?.hasAttribute("data-error")).toBe(true);
    expect(calls[1]?.hasAttribute("data-running")).toBe(true);
  });

  it("shows no New pill on the first paint", () => {
    mount(conversation());
    expect(document.querySelector("[data-new]")).toBeNull();
  });
});

describe("the New pill", () => {
  /** happy-dom lays nothing out: the pane gets a 300 px window over 1000 px of content, and a
   *  scrollTop that clamps to the range a browser allows. `top` maps an element to where its
   *  top edge sits in the content. */
  function stubPane(pane: HTMLElement, tops: () => Map<Element, number>): void {
    let scrollTop = 0;
    Object.defineProperty(pane, "scrollHeight", { configurable: true, get: () => 1000 });
    Object.defineProperty(pane, "clientHeight", { configurable: true, get: () => 300 });
    Object.defineProperty(pane, "scrollTop", {
      configurable: true,
      get: () => scrollTop,
      set: (v: number) => {
        scrollTop = Math.max(0, Math.min(v, 700));
      },
    });
    pane.getBoundingClientRect = () => new DOMRect(0, 0, 400, 300);
    const place = (el: Element): void => {
      (el as HTMLElement).getBoundingClientRect = () => {
        const top = (tops().get(el) ?? 0) - scrollTop;
        return new DOMRect(0, top, 400, 40);
      };
    };
    for (const el of pane.querySelectorAll("[data-item], [data-new]")) place(el);
  }

  it("puts items that arrive after the first paint under a New pill and never moves the scroll", () => {
    const view = mount(conversation());
    const pane = document.querySelector("[data-block='transcript']") as HTMLElement;
    // the page opened at the end: 700 is the end of a 1000 px page in a 300 px window
    stubPane(pane, () => new Map());
    pane.scrollTop = 450;
    const before = pane.scrollTop;

    view.rerender(
      <TranscriptPane
        items={[
          ...conversation(),
          reply("r3", "First run finished clean."),
          reply("r4", "Checking the size."),
        ]}
        now={NOW}
      />,
    );
    const pill = screen.getByRole("button", { name: "New" });
    expect(pill.closest("[data-new]")).not.toBeNull();
    // the pill sits right before the first new item
    const next = pill.closest("[data-new]")?.nextElementSibling as HTMLElement;
    expect(next.dataset.item).toBe("r3");
    expect(pane.scrollTop).toBe(before);
  });

  it("goes to the first new item on a tap", () => {
    const view = mount(conversation());
    const pane = document.querySelector("[data-block='transcript']") as HTMLElement;
    view.rerender(
      <TranscriptPane items={[...conversation(), reply("r3", "First run finished clean.")]} now={NOW} />,
    );
    const first = document.querySelector("[data-item='r3']") as HTMLElement;
    stubPane(pane, () => new Map([[first, 620]]));
    pane.scrollTop = 0;
    void act(() => {
      screen.getByRole("button", { name: "New" }).click();
    });
    // the first new item's top edge now sits at the pane's top, less the pill's own room
    const top = first.getBoundingClientRect().top;
    expect(top).toBeGreaterThanOrEqual(0);
    expect(top).toBeLessThan(60);
  });

  it("clears once the reader has scrolled past the first new item", () => {
    const view = mount(conversation());
    const pane = document.querySelector("[data-block='transcript']") as HTMLElement;
    view.rerender(
      <TranscriptPane items={[...conversation(), reply("r3", "First run finished clean.")]} now={NOW} />,
    );
    const first = document.querySelector("[data-item='r3']") as HTMLElement;
    stubPane(pane, () => new Map([[first, 900]]));
    // the new item is below the window: reading has not reached it
    pane.scrollTop = 100;
    void act(() => {
      pane.dispatchEvent(new Event("scroll"));
    });
    expect(document.querySelector("[data-new]")).not.toBeNull();
    // the window now holds the whole new item
    pane.scrollTop = 700;
    void act(() => {
      pane.dispatchEvent(new Event("scroll"));
    });
    expect(document.querySelector("[data-new]")).toBeNull();
  });
});
