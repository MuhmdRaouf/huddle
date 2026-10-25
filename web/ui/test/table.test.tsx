import { render, screen } from "@testing-library/preact";
import { describe, expect, it } from "vite-plus/test";
import { type Column, Table } from "../src/table.tsx";

type Row = { model: string; cost: number };

const COLUMNS: Column<Row>[] = [
  { key: "model", label: "Model", cell: (r) => r.model },
  { key: "cost", label: "Cost", cell: (r) => `$${r.cost.toFixed(2)}`, align: "end", mono: true },
];

describe("Table", () => {
  it("renders a captioned table in a focusable scroll region, numbers aligned to the end", () => {
    render(
      <Table
        label="Cost by model"
        columns={COLUMNS}
        rows={[
          { model: "glm-5.3", cost: 1.5 },
          { model: "kimi-k3", cost: 0.25 },
        ]}
        rowKey={(r) => r.model}
      />,
    );
    const region = screen.getByRole("region", { name: "Cost by model" });
    expect(region.className).toBe("ui-table-wrap");
    expect(region.getAttribute("tabindex")).toBe("0");
    const table = screen.getByRole("table", { name: "Cost by model" });
    expect(table.className).toBe("ui-table");
    const heads = screen.getAllByRole("columnheader");
    expect(heads.map((h) => h.textContent)).toEqual(["Model", "Cost"]);
    expect(heads[1]?.getAttribute("scope")).toBe("col");
    expect(heads[1]?.className).toBe("ui-end");
    const cells = screen.getAllByRole("cell");
    expect(cells.map((c) => c.textContent)).toEqual(["glm-5.3", "$1.50", "kimi-k3", "$0.25"]);
    expect(cells[0]?.className).toBe("");
    expect(cells[1]?.className).toBe("ui-end ui-mono");
  });

  it("says so in one wide row when there are no rows", () => {
    render(
      <Table
        label="Cost by model"
        columns={COLUMNS}
        rows={[]}
        rowKey={(r) => r.model}
        empty="No requests yet"
      />,
    );
    const cell = screen.getByRole("cell", { name: "No requests yet" });
    expect(cell.getAttribute("colspan")).toBe("2");
    expect(cell.className).toBe("ui-table-empty");
  });

  it("falls back to a plain empty line", () => {
    render(<Table label="Rows" columns={COLUMNS} rows={[]} rowKey={(r) => r.model} />);
    expect(screen.getByRole("cell", { name: "Nothing here yet" })).toBeTruthy();
  });
});
