// audit.ts — huddle's audit log (SPEC 7.7) as the Radar Timeline reads it: the owner's decisions
// — allows, answers, sends, pairings, sign-ins — newest first, one page at a time. The log is
// owner-only: a browser signed in as a member is refused, which reads here as a named refusal,
// never a thrown one, so the timeline keeps its events either way.

/** One audit row as GET /api/audit answers it. */
export type AuditRow = {
  id: number;
  at: string;
  actor: string;
  action: string;
  target: string;
  detail: unknown;
};

/** One page of the log: the rows (newest first) and the id the next page starts after. */
export type AuditPage = { rows: AuditRow[]; refused: string | null };

/** The most rows one read asks for; the timeline merges them with the events it already holds. */
export const AUDIT_PAGE = 200;

/** Reads one page of the audit log. A refusal (403: not the owner) answers its sentence. */
export async function fetchAudit(outer: typeof fetch = fetch): Promise<AuditPage> {
  const response = await outer(`/api/audit?limit=${AUDIT_PAGE}`, { headers: { Accept: "application/json" } });
  if (!response.ok) {
    const who =
      response.status === 403
        ? "Only the owner reads the audit log."
        : `The audit log is unavailable (${response.status}).`;
    return { rows: [], refused: who };
  }
  const parsed = (await response.json()) as { entries?: unknown };
  const rows = Array.isArray(parsed.entries)
    ? parsed.entries.filter((entry): entry is AuditRow => {
        if (typeof entry !== "object" || entry === null) return false;
        const row = entry as Record<string, unknown>;
        return typeof row.id === "number" && typeof row.at === "string";
      })
    : [];
  return { rows, refused: null };
}

/** A row's moment as an epoch the merged timeline sorts by; an unreadable stamp sinks the row. */
export function auditTs(row: AuditRow): number {
  const parsed = Date.parse(row.at.endsWith("Z") || row.at.includes("+") ? row.at : `${row.at}Z`);
  return Number.isNaN(parsed) ? Number.NaN : parsed;
}
