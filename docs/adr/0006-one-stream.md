# ADR-6: one stream serves the hub's pushes

Status: accepted, 2026-11-01. It sets D26 in [DECISIONS.md](../DECISIONS.md).

## Context

- Five paths push changes to a client, and none of them resumes:
  - the channel stream, `GET /api/c/<ch>/live` (`internal/huddle/server/server.go:2599`), `retry: 3000` then
    `data: {"ch","type","data","at"}`; a second `hello` makes the page refetch everything
    (`web/hub/src/live.ts:123`); a slow client loses frames in silence (`server.go:2654`);
  - the registry stream, `GET /api/registry/live` (`server.go:1830`), `registry.session` and `needs.item`
    frames; no resume, silent drops (`server.go:1908`), and a `registry.session` frame no reader reads;
  - the radar stream, `GET /api/radar/api/stream` (`internal/radar/server/http.go:470`), a snapshot on every
    connect and the whole session list again in every 500 ms window;
  - the kanban event stream, `GET /api/events` (`internal/kanban/server/sse.go:108`), `snapshot` and `delta`
    named events, resume by a `rev` check that refetches the whole board;
  - long polls: MCP `wait`/`poll`, `GET /api/needs/<id>/wait`, the satellite queue.
- The channel stream encodes each message once per subscriber (`server.go:2648`), not once for all of them.
- A tab holds up to four `EventSource`s at once; a host allows six connections. Beside the streams the web
  runs fifteen fetch loops, among them Home's 10 s poll of `/api/channels` plus one `/attention` call per
  channel, and radar's 5 s auto-refresh.
- Producers run whether or not anyone listens; only the transcript watch gates itself on open streams.

## Decision

1. `GET /api/stream` is the one stream the hub serves the web with:

   ```text
   GET /api/stream?topics=sessions,needs,channel:e2e,radar.live&after=<seq>&snapshot=<topics>
       credentialed like /api/registry/live; Last-Event-ID, when present, wins over after.
   frames:
     retry: 3000
     id: <seq>
     data: {"topic":"sessions","seq":N,"op":"upsert","id":"<key>","data":{...}}
   control frames (no seq, no id line):
     data: {"type":"hello","head":N,"served":["sessions",...],"gone":["channel:e2e"]}
     data: {"type":"ping"}                    a data frame every 15 s (SSEPingMS moves it)
   ops:
     snapshot  id "" ; data = {"rows":[{"id","data"},...]}   the topic's whole state; replaces it
     upsert    id = key ; data = the row
     remove    id = key
     touch     id = a name ("usage", "audit", ...) ; no data: "refetch yours"
   ```

2. A frame's message is `{topic, seq, op, id, data}` (`internal/domain/stream_ring.go`). `data` is absent on
   `remove` and `touch`: they name a row or a view and carry no state. A `snapshot`'s `id` is empty and its
   `data` is `{rows:[{id,data}]}`.
3. One global sequence per hub process, assigned by the broker at publish
   (`internal/huddle/stream/broker.go`). It starts at the boot time in microseconds (about 1.77e15, inside
   JavaScript's 2^53), so a restarted hub issues seqs above every one its predecessor issued, and a client's
   old `after` falls outside the new ring and earns snapshots.
4. The ring holds the last 2,048 frames and 4 MiB of encoded bytes, and never evicts the frame it just
   wrote, so a single frame larger than the byte cap stays alone. A reconnect replays only from a seq inside
   the ring: an `after` at or above the ring's tail and at or below its head gets the requested topics'
   frames after it. Any other `after` — 0, below the tail, above the head — answers with a snapshot per
   topic. `snapshot=` names topics that get a snapshot whatever `after` says, and their replayed frames are
   dropped: it is how a client adds a topic to a live resume (a channel just opened, a board just picked).
5. A topic a snapshotter serves gets its snapshot stamped with the head seq; the broker reads the snapshot
   under its lock, so the rows match the seq they carry. A topic no snapshotter serves gets no snapshot, and
   `gone` names it. The stream serves what this hub owns: `hello.served` omits a proxied radar and a relayed
   board (D-S6), and the web falls back to that domain's old stream.
6. A frame is encoded once in the broker, and the same bytes go to every subscriber and into the ring. A
   frame that cannot be encoded takes no seq.
7. A slow subscriber is closed, not skipped: past 256 queued frames the subscription ends with reason
   `slow`. A silent drop loses data with no sign; a closed client reconnects with its last `after` and loses
   nothing.
8. Producers run only while someone listens: each checks `Subscribers(prefix)` first, so an idle hub stays
   inside the light guard's budget.
9. Two kinds of topic. A *state topic* carries rows; a *touch topic* carries only "this changed, refetch
   yours", for reads that take parameters or pages (usage by range, flow, models, blocks, history, audit,
   insights, a session's tasks). Radar's report routes keep their REST shapes; the timers become
   touch-driven refetches of the view on screen (D-S3). Each topic has one producer file,
   `stream_<topic>.go`, registered in one place beside the route.

   | Topic | Replaces | Id | Snapshot |
   |---|---|---|---|
   | `sessions` | `/api/registry/sessions`, `registry.session` frames | `session_id` | the rows the registry list route builds |
   | `needs` | `/api/needs`, `needs.item` | item id; `remove` once it is neither open nor snoozed | `registry.Items` |
   | `channels` | Home's 10 s poll of `/api/channels` and one `/attention` per channel | channel name | one row per channel: the `/api/channels` row and its needs count |
   | `channel:<name>` | the channel stream | event: seq; presence: member name; touches: `task`, `plan`, `ack`, `channel`, `conflict`, `extras` | roster, turn and `last` |
   | `radar.live` | the radar stream's `snapshot`, `sessions`, `request`, `tool`, `event`, `ingest` | session id; record id | summary, models, `catchingUp` and the session items; records stay out, and a client whose seq left the ring backfills by REST |
   | `radar.facts` | the 10 s `pollInsights` | `alerts`, `budget-status`, `spend`, `health`, `quota`, `settings` | the same six JSON answers, republished only when the bytes change |
   | `radar.touch` | the 5/15/60 s auto-refresh and the 60 s day, graph and self-check pollers | `usage`, `blocks`, `flow`, `models`, `attribution`, `router`, `advisor`, `day`, `selfcheck`, `history` | none; touches coalesce to at least 2 s on a store version change |
   | `kanban:<key>` | the kanban event stream | card id; `_meta` for board fields; `touch`, `reload` | the board watcher's snapshot; delta data keeps its `rev` |
   | `session:<id>` | the 60 s `tasks` TTL and the `conversation.grew` frame | `tasks`, `conversation:<agent>`, `live` (touches) | none |
   | `audit`, `insights` | the 30 s and 300 s pollers | `rows`, `tips` (touches) | none |

10. The web reads the stream through one client and one store per domain: the schemas, `decode`, the client
    and the keyed store live in `api/` (D-S7). A keyed store applies `snapshot`, `upsert` and `remove` by id,
    and ignores `touch`. The client sends `after = last seq`, adds `snapshot=` for topics added since its
    last connect, backs off 1, 2, 4, 8 s, reads a `gone` topic as "snapshots follow", and sits under the
    shared watchdog (40 s of silence, 20 s stale on return). One `sessions` store holds registry rows keyed
    by Claude session id, and the radar item and the channel member join it by id (D-S2).
11. Strangler rule: server cards add and delete nothing; a web card moves one reader and deletes nothing on
    the server. An old route dies only once its last reader has moved, in a drop card of its own. Routes
    with readers outside the web stay: `/api/c/<ch>/live` (the CLI and the MCP bridge), `/api/events` (the
    standalone board page), and every REST read. `/health` stays the one read poll, at 60 s, beside the 30 s
    focus heartbeat, which is client to server (D-S4).

## Consequences

- One `EventSource` per tab replaces up to four, and frees three of the six connections a host allows.
- An idle stream is pings only: under 1 KB/s over 30 s on the built binary, as the test plan's idle-bytes
  row requires.
- A client that falls behind loses nothing: inside the ring it replays, outside it takes a snapshot, and a
  restart moves the seq space so the next connect snapshots.
- Encoding runs once per frame, not once per subscriber.
- A producer with no listener does no work; each topic card proves its producer idles, and the light guard
  measures the stream path once the polls it names have moved.
- Two code paths stay until the drop cards land: the old routes keep the readers they still have, and a
  proxied radar or relayed board falls back to its old stream.
- The ring costs at most 4 MiB of the hub's RSS ceiling and keeps no state on disk: a seq lives in its
  process, so every resume across a restart is a snapshot.
- A client that can never keep up loops: close, reconnect, fall behind, close. The client's backoff caps the
  cycle at 8 s.
- A second `hello` never makes the page refetch: the first frame after a gap is a snapshot.
