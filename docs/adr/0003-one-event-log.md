# ADR-3: No single event log; each store owns its events

Status: rejected as proposed, 2026-10-27; the federation below is accepted. It sets D24 in
[DECISIONS.md](../DECISIONS.md).

## Context

The proposal was one append-only log, `hub.db events`, that hooks, the router and the hub all write, with
Radar and Kanban as views over it.

The event streams as they stand, each with its file, its writer process and its replay cursor:

- **The spool**, JSONL files under `$RADAR_HOME/spool`. The hooks' radar part, the router's lanes,
  routerd, the gateway and the hub write it; the byte offsets kept in history.db replay it.
- **`hub.db events`**, written by the hub, one transaction per report; the `seq` cursor replays it.
- **Channel events**, one SQLite file per channel under the huddle home's `channels/`. The hub writes
  them; the channel's `seq` with the member's cursor replays them.
- **Radar's in-memory store**, written by the one ingester, capped in memory. It replays nothing; it
  feeds history.db.
- **history.db `events`**, written by the history writer in batches; the `seq` cursor replays it, and
  duplicate rows are ignored.
- **ledger.db**, written by radar's ingester inside the hub, one row per request; the request id replays
  it through idempotent upserts.
- **router.db and its day segments**, written by the router; the pair of request id and side keys each
  body.
- **The cost ledger's day files**, one JSON file per UTC day, written by the radar insights server, a
  rolling window; they replay nothing.
- **The OTel rows**, in memory, written by the OTLP ingest, capped; they replay nothing.
- **kanban.db and its LOG**, written by whichever process runs a kanban mutation; the LOG is the
  append-only audit trail, keyed by ticket id.
- **`hub.db` audit rows**, written by the hub; audit rows are never deleted, and the table reads back in
  full.

The proposal fails here for three reasons:

- Captured bodies cannot ride the spool: a body runs up to 3.5 MB, so the router writes its own store,
  and the router never waits on the hub.
- A channel file stays self-contained: its member rows carry the channel's own state, and hub.db is not
  required for a channel read.
- A log behind hub.db re-couples every writer to the hub's restarts and its migrations. Two incidents on
  2026-10-25 came from exactly that coupling: a hub restart cut every Claude stream in flight, and a
  start stopped mid-migration left hub.db unopenable on the `events` table
  ([INCIDENTS.md](../INCIDENTS.md)).

## Decision

1. **One writer per store.** Each store has one writer process, and owns its own retention: the spool;
   `hub.db events`; each channel file; ledger.db; router.db and its segments; history.db; kanban.db and
   its LOG.
2. **Joins.** Readers join stores by session id, request id or a `seq` cursor, and never write another
   part's store.
3. **Retention, each with the process that runs it:**
   - `hub.db events` and channel events: 90 days, on the hub's hourly sweep, keeping an unanswered ask.
   - ledger.db: 90 days, hourly.
   - router.db bodies: 7 days, hourly, in the router.
   - the spool: 7 days, up to 64 MiB.
   - history.db: the user's retention setting.
4. **history.db is a store of record.** It is Radar's own record, not a rebuildable projection: the spool
   keeps only 7 days and 64 MiB, so it cannot rebuild history.db.
5. **The satellite path is intended.** `recordRadarHook` (`internal/huddle/server`) is a second path on
   purpose: a satellite's own server records for Radar when no hook on that machine did.
6. **The Requests view** reads router.db by request id, on demand.

## Consequences

What stays duplicated, and why that is accepted:

- A Claude Code event lands twice, once in `hub.db events` and once as a spool line, each for its own
  reader: the hub's audit replay reads the first, Radar's ingest reads the second.
- The satellite path stays: a satellite's own server records for Radar where no hook on that machine did.

What a new store must do:

- name its one writer;
- name its join key;
- name its retention and the process that runs it;
- add itself to this ADR's list.
