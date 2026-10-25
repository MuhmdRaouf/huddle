import { fireEvent, render, screen } from "@testing-library/preact";
import { describe, expect, it, vi } from "vite-plus/test";
import { List, ListItem } from "../src/list.tsx";

describe("List", () => {
  it("renders named rows with their meta, leading and trailing parts", () => {
    render(
      <List label="Sessions">
        <ListItem title="api-worker" meta="idle 2 min" leading={<i>A</i>} trailing={<b>ok</b>} />
      </List>,
    );
    const list = screen.getByRole("list", { name: "Sessions" });
    expect(list.className).toBe("ui-list");
    const row = list.querySelector("li > .ui-list-row") as HTMLElement;
    expect(row.tagName).toBe("DIV");
    expect(row.querySelector(".ui-list-title")?.textContent).toBe("api-worker");
    expect(row.querySelector(".ui-list-meta")?.textContent).toBe("idle 2 min");
    expect(row.querySelector(".ui-list-leading i")).toBeTruthy();
    expect(row.querySelector(".ui-list-trailing b")).toBeTruthy();
  });

  it("makes a row a button when it can be tapped", () => {
    const onClick = vi.fn();
    render(
      <List label="Channels">
        <ListItem title="huddle" onClick={onClick} />
      </List>,
    );
    const row = screen.getByRole("button", { name: "huddle" });
    expect(row.className).toBe("ui-list-row");
    expect(row.querySelector(".ui-list-meta")).toBeNull();
    fireEvent.click(row);
    expect(onClick).toHaveBeenCalledOnce();
  });
});
