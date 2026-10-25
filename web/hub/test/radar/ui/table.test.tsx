import { screen } from "@testing-library/preact";
import { userEvent } from "@testing-library/user-event";
import { createRef } from "preact";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import {
  type Column,
  ExportButton,
  HEAD_CELL,
  Table,
  TableHead,
  TableRow,
  timeText,
} from "../../../src/radar/app/table.tsx";
import { fmtTime } from "../../../src/radar/fmt.ts";
import { initialClientState } from "../../../src/radar/state.ts";
import { renderApp } from "./render.tsx";
import { expectTokensOnly } from "./tokens.ts";

// every render in this file paints only token classes and labels its table cells for the phone
afterEach(() => expectTokensOnly());

/** The indexed item; throws instead of letting `noUncheckedIndexedAccess` turn into an undefined deref. */
function at<T>(items: T[], index: number): T {
  const item = items[index];
  if (item === undefined) throw new Error(`nothing at index ${index}`);
  return item;
}

const COLUMNS: Column<"alpha" | "count">[] = [
  { key: "alpha", label: "Alpha" },
  { key: "count", label: "Count", numeric: true },
];

describe("TableHead", () => {
  it("renders one sortable header per column on the token well, numeric columns right-aligned", () => {
    const { container } = renderApp(
      <TableHead columns={COLUMNS} sort={{ key: "alpha", dir: "desc" }} action="sort-things" />,
    );
    const heads = [...container.querySelectorAll("th")];
    expect(heads.map((th) => th.textContent)).toEqual(["Alpha", "Count"]);
    expect(heads.map((th) => th.className)).toEqual([
      `${HEAD_CELL} text-left`,
      `${HEAD_CELL} num text-right`,
    ]);
    expect(HEAD_CELL).toContain("bg-[var(--ui-surface-2)]");
    expect(HEAD_CELL).toContain("text-[var(--ui-muted)]");
    expect(HEAD_CELL).toContain("sticky top-0");
    expect(heads.map((th) => th.getAttribute("scope"))).toEqual(["col", "col"]);
    // numeric sort buttons run the arrow before the label
    expect(at(heads, 1).querySelector("button")?.className).toContain("flex-row-reverse");
    expect(at(heads, 0).querySelector("button")?.className).not.toContain("flex-row-reverse");
  });

  it("marks the sorted column with aria-sort, its arrow in the accent and a touch-sized button", () => {
    const descending = renderApp(
      <TableHead columns={COLUMNS} sort={{ key: "alpha", dir: "desc" }} action="sort-things" />,
    );
    const heads = [...descending.container.querySelectorAll("th")];
    expect(heads.map((th) => th.getAttribute("aria-sort"))).toEqual(["descending", "none"]);
    expect(at(heads, 0).querySelector('svg[data-icon="chevronDown"]')?.getAttribute("class")).toContain(
      "text-[var(--ui-accent)]",
    );
    expect(at(heads, 1).querySelector('svg[data-icon="chevronsUpDown"]')).toBeTruthy();
    const buttons = [...descending.container.querySelectorAll("button")];
    expect(buttons.map((button) => button.className.split(" ").includes("text-[var(--ui-text)]"))).toEqual([
      true,
      false,
    ]);
    for (const button of buttons) expect(button.className).toContain("pointer-coarse:min-h-[var(--ui-tap)]");
    expect(buttons.map((button) => button.getAttribute("title"))).toEqual(["Sort by alpha", "Sort by count"]);

    const ascending = renderApp(
      <TableHead columns={COLUMNS} sort={{ key: "count", dir: "asc" }} action="sort-things" />,
    );
    const upHead = at([...ascending.container.querySelectorAll("th")], 1);
    expect(upHead.getAttribute("aria-sort")).toBe("ascending");
    expect(upHead.querySelector('svg[data-icon="chevronUp"]')).toBeTruthy();
  });

  it("runs the header's sort action with the column key on click", async () => {
    const { container, act } = renderApp(
      <TableHead columns={COLUMNS} sort={{ key: "alpha", dir: "desc" }} action="sort-things" />,
    );
    await userEvent.click(at([...container.querySelectorAll("button")], 1));
    expect(act).toHaveBeenCalledWith("sort-things", "count");
  });
});

describe("timeText", () => {
  it("shows the age with the clock as its tooltip, and the reverse in absolute mode", () => {
    const state = initialClientState();
    expect(timeText(state, 60_000, 70_000)).toEqual({ text: "10s ago", title: fmtTime(60_000) });
    expect(timeText({ ...state, timeMode: "absolute" }, 60_000, 70_000)).toEqual({
      text: fmtTime(60_000),
      title: "10s ago",
    });
  });
});

describe("ExportButton", () => {
  it("offers the CSV download of the shown rows and runs the export on click", async () => {
    const { act } = renderApp(<ExportButton kind="things" count={1} />);
    const button = screen.getByRole("button");
    expect(button.className).toBe("ui-btn ui-btn-ghost");
    expect(button.getAttribute("data-action")).toBe("export");
    expect(button.getAttribute("data-value")).toBe("things");
    expect(button.textContent).toBe("Export CSV");
    expect(button.querySelector('svg[data-icon="download"]')).toBeTruthy();
    await userEvent.click(button);
    expect(act).toHaveBeenCalledWith("export", "things");
  });

  it("pluralises the row count in its tooltip", () => {
    renderApp(<ExportButton kind="things" count={2} />);
    expect(screen.getByRole("button").getAttribute("title")).toBe("Download the 2 rows shown as CSV");
  });
});

describe("Table", () => {
  it("draws dense rows on a desk and a card per row on the phone, in a wrap that scrolls only on a desk", () => {
    const { container } = renderApp(
      <Table dataKey="things-table">
        <tbody>
          <tr>
            <td data-label="Cell">cell</td>
          </tr>
        </tbody>
      </Table>,
    );
    const wrap = container.querySelector("[data-key='things-table']");
    expect(wrap?.className).toBe("sm:max-h-[calc(100dvh-17rem)] sm:min-h-64 sm:overflow-auto");
    const table = wrap?.querySelector("table");
    expect(table?.hasAttribute("data-radar-table")).toBe(true);
    const cls = table?.className ?? "";
    // the desk: dense rows on the line token
    expect(cls).toContain("[&_td]:border-t [&_td]:border-[var(--ui-line)] [&_td]:px-3 [&_td]:py-2");
    // the phone: no head, each row a bordered card of labelled cells, the title cell across it
    expect(cls).toContain("max-sm:block max-sm:[&_thead]:hidden");
    expect(cls).toContain("max-sm:[&_tr]:grid");
    expect(cls).toContain("max-sm:[&_tr]:rounded-[var(--ui-radius)] max-sm:[&_tr]:border");
    expect(cls).toContain("max-sm:[&_td[data-label]]:before:content-[attr(data-label)]");
    expect(cls).toContain("max-sm:[&_td[data-cell=title]]:col-span-2");
    expect(container.textContent).toContain("cell");
  });

  it("omits the data-key when none is given and carries extra classes through", () => {
    const { container } = renderApp(
      <Table class="extra">
        <tbody>
          <tr>
            <td data-label="Cell">cell</td>
          </tr>
        </tbody>
      </Table>,
    );
    expect(container.querySelector("div")?.hasAttribute("data-key")).toBe(false);
    expect(container.querySelector("table")?.className.endsWith(" extra")).toBe(true);
  });

  it("lays out fixed widths from its columns and leaves the rest to share what is left", () => {
    const { container } = renderApp(
      <Table
        dataKey="things-table"
        columns={[{ key: "a", width: "w-24" }, { key: "b" }, { key: "c", width: "w-32" }]}
      >
        <tbody>
          <tr>
            <td data-label="A">one</td>
            <td data-label="B">two</td>
            <td data-label="C">three</td>
          </tr>
        </tbody>
      </Table>,
    );
    expect(container.querySelector("table")?.className).toContain(" table-fixed");
    const cols = [...container.querySelectorAll("col")];
    expect(cols.map((col) => col.className)).toEqual(["w-24", "", "w-32"]);
  });

  it("hands the scrollable wrap to wrapRef", () => {
    const ref = createRef<HTMLDivElement>();
    renderApp(
      <Table wrapRef={ref}>
        <tbody>
          <tr>
            <td data-label="Cell">cell</td>
          </tr>
        </tbody>
      </Table>,
    );
    expect(ref.current?.querySelector("table")).toBeTruthy();
  });
});

describe("TableRow", () => {
  it("renders a plain row carrying its state in token classes", () => {
    const { container } = renderApp(
      <table>
        <tbody>
          <TableRow>
            <td>plain</td>
          </TableRow>
          <TableRow failed active fresh dimmed>
            <td>marked</td>
          </TableRow>
        </tbody>
      </table>,
    );
    const rows = [...container.querySelectorAll("tr")];
    expect(rows[0]?.className).toBe("row");
    expect(rows[1]?.className).toBe(
      "row bg-[var(--ui-fail-soft)] bg-[color-mix(in_oklab,var(--ui-accent)_10%,transparent)] row-new text-[var(--ui-muted)]",
    );
    expect(rows[0]?.hasAttribute("tabindex")).toBe(false);
    expect(screen.getByText("plain")).toBeTruthy();
  });

  it("focuses a clickable row and opens it on click, Enter and Space", async () => {
    const onOpen = vi.fn();
    const { container } = renderApp(
      <table>
        <tbody>
          <TableRow onOpen={onOpen}>
            <td>open me</td>
          </TableRow>
        </tbody>
      </table>,
    );
    const row = container.querySelector("tr");
    expect(row?.getAttribute("tabindex")).toBe("0");
    expect(row?.className).toContain("hover:bg-[var(--ui-surface-2)]");
    await userEvent.click(row as HTMLElement);
    expect(onOpen).toHaveBeenCalledTimes(1);
    await userEvent.keyboard("{Enter}");
    expect(onOpen).toHaveBeenCalledTimes(2);
    await userEvent.keyboard(" ");
    expect(onOpen).toHaveBeenCalledTimes(3);
  });
});
