// Chat.test.tsx — the channel's chat: the composer sends to everyone or one member through the
// channel's send op and clears on success, keeps the words on a failure; the members sheet lists
// the live sessions with their claims, and its remove asks once before it removes.

import { fireEvent, screen, waitFor } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import type { HuddleContextValue } from "../../src/app/context.tsx";
import { Chat } from "../../src/chat/Chat.tsx";
import type { RosterSession } from "../../src/store.ts";
import { makeCtx, makeState, NOW, renderIn } from "../helpers";

const roster = [
  { name: "gamma", state: "working" },
  { name: "delta", state: "idle" },
  { name: "gone", state: "left" },
] as RosterSession[];

const claims = [{ path: "/work/app/src/chat.ts", holder: "gamma", expires: "2026-10-08T13:00:00Z" }];

function chat(act: HuddleContextValue["act"] = vi.fn(async () => true)) {
  renderIn(
    <Chat events={[]} now={NOW} reservations={claims} />,
    makeCtx(makeState({ sessions: { sessions: roster } }), { act }),
  );
  return act;
}

describe("the chat composer", () => {
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
