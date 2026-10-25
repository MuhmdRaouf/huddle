// Chat.test.tsx — the channel's chat: the composer sends to everyone or one member through the
// channel's send op and clears on success, keeps the words on a failure, and says each member's
// route from the registry; the members sheet lists
// the live sessions with their claims, and its remove asks once before it removes.

import { fireEvent, screen, waitFor } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import type { HuddleContextValue } from "../../src/app/context.tsx";
import { Chat } from "../../src/chat/Chat.tsx";
import type { FeedEvent, RosterSession } from "../../src/store.ts";
import type { RegistrySession } from "../../src/registry.ts";
import { legacyClasses, makeCtx, makeRegistry, makeState, NOW, renderIn } from "../helpers";

const roster = [
  { name: "gamma", state: "working" },
  { name: "delta", state: "idle" },
  { name: "gone", state: "left" },
] as RosterSession[];

const claims = [{ path: "/work/app/src/chat.ts", holder: "gamma", expires: "2026-10-08T13:00:00Z" }];

/** daisyUI's form classes, which the shared legacy check does not name. */
const DAISY_FORM = /^(select|textarea|input)(-.+)?$/;

/** The daisyUI form classes painted under the document. */
const daisyForms = (): string[] =>
  [...document.querySelectorAll("[class]")].flatMap((el) =>
    (el.getAttribute("class") ?? "").split(/\s+/).filter((c) => DAISY_FORM.test(c)),
  );

/** The owner's message and a session's answer. */
const talk: FeedEvent[] = [
  { seq: 1, topic: "msg", from: "owner", to: "gamma", msg: "status?", ts: "2026-10-08T11:58:00Z" },
  { seq: 2, topic: "reply", from: "gamma", to: "owner", msg: "green", ts: "2026-10-08T11:59:00Z" },
];

function chat(act: HuddleContextValue["act"] = vi.fn(async () => true), events: FeedEvent[] = []) {
  renderIn(
    <Chat events={events} now={NOW} reservations={claims} />,
    makeCtx(makeState({ sessions: { sessions: roster } }), { act }),
  );
  return act;
}

describe("the chat composer", () => {
  it("says a member waits for its next prompt unless its session's claude shows pushes", () => {
    const rows = [
      { session_id: "s-g", channel: "ch", member: "gamma", delivery_route: "now" },
      { session_id: "s-d", channel: "ch", member: "delta", delivery_route: "next_prompt" },
      { session_id: "s-x", channel: "other", member: "delta", delivery_route: "now" },
    ] as RegistrySession[];
    renderIn(
      <Chat events={[]} now={NOW} reservations={claims} />,
      makeCtx(makeState({ sessions: { sessions: roster } }), { registry: makeRegistry({ sessions: rows }) }),
    );
    const to = screen.getByRole("combobox", { name: "To" });
    const line = (): string => document.querySelector("[data-route-line]")?.textContent ?? "";
    expect(line()).toBe("Delivers on the next prompt"); // everyone: delta waits
    fireEvent.change(to, { target: { value: "gamma" } });
    expect(line()).toBe("Delivers now");
    fireEvent.change(to, { target: { value: "delta" } });
    expect(line()).toBe("Delivers on the next prompt");
  });

  it("sends the words to everyone and clears the box", async () => {
    const act = chat();
    const box = screen.getByRole("textbox", { name: "Message" }) as HTMLTextAreaElement;
    fireEvent.input(box, { target: { value: "  stand by  " } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(act).toHaveBeenCalledWith("send", { msg: "stand by" }));
    await waitFor(() => expect(box.value).toBe(""));
  });

  it("sends to the member the To menu names, and offers no session that left", async () => {
    const act = chat();
    const to = screen.getByRole("combobox", { name: "To" }) as HTMLSelectElement;
    expect([...to.options].map((o) => o.value)).toEqual(["", "gamma", "delta"]);
    fireEvent.change(to, { target: { value: "delta" } });
    fireEvent.input(screen.getByRole("textbox", { name: "Message" }), { target: { value: "hi" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(act).toHaveBeenCalledWith("send", { to: "delta", msg: "hi" }));
  });

  it("keeps the words when the send fails, and sends nothing blank", async () => {
    const act = chat(vi.fn(async () => null));
    const box = screen.getByRole("textbox", { name: "Message" }) as HTMLTextAreaElement;
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(act).not.toHaveBeenCalled();
    fireEvent.input(box, { target: { value: "hello" } });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(act).toHaveBeenCalledWith("send", { msg: "hello" }));
    expect(box.value).toBe("hello");
  });
});

describe("the members sheet", () => {
  it("lists the live members with their claims", () => {
    chat();
    fireEvent.click(screen.getByRole("button", { name: /^Members/ }));
    const gamma = document.querySelector('[data-member="gamma"]');
    expect(gamma?.querySelector("[data-claim]")?.textContent).toContain("chat.ts");
    expect(document.querySelector('[data-member="delta"] [data-claim]')).toBeNull();
    expect(document.querySelector('[data-member="gone"]')).toBeNull();
  });

  it("asks once, then removes the member through the remove op", async () => {
    const act = chat();
    fireEvent.click(screen.getByRole("button", { name: /^Members/ }));
    fireEvent.click(screen.getByRole("button", { name: "Remove gamma" }));
    expect(act).not.toHaveBeenCalled();
    expect(screen.getByText("Remove gamma from this channel?")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByText("Remove gamma from this channel?")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Remove gamma" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(act).toHaveBeenCalledWith("remove", { target: "gamma" }));
    await waitFor(() => expect(document.querySelector('[data-member="gamma"]')).toBeNull());
  });
});

describe("the chat on the tokens", () => {
  it("paints the messages card, the bubbles and the composer from the tokens alone", () => {
    chat(undefined, talk);
    const card = document.querySelector("[data-messages] section") as HTMLElement;
    expect(card.className).toContain("ui-card");
    expect(screen.getByRole("region", { name: "Messages" })).toBe(card);
    expect(screen.getByRole("button", { name: /^Members/ }).className).toContain("ui-btn-ghost");
    expect(screen.getByRole("button", { name: "Send" }).className).toContain("ui-btn-primary");
    expect((screen.getByRole("combobox", { name: "To" }) as HTMLElement).className).toContain("ui-input");
    expect((screen.getByRole("textbox", { name: "Message" }) as HTMLElement).className).toContain("ui-input");
    // the owner's bubble in the accent, the session's on the raised surface
    const mine = document.querySelector("[data-seq='1'] p") as HTMLElement;
    const theirs = document.querySelector("[data-seq='2'] p") as HTMLElement;
    expect(mine.className).toContain("bg-[var(--ui-accent)]");
    expect(theirs.className).toContain("bg-[var(--ui-surface-2)]");
    expect(legacyClasses()).toEqual([]);
    expect(daisyForms()).toEqual([]);
  });

  it("paints the members sheet and its confirm row from the tokens alone", () => {
    chat();
    fireEvent.click(screen.getByRole("button", { name: /^Members/ }));
    const remove = screen.getByRole("button", { name: "Remove gamma" });
    expect(remove.className).toContain("ui-btn-ghost");
    fireEvent.click(remove);
    const confirm = document.querySelector("[data-member-confirm]") as HTMLElement;
    const [yes, no] = [...confirm.querySelectorAll("button")];
    expect(yes?.className).toContain("ui-btn-danger");
    expect(no?.className).toContain("ui-btn-ghost");
    expect(legacyClasses()).toEqual([]);
  });
});
