// audit.test.ts — the audit read the Timeline merges in: the rows a good answer carries, the
// sentence a refusal leaves, the malformed entries dropped, and the stamps the merge sorts by.

import { describe, expect, it, vi } from "vite-plus/test";
import type { HttpResponse } from "../../src/radar/app/controller.ts";
import { AUDIT_PAGE, auditTs, fetchAudit } from "../../src/radar/audit.ts";

const answer = (status: number, body: unknown): HttpResponse => ({
  ok: status >= 200 && status < 300,
  status,
  json: async () => body,
});

function fetchOf(response: HttpResponse): typeof fetch {
  return vi.fn(async () => response) as unknown as typeof fetch;
}

describe("fetchAudit", () => {
  it("asks the server's audit route for one page", async () => {
    const read = vi.fn(async () => answer(200, { entries: [] })) as unknown as typeof fetch;
    await fetchAudit(read);
    expect(read).toHaveBeenCalledWith(`/api/audit?limit=${AUDIT_PAGE}`, {
      headers: { Accept: "application/json" },
    });
  });

  it("carries the rows a good answer lists", async () => {
    const entries = [
      { id: 7, at: "2026-10-16T09:00:00Z", actor: "owner", action: "allow", target: "Bash", detail: null },
      {
        id: 6,
        at: "2026-10-16T08:59:00Z",
        actor: "laptop",
        action: "signin",
        target: "dashboard",
        detail: null,
      },
    ];
    const page = await fetchAudit(fetchOf(answer(200, { entries })));
    expect(page.refused).toBeNull();
    expect(page.rows).toEqual(entries);
  });

  it("says why when the log is not this browser's to read", async () => {
    const page = await fetchAudit(fetchOf(answer(403, {})));
    expect(page.rows).toEqual([]);
    expect(page.refused).toBe("Only the owner reads the audit log.");
  });

  it("names any other failed read with its status", async () => {
    const page = await fetchAudit(fetchOf(answer(503, {})));
    expect(page.refused).toBe("The audit log is unavailable (503).");
  });

  it("drops entries without an id or a stamp, and a body that lists nothing", async () => {
    const page = await fetchAudit(
      fetchOf(
        answer(200, {
          entries: [
            { id: 1, at: "2026-10-16T09:00:00Z", actor: "a", action: "allow", target: "Bash", detail: null },
            { id: "two", at: "2026-10-16T09:00:00Z" },
            { id: 3 },
            "row",
            null,
          ],
        }),
      ),
    );
    expect(page.rows).toHaveLength(1);
    expect(page.rows[0]?.id).toBe(1);
    expect((await fetchAudit(fetchOf(answer(200, { entries: "no" })))).rows).toEqual([]);
  });
});

describe("auditTs", () => {
  it("reads the log's UTC stamps and explicit offsets", () => {
    expect(
      auditTs({ id: 1, at: "2026-10-16T09:00:00Z", actor: "", action: "", target: "", detail: null }),
    ).toBe(Date.UTC(2026, 9, 16, 9, 0, 0));
    expect(
      auditTs({ id: 2, at: "2026-10-16T09:00:00+02:00", actor: "", action: "", target: "", detail: null }),
    ).toBe(Date.UTC(2026, 9, 16, 7, 0, 0));
  });

  it("reads a stamp with no zone as UTC and sinks an unreadable one", () => {
    expect(
      auditTs({ id: 3, at: "2026-10-16 09:00:00", actor: "", action: "", target: "", detail: null }),
    ).toBe(Date.UTC(2026, 9, 16, 9, 0, 0));
    expect(
      Number.isNaN(auditTs({ id: 4, at: "not a stamp", actor: "", action: "", target: "", detail: null })),
    ).toBe(true);
  });
});
