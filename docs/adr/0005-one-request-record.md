# ADR-5: ledger.db is the one record of each request

Status: accepted, 2026-10-29. It sets D25 in [DECISIONS.md](../DECISIONS.md).

## Context

- Radar keeps each model request twice:
  - `history.db`'s `requests` table (`internal/radar/history/schema.go:50-85`), written by the history writer
    from Radar's in-memory store;
  - `ledger.db`'s `ledger_requests` table (`internal/radar/ledger/schema.go:12-46`), written by Radar's
    ingester from the router's spool lines and the transcripts.
- The two stores disagree:
  - ten history columns have no ledger column: `cache_write_1h`, `speed`, `geo`, `service_tier`, `provider`,
    `via`, `what`, `thinking`, `headers`, `totals`;
  - codex, opencode and transcript entries without a request id reach history only;
  - `cost_usd` means the harness-reported cost in history but Radar's price in the ledger;
  - `stop_reason` carries a route's failing status text in history;
  - a request with no session is `outside-a-session` in history and `""` in the ledger;
  - the two ids match only through the request-id header.
- Every tree, cost and usage view joins history's `nodes` table on `node_id`.

## Decision

1. `ledger.db` is the one record of each model request. `history.db` keeps its `nodes` table as the tree index
   (sessions, subagents, jobs), and stops holding request rows once every reader reads the ledger.
2. The ledger grows to hold everything a reader needs:
   - the ten columns;
   - a `node_id`, filled at ingest by the same rule history uses;
   - a `history_id` column with the record id history gives the same request;
   - codex, opencode and id-less transcript records, filed under their history ids.
3. Two costs, two columns:
   - `price_usd` is Radar's own price from its pricing table;
   - `harness_cost_usd` is the cost the harness reported, empty when it reported none.

   No column is named `cost_usd`.
4. `stop_reason` is the model's own stop reason, from the transcript. A failing route call is told by `status`,
   `outcome` and `error_code`, never by `stop_reason`.
5. A request with no session is stored under the session id `outside-a-session`, the spelling the views already
   use.
6. Each widening step migrates the rows the ledger already holds:
   - the schema change runs in `Open`;
   - any backfill runs as a resumable background batch of 1,000 rows per transaction, with its cursor in a
     ledger meta table and its progress reported to the health registry as `ledger.backfill`;
   - a hub start never waits on a backfill.
7. `headers` hold only the allow-listed pairs, in the same redacted form history stores. Authorization, API
   keys, cookies and tokens never land in either store in clear text.
8. The ledger keeps rows for the user's history retention setting (7, 30, 90 days, or forever).
9. The readers move to the ledger one per cycle, then the old history rows migrate into the ledger, then the
   history writer stops writing `requests`. Dropping the `requests` table waits for the owner's yes.

## Consequences

- One place answers "what did this request cost and how did it end". Budgets, usage, blocks and the history
  views read the same numbers.
- Disk: each request is stored once instead of twice.
- The ledger schema widens across several versions. Each version bump comes with its migration and a test that
  compares the ledger's rows with history's for the same request ids.
- Codex and opencode traffic counts in budgets and blocks.
- A reader that shows a route's 429 as a stop reason shows it as the call's outcome from then on.
- `internal/radar/cost/ledger.go` is a different, in-memory day ledger. Text that means the request record
  names `ledger.db` or `ledger_requests`.
